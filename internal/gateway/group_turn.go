package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// groupTurn narrows a routing group's turn, bound by user, to its user's
// own keys and accounts of each member's provider (usersOn), as a turn to
// one provider is narrowed (bindUser): never another user's. As a
// decision group's are (groupUsers), a provider is narrowed only as the
// tries reach it (reach), so the user is bound on the providers the turn
// goes to, not on every member's: a fallback's seats aren't taken while
// the first answers. The user's own keep the places the group's routing,
// the conversation's affinity and its rules gave them; another user's
// are left out where they are.
type groupTurn struct {
	s       *Server
	r       *http.Request
	members map[string]provider.Provider
	pools   map[string]pooled // each provider's keys and accounts, before
	done    map[string]bool   // providers narrowed
	free    map[string]bool   // narrowed with nothing bound: its own are all of them
	own     map[string]map[string]bool
	us      userTries
	refused error      // the first provider's refusal of the user (a userRefusedError)
	lapsed  error      // the refusal of the user's last own of a provider to be signed in again, no spare left
	ub      *UserBound // the first binding narrowed, for the routing log
	// pinLapsed is why the user's own account pinned has to be signed in
	// again, the turn gone as an unpinned one (pinned): told as a limit
	// when none of its own takes it in its place
	pinLapsed string
}

// groupTurnOf is the groupTurn of r, a turn to the group of members ms,
// its candidates cs planned as pl index for index; nil bound by session.
func (s *Server) groupTurnOf(r *http.Request, ms []provider.Member, cs []candidate, pl planned) *groupTurn {
	if !s.bind.byUser() {
		return nil
	}
	g := &groupTurn{s: s, r: r, members: map[string]provider.Provider{}, pools: map[string]pooled{}, done: map[string]bool{},
		free: map[string]bool{}, own: map[string]map[string]bool{}, us: userTries{}}
	for _, m := range ms {
		g.members[m.Provider.ID] = m.Provider
	}
	for _, c := range cs {
		if _, ok := g.pools[c.p.ID]; !ok {
			pid := c.p.ID
			of, ofPl := keepIn(cs, pl, func(c candidate) bool { return c.p.ID == pid })
			g.pools[pid] = pooled{of, ofPl}
		}
	}
	return g
}

// bound says pid is a provider whose keys or accounts usersOn narrows: a
// member's with a key or an account, or one no member is of.
func (g *groupTurn) bound(pid string) bool {
	p, ok := g.members[pid]
	return !ok || p.Account != nil || p.Key != ""
}

// mine says c is one of its user's own to ask: of a provider with nothing
// to narrow, or of one narrowed already that the user is on. One of a
// provider not narrowed yet isn't, yet; one no member is of never is.
func (g *groupTurn) mine(c candidate) bool {
	pid := c.p.ID
	if _, member := g.members[pid]; !member {
		return false
	}
	if !g.bound(pid) || g.free[pid] {
		return true
	}
	return g.done[pid] && g.own[pid][strings.ToLower(c.who())]
}

// narrow binds the user on pid's keys and accounts now (usersOn), once,
// and says the routing log's plan of those it is on. A provider whose
// binding refuses the user is left with none of its own, the first such
// refusal kept.
func (g *groupTurn) narrow(pid string) (order, left []Weighed) {
	g.done[pid] = true
	p, member := g.members[pid]
	switch {
	case !member:
		return nil, nil // not a member's provider: never asked
	case !g.bound(pid):
		g.free[pid] = true // nothing to narrow
		return nil, nil
	}
	pool := g.pools[pid]
	own, opl, u, err := g.s.usersOn(g.r, p, pool.cs, pool.pl)
	if err != nil {
		if g.refused == nil {
			g.refused = err
		}
		return nil, nil
	}
	if u == nil {
		g.free[pid] = true // nothing bound: as planned
		return nil, nil
	}
	set := map[string]bool{}
	for _, c := range own {
		set[strings.ToLower(c.who())] = true
	}
	// its own whose list lacks the model, asked once one refused it
	// (unlistedNext), as a provider's turn has them (ownedBy)
	for _, c := range opl.unlisted {
		set[strings.ToLower(c.who())] = true
	}
	g.own[pid], g.us[pid] = set, u
	if g.ub == nil {
		g.ub = u.ub
	}
	return opl.order, opl.left
}

// pinned is cs, the group's candidates for a turn pinned (AccountHeader)
// to pin, planned as pl index for index, narrowed to the one seat of the
// user's own pin names (its account or its id in the routing trace), of
// the first member's provider in the group's order it is on, as a
// provider's turn's pin is (pinnedOwn, the owner's decision of
// 2026-10-30, T-009, T-036); own is pin as pinTo knows it. The user's
// seats are read from its binding, its use stamped as bindUser stamps it,
// so no seat is taken and no spare added for a pin. own is "" when pin
// names none of the user's own, another user's or an account it isn't
// on: the turn goes as an unpinned one, and the account named is never
// asked for it. user-accounts.json unread is none of its own, the turn's
// narrowing then saying why. Pinned to its own that has to be signed in
// again, the turn goes as an unpinned one too, on its other own that can
// answer, as a provider's turn's lapsed pin does (the owner's decision of
// 2026-11-01, T-037): those to be signed in again are left out, and with
// none of its own left the turn is refused as a limit (refusal).
func (g *groupTurn) pinned(pin string, cs []candidate, pl planned) (_ []candidate, _ planned, own string) {
	user, _ := userOf(g.r.Header)
	key := userKey(g.r)
	userBinds.Lock()
	defer userBinds.Unlock()
	if user == "" || loadUsers() != nil {
		return cs, pl, ""
	}
	// the seat pin names on pid, if the user is on one there
	named := func(pid string) (userSeat, bool) {
		if _, member := g.members[pid]; !member || !g.bound(pid) {
			return userSeat{}, false
		}
		for _, s := range userBinds.m[key][pid][user].seats() {
			if strings.EqualFold(pin, s.Account) || strings.EqualFold(pin, s.Who) {
				return s, true
			}
		}
		return userSeat{}, false
	}
	var pid, id, lapsed string // its provider, its row's ID, as pinTo knows it, and why it is to be signed in again
	var seat userSeat
	for i, c := range cs {
		if s, ok := named(c.p.ID); ok && i < len(pl.order) && seatIn([]userSeat{s}, c.who()) {
			pid, id, seat = c.p.ID, pl.order[i].ID, s
			if c.p.Account != nil {
				lapsed = provider.SignInLapsed(c.p.Account)
			}
			break
		}
	}
	for _, w := range pl.left {
		if pid != "" {
			break
		}
		if s, ok := named(w.Provider); ok && leftIs(w, w.Provider, s.Who) {
			pid, id, seat, lapsed = w.Provider, w.ID, s, w.Lapsed
		}
	}
	if pid == "" {
		return cs, pl, ""
	}
	if lapsed != "" {
		// its own to be signed in again: none of those is asked, one
		// brought back as none other could answer (lapsedBack) included;
		// its use is stamped as its own are narrowed (reach)
		g.pinLapsed = lapsed
		out, opl := keepAligned(cs, pl, func(c candidate) bool {
			return c.p.Account == nil || provider.SignInLapsed(c.p.Account) == ""
		})
		return out, opl, ""
	}
	// its use kept as any bound user's, so it isn't let go as idle
	now := time.Now().UTC()
	noteSeen(key, pid, user, now)
	if !userBinds.stale && touchUsed(key, pid, user, now) {
		saveUsersLogged()
	}
	b := userBinds.m[key][pid][user]
	g.done[pid], g.own[pid] = true, map[string]bool{strings.ToLower(seat.Who): true}
	g.ub = &UserBound{User: user, Tier: b.Tier, Why: "kept"}
	if tier, _, err := tierOf(g.r.Header); err == nil && tierNamed(g.r.Header) {
		g.ub.Tier = tier.Name
	}
	g.ub.shown(b.seats())
	out, opl := pinnedOwn(seat, pid, cs, pl)
	return out, opl, id
}

// reach is cs with its provider narrowed when the tries reach cs[i]
// first, every one of that provider's from i on that isn't the user's
// left out, until cs[i] is one of the user's own to ask, or none is left
// (i past the end). Those after it of a provider narrowed already that
// aren't the user's go too, so that cs[i] is the last when nothing after
// it may be asked. seen is told the routing log's plan of each narrowed.
func (g *groupTurn) reach(cs []candidate, i int, seen func(order, left []Weighed)) []candidate {
	if g == nil {
		return cs
	}
	for i < len(cs) {
		pid := cs[i].p.ID
		if !g.done[pid] {
			g.narrowSeen(pid, seen)
			cs = append(slices.Clone(cs[:i]), slices.DeleteFunc(slices.Clone(cs[i:]), func(c candidate) bool { return c.p.ID == pid && !g.mine(c) })...)
			continue
		}
		if g.mine(cs[i]) {
			return append(cs[:i+1:i+1], slices.DeleteFunc(slices.Clone(cs[i+1:]), func(c candidate) bool { return g.done[c.p.ID] && !g.mine(c) })...)
		}
		cs = slices.Delete(slices.Clone(cs), i, i+1)
	}
	return cs
}

// narrowSeen narrows pid (narrow), seen told its plan.
func (g *groupTurn) narrowSeen(pid string, seen func(order, left []Weighed)) {
	if order, left := g.narrow(pid); seen != nil && len(order)+len(left) > 0 {
		seen(order, left)
	}
}

// keep is cs, candidates added to be asked next (the unlisted, the roomy
// left out), of the user's own alone, their providers narrowed now, and
// ws, their rows index for index, with them: another user's is never
// asked, nor shown in the routing log. seen is told the plan of each
// provider narrowed.
func (g *groupTurn) keep(cs []candidate, ws []Weighed, seen func(order, left []Weighed)) ([]candidate, []Weighed) {
	if g == nil {
		return cs, ws
	}
	var out []candidate
	var outW []Weighed
	for k, c := range cs {
		if _, member := g.members[c.p.ID]; member && !g.done[c.p.ID] {
			g.narrowSeen(c.p.ID, seen)
		}
		if !g.mine(c) {
			continue
		}
		out = append(out, c)
		if len(ws) == len(cs) {
			outW = append(outW, ws[k])
		}
	}
	if len(ws) != len(cs) {
		outW = nil // rows not index for index: none shown
	}
	return out, outW
}

// only is cs of the user's own alone (mine): for what is spent on an
// account without its turn reaching its provider (a Codex reset).
func (g *groupTurn) only(cs []candidate) []candidate {
	if g == nil {
		return cs
	}
	return slices.DeleteFunc(slices.Clone(cs), func(c candidate) bool { return !g.mine(c) })
}

// user is the first binding narrowed, for the routing log; nil for none.
func (g *groupTurn) user() *UserBound {
	if g == nil {
		return nil
	}
	return g.ub
}

// plan is ws, the routing log's rows, before any provider is narrowed:
// those of providers with nothing to narrow alone, as a decision group's
// (groupUsers.begin), and of those narrowed already (pinned); the rest
// are told as each is narrowed (reach).
func (g *groupTurn) plan(ws []Weighed) []Weighed {
	if g == nil {
		return ws
	}
	return slices.DeleteFunc(slices.Clone(ws), func(w Weighed) bool { return g.bound(w.Provider) && !g.done[w.Provider] })
}

// spare is, for c, one of the user's accounts that failed with status and
// body, a spare of its provider's to ask next to it when c turns out to
// have to be signed in again and is the last of its own there (rest, the
// candidates after it): as a provider's turn adds one (growUser), the
// user's tier letting it have one more. None, else; the group's next
// member, or the lapse told as a limit, then answers.
func (g *groupTurn) spare(c candidate, status int, body []byte, rest []candidate) []candidate {
	if g == nil || c.p.Account == nil {
		return nil
	}
	lastOwn := !slices.ContainsFunc(rest, func(n candidate) bool { return n.p.ID == c.p.ID && g.mine(n) })
	next, refused, lapsed := g.us[c.p.ID].lapsed(c, status, body, lastOwn)
	if !lapsed {
		return nil
	}
	if refused != nil && g.lapsed == nil {
		g.lapsed = refused // told as a provider's turn's would be (userTryRefusal)
	}
	// a spare for this member's model: a member of another model of the
	// provider keeps its own place in the group's order
	next = slices.DeleteFunc(next, func(n candidate) bool { return n.model != c.model })
	for _, n := range next {
		g.own[c.p.ID][strings.ToLower(n.who())] = true
	}
	return next
}

// refusal is why none of the user's own was left to ask: its last own of
// a provider to be signed in again with no spare left, else the first
// provider's refusal, else that none of the accounts it is on of the
// group's members can take the request now. Pinned to its own to be
// signed in again (pinned), that none can is told as a limit, back in
// lapsedRetry, as a provider's turn's lapsed pin is (userLapsedRefused);
// a refusal that is a limit already, or of a binding that can't be
// read, goes as it is.
func (g *groupTurn) refusal() (*UserBound, userRefusal) {
	var ur *userRefusedError
	if errors.As(g.lapsed, &ur) {
		return ur.u, ur.refused
	}
	refused := errors.As(g.refused, &ur)
	user, _ := userOf(g.r.Header)
	if g.pinLapsed != "" && (!refused || !ur.refused.unknown && ur.refused.status == http.StatusServiceUnavailable) {
		return &UserBound{User: user}, userRefusal{status: http.StatusTooManyRequests, limit: failQuota, until: time.Now().Add(lapsedRetry),
			msg: AccountHeader + ": " + g.pinLapsed + "; none of the user's accounts can take it in its place, and none is free to bind it to"}
	}
	if refused {
		return ur.u, ur.refused
	}
	return nil, userRefusal{status: http.StatusServiceUnavailable, until: time.Now().Add(lapsedRetry),
		msg: fmt.Sprintf("%s: none of the accounts %q is on of the group's members can take this request now; no other is tried in its place", UserHeader, user)}
}

// groupTurnRefused answers a group's turn none of whose user's own was
// left to ask (groupTurn), as serve answers a turn its user's binding
// refuses (userRefused), in the stream when its headers went already;
// the error of a try before, which a member left out was to answer for,
// is the call's.
func (s *Server) groupTurnRefused(w http.ResponseWriter, from provider.Protocol, kept *keptAlive, call *Call, g *groupTurn) {
	u, refused := g.refusal()
	before := call.Error
	if kept == nil || !kept.sent {
		s.userRefused(w, from, call, u, refused)
	} else {
		call.Status, call.Error = refused.status, "user bound: "+u.accountOr()+": "+refused.msg
		if refused.limit != "" {
			userLimited(w.Header(), refused.limit, refused.until, refused.model)
		}
		failTo(w, kept, from, refused.status, refused.told())
	}
	if before != "" {
		call.Error += " (tried first: " + before + ")"
	}
}
