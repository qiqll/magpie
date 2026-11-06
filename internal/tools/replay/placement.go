package main

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Placements is where routing put each conversation, read from routing/:
// a stint is the requests a conversation had answered on one account,
// from the request routing put it there (its first, or a move) to the
// request it left or its last. Each says what the account had left when
// the stint began — the share its fullest window had left, 100 less the
// Weighed's Used, which is what the stint could spend before a window
// refused it — and what the best other account ready then had, and how
// the stint ended. It tells whether conversations put on an account with
// little left were moved for it soon after while another had room.
//
// Under a user's binding (Route.User) a request's Order is the user's own
// accounts alone, Smart's order among them: so for a bound user, "another
// had more" is another of that user's accounts. Which accounts a user is
// bound to is bindUser's pick (the one fewest users are on), not this.
type Placements struct {
	Convs  int `json:"convs"`
	Stints int `json:"stints"`
	// Started counts stints by how they began: "first" (routing's pick for
	// a new conversation), "moved" (the conversation left another
	// account), "evicted" (routing had forgotten the conversation, its
	// stick dropped, and put it afresh), "seen" (its first route is before
	// the oldest kept) or "unkept" (Stays: Off, or no affinity told).
	Started map[string]int `json:"started"`
	// Pools are the stints of bound users' conversations and of the rest.
	Pools []Pool `json:"pools"`
	// Rise is how far the account's fullest window rose over a stint that
	// ended with its conversation and saw no renewal, at the 50th, 75th
	// and 90th centile: what a conversation, and whatever else the account
	// answered meanwhile, cost it up to its last request.
	Rise       [3]float64 `json:"rise"`
	RiseStints int        `json:"riseStints"`
	// Ready is how many accounts routing could have put a new conversation
	// on (readyForNew: told or about to be, not resting, held, barred,
	// capped, a reserve, a fallback, nor past LowShare unless low on its
	// week alone), at the 50th and 90th centile over first stints.
	Ready [2]float64 `json:"ready"`
	// Users is how many users were bound to accounts, and Bound how many
	// accounts each was bound to at the 50th and 90th centile.
	Users int        `json:"users"`
	Bound [2]float64 `json:"bound"`
	// Unanswered is a conversation's requests no try answered, and
	// Untold those whose Order was empty.
	Unanswered int `json:"unanswered"`
	Untold     int `json:"untold"`
	// UnansweredWhy is those requests by why their last try failed (its
	// Fail, else its status), "no try" when none was made, and the user's
	// binding why (full, none) beside it when there is one.
	UnansweredWhy map[string]int `json:"unansweredWhy"`
	// MoveCauses is the conversations' moves from one account to another
	// (a stint's end other than its conversation's) by what the moving
	// request's route says: "one" or "several" accounts in its order,
	// the user's binding why, whether the binding changed since the
	// conversation's request before ("changed") or not ("same"), and
	// where the account it left was then: "ready", or why not
	// (resting, capped, lapsed, held, barred, reserve, spent, low...), in
	// the order or "left:" out of it, or "absent" from both, and
	// "unbound" when the user's binding no longer names it.
	MoveCauses map[string]int `json:"moveCauses"`
}

// Pool is stints bucketed by their account's headroom as they began.
type Pool struct {
	Name    string   `json:"name"` // "bound" or "unbound"
	Buckets []Bucket `json:"buckets"`
	Unknown Bucket   `json:"unknown"` // the account's use wasn't told
}

// Bucket is the stints that began with their account's headroom in
// [From, To).
type Bucket struct {
	From, To float64
	Stints   int `json:"stints"`
	First    int `json:"first"` // of them, a new conversation's
	// Forced: it ended as its account couldn't take the next request:
	// "spent" (all but used up), "resting", "refused" (a try on it was
	// turned away for quota, credit or rate, and another answered),
	// "failed:<why>" (a try on it failed otherwise, and another answered)
	// or "stranded" (the conversation's last requests on it were refused
	// and nothing answered after). Ended counts every way a stint ended,
	// those too: "end" (the conversation's last), "cold", "gone" and
	// others as Affinity.Why says.
	Forced int            `json:"forced"`
	Ended  map[string]int `json:"ended"`
	// Requests is the stints' median count of requests answered (tool
	// round trips within a turn are each one).
	Requests float64 `json:"requests"`
	// FiveHours and Week: of those whose fullest window was told, how
	// many had the five hours (a window shorter than the biggest), or the
	// biggest, as the fullest.
	FiveHours int `json:"fiveHours"`
	Week      int `json:"week"`
	// Roomier: another ready account had at least roomier (30) points
	// more left when it began.
	Roomier int `json:"roomier"`
}

const roomier = 30

var bucketEdges = []float64{0, 10, 25, 50, 75, 101}

func forced(why string) bool {
	switch why {
	case "spent", "resting", "refused", "stranded":
		return true
	}
	return len(why) > 7 && why[:7] == "failed:"
}

// ofConversation: a route of a conversation's own request, routed: not a
// side request (a title), a subagent's, nor one pinned to an account.
func (l routeLine) ofConversation() bool {
	return l.Session != "" && l.Kind == "" && l.Sub == "" && l.SubagentID == "" && len(l.Subagent) == 0 && l.Pinned == ""
}

// slim is the route as place reads it, so a long history stays small:
// of a route not a conversation's own, only its user's binding; of each
// account weighed, only what place reads.
func (l routeLine) slim() routeLine {
	if !l.ofConversation() {
		return routeLine{User: l.User}
	}
	for i, w := range l.Order {
		fullest := ""
		switch {
		case w.UsedRenews == nil || len(w.Renews) == 0:
		case len(w.Renews) > 1 && !w.UsedRenews.Equal(w.Renews[0]):
			fullest = "short"
		default:
			fullest = "long"
		}
		l.Order[i] = weighedLine{ID: w.ID, Who: w.Who, Known: w.Known, Used: w.Used, UsedRenews: w.UsedRenews, fullest: fullest, isReady: w.readyForNew(), state: w.status()}
	}
	for i, w := range l.Left {
		l.Left[i] = weighedLine{ID: w.ID, Who: w.Who, state: w.status()}
	}
	return l
}

// status is why an account weighed couldn't take a new conversation, or
// "ready".
func (w weighedLine) status() string {
	switch {
	case w.Rest != nil:
		return "resting"
	case w.Capped != 0:
		return "capped"
	case w.Lapsed != "":
		return "lapsed"
	case w.Held:
		return "held"
	case w.Barred:
		return "barred"
	case w.Reserve != 0:
		return "reserve"
	case w.Aside:
		return "aside"
	case w.Fallback:
		return "fallback"
	case w.Known && w.Used >= UsedShare:
		return "spent"
	case w.Known && w.Used >= LowShare && !w.WeekLow:
		return "low"
	case !w.Known && !w.Learns:
		return "untold"
	}
	return "ready"
}

// boundTo is the accounts the route's user is bound to, by name in lower
// case; nil when it isn't bound.
func (l routeLine) boundTo() map[string]bool {
	if l.User == nil || l.User.User == "" {
		return nil
	}
	m := map[string]bool{}
	for _, a := range append([]string{l.User.Account}, l.User.Spares...) {
		if a != "" {
			m[strings.ToLower(a)] = true
		}
	}
	return m
}

// whereIs is where the account id (named who) was in the route: its
// status in the order, "left:" and its status out of it, else "absent";
// "unbound" before it when the user's binding doesn't name it.
func (l routeLine) whereIs(id, who string) string {
	at := "absent"
	if i := slices.IndexFunc(l.Order, func(w weighedLine) bool { return w.ID == id }); i >= 0 {
		at = l.Order[i].state
	} else if i := slices.IndexFunc(l.Left, func(w weighedLine) bool { return w.ID == id }); i >= 0 {
		at = "left:" + l.Left[i].state
	}
	if b := l.boundTo(); b != nil && who != "" && !b[strings.ToLower(who)] {
		if at == "absent" {
			return "unbound"
		}
		return "unbound, " + at
	}
	return at
}

// unansweredWhy is why nothing answered the route.
func (l routeLine) unansweredWhy() string {
	why := "no try"
	if n := len(l.Tries); n > 0 {
		t := l.Tries[n-1]
		why = cmp.Or(t.Fail, fmt.Sprintf("status %d", t.Status))
	}
	if l.User != nil && (l.User.Why == "full" || l.User.Why == "none") {
		why += " (user " + l.User.Why + ")"
	}
	return why
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func (l routeLine) user() string {
	if l.User != nil {
		return l.User.User
	}
	return ""
}

// answered is the ID of the try that answered, "" when none did.
func (l routeLine) answered() string {
	for i := len(l.Tries) - 1; i >= 0; i-- {
		if t := l.Tries[i]; t.Status >= 200 && t.Status < 300 {
			return t.ID
		}
	}
	return ""
}

func (l routeLine) weighed(id string) (weighedLine, bool) {
	for _, w := range l.Order {
		if w.ID == id {
			return w, true
		}
	}
	return weighedLine{}, false
}

// failedOn is why a try on id failed in the route: "refused" for its
// quota, credit or rate, "failed:<why>" otherwise, "" when none did.
func (l routeLine) failedOn(id string) string {
	why := ""
	for _, t := range l.Tries {
		if t.ID != id || t.Status >= 200 && t.Status < 300 {
			continue
		}
		switch t.Fail {
		case "quota", "credit", "rate":
			return "refused"
		case "", "canceled":
		default:
			why = "failed:" + t.Fail
		}
	}
	return why
}

// readyForNew is the gateway's readyForNew, as a Weighed tells it.
func (w weighedLine) readyForNew() bool {
	if w.Rest != nil || w.Barred || w.Held || w.Capped != 0 || w.Reserve != 0 || w.Lapsed != "" || w.Fallback || w.Aside {
		return false
	}
	if !w.Known {
		return w.Learns
	}
	return w.Used < LowShare || w.WeekLow && w.Used < UsedShare
}

// LowShare and UsedShare are routes'.
const (
	LowShare  = 90
	UsedShare = 98
)

type stint struct {
	id       string
	who      string
	bound    bool
	headroom float64 // -1: not told
	best     float64 // the best other ready account's headroom, -1: none
	fullest  string
	started  string
	ended    string
	requests int
	from, to float64 // its account's Used at the stint's first and last request
	toKnown  bool
	window   *time.Time // its fullest window's renewal, last told
	renewed  bool
	refused  bool // its last requests were refused on it, none answered since
}

func place(routes []routeLine) *Placements {
	p := &Placements{Started: map[string]int{}, UnansweredWhy: map[string]int{}, MoveCauses: map[string]int{}}
	bound := map[string]int{}
	byConv := map[string][]routeLine{}
	for _, l := range routes {
		if l.User != nil && l.User.User != "" {
			bound[l.User.User] = max(bound[l.User.User], 1+len(l.User.Spares))
		}
		if !l.ofConversation() {
			continue
		}
		// the gateway keeps a conversation by its id and user, whatever
		// model it is on (stickKey)
		k := l.Provider + "\x00" + l.user() + "\x00" + l.Session
		byConv[k] = append(byConv[k], l)
	}
	var stints []stint
	var ready []float64
	for _, rs := range byConv {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Time.Before(rs[j].Time) })
		var cur *stint
		var lastBound map[string]bool
		for k, l := range rs {
			if len(l.Order) == 0 {
				p.Untold++
			}
			changed := k > 0 && !sameSet(lastBound, l.boundTo())
			lastBound = l.boundTo()
			id := l.answered()
			if id == "" {
				p.Unanswered++
				p.UnansweredWhy[l.unansweredWhy()]++
				if cur != nil && l.failedOn(cur.id) == "refused" {
					cur.refused = true
				}
				continue
			}
			why := ""
			if l.Affinity != nil {
				why = l.Affinity.Why
			}
			if cur == nil || id != cur.id || why == "first" {
				started := "seen"
				switch {
				case why == "first" && cur == nil:
					started = "first"
				case why == "first":
					started = "evicted"
				case cur != nil:
					started = "moved"
				case why == "" || why == "off":
					started = "unkept"
				}
				if cur != nil {
					cur.ended = "moved"
					if f := l.failedOn(cur.id); f != "" {
						cur.ended = f
					} else if cur.refused {
						cur.ended = "refused"
					} else if why != "" {
						cur.ended = why
					}
					stints = append(stints, *cur)
					if id != cur.id {
						n, uw, bc := "several", "-", "same"
						if len(l.Order) == 1 {
							n = "one"
						}
						if l.User != nil && l.User.User != "" {
							uw = cmp.Or(l.User.Why, "?")
						}
						if changed {
							bc = "changed"
						}
						p.MoveCauses[n+" · user "+uw+" · binding "+bc+" · left "+l.whereIs(cur.id, cur.who)]++
					}
				}
				cur = &stint{id: id, bound: l.User != nil && l.User.User != "", headroom: -1, best: -1, started: started}
				if w, ok := l.weighed(id); ok {
					cur.who = w.Who
				}
				if w, ok := l.weighed(id); ok && w.Known {
					cur.from = w.Used
					cur.headroom = min(100, max(0, 100-w.Used))
					cur.fullest = w.fullest
				}
				n := 0
				for _, w := range l.Order {
					if !w.isReady {
						continue
					}
					n++
					if w.ID != id && w.Known && 100-w.Used > cur.best {
						cur.best = 100 - w.Used
					}
				}
				if started == "first" {
					ready = append(ready, float64(n))
				}
			}
			cur.requests++
			cur.refused = false // it answered
			if w, ok := l.weighed(cur.id); ok && w.Known {
				last := cur.from
				if cur.toKnown {
					last = cur.to
				}
				// a window renewed in the stint: its share fell, or the
				// fullest window's renewal moved
				if w.Used < last || cur.window != nil && w.UsedRenews != nil && !w.UsedRenews.Equal(*cur.window) {
					cur.renewed = true
				}
				cur.to, cur.toKnown, cur.window = w.Used, true, w.UsedRenews
			}
		}
		if cur != nil {
			cur.ended = "end"
			if cur.refused {
				cur.ended = "stranded"
			}
			stints = append(stints, *cur)
			p.Convs++
		}
	}
	newPool := func(name string) Pool {
		pl := Pool{Name: name, Unknown: Bucket{From: -1, To: -1, Ended: map[string]int{}}}
		for i := 0; i+1 < len(bucketEdges); i++ {
			pl.Buckets = append(pl.Buckets, Bucket{From: bucketEdges[i], To: bucketEdges[i+1], Ended: map[string]int{}})
		}
		return pl
	}
	p.Pools = []Pool{newPool("bound"), newPool("unbound")}
	reqs := map[*Bucket][]float64{}
	var rises []float64
	for _, s := range stints {
		p.Stints++
		p.Started[s.started]++
		pl := &p.Pools[1]
		if s.bound {
			pl = &p.Pools[0]
		}
		b := &pl.Unknown
		if s.headroom >= 0 {
			for k := range pl.Buckets {
				if s.headroom >= pl.Buckets[k].From && s.headroom < pl.Buckets[k].To {
					b = &pl.Buckets[k]
					break
				}
			}
		}
		b.Stints++
		if s.started == "first" {
			b.First++
		}
		b.Ended[s.ended]++
		if forced(s.ended) {
			b.Forced++
		}
		switch s.fullest {
		case "short":
			b.FiveHours++
		case "long":
			b.Week++
		}
		if s.headroom >= 0 && s.best >= s.headroom+roomier {
			b.Roomier++
		}
		reqs[b] = append(reqs[b], float64(s.requests))
		if s.ended == "end" && s.headroom >= 0 && s.toKnown && !s.renewed {
			rises = append(rises, s.to-s.from)
		}
	}
	for i := range p.Pools {
		pl := &p.Pools[i]
		for k := range pl.Buckets {
			pl.Buckets[k].Requests = centile(reqs[&pl.Buckets[k]], 50)
		}
		pl.Unknown.Requests = centile(reqs[&pl.Unknown], 50)
	}
	p.RiseStints = len(rises)
	p.Rise = [3]float64{centile(rises, 50), centile(rises, 75), centile(rises, 90)}
	p.Ready = [2]float64{centile(ready, 50), centile(ready, 90)}
	var bs []float64
	for _, n := range bound {
		bs = append(bs, float64(n))
	}
	p.Users = len(bound)
	p.Bound = [2]float64{centile(bs, 50), centile(bs, 90)}
	return p
}

// centile is the q-th centile of v, nearest rank; 0 when v is empty.
func centile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(q/100*float64(len(s))+0.5) - 1
	return s[min(max(i, 0), len(s)-1)]
}
