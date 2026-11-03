package gateway

// Binding by use (user-tiers.json's by_use, off unless the file turns it
// on, T-058): an account shared by users of a tier that shares (share 2
// or more) takes another user only while what its users use fits in its
// windows. A user's use is its tokens to the provider over the last
// lookback_days, from the usage log (its rows' user, T-020): over a long
// window (a day or more, the week) its total scaled to the window's
// length, over a short one (the five hours) the 90th percentile of what
// it sent in any stretch that long. An account's window holds what magpie
// routed through it since the window began over the share the vendor says
// is used (as the Usage page's Holds), read from the allowances routing
// keeps already: nothing reads an account's usage for this. One whose
// window can't be told takes the median of its provider's others, and
// with none, the tier's share alone holds. A user with under a day of
// use counts as 1/share of an account, the share it takes without by_use.
// It holds only for a new seat (a first account or a spare); nobody bound
// is moved. The log is read in the background, every useEvery, never
// while userBinds is held: a binding reads what was read last.

import (
	"cmp"
	"fmt"
	"log"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// ByUse is user-tiers.json's by_use. Off, or Tiers without it, binds by
// the tiers' share alone.
type ByUse struct {
	On bool `json:"on"`
	// LookbackDays is how many days of each user's use are read
	LookbackDays int `json:"lookback_days"`
	// WeekTarget is how much of a long window (a day or more) an
	// account's users may be reckoned to use, in percent; FiveHourTarget
	// of a short one
	WeekTarget     int `json:"week_target"`
	FiveHourTarget int `json:"five_hour_target"`
}

// byUseJSON is by_use as the file has it: a value left out is the
// default's.
type byUseJSON struct {
	On             bool   `json:"on"`
	LookbackDays   *int   `json:"lookback_days"`
	WeekTarget     *int   `json:"week_target"`
	FiveHourTarget *int   `json:"five_hour_target"`
	Unit           string `json:"unit"`
}

// The defaults of by_use.
const (
	byUseLookback = 7
	byUseWeek     = 90
	byUseFiveHour = 100
)

// parseByUse is by_use as j says it, or why it can't be taken.
func parseByUse(j *byUseJSON) (ByUse, error) {
	if j == nil {
		return ByUse{}, nil
	}
	b := ByUse{On: j.On, LookbackDays: byUseLookback, WeekTarget: byUseWeek, FiveHourTarget: byUseFiveHour}
	set := func(name string, v *int, lo, hi int, to *int) error {
		if v == nil {
			return nil
		}
		if *v < lo || *v > hi {
			return fmt.Errorf("by_use: %s is a whole number from %d to %d, not %d", name, lo, hi, *v)
		}
		*to = *v
		return nil
	}
	if err := set("lookback_days", j.LookbackDays, 1, 30, &b.LookbackDays); err != nil {
		return ByUse{}, err
	}
	if err := set("week_target", j.WeekTarget, 1, 1000, &b.WeekTarget); err != nil {
		return ByUse{}, err
	}
	if err := set("five_hour_target", j.FiveHourTarget, 1, 1000, &b.FiveHourTarget); err != nil {
		return ByUse{}, err
	}
	if u := strings.TrimSpace(j.Unit); u != "" && !strings.EqualFold(u, "tokens") {
		return ByUse{}, fmt.Errorf(`by_use: unit is "tokens", the only one counted, not %q`, j.Unit)
	}
	return b, nil
}

func (b ByUse) lookback() time.Duration {
	return time.Duration(cmp.Or(b.LookbackDays, byUseLookback)) * 24 * time.Hour
}

// useEvery is how often the usage log is read again for by_use.
const useEvery = 10 * time.Minute

// useHistory is the least use a user is reckoned by; with less it counts
// as 1/share of an account.
const useHistory = 24 * time.Hour

// useTally is tokens by the bucket of unit seconds (Unix time / unit →
// tokens), and the first call's time: a user's by the hour, an
// account's by the minute, so a window's start takes in no more than a
// minute before it.
type useTally struct {
	first   time.Time
	unit    int64
	buckets map[int64]int64
}

func (t *useTally) add(at time.Time, n int64) {
	if t.first.IsZero() || at.Before(t.first) {
		t.first = at
	}
	t.buckets[at.Unix()/t.unit] += n
}

// since is the tokens from the bucket at is in on.
func (t *useTally) since(at time.Time) int64 {
	var n int64
	from := at.Unix() / t.unit
	for h, v := range t.buckets {
		if h >= from {
			n += v
		}
	}
	return n
}

// useAccountSpan is how far back an account's calls are read, whatever
// lookback_days says: its week is told only from the week's start.
const useAccountSpan = 7 * 24 * time.Hour

// useSnapshot is the usage log as by_use read it: each user's tokens by
// provider (provider id + "\x00" + user), by the gateway key it called
// with (its CallerKeyID, "" for none), from since to at, and each
// account's (its who lower-cased, "<provider>@<account>"), from
// accSince (useAccountSpan back, or since when that is earlier). A
// user's keys are told apart so that its use is counted by its group
// (groupOf) as the keys share their users when it is read, not as the
// log was (T-092): a name under a key that doesn't share its users is
// another user from the same name under another key.
type useSnapshot struct {
	at, since, accSince time.Time
	users               map[string]map[string]*useTally
	accounts            map[string]*useTally
}

// accountsSince is how far back buildUseSnapshot reads accounts' calls,
// for a snapshot of users' from since.
func accountsSince(now, since time.Time) time.Time {
	if a := now.Add(-useAccountSpan); a.Before(since) {
		return a
	}
	return since
}

// buildUseSnapshot reads each call visit gives from accountsSince on,
// users' from since.
func buildUseSnapshot(now, since time.Time, visit func(func(usage.Record))) *useSnapshot {
	accSince := accountsSince(now, since)
	s := &useSnapshot{at: now, since: since, accSince: accSince, users: map[string]map[string]*useTally{}, accounts: map[string]*useTally{}}
	tally := func(m map[string]*useTally, k string, unit int64) *useTally {
		t := m[k]
		if t == nil {
			t = &useTally{unit: unit, buckets: map[int64]int64{}}
			m[k] = t
		}
		return t
	}
	visit(func(r usage.Record) {
		if r.Time.Before(accSince) || r.Time.After(now) || r.IsRejected() || r.IsReported() || r.Input+r.Output == 0 || r.Provider == "" {
			return
		}
		n := int64(r.Input + r.Output)
		if r.User != "" && !r.Time.Before(since) {
			k := r.Provider + "\x00" + r.User
			if s.users[k] == nil {
				s.users[k] = map[string]*useTally{}
			}
			tally(s.users[k], r.CallerKeyID, 3600).add(r.Time, n)
		}
		if a := r.Account(); a != "" {
			tally(s.accounts, strings.ToLower(r.Provider+"@"+a), 60).add(r.Time, n)
		}
	})
	return s
}

// tallyOf is user's tokens to provider pid under every gateway key of
// its group, as sharers (access.UserSharers) groups them: nil for none.
func (s *useSnapshot) tallyOf(pid string, user userName, sharers map[string]bool) *useTally {
	var t *useTally
	merged := false
	for key, kt := range s.users[pid+"\x00"+user.name] {
		if groupOf(sharers, key) != user.group {
			continue
		}
		if t == nil {
			t = kt
			continue
		}
		if !merged {
			t = &useTally{first: t.first, unit: t.unit, buckets: maps.Clone(t.buckets)}
			merged = true
		}
		if kt.first.Before(t.first) {
			t.first = kt.first
		}
		for h, v := range kt.buckets {
			t.buckets[h] += v
		}
	}
	return t
}

// demand is what user sends provider pid over a window of span, under
// every gateway key of its group as sharers groups them; ok is false
// when it has under useHistory of use, or none.
func (s *useSnapshot) demand(pid string, user userName, sharers map[string]bool, span time.Duration) (_ float64, ok bool) {
	t := s.tallyOf(pid, user, sharers)
	if t == nil {
		return 0, false
	}
	from := t.first
	if from.Before(s.since) {
		from = s.since
	}
	history := s.at.Sub(from)
	if history < useHistory {
		return 0, false
	}
	if span >= 24*time.Hour {
		return float64(t.since(s.since)) * float64(span) / float64(history), true
	}
	// the 90th percentile of what it sent in each stretch of span, by the
	// hour, over its history
	k := int64(math.Ceil(span.Hours()))
	first, last := from.Unix()/3600, s.at.Unix()/3600
	var sums []float64
	for end := first + k - 1; end <= last; end++ {
		var n int64
		for h := end - k + 1; h <= end; h++ {
			n += t.buckets[h]
		}
		sums = append(sums, float64(n))
	}
	if len(sums) == 0 {
		return 0, true
	}
	slices.Sort(sums)
	return sums[int(math.Ceil(0.9*float64(len(sums))))-1], true
}

var useSnap = struct {
	sync.Mutex
	s        *useSnapshot
	lookback time.Duration
	building bool
}{}

// readUse is the log as last read for by_use, looking back lookback, and
// starts a read in the background when that is useEvery old or more,
// or looked back another length: nil before the first is read.
func readUse(lookback time.Duration) *useSnapshot {
	useSnap.Lock()
	defer useSnap.Unlock()
	s := useSnap.s
	if s != nil && useSnap.lookback != lookback {
		s = nil
	}
	if (s == nil || time.Since(s.at) >= useEvery) && !useSnap.building {
		useSnap.building = true
		go func() {
			now := time.Now()
			since := now.Add(-lookback)
			from := accountsSince(now, since)
			n := buildUseSnapshot(now, since, func(fn func(usage.Record)) { usage.Visit(from, fn) })
			useSnap.Lock()
			useSnap.s, useSnap.lookback, useSnap.building = n, lookback, false
			useSnap.Unlock()
			overSaid.Clear() // said again, if still so, by the new read
			log.Printf("user accounts: by use, read %d user(s)' and %d account(s)' calls of the last %d days", len(n.users), len(n.accounts), int(lookback/(24*time.Hour)))
		}()
	}
	return s
}

// useCap is how many tokens one window of an account holds, and how
// much of it its users may be reckoned to use (target, a share).
type useCap struct {
	span   time.Duration
	holds  float64
	target float64
}

// useBudget is what by_use binds a request's user by: each account's
// windows (caps, by who lower-cased). Its users are grouped as
// user-accounts.json was last read (userGroup): load and over are called
// with userBinds held.
type useBudget struct {
	pid  string
	snap *useSnapshot
	caps map[string][]useCap
}

// byUseOf is the budget a new seat of provider pid among cands is bound
// by, nil with by_use off or before the log is first read. It reads the
// allowances routing keeps, never with userBinds held.
func byUseOf(pid string, cands []candidate, tiers Tiers, now time.Time) *useBudget {
	if !tiers.ByUse.On {
		return nil
	}
	snap := readUse(tiers.ByUse.lookback())
	if snap == nil {
		return nil
	}
	b := &useBudget{pid: pid, snap: snap, caps: map[string][]useCap{}}
	target := func(span time.Duration) float64 {
		if span >= 24*time.Hour {
			return float64(cmp.Or(tiers.ByUse.WeekTarget, byUseWeek)) / 100
		}
		return float64(cmp.Or(tiers.ByUse.FiveHourTarget, byUseFiveHour)) / 100
	}
	// spans each account of pid has a window of, and those told
	type span struct {
		who  string
		span time.Duration
	}
	var have []span
	var unread []string // accounts whose allowance isn't known
	told := map[time.Duration][]float64{}
	for _, c := range cands {
		if c.p.ID != pid || c.p.Account == nil {
			continue
		}
		who := strings.ToLower(c.who())
		if slices.ContainsFunc(have, func(s span) bool { return s.who == who }) {
			continue
		}
		a, ok := c.allowance()
		if !ok {
			if !slices.Contains(unread, who) {
				unread = append(unread, who)
			}
			continue
		}
		for _, l := range a {
			if l.Model != "" || l.Span <= 0 {
				continue
			}
			have = append(have, span{who, l.Span})
			start := l.Resets.Add(-l.Span)
			// a window that goes on past itself on credits may have had
			// calls paid so: its share used doesn't tell what it holds
			if l.Credits || l.Used < provider.HoldsFloor || !l.Resets.After(now) || start.Before(snap.accSince) {
				continue
			}
			t := snap.accounts[who]
			if t == nil {
				continue
			}
			if routed := t.since(start); routed > 0 {
				holds := float64(routed) * 100 / l.Used
				b.caps[who] = append(b.caps[who], useCap{l.Span, holds, target(l.Span)})
				told[l.Span] = append(told[l.Span], holds)
			}
		}
	}
	// an account whose allowance isn't read is unknown, never room: it has
	// each window its provider's others are told of
	for _, who := range unread {
		for sp := range told {
			have = append(have, span{who, sp})
		}
	}
	// one not told takes the median of its provider's others of its span
	for _, s := range have {
		if slices.ContainsFunc(b.caps[s.who], func(c useCap) bool { return c.span == s.span }) || len(told[s.span]) == 0 {
			continue
		}
		hs := slices.Clone(told[s.span])
		slices.Sort(hs)
		m := hs[len(hs)/2]
		if len(hs)%2 == 0 {
			m = (hs[len(hs)/2-1] + hs[len(hs)/2]) / 2
		}
		b.caps[s.who] = append(b.caps[s.who], useCap{s.span, m, target(s.span)})
	}
	return b
}

// seatLoad is a user on an account, as by_use counts it: its tier's
// share, and how many accounts of the provider it is on (its use spread
// over them).
type seatLoad struct{ share, seats int }

// load is how much of a window c holds user, on l, is reckoned to use:
// its use over the accounts it is on; with under a day of use, 1/share,
// and nothing for a tier of no limit (share 0), which no share holds.
func (b *useBudget) load(user userName, l seatLoad, c useCap) float64 {
	if d, ok := b.snap.demand(b.pid, user, userBinds.sharers, c.span); ok {
		return d / float64(max(l.seats, 1)) / c.holds
	}
	if l.share <= 0 {
		return 0
	}
	return 1 / float64(l.share)
}

// over is why the account who, with others on it, has no room by use for
// user, on mine: the first window its users with user would be reckoned
// past its target in; "" when it has room or its windows aren't known.
func (b *useBudget) over(who string, others map[userName]seatLoad, user userName, mine seatLoad) string {
	for _, c := range b.caps[who] {
		sum := b.load(user, mine, c)
		for u, l := range others {
			sum += b.load(u, l, c)
		}
		if sum > c.target {
			return fmt.Sprintf("its %s window would be %.0f%% used by its users' use, past its %.0f%%", spanSaid(c.span), sum*100, c.target*100)
		}
	}
	return ""
}

// spanSaid is a window's length as a log says it: "5h", "7d".
func spanSaid(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return strings.TrimSuffix(strings.TrimSuffix(d.Round(time.Minute).String(), "0s"), "0m")
}

// overSaid is each account and user by_use kept apart, logged once a
// change.
var overSaid sync.Map

// noteOver logs that the account who of pid has no room by use for user,
// why, once a change; why "" forgets it.
func noteOver(who string, user userName, why string) {
	k := who + "\x00" + user.group + "\x00" + user.name
	if why == "" {
		overSaid.Delete(k)
		return
	}
	if was, loaded := overSaid.Swap(k, why); loaded && was == why {
		return
	}
	log.Printf("user accounts: by use, %s has no room for %q: %s", who, user.name, why)
}
