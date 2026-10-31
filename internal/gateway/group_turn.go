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
	refused error // the first provider's refusal of the user (a userRefusedError)
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

// askable says c may still be asked: one of the user's own (mine), or of
// a member's provider not narrowed yet, which reach narrows. nil, every
// one is.
func (g *groupTurn) askable(c candidate) bool {
	if g == nil {
		return true
	}
	_, member := g.members[c.p.ID]
	return g.mine(c) || member && !g.done[c.p.ID]
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
	g.own[pid], g.us[pid] = set, u
	return opl.order, opl.left
}

// reach is cs with its provider narrowed when the tries reach cs[i]
// first, every one of that provider's from i on that isn't the user's
// left out, until cs[i] is one of the user's own to ask, or none is left
// (i past the end). seen is told the routing log's plan of each narrowed.
func (g *groupTurn) reach(cs []candidate, i int, seen func(order, left []Weighed)) []candidate {
	if g == nil {
		return cs
	}
	for i < len(cs) {
		pid := cs[i].p.ID
		if !g.done[pid] {
			if order, left := g.narrow(pid); seen != nil && len(order)+len(left) > 0 {
				seen(order, left)
			}
			cs = append(slices.Clone(cs[:i]), slices.DeleteFunc(slices.Clone(cs[i:]), func(c candidate) bool { return c.p.ID == pid && !g.mine(c) })...)
			continue
		}
		if g.mine(cs[i]) {
			return cs
		}
		cs = slices.Delete(slices.Clone(cs), i, i+1)
	}
	return cs
}

// keep is cs, candidates to be asked next, without those that may not be
// (askable), and ws, their rows index for index, with them.
func (g *groupTurn) keep(cs []candidate, ws []Weighed) ([]candidate, []Weighed) {
	if g == nil {
		return cs, ws
	}
	var out []candidate
	var outW []Weighed
	for k, c := range cs {
		if !g.askable(c) {
			continue
		}
		out = append(out, c)
		if len(ws) == len(cs) {
			outW = append(outW, ws[k])
		}
	}
	if len(ws) != len(cs) {
		outW = ws
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

// plan is ws, the routing log's rows, before any provider is narrowed:
// those of providers with nothing to narrow alone, as a decision group's
// (groupUsers.begin); the rest are told as each is narrowed (reach).
func (g *groupTurn) plan(ws []Weighed) []Weighed {
	if g == nil {
		return ws
	}
	return slices.DeleteFunc(slices.Clone(ws), func(w Weighed) bool { return g.bound(w.Provider) })
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
	lastOwn := !slices.ContainsFunc(rest, func(n candidate) bool { return n.p.ID == c.p.ID })
	next, _, lapsed := g.us[c.p.ID].lapsed(c, status, body, lastOwn)
	if !lapsed {
		return nil
	}
	for _, n := range next {
		g.own[c.p.ID][strings.ToLower(n.who())] = true
	}
	return next
}

// refusal is why none of the user's own was left to ask: the first
// provider's refusal, else that none of the accounts it is on of the
// group's members can take the request now.
func (g *groupTurn) refusal() (*UserBound, userRefusal) {
	var ur *userRefusedError
	if errors.As(g.refused, &ur) {
		return ur.u, ur.refused
	}
	user, _ := userOf(g.r.Header)
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
		failTo(w, kept, from, refused.status, refused.told())
	}
	if before != "" {
		call.Error += " (tried first: " + before + ")"
	}
}
