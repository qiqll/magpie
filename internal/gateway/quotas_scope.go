package gateway

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A key held to a budget, models or accounts is told none of this
// magpie's quotas (#1981), unless the key's owner lets it be told those
// of what it may use (access.Key.Quotas, Kenoia, #2216). It is then told
// a card only when:
//
//   - the card's provider is one of this magpie's, in the catalog, and
//     the key may use every model the catalog lists of it: a key held to
//     some of a provider's models is told nothing of the provider, since
//     its quota is spent by the models the key may not use as well; and
//   - the card's account or key is one the key may use (AllowsAccount):
//     when the key's account list names the provider, the card's account
//     must be one it names, matched by who is signed in or the key's
//     name; a card no account of the provider is known by is told to no
//     such key.
//
// Bound by user, a request that names its user (UserHeader) is told, of
// those, only the cards of the accounts and keys the user is on (T-090),
// as it spends only their Codex resets (resetNotUsers): another user's
// card names its account and what is left of it.
//
// The quotas, their kept report, their history, the cards another magpie
// reads and a card's refresh all go through it. The accounts' lanes
// (/v1/magpie/concurrency) and the usage summary are still refused a
// held key; a user bound by user is told its own seats' lanes and its own
// calls on them there (usersSeats, userCalls; T-093).

// quotaScope is what a held key with Quotas set, or a user bound by user,
// may be told; nil is everything, for a key held to nothing and this
// machine's owner.
type quotaScope struct {
	who   access.Identity
	every map[string]bool // provider id → the key may use every model of it; nil for a key held to nothing
	refs  map[string][]provider.AccountRef
	// bound, the request's user is told only the cards of seats, the
	// accounts and keys it is on (usersSeats)
	bound bool
	seats []userSeat
}

// scopeQuotas is the request's quota scope. It answers a held key that
// may not be told quotas 403 (refuseHeld), a user header that can't be
// taken 400 and a user whose seats can't be read 503 (usersSeats), and
// says so with ok false.
func (s *Server) scopeQuotas(w http.ResponseWriter, r *http.Request) (*quotaScope, bool) {
	who := access.Caller(r.Context())
	var sc *quotaScope
	if heldKey(who) {
		if !who.Quotas {
			refuseHeld(w, r, heldRefused)
			return nil, false
		}
		sc = newQuotaScope(who, provider.Catalog())
	}
	own, ok := s.usersSeats(w, r)
	if !ok {
		return nil, false
	}
	if own != nil {
		if sc == nil {
			sc = &quotaScope{who: who, refs: map[string][]provider.AccountRef{}}
		}
		sc.bound, sc.seats = true, own.seats
	}
	return sc, true
}

// usersOwn is what a request bound by user that names its user is told
// of (usersSeats): its user, the accounts and keys it is on, and the keys
// its name is one user under, as user-accounts.json was read for them.
type usersOwn struct {
	user  string
	seats []userSeat
	// sharers and group: userGroup as read with seats, for mine
	sharers map[string]bool
	group   string
}

// mine says a call or request named user, under gateway key key
// (Record.CallerKeyID), is the request's user's: one name under the keys
// of its group (userGroup; T-089).
func (o *usersOwn) mine(user, key string) bool {
	return user == o.user && groupOf(o.sharers, key) == o.group
}

// usersSeats is, bound by user, every account and key r's user
// (UserHeader) is on, of every provider, as its group shares them
// (sharedOf, userGroup; T-089), as resetNotUsers reads its Codex ones.
// own is nil for a request that names no user, the key's owner's, and
// bound by session: told every card, as before. A user header that
// can't be taken is answered 400; a user-accounts.json that can't be
// read, or changed and doesn't read now (stale), 503: which seats are the
// user's is unknown, so it is told none.
func (s *Server) usersSeats(w http.ResponseWriter, r *http.Request) (own *usersOwn, ok bool) {
	if !s.bind.byUser() {
		return nil, true
	}
	user, bad := userOf(r.Header)
	if bad != "" {
		writeError(w, provider.Chat, http.StatusBadRequest, bad)
		return nil, false
	}
	if user == "" {
		return nil, true
	}
	own = &usersOwn{user: user}
	key := userKey(r)
	userBinds.Lock()
	err := loadUsers()
	if err == nil && userBinds.stale {
		err = errUsersChanged
	}
	g, done := userGroup(key), map[string]bool{}
	for k, ps := range userBinds.m {
		if userGroup(k) != g {
			continue
		}
		for pid, us := range ps {
			if _, has := us[user]; !has || done[pid] {
				continue
			}
			done[pid] = true
			b, _ := sharedOf(key, pid, user)
			own.seats = append(own.seats, b.seats()...)
		}
	}
	own.sharers, own.group = maps.Clone(userBinds.sharers), g
	userBinds.Unlock()
	if err != nil {
		writeError(w, provider.Chat, http.StatusServiceUnavailable, fmt.Sprintf("%s: magpie can't read user-accounts.json, so it doesn't know which accounts %q is on and tells it nothing of them. Its log says why", UserHeader, user))
		return nil, false
	}
	return own, true
}

func newQuotaScope(who access.Identity, catalog []provider.Entry) *quotaScope {
	s := &quotaScope{who: who, every: map[string]bool{}, refs: map[string][]provider.AccountRef{}}
	for _, e := range catalog {
		if e.Group != "" || e.Provider.ID == "" {
			continue
		}
		id := e.Provider.ID
		ok, seen := s.every[id]
		if seen && !ok {
			continue
		}
		s.every[id] = modelAllowed(who, e.Provider, e.Model)
	}
	return s
}

// provider says the key may be told provider id's quota at all.
func (s *quotaScope) provider(id string) bool {
	if s == nil {
		return true
	}
	if s.bound && !slices.ContainsFunc(s.seats, func(st userSeat) bool { return seatOf(st, id) }) {
		return false
	}
	return s.every == nil || s.every[id]
}

// seatOf says st is an account or key of provider id (candidate.who).
func seatOf(st userSeat, id string) bool {
	who := strings.ToLower(st.Who)
	id = strings.ToLower(id)
	return strings.HasPrefix(who, id+"@") || strings.HasPrefix(who, id+"#")
}

// refsOf is provider id's accounts and keys (AccountIDs), read once.
func (s *quotaScope) refsOf(id string) ([]provider.AccountRef, bool) {
	if refs, ok := s.refs[id]; ok {
		return refs, true
	}
	p, err := provider.Find(id)
	if err != nil {
		return nil, false
	}
	refs := p.AccountIDs()
	s.refs[id] = refs
	return refs, true
}

// card says the key may be told the card of provider id's account user.
func (s *quotaScope) card(id, user string) bool {
	if s == nil {
		return true
	}
	if !s.provider(id) {
		return false
	}
	if s.bound && !s.onSeat(id, user) {
		return false
	}
	if s.every == nil || len(s.who.Accounts) == 0 {
		return true
	}
	refs, ok := s.refsOf(id)
	if !ok {
		return false
	}
	if ref, ok := namelessCard(refs, user); ok {
		return s.who.AllowsAccount(id, ref.ID)
	}
	matched := false
	for _, a := range refs {
		if !strings.EqualFold(strings.TrimSpace(a.User), strings.TrimSpace(user)) {
			continue
		}
		// two accounts or keys shown by one name: the key may use both,
		// or is told neither's card
		if !s.who.AllowsAccount(id, a.ID) {
			return false
		}
		matched = true
	}
	if matched {
		return true
	}
	// a card no account is known by: only for a provider the key's list
	// names no account of, which it uses on every one
	return s.who.AllowsAccount(id, "")
}

// namelessCard is the account or key of refs (AccountIDs) a card named by
// no one (user "") is of: a provider's only account, or its first key,
// whose card cardKeys names "" while no other key is on. AccountIDs lists
// the first key first, and a key that is off, or the first key listed
// again (#1772), beside it, so one key on is not one ref.
func namelessCard(refs []provider.AccountRef, user string) (provider.AccountRef, bool) {
	if strings.TrimSpace(user) != "" || len(refs) == 0 || len(refs) > 1 && !refs[0].Key {
		return provider.AccountRef{}, false
	}
	return refs[0], true
}

// onSeat says the card of provider id's account user is of an account or
// key the user is on (seats), matched to them as card matches a key's
// account list: by who is signed in or the key's name, every account or
// key shown by that name the user's, or the card is not. A card no
// account or key here is known by is the user's only when a seat names
// its account.
func (s *quotaScope) onSeat(id, user string) bool {
	user = strings.TrimSpace(user)
	refs, _ := s.refsOf(id)
	who := func(a provider.AccountRef) string {
		if a.Key {
			return id + "#" + a.ID
		}
		return id + "@" + strings.ToLower(strings.TrimSpace(a.User))
	}
	if ref, ok := namelessCard(refs, user); ok {
		return seatIn(s.seats, who(ref))
	}
	matched := false
	for _, a := range refs {
		if !strings.EqualFold(strings.TrimSpace(a.User), user) {
			continue
		}
		if !seatIn(s.seats, who(a)) {
			return false
		}
		matched = true
	}
	return matched || user != "" && seatIn(s.seats, id+"@"+strings.ToLower(user))
}

// cards is cs as the key may be told them.
func (s *quotaScope) cards(cs []provider.SubscriptionQuota) []provider.SubscriptionQuota {
	if s == nil {
		return cs
	}
	out := []provider.SubscriptionQuota{}
	for _, q := range cs {
		if q.From == "" && s.card(q.Provider, q.User) {
			out = append(out, q)
		}
	}
	return out
}

// writeCards answers another magpie the cards the key may be told. A held
// key told none of the cards here is answered as held: the other magpie
// then shows the key's own limit, not that nothing was read here, which a
// refresh wouldn't change. A user told none is told an empty list.
func writeCards(w http.ResponseWriter, r *http.Request, s *quotaScope) {
	all := provider.CachedCards(time.Now())
	cs := s.cards(all)
	if len(cs) == 0 && len(all) > 0 && refuseHeld(w, r, heldRefused) {
		return
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": cs})
}

// quotas is qs as the key may be told them. A window's whole (Holds,
// #2333) is left out: it is reckoned from every call routed through the
// account, other keys', users' and the owner's too, which a held key or a
// user is not told.
func (s *quotaScope) quotas(qs []provider.Quota) []provider.Quota {
	if s == nil {
		return qs
	}
	out := []provider.Quota{}
	for _, q := range qs {
		if q.From == "" && s.card(q.Provider, q.User) {
			ws := make([]provider.QuotaSpan, len(q.Windows))
			for i, w := range q.Windows {
				w.Holds, w.HoldsWhy = nil, ""
				ws[i] = w
			}
			q.Windows = ws
			out = append(out, q)
		}
	}
	return out
}

// histories is hs as the key may be told them.
func (s *quotaScope) histories(hs []provider.QuotaHistory) []provider.QuotaHistory {
	if s == nil {
		return hs
	}
	out := []provider.QuotaHistory{}
	for _, h := range hs {
		if s.card(h.Provider, h.User) {
			out = append(out, h)
		}
	}
	return out
}
