package gateway

// A caller's own users, each bound to accounts of a provider (or its
// keys), bound so when the gateway is (--bind=user, bind.go): the caller
// names the user in UserHeader, the first request of that user to the
// provider binds it to the account fewest users are bound to, and from
// then on only its accounts are asked for it, of any of the provider's
// models: its first, then its spares, in order. An account takes a user
// only while it has a seat for it (seatUse.takes): as many users as the
// tier of each, that user's too, shares one with (user_tiers.go, share),
// so a user of a tier with its account alone is bound to one nobody
// else is; with no account of the provider that has one, the user's
// request is a 429. A spare is added, up to
// its tier's max_accounts (user_tiers.go), only when each account it has
// is out a while (resting out
// of its allowance or credit userLongRest or more, at its usage cap, all
// but used up); a rate limit, or a rest back sooner, is the caller's
// error, and nothing else is asked in its place. An account goes from a
// user only when it does — it is no longer on the provider (signed out,
// switched off, removed) — or its vendor turned it away for good (a
// sign-in revoked and marked so, an account restricted or to be
// verified): its first spare is its first then, and a user left with
// none is bound afresh on its next request. A user that sends nothing to
// the provider for userIdle lets go of its accounts there too (sweepIdle),
// for others to take their seats; on its next request it is bound afresh.
//
// Each gateway key's users are its own, apart from another key's and from
// those of callers without a key: the same id sent with two keys is two
// users. The bindings are kept for good in user-accounts.json next to
// providers.json. A file that can't be read is unknown, never "no users":
// what was read before still binds, nothing new is bound and nothing is
// written over it. One edited by hand is read again.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
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

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
	"github.com/yetone/magpie/internal/steady"
)

// UserHeader names the caller's own user a request is for, to keep that
// user on accounts of its own of each provider. It goes no further than
// magpie.
const UserHeader = "X-Magpie-User"

// userMax is the longest user UserHeader takes.
const userMax = 128

// UserLimitHeader, on an error to a request with a user, says the user
// can't be answered for a limit until a time: the account it is bound to
// (bound to none, the provider's accounts) is out of its allowance
// ("quota"), its credit ("credit"), rate limited ("rate") or at the usage
// cap set on it in magpie ("cap"). UserUntilHeader is when it is back,
// RFC 3339 in UTC, as Retry-After is in seconds: the caller stops sending
// the user's requests until then. UserModelHeader names the one model
// out, when the account still answers its others. Neither is there when
// the user's account can answer again at once, or is let go (bound
// afresh next); a limit without UserUntilHeader is one magpie doesn't
// know the end of.
const (
	UserLimitHeader = "X-Magpie-User-Limit"
	UserUntilHeader = "X-Magpie-User-Until"
	UserModelHeader = "X-Magpie-User-Model"
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

// userLimited marks h, an error's headers, with UserLimitHeader limit,
// the one model it is for (model, "" for the account) and, when it is
// still to come, until, to the second after: never earlier than the
// account is back.
func userLimited(h http.Header, limit string, until time.Time, model string) {
	h.Set(UserLimitHeader, limit)
	if model != "" {
		h.Set(UserModelHeader, model)
	}
	if until = until.Add(time.Second - 1).Truncate(time.Second); time.Until(until) > 0 {
		h.Set(UserUntilHeader, until.UTC().Format(time.RFC3339))
		h.Set("Retry-After", strconv.Itoa(int(math.Ceil(time.Until(until).Seconds()))))
	}
}

// userRefusal is why a user's request goes to no account: its status and
// message, and its UserLimitHeader, when the account is back and the one
// model out ("" for the account), when it is a limit.
type userRefusal struct {
	status int
	msg    string
	limit  string
	until  time.Time
	model  string
	// unknown: what the user is bound to can't be told (the file doesn't
	// read), a 503 that goes as it is
	unknown bool
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

// userSeat is one account a user is bound to.
type userSeat struct {
	// Who is candidate.who(): "<provider>@<account's user>" for an
	// account, "<provider>#<key id>" for a key
	Who     string    `json:"who"`
	Account string    `json:"account"` // its user (an email, a login) or key's name, as the Routing page names it
	At      time.Time `json:"at"`      // when the user was bound to it
}

// userBinding is the accounts a user is bound to, of one provider: its
// first in Who, Account and At, as a magpie before spares reads it, then
// its spares in More, in the order they were added. Tier is the tier its
// last request named (UserTierHeader, the default without it), for the
// Gateway page; "" from a magpie before tiers.
type userBinding struct {
	Who     string     `json:"who"`
	Account string     `json:"account"`
	At      time.Time  `json:"at"`
	More    []userSeat `json:"more,omitempty"`
	Tier    string     `json:"tier,omitempty"`
	// Used is when the user last sent the provider a request, to within
	// userUsedEvery; zero from a magpie before it, stamped now when seen
	Used time.Time `json:"used,omitzero"`
}

// seats is b's accounts, its first first; one named twice, or by
// nobody (edited by hand), counts once or not at all.
func (b userBinding) seats() []userSeat {
	var out []userSeat
	for _, s := range append([]userSeat{{Who: b.Who, Account: b.Account, At: b.At}}, b.More...) {
		if s.Who != "" && !slices.ContainsFunc(out, func(o userSeat) bool { return strings.EqualFold(o.Who, s.Who) }) {
			out = append(out, s)
		}
	}
	return out
}

// bindingOf is the binding of seats, the first of them its first.
func bindingOf(seats []userSeat) userBinding {
	if len(seats) == 0 {
		return userBinding{}
	}
	b := userBinding{Who: seats[0].Who, Account: seats[0].Account, At: seats[0].At}
	if len(seats) > 1 {
		b.More = slices.Clone(seats[1:])
	}
	return b
}

// withSeats is b bound to seats instead, its tier and its last use kept.
func (b userBinding) withSeats(seats []userSeat) userBinding {
	n := bindingOf(seats)
	n.Tier, n.Used = b.Tier, b.Used
	return n
}

// userIdle is how long a user sends a provider nothing before it lets go
// of its accounts there (sweepIdle).
const userIdle = 3 * 24 * time.Hour

// UserIdleHours is userIdle in hours, as the gateway tells the Gateway page.
const UserIdleHours = int(userIdle / time.Hour)

// userUsedEvery is how stale a binding's Used may get before a request
// writes it again: the file is written once an hour a user at most, not
// on every request.
const userUsedEvery = time.Hour

// userSeen is when each user last sent a provider a request in this
// process, gateway key, provider and user (seenKey) → when: kept while
// user-accounts.json doesn't read too, so a user served from memory then
// isn't idle for the file's older `used` once it reads again.
var userSeen = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func seenKey(keyID, pid, user string) string { return keyID + "\x00" + pid + "\x00" + user }

// noteSeen notes now as user's last request to provider pid, of keyID.
func noteSeen(keyID, pid, user string, now time.Time) {
	userSeen.Lock()
	userSeen.m[seenKey(keyID, pid, user)] = now
	userSeen.Unlock()
}

// lastUse is when b, user's of keyID to provider pid, was last in use, as
// of now: its `used` (one ahead of now, edited or the clock set back, is
// now), this process's last request of it (userSeen), and the end of a
// rest one of its accounts is on, which the user was told to wait out.
func lastUse(b userBinding, keyID, pid, user string, now time.Time) time.Time {
	last := b.Used
	if last.After(now) {
		last = now
	}
	userSeen.Lock()
	if t := userSeen.m[seenKey(keyID, pid, user)]; t.After(last) {
		last = t
	}
	userSeen.Unlock()
	for _, st := range b.seats() {
		if r, ok := restOf(strings.ToLower(st.Who)); ok && r.Until.After(last) {
			last = r.Until
		}
	}
	return last
}

// userGap is how long a provider gets no request from any user before
// that time stops counting toward anyone's userIdle: a gateway down, or
// nobody needing a seat, for a long weekend lets nobody go for it.
const userGap = 12 * time.Hour

// sweepIdle lets go of every binding to provider pid, of every gateway key,
// whose user has sent it nothing for userIdle (lastUse), but user's of
// keyID, whose request this is. When no user sent pid anything for
// userGap or more before this request, that gap doesn't count: every
// binding's Used moves on by it, as if the clock had stopped. One with no
// Used yet (bound before it was kept) is stamped now instead, so an
// upgrade frees nobody at once. changed says a binding was, for the file
// to be written. Called with userBinds held, never while the file
// doesn't read.
func sweepIdle(pid, keyID, user string, now time.Time) (changed bool) {
	last := func(k, u string, b userBinding) time.Time {
		if t := lastUse(b, k, pid, u, now); t.Before(now) {
			return t
		}
		return now
	}
	var prev time.Time
	for k, ps := range userBinds.m {
		for u, b := range ps[pid] {
			if t := last(k, u, b); !b.Used.IsZero() && t.After(prev) {
				prev = t
			}
		}
	}
	if gap := now.Sub(prev); !prev.IsZero() && gap >= userGap {
		for k, ps := range userBinds.m {
			for u, b := range ps[pid] {
				if !b.Used.IsZero() {
					b.Used = last(k, u, b).Add(gap)
					if b.Used.After(now) {
						b.Used = now
					}
					ps[pid][u] = b
					changed = true
				}
			}
		}
		log.Printf("user accounts: no user sent %s a request for %s: that time doesn't count toward any user's %s idle", pid, gap.Round(time.Minute), userIdle)
	}
	gone := 0
	for k, ps := range userBinds.m {
		for u, b := range ps[pid] {
			switch {
			case k == keyID && u == user:
			case b.Used.IsZero():
				b.Used = now
				ps[pid][u] = b
				changed = true
			case now.Sub(last(k, u, b)) >= userIdle:
				delete(ps[pid], u)
				gone++
			}
		}
		if len(ps[pid]) == 0 {
			delete(ps, pid)
		}
	}
	if gone > 0 {
		log.Printf("user accounts: %d user(s) of %s sent it nothing for %s: their accounts there are let go, each bound afresh on its next request", gone, pid, userIdle)
	}
	return changed || gone > 0
}

// touchUsed keeps now as when user, of keyID, last sent provider pid a
// request, when it was kept userUsedEvery ago or more; changed says it
// was. Called with userBinds held.
func touchUsed(keyID, pid, user string, now time.Time) (changed bool) {
	b, ok := userBinds.m[keyID][pid][user]
	if !ok || now.Sub(b.Used) < userUsedEvery && !b.Used.After(now) {
		return false
	}
	b.Used = now
	userBinds.m[keyID][pid][user] = b
	return true
}

// seatIn says who is one of seats.
func seatIn(seats []userSeat, who string) bool {
	return slices.ContainsFunc(seats, func(s userSeat) bool { return strings.EqualFold(s.Who, who) })
}

// userLongRest is how long an account a user is bound to must still rest,
// out of its allowance or its credit, for the user to be given a spare:
// one rate limited, or back sooner, is waited for.
const userLongRest = 30 * time.Minute

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
	Account string `json:"account,omitempty"` // its first account
	// Spares: the accounts it is bound to after its first, in order
	Spares []string `json:"spares,omitempty"`
	// Why: "kept" (bound before), "bound" (bound now), "gone" (an
	// account it was bound to is gone, and it is bound now to the
	// others, or to another), "added" (none of its accounts could
	// answer, and a spare was added), "none" (no account of the provider
	// could be bound now), "full" (none has a seat for its tier, or those
	// that have rest)
	Why string `json:"why"`
	// Tier: the request's tier (UserTierHeader, the default without it)
	Tier string `json:"tier,omitempty"`

	// what growUser adds a spare to: the gateway key, the provider, the
	// tier and the accounts bound when the request came; left, those of
	// them left out of the plan, for a limit to say when the first is back
	key, pid string
	max      int
	tier     Tier
	seats    []userSeat
	left     []Weighed
}

// shown sets u's accounts to seats, as the trace tells them.
func (u *UserBound) shown(seats []userSeat) {
	u.seats, u.Account, u.Spares = seats, "", nil
	for i, s := range seats {
		if i == 0 {
			u.Account = s.Account
		} else {
			u.Spares = append(u.Spares, s.Account)
		}
	}
}

// userOf is the user a request names in the first of userHeaders it
// sends, "" for none; bad is why a value can't be taken.
func userOf(h http.Header) (user, bad string) {
	for _, name := range userHeaders() {
		if user = strings.TrimSpace(h.Get(name)); user == "" {
			continue
		}
		if bad := idBad(name, user); bad != "" {
			return "", bad
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
		// another file's users: what this process saw of the last one's
		// isn't theirs
		userSeen.Lock()
		clear(userSeen.m)
		userSeen.Unlock()
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
	for _, c := range seatCandidates(p) {
		seats[strings.ToLower(c.who())] = true
	}
	// the account the agent is signed in to counts paused or not: a pause
	// is the user's to undo
	return seats, p.Account == nil || p.IsPlugin() || provider.LoginsKnown()
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

// bindUser narrows cands to the accounts of p that user, of the gateway
// key keyID, is bound to: its first, then its spares, in that order,
// each while it doesn't rest, one all but used up after the rest. A user
// bound to none, or only to accounts that are gone, is bound first to the
// one of cands with a seat for tier fewest users of p are bound to
// (firstOf); with none that has one, or those that have resting, it is
// refused a 429 (fullRefusal). One whose
// accounts are all out is given a spare by growUser, up to tier's
// max_accounts, after strictKeep: narrowed to none here, the refusal is
// its. The binding keeps tier's name, written only when it changed.
func bindUser(keyID, user string, tier Tier, p provider.Provider, cands []candidate, pl planned) ([]candidate, planned, *UserBound, userRefusal) {
	tiers, _ := loadTiers() // read already by tierOf: what it said
	// which of p's accounts have to be signed in again, read before the
	// lock: logins.json isn't read while userBinds is held
	lapsedWho := lapsedOf(p.ID, cands, pl)
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil {
		return nil, pl, nil, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: magpie can't read user-accounts.json, so it doesn't know which account %q is bound to; nothing was sent. Its log says why", UserHeader, user)}
	}
	ub := &UserBound{User: user, Tier: tier.Name, key: keyID, pid: p.ID, max: tier.MaxAccounts, tier: tier}
	// users idle for days let go of their seats before any are counted;
	// never written over a file that doesn't read, but this request seen
	// all the same
	now := time.Now().UTC()
	defer noteSeen(keyID, p.ID, user, now)
	if !userBinds.stale && sweepIdle(p.ID, keyID, user, now) {
		saveUsersLogged()
	}
	seats := userBinds.m[keyID][p.ID][user].seats()
	// an account not among cands may be gone: signed out, switched off or
	// removed (seatsOf), unknown while the saved accounts can't be read.
	// One whose sign-in has to be made again stays bound (the owner's
	// decision, 2026-10-29): never asked while it is, the user's other
	// accounts take its requests, or a spare added beside it (growUser),
	// and signed in again it is the user's as before
	var kept, lapsed []userSeat
	var on map[string]bool
	known := true
	for _, st := range seats {
		if why := lapsedWho[strings.ToLower(st.Who)]; why != "" {
			noteSeatLapsed(st.Who, why)
			lapsed = append(lapsed, st)
			continue
		}
		clearSeatLapsed(st.Who)
		if slices.ContainsFunc(cands, func(c candidate) bool { return c.p.ID == p.ID && strings.EqualFold(c.who(), st.Who) }) {
			kept = append(kept, st)
			continue
		}
		if on == nil {
			on, known = seatsOf(p)
		}
		if !known || on[strings.ToLower(st.Who)] {
			kept = append(kept, st)
		}
	}
	if len(kept)+len(lapsed) < len(seats) {
		if userBinds.stale {
			return nil, pl, ub, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: an account %q was bound to is gone, and user-accounts.json can't be read to bind it again; nothing was sent. magpie's log says why", UserHeader, user)}
		}
		// the first gone, its first spare is its first
		setSeats(keyID, p.ID, user, slices.DeleteFunc(slices.Clone(seats), func(st userSeat) bool { return !seatIn(kept, st.Who) && !seatIn(lapsed, st.Who) }))
		setTier(keyID, p.ID, user, tier.Name)
		saveUsersLogged()
		ub.Why = "gone"
	}
	if len(kept) == 0 && len(lapsed) > 0 {
		// bound to none that can be asked now: growUser adds a spare
		// beside them, its tier letting it have one more, or says why
		// none is asked, a 429 that names no sign-in. With the file not
		// reading, no spare can be added: unknown, a 503
		if userBinds.stale {
			return nil, pl, ub, userRefusal{status: http.StatusServiceUnavailable, unknown: true, msg: fmt.Sprintf("%s: user-accounts.json can't be read, so no other account can be added for %q now; nothing was sent. magpie's log says why", UserHeader, user)}
		}
		// its use and tier kept as any bound user's, so it isn't let go
		// as idle while its accounts wait to be signed in again
		if tiered, used := setTier(keyID, p.ID, user, tier.Name), touchUsed(keyID, p.ID, user, now); tiered || used {
			saveUsersLogged()
		}
		ub.Why = "lapsed"
		ub.shown(lapsed)
		out, opl := ownedBy(lapsed, p.ID, nil, pl)
		ub.left = opl.left
		return out, opl, ub, userRefusal{}
	}
	if len(kept) == 0 {
		if userBinds.stale {
			return nil, pl, ub, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: user-accounts.json can't be read, so %q isn't bound to an account now; nothing was sent. magpie's log says why", UserHeader, user)}
		}
		uses := seatUsesOf(p.ID, tiers)
		pick := firstOf(p.ID, cands, pl, tier, uses, lapsedWho)
		if pick < 0 {
			if slices.ContainsFunc(cands, func(c candidate) bool { return c.p.ID == p.ID && !uses.of(c).takes(tier.Share) }) {
				// some are another's alone, or shared by as many as can:
				// those others' accounts are never asked for it
				ub.Why = "full"
				return nil, pl, ub, fullRefusal(user, tier, p.ID, cands, pl, uses, lapsedWho)
			}
			// none can answer now: routed as it would be among the
			// provider's, bound once one can; never on to another's
			// accounts (a fallback), whose seats aren't counted here
			ub.Why = "none"
			cands, pl = onlyOf(p.ID, cands, pl)
			return cands, pl, ub, userRefusal{}
		}
		kept = []userSeat{{Who: cands[pick].who(), Account: shownAs(cands[pick]), At: time.Now().UTC()}}
		setSeats(keyID, p.ID, user, kept)
		setTier(keyID, p.ID, user, tier.Name)
		saveUsersLogged()
		if ub.Why == "" {
			ub.Why = "bound"
		}
	}
	if ub.Why == "" {
		ub.Why = "kept"
	}
	// another tier than its last request's, or its last use an hour or
	// more ago: kept, never written over a file that doesn't read
	if !userBinds.stale {
		if tiered, used := setTier(keyID, p.ID, user, tier.Name), touchUsed(keyID, p.ID, user, now); tiered || used {
			saveUsersLogged()
		}
	}
	// the user's accounts as bound, those to be signed in again among
	// them (counted toward its tier, kept for when they are back); asked
	// of those that can be, in their order
	all := slices.DeleteFunc(slices.Clone(seats), func(st userSeat) bool { return !seatIn(kept, st.Who) && !seatIn(lapsed, st.Who) })
	if len(all) == 0 {
		all = kept
	}
	ub.shown(all)
	out, opl := ownedBy(kept, p.ID, cands, pl)
	ub.left = opl.left
	return out, opl, ub, userRefusal{}
}

// lapsedOf is why each account of provider pid has to be signed in
// again, by its who lower-cased: left out of the plan so (leaveLapsed),
// or among cands brought back as none other could answer (lapsedBack).
// It reads logins.json: never asked with userBinds held.
func lapsedOf(pid string, cands []candidate, pl planned) map[string]string {
	out := map[string]string{}
	for _, w := range pl.left {
		if w.Lapsed != "" && w.Provider == pid && w.Kind == "account" {
			out[strings.ToLower(pid+"@"+w.Who)] = w.Lapsed
		}
	}
	var accts []*provider.Account
	var who []string
	for _, c := range cands {
		if c.p.ID == pid && c.p.Account != nil {
			accts, who = append(accts, c.p.Account), append(who, strings.ToLower(c.who()))
		}
	}
	if len(accts) == 0 {
		return out // none to read logins.json for
	}
	for i, why := range provider.SignInLapsedOf(accts) {
		if why != "" {
			out[who[i]] = why
		}
	}
	return out
}

// seatLapsedSaid is the lapse of each user's account logged, by its who
// lower-cased, for it to be logged once a change, not once a request.
var seatLapsedSaid sync.Map

// noteSeatLapsed logs that a user's account who ("<provider>@<account>")
// is passed over until it is signed in again, why: the operator's to see
// (its row on the Providers page says Sign-in required too), never the
// caller's.
func noteSeatLapsed(who, why string) {
	if was, loaded := seatLapsedSaid.Swap(strings.ToLower(who), why); loaded && was == why {
		return
	}
	pid, account, _ := strings.Cut(who, "@")
	log.Printf("user accounts: %s account %s has to be signed in again (%s): its users stay bound to it, and are served by their other accounts or a spare until it is", pid, account, why)
}

// noteTryLapsed logs c's account as one to be signed in again when a try
// on it failed so, once its mark says so: a key isn't an account to sign
// in again, and a 401 that marks nothing (a refresh mended it) or whose
// mark bindUser logs on the next request isn't logged twice.
func noteTryLapsed(c candidate) {
	if c.p.Account == nil || c.p.Account.User == "" {
		return
	}
	if why := provider.SignInLapsed(c.p.Account); why != "" {
		noteSeatLapsed(c.who(), why)
	}
}

// clearSeatLapsed forgets the lapse logged of who, back among the user's
// accounts that can be asked: a lapse again is logged again.
func clearSeatLapsed(who string) { seatLapsedSaid.Delete(strings.ToLower(who)) }

// lapsedRetry is when a user whose accounts all have to be signed in
// again, or are out, is told to try again with none known back sooner:
// a sign-in made again is a person's to do.
const lapsedRetry = 5 * time.Minute

// setTier keeps tier as user's, of the gateway key keyID, with its
// binding to accounts of provider pid; changed says it wasn't before.
// Called with userBinds held.
func setTier(keyID, pid, user, tier string) (changed bool) {
	b, ok := userBinds.m[keyID][pid][user]
	if !ok || b.Tier == tier {
		return false
	}
	b.Tier = tier
	userBinds.m[keyID][pid][user] = b
	return true
}

// setSeats binds user, of the gateway key keyID, to seats of provider pid,
// or to none, its tier kept. Called with userBinds held.
func setSeats(keyID, pid, user string, seats []userSeat) {
	if len(seats) == 0 {
		delete(userBinds.m[keyID][pid], user)
		return
	}
	if userBinds.m == nil {
		userBinds.m = map[string]map[string]map[string]userBinding{}
	}
	if userBinds.m[keyID] == nil {
		userBinds.m[keyID] = map[string]map[string]userBinding{}
	}
	if userBinds.m[keyID][pid] == nil {
		userBinds.m[keyID][pid] = map[string]userBinding{}
	}
	userBinds.m[keyID][pid][user] = userBinds.m[keyID][pid][user].withSeats(seats)
}

// seatUse is how an account is taken: by n users of every key, as a
// first or a spare, the least share of their tiers share (0 for no
// limit).
type seatUse struct{ n, share int }

// takes says the account has a seat for one more user, of a tier with
// share: as many as its share and each user's on it let it have.
func (u seatUse) takes(share int) bool {
	n := u.n + 1
	return (share == 0 || n <= share) && (u.share == 0 || n <= u.share)
}

// seatUses is how each account of provider pid is taken, lower-cased
// who → its use, each user's share its tier's in tiers (Tiers.shareOf).
// Called with userBinds held.
type seatUses map[string]seatUse

func seatUsesOf(pid string, tiers Tiers) seatUses {
	uses := seatUses{}
	for _, ps := range userBinds.m {
		for _, b := range ps[pid] {
			share := tiers.shareOf(b.Tier)
			for _, s := range b.seats() {
				who := strings.ToLower(s.Who)
				u := uses[who]
				u.n++
				if share > 0 && (u.share == 0 || share < u.share) {
					u.share = share
				}
				uses[who] = u
			}
		}
	}
	return uses
}

// of is how c's account is taken.
func (uses seatUses) of(c candidate) seatUse { return uses[strings.ToLower(c.who())] }

// firstOf is the account of provider pid in cands to bind a user of tier
// to first: as spareOf picks, and when every one is past routes.LowShare,
// the one of those not resting nor marked revoked all the same. -1 for
// none. Called with userBinds held.
func firstOf(pid string, cands []candidate, pl planned, tier Tier, uses seatUses, lapsedWho map[string]string) int {
	if i := spareOf(pid, cands, pl, nil, true, tier, uses, lapsedWho); i >= 0 {
		return i
	}
	return spareOf(pid, cands, pl, nil, false, tier, uses, lapsedWho)
}

// spareOf is the account of provider pid in cands to bind a user of tier
// to, beside seats: of those not among them, with a seat for it
// (seatUse.takes), not resting, not to be signed in again (lapsedWho,
// lapsedOf's, read before userBinds was locked)
// and, with room, under routes.LowShare of each window counting the
// model, the one fewest users of every key are bound to; for a tier that
// shares one with a few (share 2 or more), the one most are, so a user
// that shares leaves alone the accounts nobody is on, for the users that
// have theirs alone. The plan's order (Smart's) between those alike.
// Those held at a cap aren't among cands. -1 for none. Called with
// userBinds held.
func spareOf(pid string, cands []candidate, pl planned, seats []userSeat, room bool, tier Tier, uses seatUses, lapsedWho map[string]string) int {
	pick := -1
	for i, c := range cands {
		if c.p.ID != pid || seatIn(seats, c.who()) || !uses.of(c).takes(tier.Share) || resting(c) || lapsedWho[strings.ToLower(c.who())] != "" {
			continue
		}
		if room && i < len(pl.order) && pl.order[i].Known && pl.order[i].Used >= routes.LowShare {
			continue
		}
		n, at := uses.of(c).n, uses.of(cands[max(pick, 0)]).n
		if pick < 0 || tier.Share >= 2 && n > at || tier.Share < 2 && n < at {
			pick = i
		}
	}
	return pick
}

// shares says how many users tier lets an account it is on be bound to.
func (t Tier) shares() string {
	switch t.Share {
	case 0:
		return fmt.Sprintf("tier %s shares one with any number of users", t.Name)
	case 1:
		return fmt.Sprintf("tier %s has one alone", t.Name)
	case 2:
		return fmt.Sprintf("tier %s shares one with one other user at most", t.Name)
	}
	return fmt.Sprintf("tier %s shares one with %d other users at most", t.Name, t.Share-1)
}

// onlyOf is cands narrowed to provider pid's, pl's order with them.
func onlyOf(pid string, cands []candidate, pl planned) ([]candidate, planned) {
	var out []candidate
	var order []Weighed
	for i, c := range cands {
		if c.p.ID == pid {
			out = append(out, c)
			if i < len(pl.order) {
				order = append(order, pl.order[i])
			}
		}
	}
	pl.order = order
	return out, pl
}

// fullRefusal is why user, of tier, is bound to no account of provider
// pid: none of cands has a seat for it but those resting, signed out by
// their vendor or held at a cap (pl's held and capped), and those taken
// are never asked for it. A 429, the "quota" of the pool: until the first
// with a seat is back, a rest's end or a window's renewal, else unknown,
// as a user leaves an account only when it goes.
func fullRefusal(user string, tier Tier, pid string, cands []candidate, pl planned, uses seatUses, lapsedWho map[string]string) userRefusal {
	r := userRefusal{status: http.StatusTooManyRequests, limit: failQuota}
	var why []string
	marked := false
	for _, c := range cands {
		if c.p.ID != pid || !uses.of(c).takes(tier.Share) {
			continue
		}
		if rest, ok := restingOf(c); ok {
			if r.until.IsZero() || rest.Until.Before(r.until) {
				r.limit, r.until, r.model = cmp.Or(userLimit(rest.Why), failQuota), rest.Until, rest.Model
				why = []string{fmt.Sprintf("rests until %s (%s)", rest.Until.Format(time.RFC3339), rest.Why)}
			}
		} else if lapsedWho[strings.ToLower(c.who())] != "" {
			marked = true
		}
	}
	var held []Weighed
	for _, c := range append(slices.Clone(pl.held), pl.capped...) {
		if c.p.ID != pid || !uses.of(c).takes(tier.Share) {
			continue
		}
		for _, w := range pl.left {
			if leftIs(w, pid, c.who()) && (w.Capped > 0 || w.NoCredits) && !slices.ContainsFunc(held, func(h Weighed) bool { return h.ID == w.ID && h.Who == w.Who }) {
				held = append(held, w)
			}
		}
	}
	if len(held) > 0 {
		msg, back := cappedError(held[0].Model, held, time.Now())
		if !back.IsZero() && (r.until.IsZero() || back.Before(r.until)) {
			r.limit, r.until, r.model = cappedLimit(held), back, ""
			why = []string{msg}
		} else if len(why) == 0 {
			r.limit, why = cappedLimit(held), []string{msg}
		}
	}
	if marked {
		why = append(why, "one is signed out by its vendor")
	}
	if len(why) == 0 {
		r.msg = fmt.Sprintf("%s: no account of the provider is free for %q: each is taken by as many users as can share it (its %s, %s); nothing was sent", UserHeader, user, tier.shares(), UserTierHeader)
		return r
	}
	r.msg = fmt.Sprintf("%s: no account of the provider is free for %q: of those its %s (%s) can share, the first back %s, and the others are taken; nothing was sent", UserHeader, user, tier.shares(), UserTierHeader, strings.Join(why, "; "))
	return r
}

// growUser gives u, bound to accounts of p none of which can answer the
// request now, a spare of pool (the candidates before bindUser narrowed
// them), and asks it first: when each of its accounts is out for a while
// (resting out of its allowance or credit userLongRest or more, held at a
// usage cap, all but used up), it has fewer than its tier's max_accounts
// (u.max; a user with as many or more, its tier lowered since, keeps them
// and is given none), and one of pool not among them has room and a seat
// for its tier (spareOf).
// cands are those of its accounts left, all but used up, asked after the
// spare; the refusal is why none is asked, with none left. Once a
// request: grew says it added one.
func growUser(u *UserBound, pool []candidate, poolPl planned, cands []candidate, pl planned, failed ...string) (_ []candidate, _ planned, _ *UserBound, grew bool, _ userRefusal) {
	if u == nil || len(u.seats) == 0 || slices.ContainsFunc(pl.order, func(w Weighed) bool { return !w.Known || w.Used < provider.SpentShareOf(w.Routing) }) {
		return cands, pl, u, false, userRefusal{}
	}
	// the lapsed, read before the lock (logins.json isn't read under
	// userBinds), those failed as lapsed in this request (failed: their
	// who) among them though their mark isn't written yet
	lapsedWho := lapsedOf(u.pid, pool, poolPl)
	maps.Copy(lapsedWho, lapsedOf(u.pid, nil, pl))
	for _, who := range failed {
		lapsedWho[strings.ToLower(who)] = cmp.Or(lapsedWho[strings.ToLower(who)], failAuth)
	}
	out, rests, held, lapsed := true, []Rest{}, []Weighed{}, 0
	for _, st := range u.seats {
		if lapsedWho[strings.ToLower(st.Who)] != "" {
			lapsed++ // out till it is signed in again
			continue
		}
		switch r, w, state := seatState(st, u.pid, pool, pl); state {
		case seatSpent:
		case seatRests:
			r.Key = strings.ToLower(st.Who) // whose, for the refusal to say
			rests = append(rests, r)
			if r.Why != failQuota && r.Why != failCredit || time.Until(r.Until) < userLongRest {
				out = false
			}
		case seatHeld:
			held = append(held, w)
		default:
			out = false
		}
	}
	refuse := func(note string) ([]candidate, planned, *UserBound, bool, userRefusal) {
		if len(cands) > 0 {
			return cands, pl, u, false, userRefusal{}
		}
		if lapsed == len(u.seats) {
			// every one of its accounts to be signed in again; with some
			// only, the others say why (a 400 for one without the model)
			return nil, pl, u, false, lapsedRefusal(u, note)
		}
		return nil, pl, u, false, setRefusal(u, rests, held, pl, note)
	}
	if !out {
		return refuse("")
	}
	if len(u.seats) >= u.max {
		return refuse(u.atTier(len(u.seats)))
	}
	tiers, _ := loadTiers()
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil || userBinds.stale {
		if lapsed > 0 && len(cands) == 0 {
			// unknown, not a limit: its accounts are to be signed in
			// again, and whether one could be added can't be told
			return nil, pl, u, false, userRefusal{status: http.StatusServiceUnavailable, unknown: true, msg: fmt.Sprintf("%s: user-accounts.json can't be read, so no other account can be added for %q now; nothing was sent. magpie's log says why", UserHeader, u.User)}
		}
		return refuse("user-accounts.json can't be read to add a spare")
	}
	now := userBinds.m[u.key][u.pid][u.User].seats()
	// another request of the user's added a spare since this one was
	// bound: that one, if it can answer, not another beside it
	for _, st := range now {
		if seatIn(u.seats, st.Who) || lapsedWho[strings.ToLower(st.Who)] != "" {
			continue
		}
		i := slices.IndexFunc(pool, func(c candidate) bool { return c.p.ID == u.pid && strings.EqualFold(c.who(), st.Who) && !resting(c) })
		if i < 0 {
			continue
		}
		added, addedPl := ownedBy([]userSeat{st}, u.pid, pool, poolPl)
		grown := *u
		grown.Why = "added"
		grown.shown(now)
		grown.left = pl.left
		return append(added, cands...), planned{order: append(addedPl.order, pl.order...), left: pl.left, held: pl.held, capped: pl.capped, lapsed: pl.lapsed}, &grown, true, userRefusal{}
	}
	if len(now) >= u.max {
		return refuse(u.atTier(len(now)))
	}
	uses := seatUsesOf(u.pid, tiers)
	pick := spareOf(u.pid, pool, poolPl, now, true, u.tier, uses, lapsedWho)
	if pick < 0 {
		if !slices.ContainsFunc(pool, func(c candidate) bool {
			return c.p.ID == u.pid && !seatIn(now, c.who()) && uses.of(c).takes(u.tier.Share)
		}) {
			return refuse(fmt.Sprintf("every other account of the provider is taken by as many users as can share it (its %s, %s)", u.tier.shares(), UserTierHeader))
		}
		return refuse("no other account of the provider has room to be added")
	}
	seats := append(slices.Clone(now), userSeat{Who: pool[pick].who(), Account: shownAs(pool[pick]), At: time.Now().UTC()})
	setSeats(u.key, u.pid, u.User, seats)
	saveUsersLogged()
	added, addedPl := ownedBy(seats[len(seats)-1:], u.pid, pool, poolPl)
	grown := *u
	grown.Why = "added"
	grown.shown(seats)
	grown.left = pl.left
	return append(added, cands...), planned{order: append(addedPl.order, pl.order...), left: pl.left, held: pl.held, capped: pl.capped, lapsed: pl.lapsed}, &grown, true, userRefusal{}
}

// userTryRefusal is what the caller of a user's request is answered when
// the last of its accounts tried turned out lapsed and no spare was added:
// growUser's refusal when it is a limit or unknown (the file not reading),
// else the lapse's 429 — a refusal naming the user's accounts (paused,
// not serving the model) isn't the caller's when what failed was a
// sign-in.
func userTryRefusal(u *UserBound, refused userRefusal) userRefusal {
	if refused.status == http.StatusTooManyRequests || refused.unknown {
		return refused
	}
	return lapsedRefusal(u, "")
}

// lapsedRefusal is why none of the accounts u is bound to is asked when
// they all have to be signed in again: a 429 back in lapsedRetry, which
// the caller is told as any limit (userLimitSaid), never that a sign-in
// lapsed; the call's error, the operator's, says it and why no spare was
// added (note).
func lapsedRefusal(u *UserBound, note string) userRefusal {
	msg := fmt.Sprintf("%s: %q is bound to %s, to be signed in again; no other account is tried in its place", UserHeader, u.User, strings.Join(accountsOf(u.seats), ", "))
	if note != "" {
		msg += ". No spare was added: " + note
	}
	return userRefusal{status: http.StatusTooManyRequests, msg: msg, limit: failQuota, until: time.Now().Add(lapsedRetry)}
}

// atTier says u has n accounts of the provider, as many as its tier lets
// it have or more (lowered since): why no spare is added.
func (u *UserBound) atTier(n int) string {
	at, accounts := "at", "accounts"
	if n > u.max {
		at = "past"
	}
	if u.max == 1 {
		accounts = "account"
	}
	return fmt.Sprintf("it has %d of the provider's accounts, %s its tier %s's %d %s (%s)", n, at, u.Tier, u.max, accounts, UserTierHeader)
}

// How one of a user's accounts stands, for growUser.
const (
	seatOther = iota // paused, held from the key, not serving the model, narrowed away (sealed elsewhere)
	seatSpent        // asked, all but used up
	seatRests        // resting
	seatHeld         // left out at a usage cap, or held as it won't spend its credits
)

// seatState is how the account st of provider pid stands now: resting
// (its rest), left out held (its row in pl.left) or all but used up, of
// pool's candidates and those narrowed to pl.
func seatState(st userSeat, pid string, pool []candidate, pl planned) (Rest, Weighed, int) {
	for _, w := range pl.order {
		if leftIs(w, pid, st.Who) {
			return Rest{}, w, seatSpent
		}
	}
	for _, c := range pool {
		if c.p.ID == pid && strings.EqualFold(c.who(), st.Who) {
			if r, ok := restingOf(c); ok {
				return r, Weighed{}, seatRests
			}
		}
	}
	for _, w := range pl.left {
		if leftIs(w, pid, st.Who) && (w.Capped > 0 || w.NoCredits) {
			return Rest{}, w, seatHeld
		}
	}
	return Rest{}, Weighed{}, seatOther
}

// setRefusal is why none of the accounts u is bound to can take the
// request: those resting (rests) and held at a cap (held), the first of
// them back the user's limit until then, and note why no spare was added.
// With neither, why its first can't (boundRefusal).
func setRefusal(u *UserBound, rests []Rest, held []Weighed, pl planned, note string) userRefusal {
	b := bindingOf(u.seats)
	if len(rests) == 0 && len(held) == 0 {
		return boundRefusal(u.User, b, u.pid, nil, pl)
	}
	var r userRefusal
	if len(rests) > 0 {
		r = boundRefusal(u.User, b, u.pid, rests, pl)
	}
	if len(held) > 0 {
		msg, back := cappedError(held[0].Model, held, time.Now())
		if r.status == 0 || !back.IsZero() && (r.until.IsZero() || back.Before(r.until)) {
			r.limit, r.until, r.model = cappedLimit(held), back, ""
		}
		if r.status == 0 {
			r.msg = fmt.Sprintf("%s: %q is bound to %s: %s; no other account is tried in its place", UserHeader, u.User, strings.Join(accountsOf(u.seats), ", "), msg)
		} else {
			r.msg += "; and " + msg
		}
		r.status = http.StatusTooManyRequests
	}
	if note != "" {
		r.msg += ". No spare was added: " + note
	}
	return r
}

// accountsOf is how the Routing page names each of seats.
func accountsOf(seats []userSeat) []string {
	out := make([]string, len(seats))
	for i, s := range seats {
		out[i] = s.Account
	}
	return out
}

// boundRefusal is why the accounts b names, which user is bound to, can't
// take the request: resting (rests), left out of the plan, or narrowed
// away before (sealed by another account). Resting, they are back when the
// first of their rests ends: a limit's, the user's limit until then.
func boundRefusal(user string, b userBinding, pid string, rests []Rest, pl planned) userRefusal {
	seats := b.seats()
	names := strings.Join(accountsOf(seats), ", ")
	if len(rests) > 0 {
		said := make([]string, len(rests))
		first := rests[0]
		for i, r := range rests {
			said[i] = fmt.Sprintf("rests until %s (%s)", r.Until.Format(time.RFC3339), r.Why)
			if who := restWho(r, seats); who != "" && len(seats) > 1 {
				said[i] = who + " " + said[i]
			}
			if r.Until.Before(first.Until) {
				first = r
			}
		}
		which := ""
		if len(seats) > 1 {
			which = ": "
			if who := restWho(first, seats); who != "" {
				which = fmt.Sprintf(" (the first back is %s, until %s, for %s): ", who, first.Until.Format(time.RFC3339), cmp.Or(first.Model, "every model"))
			}
		} else {
			which = ", which "
		}
		return userRefusal{status: http.StatusTooManyRequests, msg: fmt.Sprintf("%s: %q is bound to %s%s%s; no other account is tried in its place", UserHeader, user, names, which, strings.Join(said, "; ")),
			limit: userLimit(first.Why), until: first.Until, model: first.Model}
	}
	for _, st := range seats {
		for _, w := range pl.left {
			if !leftIs(w, pid, st.Who) || w.Lapsed != "" {
				continue // one to be signed in again serves the model, once it is
			}
			switch {
			case w.Held:
				return userRefusal{status: http.StatusForbidden, msg: fmt.Sprintf("%s: %q is bound to an account the gateway key may not use", UserHeader, user)}
			case w.Capped > 0 || w.NoCredits:
				msg, back := cappedError(w.Model, []Weighed{w}, time.Now())
				return userRefusal{status: http.StatusTooManyRequests, msg: fmt.Sprintf("%s: %q is bound to %s: %s; no other account is tried in its place", UserHeader, user, names, msg),
					limit: cappedLimit([]Weighed{w}), until: back}
			default:
				return userRefusal{status: http.StatusBadRequest, msg: fmt.Sprintf("%s: %q is bound to %s, which doesn't serve %s", UserHeader, user, names, w.Model)}
			}
		}
	}
	return userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: %q is bound to %s, which can't take this request (paused, or the conversation was sealed by another account); no other account is tried in its place", UserHeader, user, names)}
}

// restWho is which of seats r is the rest of, "" when it can't be told.
func restWho(r Rest, seats []userSeat) string {
	for _, s := range seats {
		if r.Key != "" && strings.HasPrefix(strings.ToLower(r.Key), strings.ToLower(s.Who)) {
			return s.Account
		}
	}
	return ""
}

// userLimitSaid is all a user's 429 says, as Anthropic's own rate limit
// does: which account the user is bound to, its cap and its rests are the
// operator's, kept in the usage log, not the caller's. When it is back is
// in Retry-After and UserUntilHeader.
const userLimitSaid = "This request would exceed your account's rate limit. Please try again later."

// saidToUser is what a caller that sent a user (UserHeader) is told of a
// 429 magpie gives: userLimitSaid, never the accounts, caps and rests in
// msg, which are the operator's (the call's error keeps them). Another
// status, or no user, is msg.
func saidToUser(user string, status int, msg string) string {
	if user != "" && status == http.StatusTooManyRequests {
		return userLimitSaid
	}
	return msg
}

// userRefused writes refused, why a request of the user u goes to none
// of its accounts, with its limit when it is one. A 429 says only
// userLimitSaid; why is in the call's error.
func (s *Server) userRefused(w http.ResponseWriter, from provider.Protocol, call *Call, u *UserBound, refused userRefusal) {
	call.Status, call.Error = refused.status, "user bound: "+u.accountOr()
	if refused.limit != "" {
		userLimited(w.Header(), refused.limit, refused.until, refused.model)
	} else if d := time.Until(refused.until); d > 0 {
		// not a limit (a 503 with none of the user's able to answer):
		// when to try again all the same
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
	}
	msg := refused.msg
	if refused.status == http.StatusTooManyRequests {
		call.Error += ": " + msg
		msg = userLimitSaid
	}
	writeError(w, from, refused.status, msg)
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
	now, until, model := time.Now(), heldBack, ""
	switch {
	case u.restsOn(why) && try.Rest != nil:
		// rested already, as the last key out of its allowance is
		until, model, rested = try.Rest.Until, try.Rest.Model, true
	case u.restsOn(why):
		rest := s.restAfter(c, status, h, body)
		try.Rest, until, model, rested = &rest, rest.Until, rest.Model, true
	default:
		if d := retryAfter(h, now); d > 0 {
			until = now.Add(d)
		}
	}
	// of several accounts, the user is back when the first of them is:
	// at once, with one not resting nor held, when nothing is said
	if rested && len(u.seats) > 1 {
		for _, st := range u.seats {
			if strings.EqualFold(st.Who, c.who()) {
				continue
			}
			r, ok := seatRest(st, c.model)
			if !ok {
				i := slices.IndexFunc(u.left, func(w Weighed) bool { return leftIs(w, u.pid, st.Who) && (w.Capped > 0 || w.NoCredits) })
				if i < 0 {
					return rested
				}
				w := u.left[i]
				r = Rest{Why: cappedLimit([]Weighed{w})}
				if w.CapBack != nil {
					r.Until = *w.CapBack
				}
			}
			if l := cmp.Or(userLimit(r.Why), r.Why); l != "" && !r.Until.IsZero() && r.Until.Before(until) {
				limit, until, model = l, r.Until, r.Model
			}
		}
	}
	userLimited(h, limit, until, model)
	return rested
}

// seatRest is the rest of the account st, or of its model, while it
// rests.
func seatRest(st userSeat, model string) (Rest, bool) {
	who := strings.ToLower(st.Who)
	if r, ok := restOf(who); ok {
		return r, true
	}
	return restOf(who + "/" + model)
}

// cappedFor is the UserLimitHeader and when user, of the gateway key
// keyID, is back when every account of provider pid there is is held
// (ws, the first of them back at back): the first of its own accounts',
// when it is bound to any of them, else the first's.
func cappedFor(keyID, user, pid, model string, ws []Weighed, back time.Time) (string, time.Time) {
	userBinds.Lock()
	var b userBinding
	ok := loadUsers() == nil
	if ok {
		b, ok = userBinds.m[keyID][pid][user]
	}
	userBinds.Unlock()
	var own []Weighed
	for _, w := range ws {
		if ok && (w.Capped > 0 || w.NoCredits) && slices.ContainsFunc(b.seats(), func(s userSeat) bool { return leftIs(w, pid, s.Who) }) {
			own = append(own, w)
		}
	}
	if len(own) == 0 {
		return cappedLimit(ws), back
	}
	_, ownBack := cappedError(model, own, time.Now())
	return cappedLimit(own), ownBack
}

// ownedBy is cands narrowed to seats of provider pid, in their order,
// those resting left out and those all but used up after the rest; pl's
// left, held, capped and unlisted narrowed to them too, for strictKeep to
// bring back only a user's own.
func ownedBy(seats []userSeat, pid string, cands []candidate, pl planned) ([]candidate, planned) {
	var out, spent []candidate
	var order, spentAt []Weighed
	for _, st := range seats {
		for i, c := range cands {
			if c.p.ID != pid || !strings.EqualFold(c.who(), st.Who) || i >= len(pl.order) || resting(c) {
				continue
			}
			if w := pl.order[i]; w.Known && w.Used >= provider.SpentShareOf(c.p.Routing) {
				spent, spentAt = append(spent, c), append(spentAt, w)
				continue
			}
			out, order = append(out, c), append(order, pl.order[i])
		}
	}
	mine := func(c candidate) bool { return c.p.ID == pid && seatIn(seats, c.who()) }
	n := planned{order: append(order, spentAt...)}
	for _, w := range pl.left {
		if slices.ContainsFunc(seats, func(s userSeat) bool { return leftIs(w, pid, s.Who) }) {
			n.left = append(n.left, w)
		}
	}
	for _, c := range pl.held {
		if mine(c) {
			n.held = append(n.held, c)
		}
	}
	for _, c := range pl.capped {
		if mine(c) {
			n.capped = append(n.capped, c)
		}
	}
	for _, c := range pl.lapsed {
		if mine(c) {
			n.lapsed = append(n.lapsed, c)
		}
	}
	for i, c := range pl.unlisted {
		if mine(c) && i < len(pl.unlistedW) {
			n.unlisted, n.unlistedW = append(n.unlisted, c), append(n.unlistedW, pl.unlistedW[i])
		}
	}
	return append(out, spent...), n
}

// unbindTurnedAway lets go of c's account, for every user bound to it,
// when its vendor turned the account away for good (why: restricted, or
// to be verified): a user left with no account is bound afresh on its
// next request. A sign-in to be made again doesn't let go of it (the
// owner's decision, 2026-10-29). Bound by session, the file isn't
// touched.
func (s *Server) unbindTurnedAway(c candidate, why string) {
	if !s.bind.byUser() {
		return
	}
	switch why {
	case failRestricted, failVerify:
	default:
		// a sign-in to be made again (failAuth) included: its users stay
		// bound to it, served by their others or a spare till it is
		// (bindUser), and are its again once it is
		return
	}
	who := c.who()
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil || userBinds.stale {
		return // unknown: nothing is written over it
	}
	// of each user's accounts, that one: its first spare is its first
	// when it was the first, and a user left with none is bound afresh
	gone := 0
	for _, ps := range userBinds.m {
		for u, b := range ps[c.p.ID] {
			seats := b.seats()
			if !seatIn(seats, who) {
				continue
			}
			seats = slices.DeleteFunc(seats, func(s userSeat) bool { return strings.EqualFold(s.Who, who) })
			if len(seats) == 0 {
				delete(ps[c.p.ID], u)
			} else {
				ps[c.p.ID][u] = b.withSeats(seats)
			}
			gone++
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
	Who      string    // its first account or key: candidate.who()
	Account  string    // its user or key's name, as the Routing page names it
	At       time.Time // when the user was bound to it
	// Spares: the accounts it is bound to after its first, in order
	Spares []UserSpare
	// Tier: the tier its last request named, "" from a magpie before
	// tiers (the default's then)
	Tier string
	// Used: when it last sent the provider a request, to within an hour;
	// zero from a magpie before it was kept
	Used time.Time
}

// UserSpare is one of a user's accounts after its first.
type UserSpare struct {
	Who     string    // candidate.who()
	Account string    // as the Routing page names it
	At      time.Time // when it was added
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
	now := time.Now().UTC()
	add := func(key string, ps map[string]map[string]userBinding) {
		for pid, us := range ps {
			for u, b := range us {
				seats := b.seats()
				if len(seats) == 0 {
					continue
				}
				row := UserRow{Key: key, Provider: pid, User: u, Who: seats[0].Who, Account: seats[0].Account, At: seats[0].At, Tier: b.Tier, Used: shownUsed(b.Used, key, pid, u, now)}
				for _, st := range seats[1:] {
					row.Spares = append(row.Spares, UserSpare(st))
				}
				out = append(out, row)
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

// shownUsed is a binding's last use as the Gateway page says it: the
// file's, or this process's later request of it (userSeen: the file is
// written once an hour), never ahead of now (an edited file, a clock set
// back). Zero stays zero: none kept for a binding from before it was.
func shownUsed(used time.Time, keyID, pid, user string, now time.Time) time.Time {
	if used.IsZero() {
		return used
	}
	userSeen.Lock()
	if t := userSeen.m[seenKey(keyID, pid, user)]; t.After(used) {
		used = t
	}
	userSeen.Unlock()
	if used.After(now) {
		used = now
	}
	return used
}

// UserSeats is who every account or key p has is, lower-cased, as a
// binding's Who names one; known is false when the saved accounts can't
// be read, and a binding not among them can't be said gone.
func UserSeats(p provider.Provider) (seats map[string]bool, known bool) { return seatsOf(p) }

// SeatOut is why one of a user's accounts can't answer now, and until
// when: "quota", "credit", "rate" or another failure it rests for, "cap"
// at the usage cap set on it in magpie, "quota" as well when it is held
// as it won't spend its credits. Model is the one model out, "" for the
// account; Until is zero when it isn't known. CapUnknown: nothing
// rests, but a usage cap set on it can't be told, as its windows weren't
// read in this process.
type SeatOut struct {
	Why        string
	Until      time.Time
	Model      string
	CapUnknown bool
}

// UserSeatOut is why the account or key who of p, as a binding names it,
// can't answer now; Why "" while it can. rests is what rests in the
// process serving the gateway (Rests, or OwnerRests from another), nil
// when that isn't known: then only a usage cap is told, and Why "" is
// unknown, never "it can". found is false when p has no such account.
func UserSeatOut(p provider.Provider, who string, rests map[string]Rest) (out SeatOut, found bool) {
	var c candidate
	for _, x := range seatCandidates(p) {
		if strings.EqualFold(x.who(), who) {
			c, found = x, true
			break
		}
	}
	if !found {
		return SeatOut{}, false
	}
	key := c.restKey()
	if r, ok := rests[key]; ok {
		return SeatOut{Why: cmp.Or(r.Why, failOther), Until: r.Until, Model: r.Model}, true
	}
	// one of its models resting, the first back said
	var first Rest
	one := false
	for id, r := range rests {
		if m, ok := strings.CutPrefix(id, key+"/"); ok && (!one || r.Until.Before(first.Until)) {
			first, one = r, true
			first.Model = cmp.Or(r.Model, m)
		}
	}
	if one {
		return SeatOut{Why: cmp.Or(first.Why, failOther), Until: first.Until, Model: first.Model}, true
	}
	if h := capHeld(p, c.p, "", time.Now()); h != nil {
		why := "cap"
		if h.noCredits {
			why = failQuota
		}
		return SeatOut{Why: why, Until: h.back}, true
	}
	// a cap set on it with no reading of its windows here can't be told
	if a := c.p.Account; a != nil && provider.HoldCaps(p, a.Agent, a.User).Holds() {
		if _, read := allowances(a.UsageAgent())[a.User]; !read {
			return SeatOut{CapUnknown: true}, true
		}
	}
	return SeatOut{}, true
}

// seatCandidates is every account or key of p as candidates, as seatsOf
// counts them.
func seatCandidates(p provider.Provider) []candidate {
	if p.Account != nil {
		out := []candidate{{p: p, rest: p.ID}}
		for _, q := range p.AlsoOn() {
			out = append(out, candidate{p: q, rest: p.ID + "@" + q.Account.User})
		}
		return out
	}
	keys := p.KeysOn()
	var out []candidate
	for _, k := range keys {
		rest := p.ID
		if len(keys) > 1 {
			rest += "#" + provider.KeyID(k.Key)
		}
		out = append(out, candidate{p: p.WithKey(k), rest: rest})
	}
	if len(keys) == 0 {
		out = append(out, candidate{p: p, rest: p.ID})
	}
	return out
}
