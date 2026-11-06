package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
)

// start is a Monday, so the weekend scale falls on days 6 and 7.
var start = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

// turn is one request of a conversation.
type turn struct {
	at   time.Time
	conv int
	n    int // its place in the conversation, from 0
	cost float64
	last bool
}

// demand is every turn of a scenario's conversations, in time order:
// drawn once from the seed, so every routing is run on the same requests.
func demand(s Scenario, seed uint64) (turns []turn, convs int) {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	dm := s.Demand
	end := start.Add(time.Duration(s.Days) * 24 * time.Hour)
	for hour := start; hour.Before(end); hour = hour.Add(time.Hour) {
		rate := dm.PerHour[0]
		if len(dm.PerHour) == 24 {
			rate = dm.PerHour[hour.Hour()]
		}
		if wd := hour.Weekday(); (wd == time.Saturday || wd == time.Sunday) && dm.Weekend != 0 {
			rate *= dm.Weekend
		}
		for range poisson(r, rate) {
			at := hour.Add(time.Duration(r.Float64() * float64(time.Hour)))
			n := 1
			for r.Float64() > 1/dm.Turns {
				n++
			}
			for k := range n {
				turns = append(turns, turn{at: at, conv: convs, n: k, cost: dm.TurnCost * (0.5 + r.Float64()), last: k == n-1})
				at = at.Add(time.Duration(r.ExpFloat64() * float64(dm.Gap)))
				if dm.PauseOdds > 0 && r.Float64() < dm.PauseOdds {
					at = at.Add(time.Duration(r.ExpFloat64() * float64(dm.Pause)))
				}
			}
			convs++
		}
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].at.Before(turns[j].at) })
	return turns, convs
}

func poisson(r *rand.Rand, mean float64) int {
	if mean <= 0 {
		return 0
	}
	l, k, p := math.Exp(-mean), 0, 1.0
	for {
		p *= r.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

type window struct {
	span   time.Duration
	cap    float64
	used   float64 // units
	resets time.Time
	onUse  bool
}

func (w window) share() float64 { return min(100, 100*w.used/w.cap) }

type account struct {
	name      string
	windows   []window
	restUntil time.Time
	tokens    float64 // what was sent lately, an hour's half-life (routed.used)
	tokensAt  time.Time
	// seen is the allowance as routing last read it (lag)
	seen   provider.Allowance
	seenAt time.Time
}

func (a *account) allowance() provider.Allowance {
	out := make(provider.Allowance, len(a.windows))
	for i, w := range a.windows {
		out[i] = provider.Limit{Used: w.share(), Resets: w.resets, Span: w.span}
	}
	return out
}

func (a *account) decayed(now time.Time) float64 {
	if a.tokensAt.IsZero() {
		return 0
	}
	return a.tokens * math.Pow(0.5, now.Sub(a.tokensAt).Hours())
}

// Stats is what one routing did with a scenario.
type Stats struct {
	Routing string `json:"routing"`
	Stays   string `json:"stays"`
	Turns   int    `json:"turns"`
	Failed  int    `json:"failed"`
	// FailedConvs is conversations at least one of whose turns nothing
	// could answer.
	FailedConvs int `json:"failedConvs"`
	Refusals    int `json:"refusals"` // a try turned away for a window used up
	CapHits     int `json:"capHits"`  // of those, by a window under a day (the five hours)
	Moves       int `json:"moves"`    // a turn answered on another account than the turn before
	// Rewrite is units spent writing a conversation's cache again: on
	// another account, or on its own once it went cold there;
	// ColdRewrites counts the turns of the second kind.
	Rewrite      float64 `json:"rewrite"`
	ColdRewrites int     `json:"coldRewrites"`
	Served       float64 `json:"served"` // units of every turn answered, rewrites included
	// WeekRenewals is how many budget windows (a day or longer) renewed
	// in the run, and WeekLeft the share they had left between them:
	// WeekLeft / WeekRenewals is what a renewal loses on average.
	WeekRenewals int     `json:"weekRenewals"`
	WeekLeft     float64 `json:"weekLeft"`
	// Why counts why each turn of a conversation answered before went
	// where it went, as the gateway's Affinity.Why would say.
	Why map[string]int `json:"why"`
	// PerAccount is the units each account answered.
	PerAccount map[string]float64 `json:"perAccount"`
}

func (s Stats) MeanWeekLeft() float64 {
	if s.WeekRenewals == 0 {
		return 0
	}
	return s.WeekLeft / float64(s.WeekRenewals)
}

// Options are the knobs a run is turned by beside its scenario.
type Options struct {
	Routing string // a provider.Routing value, "" for Smart
	Stays   string // "" (Auto) or provider.AffinitySession
	// Lag is how old the allowance routing weighs may be: zero is live
	// (Claude Code's rate_limit_event, Codex's headers after each answer);
	// a minute is provider.Allowances' cache for a vendor read by polling.
	Lag time.Duration
}

// simulated are the routings Run models whole. Rotate and Weight turn a
// conversation round (affine's "new-turn", after) and Free first, Speed
// first, Cheapest and Use before expiry read seat fields Run doesn't
// fill, so a result for them would be wrong, not missing.
var simulated = []string{"smart", provider.Ordered, provider.LeastUsed, provider.Pace, provider.Drain}

func strategy(routing string) (routes.Strategy, error) {
	if !slices.Contains(simulated, routing) {
		return routes.Strategy{}, fmt.Errorf("routing %q isn't simulated: one of %v", routing, simulated)
	}
	if routing == "smart" {
		return routes.Smart, nil
	}
	return routes.Builtin[routing], nil
}

type conv struct {
	last   string // who answered it last
	lastAt time.Time
	failed bool
}

// heldFor is the gateway's: a conversation counts as kept on its account
// for leastHeld while it was answered this recently.
const heldFor = 30 * time.Minute

// Run routes every turn of the scenario's demand by o, as the gateway
// would: seats weighed as weighRouted weighs them, the strategy's order,
// resting ones last with only the one back soonest tried (restLast,
// probeOne), the conversation kept where Stays says (affine), a new one
// spread by leastHeld under Stays: Session, and a try refused by a window
// used up resting its account until that window renews.
func Run(s Scenario, turns []turn, convCount int, o Options) (Stats, error) {
	if o.Routing == "" {
		o.Routing = "smart"
	}
	st, err := strategy(o.Routing)
	if err != nil {
		return Stats{}, err
	}
	routing := o.Routing
	if routing == "smart" {
		routing = ""
	}
	pool := fmt.Sprintf("replay/%s/%s/%s", s.Name, routing, o.Stays)
	routes.Forget(pool)
	accts := make([]*account, len(s.Accounts))
	for i, as := range s.Accounts {
		a := &account{name: as.Name}
		for _, ws := range as.Windows {
			w := window{span: time.Duration(ws.Span), cap: ws.Cap, onUse: ws.OnUse, used: ws.Used / 100 * ws.Cap}
			switch {
			case ws.First > 0:
				w.resets = start.Add(time.Duration(ws.First))
			case !ws.OnUse:
				w.resets = start.Add(w.span)
			}
			a.windows = append(a.windows, w)
		}
		accts[i] = a
	}
	stats := Stats{Routing: o.Routing, Stays: o.Stays, Why: map[string]int{}, PerAccount: map[string]float64{}}
	if stats.Stays == "" {
		stats.Stays = "auto"
	}
	convs := make([]conv, convCount)
	active := map[int]bool{} // conversations answered within heldFor
	spent := provider.SpentShareOf(routing)
	spreads := o.Stays == provider.AffinitySession && (routing == "" || routing == provider.LeastUsed || routing == provider.Pace)
	for _, t := range turns {
		now := t.at
		for _, a := range accts {
			renew(a, now, &stats)
		}
		seats := make([]routes.Seat, len(accts))
		for i, a := range accts {
			if a.seenAt.IsZero() || now.Sub(a.seenAt) >= o.Lag {
				a.seen, a.seenAt = a.allowance(), now
			}
			seats[i] = seat(a, a.seen, now)
		}
		order := st.Order(pool, seats)
		order = restLast(order, seats, accts)
		c := &convs[t.conv]
		why := "first"
		if c.last != "" {
			why = keep(c, accts, seats, order, now, s.cold(), spent, o.Stays)
			stats.Why[why]++
		} else if spreads {
			// affine spreads only a conversation nobody answered yet
			order = leastHeld(order, seats, accts, held(convs, active, now))
		}
		cold := c.last != "" && now.Sub(c.lastAt) > s.cold()
		answered := ""
		for _, i := range order {
			a := accts[i]
			cost := t.cost
			moved := c.last != "" && a.name != c.last
			if moved || cold {
				// nothing cached for it there: its whole conversation so far
				// is written again
				cost += s.Demand.Rewrite * s.Demand.Context * float64(t.n)
			}
			if full, until := usedUp(a, now); full {
				stats.Refusals++
				if fullUnderDay(a, now) {
					stats.CapHits++
				}
				a.restUntil = until
				if routing == provider.Drain {
					routes.Drained(a.name)
				}
				continue
			}
			for k := range a.windows {
				w := &a.windows[k]
				if w.onUse && w.resets.IsZero() {
					w.resets = now.Add(w.span)
				}
				w.used += cost
			}
			a.tokens, a.tokensAt = a.decayed(now)+cost, now
			if moved {
				stats.Moves++
			}
			if cost > t.cost {
				stats.Rewrite += cost - t.cost
				if !moved {
					stats.ColdRewrites++
				}
			}
			stats.Served += cost
			stats.PerAccount[a.name] += cost
			answered = a.name
			break
		}
		stats.Turns++
		if answered == "" {
			stats.Failed++
			if !c.failed {
				c.failed = true
				stats.FailedConvs++
			}
		} else {
			c.last, c.lastAt = answered, now
			active[t.conv] = true
		}
	}
	// the weeks that renew after the last turn, before the run ends
	end := start.Add(time.Duration(s.Days) * 24 * time.Hour)
	for _, a := range accts {
		renew(a, end, &stats)
	}
	return stats, nil
}

// held is how many conversations each account has had answered within
// heldFor, as leastHeld counts its sticks. The one asking is never among
// them: only a conversation nobody answered is spread.
func held(convs []conv, active map[int]bool, now time.Time) map[string]int {
	out := map[string]int{}
	for k := range active {
		c := convs[k]
		if now.Sub(c.lastAt) > heldFor {
			delete(active, k)
			continue
		}
		out[c.last]++
	}
	return out
}

// renew empties each of a's windows whose reset has come, counting what a
// budget window had left as lost.
func renew(a *account, now time.Time, stats *Stats) {
	for k := range a.windows {
		w := &a.windows[k]
		for !w.resets.IsZero() && !w.resets.After(now) {
			if w.span >= 24*time.Hour {
				stats.WeekRenewals++
				stats.WeekLeft += 100 - w.share()
			}
			w.used = 0
			if w.onUse {
				w.resets = time.Time{}
			} else {
				w.resets = w.resets.Add(w.span)
			}
		}
	}
	if !a.restUntil.After(now) {
		a.restUntil = time.Time{}
	}
}

// seat is weighRouted's routes.Seat of one account.
func seat(a *account, al provider.Allowance, now time.Time) routes.Seat {
	const model = "model"
	u, _ := al.For(model, now)
	pace, _ := al.Pace(model, now)
	return routes.Seat{
		Key: a.name, Account: true, Known: true,
		Used:    u,
		WeekLow: u >= routes.LowShare && u < routes.UsedShare && al.CapUsed(model, now) < routes.LowShare,
		Soon:    al.Renewal(model, now),
		Lapse:   al.Lapse(model, now),
		Pace:    pace,
		Resting: a.restUntil.After(now),
		Tokens:  a.decayed(now),
		Spent:   a.restUntil.After(now),
	}
}

// restLast is the gateway's restLast and probeOne: the resting after the
// others, each in the order the strategy gave, and of several resting
// only the one back soonest, which the request probes.
func restLast(order []int, seats []routes.Seat, accts []*account) []int {
	var ready []int
	probe := -1
	for _, i := range order {
		switch {
		case !seats[i].Resting:
			ready = append(ready, i)
		case probe < 0 || accts[i].restUntil.Before(accts[probe].restUntil):
			probe = i
		}
	}
	if probe >= 0 {
		ready = append(ready, probe)
	}
	return ready
}

// keep is affine's reading of a conversation answered before: where it
// stays, and why. A kept one is moved to the front of order.
func keep(c *conv, accts []*account, seats []routes.Seat, order []int, now time.Time, cold time.Duration, spent float64, stays string) string {
	at := slices.IndexFunc(order, func(i int) bool { return accts[i].name == c.last })
	if at < 0 {
		return "gone"
	}
	i := order[at]
	switch {
	case seats[i].Resting:
		return "resting"
	case at > 0 && seats[i].Used >= spent:
		return "spent"
	case stays == provider.AffinitySession:
		bringFirst(order, at)
		return "session"
	case now.Sub(c.lastAt) > cold:
		return "cold"
	}
	bringFirst(order, at)
	return "cache"
}

func afresh(why string) bool {
	switch why {
	case "first", "gone", "resting", "spent", "cold":
		return true
	}
	return false
}

func bringFirst(order []int, at int) {
	if at <= 0 {
		return
	}
	i := order[at]
	copy(order[1:at+1], order[:at])
	order[0] = i
}

// leastHeld is the gateway's leastHeld for a new conversation under
// Stays: Session: with any conversation kept, the first of the fewest
// kept among routing's first and those behind it it would put a new
// conversation on (readyForNew: not resting, below LowShare unless low on
// its week alone).
func leastHeld(order []int, seats []routes.Seat, accts []*account, held map[string]int) []int {
	if len(held) == 0 {
		return order
	}
	at := -1
	for k, i := range order {
		s := seats[i]
		if s.Resting || k > 0 && s.Known && s.Used >= routes.LowShare && !(s.WeekLow && s.Used < routes.UsedShare) {
			continue
		}
		if at < 0 || held[accts[i].name] < held[accts[order[at]].name] {
			at = k
		}
	}
	bringFirst(order, at)
	return order
}

// usedUp says whether a window of a is used up, and when the last of
// those renews: the vendor refuses it until then.
func usedUp(a *account, now time.Time) (bool, time.Time) {
	full, until := false, time.Time{}
	for _, w := range a.windows {
		if w.used >= w.cap && w.resets.After(now) {
			full = true
			if w.resets.After(until) {
				until = w.resets
			}
		}
	}
	return full, until
}

// fullUnderDay: what stops a is only windows under a day.
func fullUnderDay(a *account, now time.Time) bool {
	for _, w := range a.windows {
		if w.used >= w.cap && w.resets.After(now) && w.span >= 24*time.Hour {
			return false
		}
	}
	return true
}
