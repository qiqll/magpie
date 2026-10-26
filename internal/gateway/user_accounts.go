package gateway

// A caller's own users, each bound to one account of a provider (or one of
// its keys): the caller names the user in UserHeader, the first request of
// that user to the provider binds it to the account fewest users are bound
// to, and from then on only that account is asked for it, of any of the
// provider's models. Past its allowance, resting or at its usage cap, the
// account's error is the caller's: nothing moves the user to another
// account. The binding goes only when the account does — it is no longer
// on the provider (signed out, switched off, removed) — or its vendor
// turned it away for good (a sign-in revoked and marked so, an account
// restricted or to be verified), and the user's next request binds it
// afresh.
//
// Each gateway key's users are its own, apart from another key's and from
// those of callers without a key: the same id sent with two keys is two
// users. The bindings are kept for good in user-accounts.json next to
// providers.json. A file that can't be read is unknown, never "no users":
// what was read before still binds, nothing new is bound and nothing is
// written over it. One edited by hand is read again.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
)

// UserHeader names the caller's own user a request is for, to keep that
// user on one account of each provider. It goes no further than magpie.
const UserHeader = "X-Magpie-User"

// userMax is the longest user UserHeader takes.
const userMax = 128

// UserLimitHeader, on an error to a request with a user, says the user
// can't be answered for a limit until a time: the account it is bound to
// (bound to none, the provider's accounts) is out of its allowance
// ("quota"), its credit ("credit"), rate limited ("rate") or at the usage
// cap set on it in magpie ("cap"). UserUntilHeader is when it is back,
// RFC 3339 in UTC, as Retry-After is in seconds: the caller stops sending
// the user's requests until then. Neither is there when the user's
// account can answer again at once, or is let go (bound afresh next).
const (
	UserLimitHeader = "X-Magpie-User-Limit"
	UserUntilHeader = "X-Magpie-User-Until"
)

// userLimit is the UserLimitHeader a failure why is, "" for one that
// isn't a limit.
func userLimit(why string) string {
	switch why {
	case failQuota, failCredit, failRate:
		return why
	}
	return ""
}

// userLimited marks h, an error's headers, with UserLimitHeader limit and,
// when it is still to come, until, to the second after: never earlier
// than the account is back.
func userLimited(h http.Header, limit string, until time.Time) {
	h.Set(UserLimitHeader, limit)
	if until = until.Add(time.Second - 1).Truncate(time.Second); time.Until(until) > 0 {
		h.Set(UserUntilHeader, until.UTC().Format(time.RFC3339))
		h.Set("Retry-After", strconv.Itoa(int(math.Ceil(time.Until(until).Seconds()))))
	}
}

// userRefusal is why a user's request goes to no account: its status and
// message, and its UserLimitHeader and when the account is back, when it
// is a limit.
type userRefusal struct {
	status int
	msg    string
	limit  string
	until  time.Time
}

// UserHeaderEnv names another header a caller's user is read from, for a
// caller that sends its users' ids already under a name of its own
// (MAGPIE_USER_HEADER=X-User-ID). Never on by default: a client that
// sends such a header for its own reasons would have its requests held to
// one account. UserHeader is read first.
const UserHeaderEnv = "MAGPIE_USER_HEADER"

var headerName = regexp.MustCompile(`^[Xx]-[A-Za-z0-9][A-Za-z0-9-]*$`)

// userHeaderRefused are the names UserHeaderEnv can't take: one that isn't
// an X- header, magpie's own, one that carries the caller's key, and one
// an agent tells itself by, which goes on to its vendor as it came
// (claudeCodeHeader, codexClientHeader): read as a user, it would reach
// the vendor, or be taken off a request that needs it.
func userHeaderRefused(name string) bool {
	l := strings.ToLower(name)
	switch {
	case !headerName.MatchString(name), strings.HasPrefix(l, "x-magpie-"),
		l == "x-api-key", l == "x-goog-api-key", strings.Contains(l, "auth"), strings.Contains(l, "token"),
		claudeCodeHeader(name), codexClientHeader(name):
		return true
	}
	return slices.ContainsFunc(sessionHeaders, func(h string) bool { return strings.EqualFold(h, name) })
}

var userHeaderSaid sync.Map // a refused UserHeaderEnv, logged once

// userHeaders are the headers a caller's user is read from, in order.
func userHeaders() []string {
	name := strings.TrimSpace(os.Getenv(UserHeaderEnv))
	if name == "" || strings.EqualFold(name, UserHeader) {
		return []string{UserHeader}
	}
	if userHeaderRefused(name) {
		if _, said := userHeaderSaid.LoadOrStore(name, true); !said {
			log.Printf("%s=%q isn't a header magpie can read users from (an X- header that is neither magpie's, a key's nor an agent's own); only %s is read", UserHeaderEnv, name, UserHeader)
		}
		return []string{UserHeader}
	}
	return []string{UserHeader, http.CanonicalHeaderKey(name)}
}

// isUserHeader says name is one of userHeaders: it goes no further than
// magpie.
func isUserHeader(name string) bool {
	return slices.ContainsFunc(userHeaders(), func(h string) bool { return strings.EqualFold(h, name) })
}

// userBinding is the account a user is bound to.
type userBinding struct {
	// Who is candidate.who(): "<provider>@<account's user>" for an
	// account, "<provider>#<key id>" for a key
	Who     string    `json:"who"`
	Account string    `json:"account"` // its user (an email, a login) or key's name, as the Routing page names it
	At      time.Time `json:"at"`      // when the user was bound to it
}

// userFile is user-accounts.json.
type userFile struct {
	// Users are those of callers without a gateway key: provider id →
	// user → binding
	Users map[string]map[string]userBinding `json:"users,omitempty"`
	// Keys are each gateway key's own: its id → provider id → user →
	// binding
	Keys map[string]map[string]map[string]userBinding `json:"keys,omitempty"`
}

var userBinds = struct {
	sync.Mutex
	// m is gateway key id ("" for none) → provider id → user → binding
	m map[string]map[string]map[string]userBinding
	// the file as last read or written, read again when it changed
	path string
	mod  time.Time
	size int64
	read bool // m is what the file had, or would have
	// stale: the file changed and doesn't read; m is what was read
	// before, and nothing is written over it
	stale bool
}{}

var userFileSaid sync.Map // a user-accounts.json that doesn't read, logged once a change

func usersPath() string {
	return filepath.Join(filepath.Dir(provider.Path()), "user-accounts.json")
}

// UserBound is what the trace tells of a request's user.
type UserBound struct {
	User    string `json:"user"`
	Account string `json:"account,omitempty"`
	// Why: "kept" (bound before), "bound" (bound now), "gone" (the
	// account it was bound to is gone, and it is bound now to another),
	// "none" (no account of the provider could be bound now)
	Why string `json:"why"`
}

// userOf is the user a request names in the first of userHeaders it
// sends, "" for none; bad is why a value can't be taken.
func userOf(h http.Header) (user, bad string) {
	for _, name := range userHeaders() {
		if user = strings.TrimSpace(h.Get(name)); user == "" {
			continue
		}
		switch {
		case len(user) > userMax:
			return "", fmt.Sprintf("%s is at most %d bytes", name, userMax)
		case strings.IndexFunc(user, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0:
			return "", name + " can't hold control or format characters"
		}
		return user, ""
	}
	return "", ""
}

// loadUsers reads the file again when it changed since it was last read
// or written. Called with userBinds held. A file that doesn't read leaves
// what was read before (stale), and is an error only when nothing was.
func loadUsers() error {
	path := usersPath()
	if path != userBinds.path {
		userBinds.m, userBinds.path, userBinds.mod, userBinds.size, userBinds.read, userBinds.stale = nil, path, time.Time{}, 0, false, false
	}
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if userBinds.read && !userBinds.mod.IsZero() {
			// removed by hand: every user forgotten
			userBinds.m = nil
		}
		userBinds.mod, userBinds.size, userBinds.read, userBinds.stale = time.Time{}, 0, true, false
		return nil
	}
	if err == nil && userBinds.read && fi.ModTime().Equal(userBinds.mod) && fi.Size() == userBinds.size {
		return nil
	}
	var f userFile
	if err == nil {
		var b []byte
		if b, err = os.ReadFile(path); err == nil {
			err = json.Unmarshal(b, &f)
		}
	}
	if err != nil {
		if _, said := userFileSaid.LoadOrStore(fmt.Sprint(path, err), true); !said {
			log.Printf("user accounts: %s can't be read (%v): the users read before stay bound, none is bound anew, and the file is left as it is", path, err)
		}
		userBinds.stale = true
		if !userBinds.read {
			return err
		}
		return nil
	}
	m := map[string]map[string]map[string]userBinding{}
	if len(f.Users) > 0 {
		m[""] = f.Users
	}
	for k, ps := range f.Keys {
		if k != "" {
			m[k] = ps
		}
	}
	userBinds.m, userBinds.mod, userBinds.size, userBinds.read, userBinds.stale = m, fi.ModTime(), fi.Size(), true, false
	return nil
}

// saveUsers writes the bindings, only for this user to read: to a file
// beside it first, flushed, renamed over it. Called with userBinds held,
// never while the file doesn't read (stale).
func saveUsers() error {
	if userBinds.stale {
		return errors.New("it doesn't read, and is left as it is")
	}
	f := userFile{Users: userBinds.m[""]}
	for k, ps := range userBinds.m {
		if k != "" && len(ps) > 0 {
			if f.Keys == nil {
				f.Keys = map[string]map[string]map[string]userBinding{}
			}
			f.Keys[k] = ps
		}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path := usersPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	target, err := edit.Target(path) // a symlink stays, its target written
	if err != nil {
		return err
	}
	if err := steady.WriteFile(target, b, 0o600); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		userBinds.mod, userBinds.size = fi.ModTime(), fi.Size()
	}
	return nil
}

// saveUsersLogged is saveUsers, its failure logged: what is bound is in
// memory still, and the next change writes it.
func saveUsersLogged() {
	if err := saveUsers(); err != nil {
		log.Printf("user accounts: can't write %s: %v", usersPath(), err)
	}
}

// seatsOf is who every account or key p has is, on or paused, whatever
// model is asked: a user bound to one that isn't among them was bound to
// one that is gone. known is false when that can't be told: the saved
// accounts couldn't be read.
func seatsOf(p provider.Provider) (seats map[string]bool, known bool) {
	seats = map[string]bool{}
	if p.Account != nil {
		// the account the agent is signed in to, paused or not: a pause is
		// the user's to undo
		seats[strings.ToLower(candidate{p: p, rest: p.ID}.who())] = true
		for _, q := range p.AlsoOn() {
			seats[strings.ToLower(candidate{p: q, rest: p.ID + "@" + q.Account.User}.who())] = true
		}
		return seats, p.IsPlugin() || provider.LoginsKnown()
	}
	keys := p.KeysOn()
	for _, k := range keys {
		q := p.WithKey(k)
		rest := p.ID
		if len(keys) > 1 {
			rest += "#" + provider.KeyID(k.Key)
		}
		seats[strings.ToLower(candidate{p: q, rest: rest}.who())] = true
	}
	if len(keys) == 0 {
		seats[strings.ToLower(candidate{p: p, rest: p.ID}.who())] = true
	}
	return seats, true
}

// shownAs is how the Routing page names c's account or key.
func shownAs(c candidate) string {
	switch {
	case c.p.Account != nil:
		return c.p.Account.User
	case c.p.KeyName != "":
		return c.p.KeyName
	case c.p.Key != "":
		return provider.Mask(c.p.Key)
	}
	return c.p.ID
}

// leftIs says w, a candidate left out of the plan (pl.left), is who of
// provider pid.
func leftIs(w Weighed, pid, who string) bool {
	if w.Provider != pid {
		return false
	}
	if w.Kind == "account" {
		return strings.EqualFold(pid+"@"+w.Who, who)
	}
	// a provider's one key rests by the provider's id alone
	return strings.EqualFold(w.ID, who) || w.ID == pid && strings.HasPrefix(who, pid+"#")
}

// bindUser narrows cands to the account of p that user, of the gateway key
// keyID, is bound to, binding it first to the one of them fewest users of
// p are bound to when it is bound to none, or to one that is gone. When
// the account it is bound to can't take the request, the refusal says
// why, and nothing else is asked in its place.
func bindUser(keyID, user string, p provider.Provider, cands []candidate, pl planned) ([]candidate, planned, *UserBound, userRefusal) {
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil {
		return nil, pl, nil, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: magpie can't read user-accounts.json, so it doesn't know which account %q is bound to; nothing was sent. Its log says why", UserHeader, user)}
	}
	ub := &UserBound{User: user}
	b, had := userBinds.m[keyID][p.ID][user]
	if had {
		out, order, rests := ownedBy(b.Who, p.ID, cands, pl)
		if len(out) > 0 {
			ub.Account, ub.Why = b.Account, "kept"
			return out, planned{order: order}, ub, userRefusal{}
		}
		seats, known := seatsOf(p)
		if !known || seats[strings.ToLower(b.Who)] {
			ub.Account, ub.Why = b.Account, "kept"
			return nil, pl, ub, boundRefusal(user, b, p.ID, rests, pl)
		}
		// signed out, switched off or removed: bound afresh below
		if userBinds.stale {
			return nil, pl, ub, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: the account %q was bound to is gone, and user-accounts.json can't be read to bind it to another; nothing was sent. magpie's log says why", UserHeader, user)}
		}
		delete(userBinds.m[keyID][p.ID], user)
		saveUsersLogged()
		ub.Why = "gone"
	}
	if userBinds.stale {
		return nil, pl, ub, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: user-accounts.json can't be read, so %q isn't bound to an account now; nothing was sent. magpie's log says why", UserHeader, user)}
	}
	// the account of p fewest users are bound to, whichever key's, of
	// those that can answer now; the plan's order between those alike
	bound := map[string]int{}
	for _, ps := range userBinds.m {
		for _, o := range ps[p.ID] {
			bound[strings.ToLower(o.Who)]++
		}
	}
	pick := -1
	for i, c := range cands {
		if c.p.ID != p.ID || resting(c) || provider.SignInMarked(c.p.Account) {
			continue
		}
		if pick < 0 || bound[strings.ToLower(c.who())] < bound[strings.ToLower(cands[pick].who())] {
			pick = i
		}
	}
	if pick < 0 {
		// none can answer now: routed as it would be, bound once one can
		ub.Why = "none"
		return cands, pl, ub, userRefusal{}
	}
	c := cands[pick]
	if userBinds.m == nil {
		userBinds.m = map[string]map[string]map[string]userBinding{}
	}
	if userBinds.m[keyID] == nil {
		userBinds.m[keyID] = map[string]map[string]userBinding{}
	}
	if userBinds.m[keyID][p.ID] == nil {
		userBinds.m[keyID][p.ID] = map[string]userBinding{}
	}
	userBinds.m[keyID][p.ID][user] = userBinding{Who: c.who(), Account: shownAs(c), At: time.Now().UTC()}
	saveUsersLogged()
	if ub.Why == "" {
		ub.Why = "bound"
	}
	ub.Account = shownAs(c)
	out, order, _ := ownedBy(c.who(), p.ID, cands, pl)
	return out, planned{order: order}, ub, userRefusal{}
}

// boundRefusal is why the account b, which user is bound to, can't take
// the request: resting (rests), left out of the plan, or narrowed away
// before (a conversation kept strictly, or sealed, on another). Resting,
// it is back when the first of its rests ends: a limit's, the user's
// limit until then.
func boundRefusal(user string, b userBinding, pid string, rests []Rest, pl planned) userRefusal {
	if len(rests) > 0 {
		said := make([]string, len(rests))
		first := rests[0]
		for i, r := range rests {
			said[i] = fmt.Sprintf("rests until %s (%s)", r.Until.Format(time.RFC3339), r.Why)
			if r.Until.Before(first.Until) {
				first = r
			}
		}
		return userRefusal{status: http.StatusTooManyRequests, msg: fmt.Sprintf("%s: %q is bound to %s, which %s; no other account is tried in its place", UserHeader, user, b.Account, strings.Join(said, "; ")),
			limit: userLimit(first.Why), until: first.Until}
	}
	for _, w := range pl.left {
		if !leftIs(w, pid, b.Who) {
			continue
		}
		switch {
		case w.Held:
			return userRefusal{status: http.StatusForbidden, msg: fmt.Sprintf("%s: %q is bound to an account the gateway key may not use", UserHeader, user)}
		case w.Capped > 0 || w.NoCredits:
			msg, back := cappedError(w.Model, []Weighed{w}, time.Now())
			return userRefusal{status: http.StatusTooManyRequests, msg: fmt.Sprintf("%s: %q is bound to %s: %s; no other account is tried in its place", UserHeader, user, b.Account, msg),
				limit: cappedLimit([]Weighed{w}), until: back}
		default:
			return userRefusal{status: http.StatusBadRequest, msg: fmt.Sprintf("%s: %q is bound to %s, which doesn't serve %s", UserHeader, user, b.Account, w.Model)}
		}
	}
	return userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: %q is bound to %s, which can't take this request (paused, or the conversation is kept on, or was sealed by, another account); no other account is tried in its place", UserHeader, user, b.Account)}
}

// cappedLimit is the UserLimitHeader of accounts ws held by magpie: "cap"
// when one is at the usage cap set on it, "quota" when each has used up
// its allowance and is set not to spend its credits.
func cappedLimit(ws []Weighed) string {
	if capped, _ := heldBy(ws); capped {
		return "cap"
	}
	return failQuota
}

// userLimitedAfter marks h, the headers of the error account c gave a
// request of the user u, when the error is a limit: UserLimitHeader and
// until when. The account u is bound to rests now, as serve rests it
// after (restsOn), for the caller to be told when it is back, and rested
// says it does (try.Rest). Bound to none, the error's own Retry-After is
// when, else when the first of those held is back (heldBack).
func (s *Server) userLimitedAfter(h http.Header, u *UserBound, c candidate, status int, body []byte, heldBack time.Time, try *Try) (rested bool) {
	why := failureOf(c, status, body)
	limit := userLimit(why)
	if limit == "" {
		return false
	}
	now, until := time.Now(), heldBack
	switch {
	case u.restsOn(why) && try.Rest != nil:
		// rested already, as the last key out of its allowance is
		until, rested = try.Rest.Until, true
	case u.restsOn(why):
		rest := s.restAfter(c, status, h, body)
		try.Rest, until, rested = &rest, rest.Until, true
	default:
		if d := retryAfter(h, now); d > 0 {
			until = now.Add(d)
		}
	}
	userLimited(h, limit, until)
	return rested
}

// ownedBy is cands narrowed to who's of provider pid, those resting left
// out and their rests in rests.
func ownedBy(who, pid string, cands []candidate, pl planned) (out []candidate, order []Weighed, rests []Rest) {
	for i, c := range cands {
		if c.p.ID != pid || !strings.EqualFold(c.who(), who) || i >= len(pl.order) {
			continue
		}
		if r, ok := restingOf(c); ok {
			rests = append(rests, r)
			continue
		}
		out, order = append(out, c), append(order, pl.order[i])
	}
	return out, order, rests
}

// unbindTurnedAway lets go of every user bound to c's account when its
// vendor turned the account away for good (why): each is bound afresh on
// its next request. A revoked sign-in counts once it is marked so: one a
// refresh mended is the account as good as before.
func unbindTurnedAway(c candidate, why string) {
	switch why {
	case failRestricted, failVerify:
	case failAuth:
		if provider.RevokedAccount(c.p.Account) && !provider.SignInMarked(c.p.Account) {
			return
		}
	default:
		return
	}
	who := c.who()
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil || userBinds.stale {
		return // unknown: nothing is written over it
	}
	gone := 0
	for _, ps := range userBinds.m {
		for u, b := range ps[c.p.ID] {
			if strings.EqualFold(b.Who, who) {
				delete(ps[c.p.ID], u)
				gone++
			}
		}
	}
	if gone > 0 {
		log.Printf("user accounts: %d user(s) let go of %s (%s), each bound afresh on its next request", gone, c.label(), why)
		saveUsersLogged()
	}
}

// accountOr is the account u is bound to, for the usage log: never the
// user itself.
func (u *UserBound) accountOr() string {
	if u == nil || u.Account == "" {
		return "none"
	}
	return u.Account
}

// restsOn says a failure of the account the request was narrowed to, its
// user's, rests it as it would with the others still there: one that
// says the account can't answer now (out of its allowance or credit, rate
// limited, restricted, to be verified), for users not bound yet to go
// elsewhere meanwhile. Never one the request itself may have caused (a
// 400, a reply cut short): a caller's bad request would rest the account
// for every user on it.
func (u *UserBound) restsOn(why string) bool {
	if u == nil || u.Why == "none" {
		return false
	}
	switch why {
	case failQuota, failCredit, failRate, failRestricted, failVerify:
		return true
	}
	return false
}

// userKey is the gateway key a request's users are of: "" for a caller
// without one.
func userKey(r *http.Request) string {
	return access.Caller(r.Context()).KeyID
}

// UserRow is one user's binding, as user-accounts.json has it.
type UserRow struct {
	Key      string    // the gateway key's id, "" for callers without one
	Provider string    // the provider's id
	User     string    // the user's id, as the caller sent it
	Who      string    // the account or key: candidate.who()
	Account  string    // its user or key's name, as the Routing page names it
	At       time.Time // when the user was bound
}

// UserBindings is every user's binding, read from user-accounts.json as
// it is now, for a process other than the gateway's too (the window, magpie
// web). No file is none; one that doesn't read is an error, never none.
func UserBindings() ([]UserRow, error) {
	b, err := os.ReadFile(usersPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	var out []UserRow
	add := func(key string, ps map[string]map[string]userBinding) {
		for pid, us := range ps {
			for u, b := range us {
				out = append(out, UserRow{Key: key, Provider: pid, User: u, Who: b.Who, Account: b.Account, At: b.At})
			}
		}
	}
	add("", f.Users)
	for k, ps := range f.Keys {
		if k != "" {
			add(k, ps)
		}
	}
	return out, nil
}

// UserSeats is who every account or key p has is, lower-cased, as a
// binding's Who names one; known is false when the saved accounts can't
// be read, and a binding not among them can't be said gone.
func UserSeats(p provider.Provider) (seats map[string]bool, known bool) { return seatsOf(p) }
