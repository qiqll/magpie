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
// Each gateway key's users are its own, apart from another key's: the same
// id sent with two keys is two users (userGroup). The keys the owner lets
// share their users (access.Key.SharesUsers, T-089) are one set of users,
// with those of callers without a key, this computer's: the same id sent
// with any of them is one user (sharedOf, T-075). The bindings are kept
// for good in user-accounts.json next to providers.json. A file that
// can't be read is unknown, never "no users": what was read before still
// binds, nothing new is bound and nothing is written over it. One edited
// by hand is read again.

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
// cap set on it in magpie ("cap"); or bound to none, no account is free
// to bind it to, each taken by as many users as can share it ("seats"),
// its accounts out of their allowance, it has as many as its tier lets it
// have ("accounts"), or its accounts have to be signed in again
// ("signin").
// UserUntilHeader is when it is back,
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

// The UserLimitHeader of a limit that is no account's own (T-062): the
// pool's seats taken, the tier's max_accounts reached, a sign-in to be
// made again. Before, each was told "quota".
const (
	limitSeats    = "seats"
	limitAccounts = "accounts"
	limitSignin   = "signin"
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
	// said: what the caller is told when msg names the user's accounts,
	// which are the operator's (told)
	said  string
	limit string
	until time.Time
	model string
	// unknown: what the user is bound to can't be told (the file doesn't
	// read), a 503 that goes as it is
	unknown bool
	// none: none of a provider's accounts the user is on can answer now
	// (usersOn), a 503 a group's turn pinned to its user's own lapsed
	// account tells as a limit (groupTurn.refusal)
	none bool
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
// last request named (UserTierHeader), for the Gateway page and for a
// request that names none (bindUser); "" from a magpie before tiers, or
// for a user whose requests never named one: the default.
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

// sharedUsers is the group of users of every gateway key that shares its
// users (access.Key.SharesUsers), and of callers without a key (userGroup).
const sharedUsers = "\x00shared"

// groupOf is the users of the gateway key keyID are one with, of sharers
// (access.UserSharers): those of every key that shares them and of
// callers without a key, as one (sharedUsers), for a key among sharers or
// none; another key's own (its id), apart from every other key's, as the
// owner chose (option B, 2026-11-03, T-089): a name is the caller's to
// send, and the caller of a key that doesn't share its users, sending
// the name another key's user goes by, is another user.
func groupOf(sharers map[string]bool, keyID string) string {
	if keyID == "" || sharers[keyID] {
		return sharedUsers
	}
	return keyID
}

// userGroup is groupOf the keys as user-accounts.json was last read
// (loadUsers). Called with userBinds held.
func userGroup(keyID string) string { return groupOf(userBinds.sharers, keyID) }

// userSharersRead is the keys that share their users, and whether the
// key store read (access.UserSharersRead); a test's to set.
var userSharersRead = access.UserSharersRead

// userSharers is the keys that share their users (userSharersRead).
func userSharers() map[string]bool {
	ids, _ := userSharersRead()
	return ids
}

// sharedOf is user's binding to provider pid as every gateway key of
// keyID's group shares it (userGroup; the owner's option A, 2026-11-03,
// T-075, held to the keys that share their users by option B, T-089): one
// name sent with any of them is one user bound to one set of accounts, so
// a conversation that comes in under two of them stays on one account; a
// key that doesn't share its users reads its own binding alone.
// user-accounts.json keeps it under each key it came with
// (keys.<key>.<provider>.<user>), each written alike (putShared), but
// those a magpie before wrote apart may differ. They are read as one: the
// first is the first of the one used last (Used; the earliest bound
// when alike), then every account of them in their order, each once, as
// spares; more than the tier's max_accounts are kept, and no spare is
// added beside them (growUser). Used is the latest of them. Tier is
// keyID's own, as a tier is a key's (T-015), "" for none there. ok says
// user is bound under any key of the group. Called with userBinds held.
func sharedOf(keyID, pid, user string) (_ userBinding, ok bool) {
	type kept struct {
		key string
		b   userBinding
	}
	var all []kept
	g := userGroup(keyID)
	for k, ps := range userBinds.m {
		if userGroup(k) != g {
			continue
		}
		if b, has := ps[pid][user]; has {
			all = append(all, kept{k, b})
		}
	}
	if len(all) == 0 {
		return userBinding{}, false
	}
	slices.SortFunc(all, func(a, b kept) int {
		if c := b.b.Used.Compare(a.b.Used); c != 0 {
			return c
		}
		if a.b.At.IsZero() != b.b.At.IsZero() {
			if a.b.At.IsZero() {
				return 1
			}
			return -1
		}
		return cmp.Or(a.b.At.Compare(b.b.At), strings.Compare(a.key, b.key))
	})
	var seats []userSeat
	var used time.Time
	for _, e := range all {
		for _, s := range e.b.seats() {
			if !seatIn(seats, s.Who) {
				seats = append(seats, s)
			}
		}
		if e.b.Used.After(used) {
			used = e.b.Used
		}
	}
	b := bindingOf(seats)
	b.Tier, b.Used = userBinds.m[keyID][pid][user].Tier, used
	return b, true
}

// putShared writes seats and used as user's binding to provider pid under
// every gateway key of keyID's group (userGroup) it is bound under, and
// under each of keys, bound there now; each keeps its own tier. No seats
// lets go of it under every key of the group. Another group's user of the
// same name is another user, and left as it is. Called with userBinds
// held.
func putShared(keyID, pid, user string, seats []userSeat, used time.Time, keys ...string) {
	g := userGroup(keyID)
	for k, ps := range userBinds.m {
		if _, has := ps[pid][user]; !has || userGroup(k) != g {
			continue
		}
		if len(seats) == 0 {
			delete(ps[pid], user)
			if len(ps[pid]) == 0 {
				delete(ps, pid)
			}
			continue
		}
		b := bindingOf(seats)
		b.Tier, b.Used = ps[pid][user].Tier, used
		ps[pid][user] = b
	}
	if len(seats) == 0 {
		return
	}
	for _, k := range keys {
		if _, has := userBinds.m[k][pid][user]; has || userGroup(k) != g {
			continue
		}
		if userBinds.m == nil {
			userBinds.m = map[string]map[string]map[string]userBinding{}
		}
		if userBinds.m[k] == nil {
			userBinds.m[k] = map[string]map[string]userBinding{}
		}
		if userBinds.m[k][pid] == nil {
			userBinds.m[k][pid] = map[string]userBinding{}
		}
		b := bindingOf(seats)
		b.Used = used
		userBinds.m[k][pid][user] = b
	}
}

// userIdle is how long a user sends a provider nothing before it lets go
// of its accounts there (sweepIdle): 60 hours (the owner's, 2026-11-02;
// 3 days before), counted from its last request to that provider.
const userIdle = 60 * time.Hour

// UserIdleHours is userIdle in hours, as the gateway tells the Gateway page.
const UserIdleHours = int(userIdle / time.Hour)

// userUsedEvery is how stale a binding's Used may get before a request
// writes it again: the file is written once an hour a user at most, not
// on every request.
const userUsedEvery = time.Hour

// userSeen is when each user last sent a provider a request in this
// process, under any gateway key of its group (userGroup, sharedOf), the
// group, provider and user (seenKey) → when: kept while user-accounts.json
// doesn't read too, so a user served from memory then isn't idle for the
// file's older `used` once it reads again.
var userSeen = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func seenKey(group, pid, user string) string { return group + "\x00" + pid + "\x00" + user }

// noteSeen notes now as user's last request to provider pid, of any key of
// group (userGroup).
func noteSeen(group, pid, user string, now time.Time) {
	userSeen.Lock()
	userSeen.m[seenKey(group, pid, user)] = now
	userSeen.Unlock()
}

// lastUse is when b, user's to provider pid under some gateway key of
// group, was last in use, as of now: its `used` (one ahead of now, edited
// or the clock set back, is now), this process's last request of the user
// under any key of the group (userSeen), and the end of a rest one of its
// accounts is on, which the user was told to wait out.
func lastUse(b userBinding, group, pid, user string, now time.Time) time.Time {
	last := b.Used
	if last.After(now) {
		last = now
	}
	userSeen.Lock()
	if t := userSeen.m[seenKey(group, pid, user)]; t.After(last) {
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

// sweepIdle lets go of every binding to provider pid whose user has sent
// it nothing for userIdle under any gateway key of its group (lastUse,
// the latest of its keys': one user, sharedOf), under every key of the
// group at once, but user's of keyID's group, whose request this is; a
// name under two groups is two users, each judged by its own use. When no
// user sent pid anything for userGap or
// more before this request, that gap doesn't count: every binding's Used
// moves on by it, as if the clock had stopped. One with no Used yet
// (bound before it was kept) is stamped now instead, so an upgrade frees
// nobody at once. changed says a binding was, for the file to be
// written. Called with userBinds held, never while the file doesn't read.
func sweepIdle(keyID, pid, user string, now time.Time) (changed bool) {
	// a user of a group: its name, and a key of the group it is bound
	// under, for sharedOf and putShared to read and write it by
	type named struct{ group, name string }
	asking := named{userGroup(keyID), user}
	last := func(k, u string, b userBinding) time.Time {
		if t := lastUse(b, userGroup(k), pid, u, now); t.Before(now) {
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
	// one with no Used yet under a key is written as it is read, under
	// every key of its group (putShared), with the latest Used it has
	// under another, or now with none, and so in use now: its first stays
	// the first of the one used last (sharedOf), not the one stamped
	unstamped := map[named]string{}
	for k, ps := range userBinds.m {
		for u, b := range ps[pid] {
			if n := (named{userGroup(k), u}); n != asking && b.Used.IsZero() {
				unstamped[n] = k
			}
		}
	}
	for u, k := range unstamped {
		b, _ := sharedOf(k, pid, u.name)
		putShared(k, pid, u.name, b.seats(), cmp.Or(b.Used, now))
		changed = true
	}
	// each user's last use, the latest under any key of its group
	latest := map[named]time.Time{}
	for k, ps := range userBinds.m {
		for u, b := range ps[pid] {
			n := named{userGroup(k), u}
			if n == asking {
				continue
			}
			if t := last(k, u, b); t.After(latest[n]) {
				latest[n] = t
			}
		}
	}
	gone := 0
	for u, t := range latest {
		if now.Sub(t) < userIdle {
			continue
		}
		for k, ps := range userBinds.m {
			if userGroup(k) == u.group {
				delete(ps[pid], u.name)
			}
		}
		gone++
	}
	for _, ps := range userBinds.m {
		if len(ps[pid]) == 0 {
			delete(ps, pid)
		}
	}
	if gone > 0 {
		log.Printf("user accounts: %d user(s) of %s sent it nothing for %s: their accounts there are let go, each bound afresh on its next request", gone, pid, userIdle)
	}
	return changed || gone > 0
}

// touchUsed keeps now as when user, under any gateway key of keyID's
// group, last sent provider pid a request, when the latest kept is
// userUsedEvery ago or more; written under every key of the group it is
// bound under, its accounts as one (putShared); changed says it was.
// Called with userBinds held.
func touchUsed(keyID, pid, user string, now time.Time) (changed bool) {
	b, ok := sharedOf(keyID, pid, user)
	if !ok || now.Sub(b.Used) < userUsedEvery && !b.Used.After(now) {
		return false
	}
	putShared(keyID, pid, user, b.seats(), now)
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
	// Keys are each gateway key's: its id → provider id → user →
	// binding. One name under several keys (and none) is one user, its
	// accounts written alike under each and its tier each key's own
	// (sharedOf, putShared)
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
	// sharers: the gateway keys that share their users (userGroup), read
	// with the file (loadUsers)
	sharers map[string]bool
	// sharersKnown: the key store read when sharers was, so a key not
	// among them keeps its users its own, not one unknown
	sharersKnown bool
	// swept: dropCopiedSeats looked at m as read, when sweptFor were the
	// keys that share their users; looked at again when either changes
	swept    bool
	sweptFor map[string]bool
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
	// Tier: the request's tier (UserTierHeader; without it its binding's,
	// else the default: bindUser)
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

// owns is the seat of u that pin, an account AccountHeader names (its
// user, or its id in the routing trace, as written or decoded:
// pinnedName), is, one to be signed in again among them; ok is false when
// it is none of u's.
func (u *UserBound) owns(pin string) (_ userSeat, ok bool) {
	i := slices.IndexFunc(u.seats, func(s userSeat) bool { return pinsSeat(pin, s) })
	if i < 0 {
		return userSeat{}, false
	}
	return u.seats[i], true
}

// pinsSeat says pin, AccountHeader as sent, names seat: by its account
// or its trace id, as written or decoded (pinnedName).
func pinsSeat(pin string, seat userSeat) bool {
	for _, n := range []string{pin, pinnedName(pin)} {
		if strings.EqualFold(n, seat.Account) || strings.EqualFold(n, seat.Who) {
			return true
		}
	}
	return false
}

// pinnedOwn is pool narrowed to seat, of provider pid alone, resting or
// not, for pinTo to say why it can't take a request pinned to it; pl's
// left, held, capped, lapsed and unlisted are narrowed to it too. An
// account of the same name at another provider (a fallback's) may be
// another user's, and is never kept.
func pinnedOwn(seat userSeat, pid string, pool []candidate, pl planned) ([]candidate, planned) {
	seats := []userSeat{seat}
	out, opl := keepAligned(pool, pl, func(c candidate) bool { return c.p.ID == pid && seatIn(seats, c.who()) })
	n := ownedOf(seats, pid, pl)
	n.order = opl.order
	return out, n
}

// callerUser is the user r names (userOf), as its usage rows keep it
// (T-020), bound by user or not: "" for none, or one that can't be taken.
func callerUser(r *http.Request) string {
	u, _ := userOf(r.Header)
	return u
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
	// which keys share their users, as they do now: a key turned to share
	// them, or not to, is so on its next request
	userBinds.sharers, userBinds.sharersKnown = userSharersRead()
	path := usersPath()
	if path != userBinds.path {
		userBinds.m, userBinds.path, userBinds.mod, userBinds.size, userBinds.read, userBinds.stale = nil, path, time.Time{}, 0, false, false
		userBinds.swept = false
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
		dropCopiedSeats() // bound in memory alone
		return nil
	}
	if err == nil && userBinds.read && fi.ModTime().Equal(userBinds.mod) && fi.Size() == userBinds.size {
		dropCopiedSeats()
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
	userBinds.swept = false
	dropCopiedSeats()
	return nil
}

// SettleUsers reads user-accounts.json again as a request bound by user
// does (loadUsers), for a gateway key turned to keep its users its own
// ("users-key") to let go of the seats it has of a shared user's at once,
// not on the next request (dropCopiedSeats).
func SettleUsers() error {
	userBinds.Lock()
	defer userBinds.Unlock()
	return loadUsers()
}

// writeUsersBackup writes dropCopiedSeats' copy of the file; a test's to
// fail.
var writeUsersBackup = steady.WriteFile

// dropCopiedSeats lets go of every seat a user of a gateway key that
// keeps its users its own (userGroup) has copied from the shared users'
// user of that name: the same account, bound at the same time (the
// owner's option A, 2026-11-04, T-091). T-075 wrote one
// user's accounts alike under every key it came with (putShared), and
// T-089 left those copies under the keys that don't share, so that key's
// user of the name had the shared one's seats and its Codex resets, and a
// seat for one user had two; a key turned back to its own users has them
// too. The shared users keep theirs; a user left with no seat is bound
// afresh on its next request. Two users bound to one account each on its
// own were bound at two times, and each keeps it: let go of, it would be
// bound to it again, and let go of again on every read. Done whenever the file was read again or
// the keys that share changed, never while either is unknown (the file
// stale, the key store unreadable: no key shares then), and only once a
// copy of the file as it was is beside it (user-accounts.json.bak-<UTC
// time>): one that can't be written lets go of nothing. Each seat let go
// of is logged by its key's id, provider, user and account. One the file
// can't be written for stays let go of in memory, written with the next
// change. Called with userBinds held.
func dropCopiedSeats() {
	if !userBinds.read || userBinds.stale || userBinds.swept && maps.Equal(userBinds.sweptFor, userBinds.sharers) {
		return
	}
	// the shared users' seats: provider → user → seats
	shared := map[string]map[string][]userSeat{}
	for k, ps := range userBinds.m {
		if userGroup(k) != sharedUsers {
			continue
		}
		for pid, us := range ps {
			for u, b := range us {
				if shared[pid] == nil {
					shared[pid] = map[string][]userSeat{}
				}
				shared[pid][u] = append(shared[pid][u], b.seats()...)
			}
		}
	}
	type drop struct {
		key, pid, user string
		gone, left     []userSeat
	}
	var drops []drop
	for k, ps := range userBinds.m {
		if userGroup(k) == sharedUsers {
			continue
		}
		for pid, us := range ps {
			for u, b := range us {
				d := drop{key: k, pid: pid, user: u}
				for _, st := range b.seats() {
					if slices.ContainsFunc(shared[pid][u], func(s userSeat) bool { return strings.EqualFold(s.Who, st.Who) && s.At.Equal(st.At) }) {
						d.gone = append(d.gone, st)
					} else {
						d.left = append(d.left, st)
					}
				}
				if len(d.gone) > 0 {
					drops = append(drops, d)
				}
			}
		}
	}
	path := usersPath()
	if len(drops) > 0 && !userBinds.sharersKnown {
		// no key shares while the store is unknown: every key's users
		// would look their own; looked at again on the next read
		if _, said := userFileSaid.LoadOrStore("sharers unknown "+path, true); !said {
			log.Printf("user accounts: which gateway keys share their users isn't known (the key store doesn't read): no seat copied to a key that keeps its users its own is let go of until it does")
		}
		return
	}
	userBinds.swept, userBinds.sweptFor = true, maps.Clone(userBinds.sharers)
	if len(drops) == 0 {
		return
	}
	slices.SortFunc(drops, func(a, b drop) int {
		return cmp.Or(strings.Compare(a.key, b.key), strings.Compare(a.pid, b.pid), strings.Compare(a.user, b.user))
	})
	was, err := os.ReadFile(path)
	if err == nil {
		err = writeUsersBackup(usersBackupPath(path, time.Now()), was, 0o600)
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil // bound in memory alone: no file to keep a copy of
	}
	if err != nil {
		log.Printf("user accounts: %d user(s) of gateway keys that keep their users their own have seats of a shared user's of the name, but a copy of %s can't be written beside it (%v): none is let go of until the file or the keys that share change", len(drops), path, err)
		return
	}
	for _, d := range drops {
		ps := userBinds.m[d.key]
		if len(d.left) == 0 {
			delete(ps[d.pid], d.user)
			if len(ps[d.pid]) == 0 {
				delete(ps, d.pid)
			}
			if len(ps) == 0 {
				delete(userBinds.m, d.key)
			}
		} else {
			ps[d.pid][d.user] = ps[d.pid][d.user].withSeats(d.left)
		}
		for _, st := range d.gone {
			log.Printf("user accounts: gateway key %s keeps its users its own, and its user %q of %s had %s, a seat of the shared users' %q: let go of", d.key, d.user, d.pid, st.Who, d.user)
		}
		if len(d.left) == 0 {
			log.Printf("user accounts: gateway key %s's user %q of %s has no seat left there: bound afresh on its next request", d.key, d.user, d.pid)
		}
	}
	if err := saveUsers(); err != nil {
		log.Printf("user accounts: the seats copied to keys that keep their users their own are let go of, but %s can't be written (%v): the next change writes it", path, err)
	}
}

// usersBackupPath is a name beside path for a copy of it made at now,
// one no copy has yet (one that can't be looked at is left to the write
// to fail on).
func usersBackupPath(path string, now time.Time) string {
	base := path + ".bak-" + now.UTC().Format("20060102T150405Z")
	out := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(out); err != nil {
			return out
		}
		out = fmt.Sprintf("%s-%d", base, i)
	}
}

// errUsersChanged is a write refused because the file changed since it
// was read: another magpie wrote it (the window's Unbind, or a gateway's
// binding while the window serves none), or it was edited by hand.
var errUsersChanged = errors.New("it changed since it was read, and is left as that change has it")

// usersChanged says the file isn't as it was last read or written: there,
// gone, or of another time or size. One that can't be looked at is
// unknown, and counts as changed. Called with userBinds held.
func usersChanged() bool {
	fi, err := os.Stat(usersPath())
	if errors.Is(err, fs.ErrNotExist) {
		return !userBinds.mod.IsZero()
	}
	return err != nil || !fi.ModTime().Equal(userBinds.mod) || fi.Size() != userBinds.size
}

// saveUsers writes the bindings, only for this user to read: to a file
// beside it first, flushed, renamed over it. Called with userBinds held,
// never while the file doesn't read (stale), and never over one that
// changed since it was read (errUsersChanged): userBinds is held in this
// process only, and the other change wins over this one, which the next
// read drops from memory too.
func saveUsers() error {
	if userBinds.stale {
		return errors.New("it doesn't read, and is left as it is")
	}
	if usersChanged() {
		return errUsersChanged
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
	if err := writeUsersFile(target, b, 0o600); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		userBinds.mod, userBinds.size = fi.ModTime(), fi.Size()
	}
	return nil
}

// writeUsersFile writes the file for saveUsers; a test's to fail.
var writeUsersFile = steady.WriteFile

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
// its. tier is the one the request named (named), kept as the binding's,
// written only when it changed; one that named none (tier the default)
// is of the tier its binding keeps instead, the default for a binding
// that keeps none or one user-tiers.json no longer has, and the binding
// is left as it is: the default never written, so a binding follows
// the file's default as it changes.
func bindUser(keyID, user string, tier Tier, named bool, p provider.Provider, cands []candidate, pl planned) ([]candidate, planned, *UserBound, userRefusal) {
	tiers, _ := loadTiers() // read already by tierOf: what it said
	// which of p's accounts have to be signed in again, read before the
	// lock: logins.json isn't read while userBinds is held
	lapsedWho := lapsedOf(p.ID, cands, pl)
	// what by_use binds a new seat by, read before the lock too: the
	// allowances routing keeps, and the usage log as last read
	budget := byUseOf(p.ID, cands, tiers, time.Now())
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil {
		return nil, pl, nil, userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: magpie can't read user-accounts.json, so it doesn't know which account %q is bound to; nothing was sent. Its log says why", UserHeader, user)}
	}
	if !named {
		if kept, ok := tiers.find(userBinds.m[keyID][p.ID][user].Tier); ok {
			tier = kept
		}
	}
	// the tier the request named, kept as the binding's
	keepTier := func() bool { return named && setTier(keyID, p.ID, user, tier.Name) }
	ub := &UserBound{User: user, Tier: tier.Name, key: keyID, pid: p.ID, max: tier.MaxAccounts, tier: tier}
	// users idle for days let go of their seats before any are counted;
	// never written over a file that doesn't read, but this request seen
	// all the same
	now := time.Now().UTC()
	defer noteSeen(userGroup(keyID), p.ID, user, now)
	if !userBinds.stale && sweepIdle(keyID, p.ID, user, now) {
		saveUsersLogged()
	}
	// its accounts under every gateway key of its group, as one (sharedOf)
	shared, _ := sharedOf(keyID, p.ID, user)
	seats := shared.seats()
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
		keepTier()
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
		if tiered, used := keepTier(), touchUsed(keyID, p.ID, user, now); tiered || used {
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
		uses := seatUsesBy(keyID, p.ID, tiers, user, budget, tier.Share)
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
		keepTier()
		saveUsersLogged()
		if ub.Why == "" {
			ub.Why = "bound"
		}
	}
	if ub.Why == "" {
		ub.Why = "kept"
	}
	// bound under other gateway keys of its group only: bound under this
	// one too, alike (putShared), for the Gateway page to name the key and its
	// tier; another tier than its last request's, or its last use an
	// hour or more ago: kept. Never written over a file that doesn't read
	if !userBinds.stale {
		joined := false
		if _, own := userBinds.m[keyID][p.ID][user]; !own {
			if b, ok := sharedOf(keyID, p.ID, user); ok {
				putShared(keyID, p.ID, user, b.seats(), b.Used, keyID)
				joined = true
			}
		}
		if tiered, used := keepTier(), touchUsed(keyID, p.ID, user, now); tiered || used || joined {
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
// binding to accounts of provider pid: a key's own (T-015), bound under
// keyID now if it was only under others (putShared); changed says it
// wasn't before. Called with userBinds held.
func setTier(keyID, pid, user, tier string) (changed bool) {
	b, ok := sharedOf(keyID, pid, user)
	if !ok || b.Tier == tier {
		return false
	}
	putShared(keyID, pid, user, b.seats(), b.Used, keyID)
	own := userBinds.m[keyID][pid][user]
	own.Tier = tier
	userBinds.m[keyID][pid][user] = own
	return true
}

// setSeats binds user, of the gateway key keyID, to seats of provider pid,
// or to none, under every key of its group it is bound under (putShared),
// each keeping its tier. Called with userBinds held.
func setSeats(keyID, pid, user string, seats []userSeat) {
	b, _ := sharedOf(keyID, pid, user)
	putShared(keyID, pid, user, seats, b.Used, keyID)
}

// seatUse is how an account is taken: by n users other than the one
// asking, bound under any gateway key (the asking key's included), as a
// first or a spare, one name under several keys of a group counted once
// (userGroup), the least share of their tiers share (0 for no limit); own
// says the user asking is on it too, under any key of its group.
type seatUse struct {
	n, share int
	own      bool
	// over: why the account has no room by its users' use for the one
	// asking (by_use, useBudget.over), shared by others already
	over string
}

// on is how many users are on the account, the one asking among them.
func (u seatUse) on() int {
	if u.own {
		return u.n + 1
	}
	return u.n
}

// takes says the account has a seat for one more user, of a tier with
// share: as many as its share and each user's on it let it have, and
// by_use, their use (seatUse.over).
func (u seatUse) takes(share int) bool {
	if u.over != "" {
		return false
	}
	n := u.n + 1
	return (share == 0 || n <= share) && (u.share == 0 || n <= u.share)
}

// seatUses is how each account of provider pid is taken, lower-cased
// who → its use, each user's share its tier's in tiers (Tiers.shareOf).
// user's own seats, under any gateway key of its group (userGroup), never
// keep it off an account (the owner's decision, 2026-10-30), and another
// name bound under several keys of a group is one user. A name is the
// caller's to send (UserHeader): among the keys that share their users
// (access.Key.SharesUsers) and callers without a key, one namespace (the
// owner's option A2, 2026-10-30), the caller of one that sends the name
// another's user goes by is on that user's seats as itself, a share-1
// account that user has alone included, when its key may use the account
// (TestUserNameOfAnotherKeySharesItsSeats). A key that doesn't share its
// users is a namespace of its own (option B, 2026-11-03, T-089): the same
// name sent with it is another user, counted on those seats as any other
// (TestUserNameOfAKeyThatDoesntShareIsAnother). Called with userBinds
// held.
type seatUses map[string]seatUse

// seatUsesOf is seatUsesBy for user of a caller without a gateway key.
func seatUsesOf(pid string, tiers Tiers, user string) seatUses {
	return seatUsesBy("", pid, tiers, user, nil, 0)
}

// seatUsesBy is how each account of provider pid is taken for user, of
// the gateway key keyID, each account others are on with no room by
// their use for user, of a tier with share, marked so (seatUse.over) by
// budget: none with budget nil, or for a tier that doesn't share (1: an
// account it takes is one nobody is on) or shares with no limit (0).
func seatUsesBy(keyID, pid string, tiers Tiers, user string, budget *useBudget, share int) seatUses {
	uses := seatUses{}
	asking := userName{userGroup(keyID), user}
	on := map[string]map[userName]int{}       // who → the users counted on it, each its tier's share
	seatsOf := map[userName]map[string]bool{} // a user → the accounts it is on, under any key of its group
	for k, ps := range userBinds.m {
		for n, b := range ps[pid] {
			name := userName{userGroup(k), n}
			share := tiers.shareOf(b.Tier)
			for _, s := range b.seats() {
				who := strings.ToLower(s.Who)
				if seatsOf[name] == nil {
					seatsOf[name] = map[string]bool{}
				}
				seatsOf[name][who] = true
				u := uses[who]
				if name == asking {
					// its own seat: counted toward which is picked,
					// never toward its share
					u.own = true
					uses[who] = u
					continue
				}
				if on[who] == nil {
					on[who] = map[userName]int{}
				}
				if was, seen := on[who][name]; !seen {
					on[who][name] = share
					u.n++
				} else if share > 0 && (was == 0 || share < was) {
					on[who][name] = share // under several keys: its least
				}
				if share > 0 && (u.share == 0 || share < u.share) {
					u.share = share
				}
				uses[who] = u
			}
		}
	}
	if budget == nil || share < 2 {
		return uses
	}
	// the user on one more account than it is: the one it would be given
	mine := seatLoad{share, len(seatsOf[asking]) + 1}
	for who, names := range on {
		u := uses[who]
		if u.n == 0 {
			continue
		}
		others := make(map[userName]seatLoad, len(names))
		for name, sh := range names {
			others[name] = seatLoad{sh, len(seatsOf[name])}
		}
		u.over = budget.over(who, others, asking, mine)
		noteOver(who, asking, u.over)
		uses[who] = u
	}
	return uses
}

// userName is a user as seatUsesBy counts it: its group of gateway keys
// (userGroup) and its name.
type userName struct{ group, name string }

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
		n, at := uses.of(c).on(), uses.of(cands[max(pick, 0)]).on()
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
// are never asked for it. A 429: the limit of the first with a seat back,
// until its rest's end or its window's renewal; with none of them
// resting or held, "seats", unknown until when, as a user leaves an
// account only when it goes.
func fullRefusal(user string, tier Tier, pid string, cands []candidate, pl planned, uses seatUses, lapsedWho map[string]string) userRefusal {
	r := userRefusal{status: http.StatusTooManyRequests, limit: limitSeats}
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
		r.msg = fmt.Sprintf("%s: no account of the provider is free for %q: each is taken by as many users as can share it (its %s, %s)%s; nothing was sent", UserHeader, user, tier.shares(), UserTierHeader, uses.byUseSaid(pid, cands))
		return r
	}
	r.msg = fmt.Sprintf("%s: no account of the provider is free for %q: of those its %s (%s) can share, the first back %s, and the others are taken%s; nothing was sent", UserHeader, user, tier.shares(), UserTierHeader, strings.Join(why, "; "), uses.byUseSaid(pid, cands))
	return r
}

// byUseSaid is what a refusal adds when by_use kept the user off one of
// cands of provider pid: "", or ", or have no room by their users' use
// (by_use: …)" with the first one's why.
func (uses seatUses) byUseSaid(pid string, cands []candidate) string {
	for _, c := range cands {
		if c.p.ID == pid {
			if why := uses.of(c).over; why != "" {
				return fmt.Sprintf(", or have no room by their users' use (by_use: %s %s)", shownAs(c), why)
			}
		}
	}
	return ""
}

// growUser gives u, bound to accounts of p none of which can answer the
// request now, a spare of pool (the candidates before bindUser narrowed
// them), and asks it first: when each of its accounts is out for a while
// (resting out of its allowance or credit userLongRest or more, held at a
// usage cap, all but used up), it has fewer than its tier's max_accounts
// (u.max; a user with as many or more, its tier lowered since, keeps them
// and is given none), and one of pool not among them has room and a seat
// for its tier (spareOf). Its accounts the gateway key may not use
// (keyHeld) are neither out nor in, nor counted toward max_accounts.
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
	// those the gateway key may not use (keyHeld) are the user's still,
	// kept for a key that may, but neither serve it here nor count toward
	// its tier's max_accounts under this key: mine are the others
	left := append(slices.Clone(poolPl.left), pl.left...)
	out, rests, held, lapsed, mine := true, []Rest{}, []Weighed{}, 0, 0
	for _, st := range u.seats {
		if keyHeld(st, u.pid, pool, left) {
			continue
		}
		mine++
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
	// limit, when no spare is added for its tier's max_accounts reached
	// ("accounts"), is the limit a 429 says in place of "quota", its
	// accounts' allowance used up: the spare is what it is out of. Out of
	// credit, rate limited or at a cap is said as it is
	refuse := func(note, limit string) ([]candidate, planned, *UserBound, bool, userRefusal) {
		if len(cands) > 0 {
			return cands, pl, u, false, userRefusal{}
		}
		if lapsed > 0 && lapsed == mine {
			// every one of its accounts to be signed in again; with some
			// only, the others say why (a 400 for one without the model)
			return nil, pl, u, false, lapsedRefusal(u, note)
		}
		r := setRefusal(u, rests, held, pl, note)
		if limit != "" && r.status == http.StatusTooManyRequests && r.limit == failQuota {
			r.limit = limit
		}
		return nil, pl, u, false, r
	}
	if !out {
		// back soon, or one of them still asked: no spare looked for. At
		// its tier's most, out of its allowance is "accounts" all the
		// same, as the try that found it said (userLimitedAfter)
		if mine >= u.max {
			return refuse("", limitAccounts)
		}
		return refuse("", "")
	}
	if mine >= u.max {
		return refuse(u.atTier(mine), limitAccounts)
	}
	tiers, _ := loadTiers()
	budget := byUseOf(u.pid, pool, tiers, time.Now()) // before the lock, as bindUser's
	userBinds.Lock()
	defer userBinds.Unlock()
	if loadUsers() != nil || userBinds.stale {
		if lapsed > 0 && len(cands) == 0 {
			// unknown, not a limit: its accounts are to be signed in
			// again, and whether one could be added can't be told
			return nil, pl, u, false, userRefusal{status: http.StatusServiceUnavailable, unknown: true, msg: fmt.Sprintf("%s: user-accounts.json can't be read, so no other account can be added for %q now; nothing was sent. magpie's log says why", UserHeader, u.User)}
		}
		return refuse("user-accounts.json can't be read to add a spare", "")
	}
	shared, _ := sharedOf(u.key, u.pid, u.User)
	now := shared.seats()
	// another request of the user's, under any gateway key of its group, added a spare
	// since this one was bound: that one, if it can answer, not another
	// beside it
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
	if n := len(slices.DeleteFunc(slices.Clone(now), func(st userSeat) bool { return keyHeld(st, u.pid, pool, left) })); n >= u.max {
		return refuse(u.atTier(n), limitAccounts)
	}
	uses := seatUsesBy(u.key, u.pid, tiers, u.User, budget, u.tier.Share)
	pick := spareOf(u.pid, pool, poolPl, now, true, u.tier, uses, lapsedWho)
	if pick < 0 {
		if !slices.ContainsFunc(pool, func(c candidate) bool {
			return c.p.ID == u.pid && !seatIn(now, c.who()) && uses.of(c).takes(u.tier.Share)
		}) {
			return refuse(fmt.Sprintf("every other account of the provider is taken by as many users as can share it (its %s, %s)%s", u.tier.shares(), UserTierHeader, uses.byUseSaid(u.pid, pool)), "")
		}
		return refuse("no other account of the provider has room to be added", "")
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
// the caller is told as any limit (userLimitSaid), its UserLimitHeader
// "signin" alone saying a sign-in lapsed (T-062), never which; the
// call's error, the operator's, says it and why no spare was added
// (note).
func lapsedRefusal(u *UserBound, note string) userRefusal {
	msg := fmt.Sprintf("%s: %q is bound to %s, to be signed in again; no other account is tried in its place", UserHeader, u.User, strings.Join(accountsOf(u.seats), ", "))
	if note != "" {
		msg += ". No spare was added: " + note
	}
	return userRefusal{status: http.StatusTooManyRequests, msg: msg, limit: limitSignin, until: time.Now().Add(lapsedRetry)}
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

// keyHeld says the account st of provider pid is one the gateway key
// asking may not use: not among pool, and left out of the plan held
// (allowedCandidates, left). It stays the user's, bound as it was, and
// is let go only as any seat is (sweepIdle, its user idle): a sign-in
// or a key's list changed doesn't touch a binding (the owner's
// decision). So it still counts toward each account's share for others
// (seatUsesOf), and a user whose key was narrowed may be given up to its
// tier's max_accounts among the key's own beside it.
func keyHeld(st userSeat, pid string, pool []candidate, left []Weighed) bool {
	if slices.ContainsFunc(pool, func(c candidate) bool { return c.p.ID == pid && strings.EqualFold(c.who(), st.Who) }) {
		return false
	}
	return slices.ContainsFunc(left, func(w Weighed) bool { return w.Held && leftIs(w, pid, st.Who) })
}

// How one of a user's accounts stands, for growUser.
const (
	seatOther = iota // paused, not serving the model, narrowed away (sealed elsewhere)
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
// With neither, why its first can't (userBoundRefusal).
func setRefusal(u *UserBound, rests []Rest, held []Weighed, pl planned, note string) userRefusal {
	b := bindingOf(u.seats)
	if len(rests) == 0 && len(held) == 0 {
		return userBoundRefusal(u.User, b, u.pid, nil, pl)
	}
	var r userRefusal
	if len(rests) > 0 {
		r = userBoundRefusal(u.User, b, u.pid, rests, pl)
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

// userBoundRefusal is why the accounts b names, which user is bound to, can't
// take the request: resting (rests), left out of the plan, or narrowed
// away before (sealed by another account). Resting, they are back when the
// first of their rests ends: a limit's, the user's limit until then.
func userBoundRefusal(user string, b userBinding, pid string, rests []Rest, pl planned) userRefusal {
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
	// one the gateway key may not use says why only when every one is:
	// another's reason is the user's then (keyHeld)
	allHeld := !slices.ContainsFunc(seats, func(st userSeat) bool {
		return !slices.ContainsFunc(pl.left, func(w Weighed) bool { return w.Held && leftIs(w, pid, st.Who) })
	})
	for _, st := range seats {
		for _, w := range pl.left {
			if !leftIs(w, pid, st.Who) || w.Lapsed != "" {
				continue // one to be signed in again serves the model, once it is
			}
			if w.Held && !allHeld {
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
				return userRefusal{status: http.StatusBadRequest, msg: fmt.Sprintf("%s: %q is bound to %s, which doesn't serve %s", UserHeader, user, names, w.Model),
					said: fmt.Sprintf("%s: %q is bound to no account that serves %s", UserHeader, user, w.Model)}
			}
		}
	}
	why := "take this request (paused, or the conversation was sealed by another account); no other account is tried in its place"
	return userRefusal{status: http.StatusServiceUnavailable, msg: fmt.Sprintf("%s: %q is bound to %s, which can't %s", UserHeader, user, names, why),
		said: fmt.Sprintf("%s: %q is bound to no account that can %s", UserHeader, user, why)}
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

// told is what the caller is told of r: a 429 only userLimitSaid, else r
// without the accounts the user is bound to (said), which are the
// operator's, as a 429's are.
func (r userRefusal) told() string {
	if r.status == http.StatusTooManyRequests {
		return userLimitSaid
	}
	return cmp.Or(r.said, r.msg)
}

// userRefused writes refused, why a request of the user u goes to none
// of its accounts, with its limit when it is one. The caller is told it
// without the user's accounts (told); they are in the call's error, and
// in magpie's log (logUserRefused).
func (s *Server) userRefused(w http.ResponseWriter, from provider.Protocol, call *Call, u *UserBound, refused userRefusal) {
	logUserRefused(u, call.Provider, refused)
	call.Status, call.Error = refused.status, "user bound: "+u.accountOr()
	if refused.limit != "" {
		userLimited(w.Header(), refused.limit, refused.until, refused.model)
	} else if d := time.Until(refused.until); d > 0 {
		// not a limit (a 503 with none of the user's able to answer):
		// when to try again all the same
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
	}
	msg := refused.told()
	if msg != refused.msg {
		call.Error += ": " + refused.msg
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
	// at once, with one not resting nor held, when nothing is said. One
	// the gateway key may not use is never back for it (keyHeld)
	if rested && len(u.seats) > 1 {
		for _, st := range u.seats {
			if strings.EqualFold(st.Who, c.who()) || keyHeld(st, u.pid, nil, u.left) {
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
	if limit == failQuota && u.atMost() {
		// out of its allowance with as many accounts as its tier lets it
		// have: told as growUser tells the next request, no spare added
		// (refuse's limit)
		limit = limitAccounts
	}
	userLimited(h, limit, until, model)
	return rested
}

// atMost says u has as many accounts of its provider as its tier lets it
// have, or more (lowered since), those the gateway key may not use not
// counted, as growUser counts them.
func (u *UserBound) atMost() bool {
	mine := 0
	for _, st := range u.seats {
		if !keyHeld(st, u.pid, nil, u.left) {
			mine++
		}
	}
	return u.max > 0 && mine >= u.max
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
		b, ok = sharedOf(keyID, pid, user)
	}
	userBinds.Unlock()
	var own []Weighed
	for _, w := range ws {
		if ok && !w.Held && (w.Capped > 0 || w.NoCredits) && slices.ContainsFunc(b.seats(), func(s userSeat) bool { return leftIs(w, pid, s.Who) }) {
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
	n := ownedOf(seats, pid, pl)
	n.order = append(order, spentAt...)
	return append(out, spent...), n
}

// ownedOf is pl's left, held, capped, lapsed and unlisted narrowed to
// seats of provider pid, with no order.
func ownedOf(seats []userSeat, pid string, pl planned) planned {
	mine := func(c candidate) bool { return c.p.ID == pid && seatIn(seats, c.who()) }
	var n planned
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
	return n
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
	// of each user's accounts, under every gateway key of its group as
	// one (sharedOf), that one: its first spare is its first when it was
	// the first, and a user left with none is bound afresh. A user is its
	// group and name, a key of the group it is bound under to read it by
	users := map[userName]string{}
	for k, ps := range userBinds.m {
		for u, b := range ps[c.p.ID] {
			if seatIn(b.seats(), who) {
				users[userName{userGroup(k), u}] = k
			}
		}
	}
	for u, k := range users {
		b, _ := sharedOf(k, c.p.ID, u.name)
		putShared(k, c.p.ID, u.name, slices.DeleteFunc(b.seats(), func(s userSeat) bool { return strings.EqualFold(s.Who, who) }), b.Used)
	}
	gone := len(users)
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
// without one. magpie's own ask for a user (forUser), made from a request
// that carries no key of its own (a Claude Code run's web search, asked
// back through its MCP callback: askedAs), is of the key the user was
// asked for by, so its user is that key's (userGroup, T-089), never the
// one of that name without a key.
func userKey(r *http.Request) string {
	if id := access.Caller(r.Context()).KeyID; id != "" {
		return id
	}
	if f, ok := r.Context().Value(askedForKey{}).(askedFor); ok {
		return f.key
	}
	return ""
}

// UserRow is one user's binding, as user-accounts.json has it.
type UserRow struct {
	Key string // the gateway key's id, "" for callers without one
	// Group: the users Key's are one with (userGroup): rows of one user
	// and provider in one group are one user, bound alike
	Group    string
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
	sharers := userSharers()
	add := func(key string, ps map[string]map[string]userBinding) {
		g := groupOf(sharers, key)
		for pid, us := range ps {
			for u, b := range us {
				seats := b.seats()
				if len(seats) == 0 {
					continue
				}
				row := UserRow{Key: key, Group: g, Provider: pid, User: u, Who: seats[0].Who, Account: seats[0].Account, At: seats[0].At, Tier: b.Tier, Used: shownUsed(b.Used, g, pid, u, now)}
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
// file's, or this process's later request of its user under any gateway
// key of group (userSeen: the file is written once an hour), never ahead
// of now (an edited file, a clock set back). Zero stays zero: none kept
// for a binding from before it was.
func shownUsed(used time.Time, group, pid, user string, now time.Time) time.Time {
	if used.IsZero() {
		return used
	}
	userSeen.Lock()
	if t := userSeen.m[seenKey(group, pid, user)]; t.After(used) {
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
