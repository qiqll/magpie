package gateway

// A caller's own users, each bound to accounts of a provider (or its
// keys), bound so when the gateway is (--bind=user, bind.go): the caller
// names the user in UserHeader, the first request of that user to the
// provider binds it to the account fewest users are bound to, and from
// then on only its accounts are asked for it, of any of the provider's
// models: its first, then its spares, in order. A spare is added, up to
// --bind-max, only when each account it has is out a while (resting out
// of its allowance or credit userLongRest or more, at its usage cap, all
// but used up); a rate limit, or a rest back sooner, is the caller's
// error, and nothing else is asked in its place. An account goes from a
// user only when it does — it is no longer on the provider (signed out,
// switched off, removed) — or its vendor turned it away for good (a
// sign-in revoked and marked so, an account restricted or to be
// verified): its first spare is its first then, and a user left with
// none is bound afresh on its next request.
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
// its spares in More, in the order they were added.
type userBinding struct {
	Who     string     `json:"who"`
	Account string     `json:"account"`
	At      time.Time  `json:"at"`
	More    []userSeat `json:"more,omitempty"`
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
	// could be bound now)
	Why string `json:"why"`

	// what growUser adds a spare to: the gateway key, the provider and
	// the accounts bound when the request came; left, those of them left
	// out of the plan, for a limit to say when the first is back
	key, pid string
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

// bindUser narrows cands to the accounts of p that user, of the gateway
// key keyID, is bound to: its first, then its spares, in that order,
// each while it doesn't rest, one all but used up after the rest. A user
// bound to none, or only to accounts that are gone, is bound first to the
// one of cands fewest users of p are bound to (firstOf). One whose
// accounts are all out is given a spare by growUser, after strictKeep:
// narrowed to none here, the refusal is its.
func bindUser(keyID, user string, p provider.Provider, cands []candidate, pl planned) ([]candidate, planned, *UserBound, userRefusal) {
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil {
		return nil, pl, nil, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: magpie can't read user-accounts.json, so it doesn't know which account %q is bound to; nothing was sent. Its log says why", UserHeader, user)}
	}
	ub := &UserBound{User: user, key: keyID, pid: p.ID}
	seats := userBinds.m[keyID][p.ID][user].seats()
	// an account not among cands may be gone: signed out, switched off or
	// removed (seatsOf), unknown while the saved accounts can't be read
	var kept []userSeat
	var on map[string]bool
	known := true
	for _, st := range seats {
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
	if len(kept) < len(seats) {
		if userBinds.stale {
			return nil, pl, ub, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: an account %q was bound to is gone, and user-accounts.json can't be read to bind it again; nothing was sent. magpie's log says why", UserHeader, user)}
		}
		// the first gone, its first spare is its first
		setSeats(keyID, p.ID, user, kept)
		saveUsersLogged()
		ub.Why = "gone"
	}
	if len(kept) == 0 {
		if userBinds.stale {
			return nil, pl, ub, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: user-accounts.json can't be read, so %q isn't bound to an account now; nothing was sent. magpie's log says why", UserHeader, user)}
		}
		pick := firstOf(p.ID, cands, pl)
		if pick < 0 {
			// none can answer now: routed as it would be, bound once one can
			ub.Why = "none"
			return cands, pl, ub, userRefusal{}
		}
		kept = []userSeat{{Who: cands[pick].who(), Account: shownAs(cands[pick]), At: time.Now().UTC()}}
		setSeats(keyID, p.ID, user, kept)
		saveUsersLogged()
		if ub.Why == "" {
			ub.Why = "bound"
		}
	}
	if ub.Why == "" {
		ub.Why = "kept"
	}
	ub.shown(kept)
	out, opl := ownedBy(kept, p.ID, cands, pl)
	ub.left = opl.left
	return out, opl, ub, userRefusal{}
}

// setSeats binds user, of the gateway key keyID, to seats of provider pid,
// or to none. Called with userBinds held.
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
	userBinds.m[keyID][pid][user] = bindingOf(seats)
}

// boundCount is how many users of every key each account of provider pid
// is bound to, as a first or a spare. Called with userBinds held.
func boundCount(pid string) map[string]int {
	bound := map[string]int{}
	for _, ps := range userBinds.m {
		for _, b := range ps[pid] {
			for _, s := range b.seats() {
				bound[strings.ToLower(s.Who)]++
			}
		}
	}
	return bound
}

// firstOf is the account of provider pid in cands to bind a user to
// first: as spareOf picks, and when every one is past routes.LowShare, the
// fewest bound of those not resting nor marked revoked all the same. -1
// for none. Called with userBinds held.
func firstOf(pid string, cands []candidate, pl planned) int {
	if i := spareOf(pid, cands, pl, nil, true); i >= 0 {
		return i
	}
	return spareOf(pid, cands, pl, nil, false)
}

// spareOf is the account of provider pid in cands to bind a user to,
// beside seats: of those not among them, not resting, not marked revoked
// (provider.SignInMarked) and, with room, under routes.LowShare of each
// window counting the model, the one fewest users of every key are bound
// to, the plan's order (Smart's) between those alike. Those held at a cap
// aren't among cands. -1 for none. Called with userBinds held.
func spareOf(pid string, cands []candidate, pl planned, seats []userSeat, room bool) int {
	bound := boundCount(pid)
	pick := -1
	for i, c := range cands {
		if c.p.ID != pid || seatIn(seats, c.who()) || resting(c) || provider.SignInMarked(c.p.Account) {
			continue
		}
		if room && i < len(pl.order) && pl.order[i].Known && pl.order[i].Used >= routes.LowShare {
			continue
		}
		if pick < 0 || bound[strings.ToLower(c.who())] < bound[strings.ToLower(cands[pick].who())] {
			pick = i
		}
	}
	return pick
}

// growUser gives u, bound to accounts of p none of which can answer the
// request now, a spare of pool (the candidates before bindUser narrowed
// them), and asks it first: when each of its accounts is out for a while
// (resting out of its allowance or credit userLongRest or more, held at a
// usage cap, all but used up), it has fewer than max, and one of pool not
// among them has room (spareOf). cands are those of its accounts left,
// all but used up, asked after the spare; the refusal is why none is
// asked, with none left. Once a request: grew says it added one.
func growUser(u *UserBound, max int, pool []candidate, poolPl planned, cands []candidate, pl planned) (_ []candidate, _ planned, _ *UserBound, grew bool, _ userRefusal) {
	if u == nil || len(u.seats) == 0 || slices.ContainsFunc(pl.order, func(w Weighed) bool { return !w.Known || w.Used < provider.SpentShareOf(w.Routing) }) {
		return cands, pl, u, false, userRefusal{}
	}
	out, rests, held := true, []Rest{}, []Weighed{}
	for _, st := range u.seats {
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
		return nil, pl, u, false, setRefusal(u, rests, held, pl, note)
	}
	if !out {
		return refuse("")
	}
	if len(u.seats) >= max {
		return refuse(fmt.Sprintf("it has %d of the provider's accounts, the most --bind-max lets a user have", len(u.seats)))
	}
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil || userBinds.stale {
		return refuse("user-accounts.json can't be read to add a spare")
	}
	now := userBinds.m[u.key][u.pid][u.User].seats()
	if len(now) >= max {
		return refuse(fmt.Sprintf("it has %d of the provider's accounts, the most --bind-max lets a user have", len(now)))
	}
	pick := spareOf(u.pid, pool, poolPl, now, true)
	if pick < 0 {
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
	return append(added, cands...), planned{order: append(addedPl.order, pl.order...), left: pl.left, held: pl.held, capped: pl.capped}, &grown, true, userRefusal{}
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
			if !leftIs(w, pid, st.Who) {
				continue
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

// userRefused writes refused, why a request of the user u goes to none
// of its accounts, with its limit when it is one.
func (s *Server) userRefused(w http.ResponseWriter, from provider.Protocol, call *Call, u *UserBound, refused userRefusal) {
	call.Status, call.Error = refused.status, "user bound: "+u.accountOr()
	if refused.limit != "" {
		userLimited(w.Header(), refused.limit, refused.until, refused.model)
	}
	writeError(w, from, refused.status, refused.msg)
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
	for i, c := range pl.unlisted {
		if mine(c) && i < len(pl.unlistedW) {
			n.unlisted, n.unlistedW = append(n.unlisted, c), append(n.unlistedW, pl.unlistedW[i])
		}
	}
	return append(out, spent...), n
}

// unbindTurnedAway lets go of c's account, for every user bound to it,
// when its vendor turned the account away for good (why): a user left
// with no account is bound afresh on its next request. A revoked sign-in counts once it is marked so: one a
// refresh mended is the account as good as before. Bound by session,
// the file isn't touched.
func (s *Server) unbindTurnedAway(c candidate, why string) {
	if !s.bind.byUser() {
		return
	}
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
				ps[c.p.ID][u] = bindingOf(seats)
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
	add := func(key string, ps map[string]map[string]userBinding) {
		for pid, us := range ps {
			for u, b := range us {
				seats := b.seats()
				if len(seats) == 0 {
					continue
				}
				row := UserRow{Key: key, Provider: pid, User: u, Who: seats[0].Who, Account: seats[0].Account, At: seats[0].At}
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

// UserSeats is who every account or key p has is, lower-cased, as a
// binding's Who names one; known is false when the saved accounts can't
// be read, and a binding not among them can't be said gone.
func UserSeats(p provider.Provider) (seats map[string]bool, known bool) { return seatsOf(p) }
