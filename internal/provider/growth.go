package provider

// The growth policy (T-078, settings/growth.go): a subscription account
// added lately is held at the share of its windows the policy in effect
// gives for its days (a ramp's, or its stage's: policyCaps, T-129), as a
// cap the user set on it would hold it — routing takes it for used up
// there and goes on to the next account
// (gateway/fallback.go capHeld, codex_switch.go). An account is the
// policy's from AccountAdded, written as logins.json is written with an
// account it didn't have (noteAdded): Claude's and Codex's sign-ins, a
// plugin's, the side logins', an agent's own read the first time. Its days
// are counted from its first use (AccountFirstUsed, 需求方 2026-11-09): an
// account added long ago and first used today is on its day 0, where
// counting from AccountAdded took it for old and held it not at all. Use
// is a request its vendor served through the gateway (NoteAccountUsed, the
// ledger's served rows): never a keep-alive, a usage read, a warm-up or the
// editor's Test. Accounts added before first use was kept had theirs read
// from the ledger once (BackfillFirstUsed); until that read went through,
// their days count from AccountAdded as before. An account added
// before magpie kept AccountAdded has none, and is not held; as it leaves the list
// it is marked from before (addedBefore), so it isn't new when back. The
// time stays while
// the account is gone from the list and back (a plugin's, when its
// sign-ins can't be read a while, or the plugin is installed again): only
// the user removing it (forgetAdded) lets it go, its first use with it, and
// one removed and added again is new again.
//
// The user's word comes first: an account with a cap of its own
// (AccountCaps) holds every window at it, and a window with a share of its
// own (AccountWindowCaps, 100 among them) keeps it. The policy fills in
// only the windows the user left alone.

import (
	"cmp"
	"log"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// growthWindows are the windows (WindowCapID) each of the policy's windows
// holds: the 5 hours, and the week as each vendor names it, per model too.
var growthWindows = map[string][]string{
	settings.GrowthFiveHours: {"5 hours"},
	settings.GrowthWeek:      {"weekly", "7 days", "7 days · opus", "7 days · sonnet", "7 days · fable"},
}

// addedBefore is the time kept for an account from before AccountAdded,
// written as it leaves logins.json: back in the list, it is had, not
// added. It is no time an account was added at: never held, and told as
// not known (accountAdded).
var addedBefore = time.Unix(0, 0).UTC()

// addedKnown is whether added is a time an account was added at: not zero,
// and not addedBefore.
func addedKnown(added time.Time) bool { return added.After(addedBefore) }

// growthCaps is the share of each window (WindowCapID) the policy stages
// hold an account whose days count from added (growthFrom) to at now: each
// window at the share of the latest started stage that lists it, so a
// window a later stage doesn't name keeps the share it had (T-128: the
// default's 5 hours stayed at 50% from day 3, where the week's stage
// alone let it go). nil when no stage has started, or added is not known
// (an account from before).
func growthCaps(stages []settings.GrowthStage, added, now time.Time) map[string]int {
	if !addedKnown(added) || len(stages) == 0 {
		return nil
	}
	days := now.Sub(added).Hours() / 24
	latest := map[string]settings.GrowthStage{}
	for _, g := range stages {
		if float64(g.From) > days {
			continue
		}
		if l, ok := latest[g.Window]; !ok || g.From > l.From {
			latest[g.Window] = g
		}
	}
	if len(latest) == 0 {
		return nil
	}
	out := map[string]int{}
	for win, g := range latest {
		for _, w := range growthWindows[win] {
			out[w] = g.Cap
		}
	}
	return out
}

// policyCaps is the share of each window (WindowCapID) the policy p holds
// an account whose days count from added to at now: a ramp's (rampCaps),
// stages' (growthCaps), none for Off. nil when it holds nothing, or added
// is not known (an account from before).
func policyCaps(p settings.GrowthPolicy, added, now time.Time) map[string]int {
	switch p.Kind {
	case settings.GrowthRamp:
		return rampCaps(p.Start, p.Days, added, now)
	case settings.GrowthStaged:
		return growthCaps(p.Stages, added, now)
	}
	return nil
}

// rampCaps is the share of each window a ramp from start% to 100% in
// days holds an account whose days count from added to at now, the 5
// hours and the week alike: start on its day 0, start+(100-start)×d/days
// on its day d (whole days gone, the share's fraction dropped), so it
// rises once a day; none from day days on, where it reaches 100%. A
// day before added (a clock set back) is day 0. nil when added is not
// known (an account from before).
func rampCaps(start, days int, added, now time.Time) map[string]int {
	if !addedKnown(added) || days <= 0 || start >= 100 {
		return nil
	}
	d := max(int(now.Sub(added).Hours()/24), 0)
	if d >= days {
		return nil
	}
	share := max(start+(100-start)*d/days, 1)
	if share >= 100 {
		return nil
	}
	out := map[string]int{}
	for _, wins := range growthWindows {
		for _, w := range wins {
			out[w] = share
		}
	}
	return out
}

// withGrowth is c with the policy's shares for an account added at added,
// at now, on the windows the user set nothing on; c as it is when the
// account has a cap of its own, which holds every window. c's map is
// never written: one with growth is a copy.
func (c WindowCaps) withGrowth(added, now time.Time) WindowCaps {
	if !addedKnown(added) || c.All > 0 {
		return c
	}
	g := policyCaps(settings.Shared().GrowthInEffect(), added, now)
	if len(g) == 0 {
		return c
	}
	wins := maps.Clone(c.Windows)
	if wins == nil {
		wins = map[string]int{}
	}
	for w, share := range g {
		if _, own := wins[w]; !own {
			wins[w] = share
		}
	}
	c.Windows = wins
	return c
}

// GrowthPoint is the share of the 5 hours and of the week a policy holds an
// account to on its day Day, 100 where it holds nothing.
type GrowthPoint struct {
	Day       int `json:"day"`
	FiveHours int `json:"fiveHours"`
	Week      int `json:"week"`
}

// GrowthCurve is p's shares on each day from 0 to last, read through
// policyCaps, which the gateway's holds read too (withGrowth): Settings ›
// Growth policy draws its chart from it, never from a formula of its own
// (T-130).
func GrowthCurve(p settings.GrowthPolicy, last int) []GrowthPoint {
	added := time.Unix(1<<30, 0).UTC()
	share := func(caps map[string]int, w string) int {
		if s, ok := caps[growthWindows[w][0]]; ok {
			return s
		}
		return 100
	}
	out := make([]GrowthPoint, 0, last+1)
	for d := 0; d <= last; d++ {
		caps := policyCaps(p, added, added.Add(time.Duration(d)*24*time.Hour))
		out = append(out, GrowthPoint{Day: d, FiveHours: share(caps, settings.GrowthFiveHours), Week: share(caps, settings.GrowthWeek)})
	}
	return out
}

// GrowthHorizon is the last day GrowthCurve is read to for p: a quarter
// past the day p last changes a share (a ramp's Days, its latest stage's
// From), so the chart shows it level after; 7 at least, GrowthDaysMost
// at most.
func GrowthHorizon(p settings.GrowthPolicy) int {
	last := 0
	switch p.Kind {
	case settings.GrowthRamp:
		last = p.Days
	case settings.GrowthStaged:
		for _, g := range p.Stages {
			last = max(last, g.From)
		}
	}
	return min(max(last+(last+3)/4, 7), settings.GrowthDaysMost)
}

// GrowthAccount is an account the growth policy counts the days of, as
// Settings › Growth policy marks it on its chart: its subscription's id
// and name, its own name and its day (growthFrom, as the gateway counts
// it). Waiting is one not used yet, on day 0 until its first use. Nothing
// of its sign-in is in it.
type GrowthAccount struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Account  string `json:"account"`
	Day      int    `json:"day"`
	Waiting  bool   `json:"waiting,omitempty"`
}

// GrowthAccounts are the accounts with a day at now, by subscription and
// name. One from before AccountAdded was kept has none and isn't listed.
func GrowthAccounts(now time.Time) []GrowthAccount {
	all, f := sharedAll(), storedFile()
	var out []GrowthAccount
	for _, s := range f.Providers {
		name := s.ID
		if p := lookIn(all, s.ID); p != nil && p.Name != "" {
			name = p.Name
		}
		for u := range s.AccountAdded {
			from := growthFrom(s.ID, u, now)
			if !addedKnown(from) {
				continue
			}
			out = append(out, GrowthAccount{
				Provider: s.ID, Name: name, Account: u,
				Day:     max(int(now.Sub(from).Hours()/24), 0),
				Waiting: gatewayServed(s) && f.FirstUsedRead && !addedKnown(s.AccountFirstUsed[u]),
			})
		}
	}
	slices.SortFunc(out, func(a, b GrowthAccount) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Account, b.Account))
	})
	return out
}

// growthFrom is when the growth policy counts the days of the account
// user of the subscription id from: its first use; now (day 0) for one not
// used yet, once the ledger was read for it (FirstUsedRead); its
// AccountAdded until then, as before. An account the gateway doesn't serve
// (gatewayServed) counts from AccountAdded always: its agent asks its
// vendor itself, so its first use is never seen, and day 0 would hold it at
// the first stage for good. Zero for one whose AccountAdded isn't known
// (from before), which is never held. CapsOf (the gateway's holds) and
// AccountCapsOf (switching the agent's own sign-in) both read it, so the
// two never disagree.
func growthFrom(id, user string, now time.Time) time.Time {
	s, ok := storedPicks(id)
	if !ok {
		return time.Time{}
	}
	k := accountKey(user)
	added := s.AccountAdded[k]
	if !addedKnown(added) {
		return time.Time{}
	}
	if !gatewayServed(s) {
		return added
	}
	if used := s.AccountFirstUsed[k]; addedKnown(used) {
		return used
	}
	if storedFile().FirstUsedRead {
		return now
	}
	return added
}

// gatewayServed says the accounts of s (a subscription's stored entry) are
// used through the gateway, where their first use is seen: every
// subscription's but Claude Code's kept to the agent (Provider.KeptToAgent's
// SwitchOnly) or not sent through magpie (ClaudeThroughMagpie), and Codex's
// switch-only. Read at each ask, so a provider moved from one side to the
// other follows at the next: moved to served, an account with no first use
// is on day 0 until the gateway serves it.
func gatewayServed(s Provider) bool {
	switch s.ID {
	case "claude":
		return !s.SwitchOnly && ClaudeThroughMagpie()
	case "codex":
		return !s.SwitchOnly
	}
	return true
}

// AccountFirstUsedOf is when the account user of the subscription id was
// first used, zero for none kept, for the GUI to say.
func AccountFirstUsedOf(id, user string) time.Time {
	if s, ok := storedPicks(id); ok {
		if at := s.AccountFirstUsed[accountKey(user)]; addedKnown(at) {
			return at
		}
	}
	return time.Time{}
}

// firstUsedSeen are the accounts (id and name, accountKey) whose first use
// this magpie has seen kept, each with the AccountAdded it was kept under:
// a request asks this set, and providers.json's memo (storedPicks, a stat
// while the file is unchanged) for the account's AccountAdded, and only an
// account's first request here, or its first since it was added again
// (here or by another magpie), writes. An account with no AccountAdded yet
// is not marked, so it is looked at again once it has one.
var firstUsedSeen struct {
	sync.Mutex
	m map[loginAccount]time.Time
}

// forgetFirstUsedSeen lets every account be looked at again.
func forgetFirstUsedSeen() {
	firstUsedSeen.Lock()
	firstUsedSeen.m = nil
	firstUsedSeen.Unlock()
}

// NoteAccountUsed notes that the vendor of the subscription id served a
// request of its account user at at (a served row of the gateway's, as
// usage.Record.ServedOn tells it): the first one is the account's first
// use, written once beside its AccountAdded. It is asked on every request,
// takes no lock a request waits on but a moment's map lock, and is called
// before the caller takes any of its own (budget.Append). An account whose
// AccountAdded isn't known (from before, or not listed yet) gets none.
func NoteAccountUsed(id, user string, at time.Time) {
	a := loginAccount{id, accountKey(user)}
	if a.id == "" || a.user == "" {
		return
	}
	s, _ := storedPicks(a.id)
	added := s.AccountAdded[a.user]
	if !addedKnown(added) {
		return // no account of the policy's, or not listed yet: looked at again
	}
	firstUsedSeen.Lock()
	if seen, ok := firstUsedSeen.m[a]; ok && seen.Equal(added) {
		firstUsedSeen.Unlock()
		return
	}
	if firstUsedSeen.m == nil {
		firstUsedSeen.m = map[loginAccount]time.Time{}
	}
	firstUsedSeen.m[a] = added // claimed: a request beside this one writes nothing
	firstUsedSeen.Unlock()
	if addedKnown(s.AccountFirstUsed[a.user]) {
		return // kept already
	}
	if !writeFirstUsed(a, at) {
		// not written: the next request of the account tries again
		firstUsedSeen.Lock()
		delete(firstUsedSeen.m, a)
		firstUsedSeen.Unlock()
	}
}

// beforeFirstUsedWrite is called as a first use is about to be written,
// for a test to hold it there.
var beforeFirstUsedWrite = func() {}

// writeFirstUsed keeps at as when account a was first used, when it has
// none and its AccountAdded is known; false when providers.json couldn't
// be read or written, which keeps nothing.
func writeFirstUsed(a loginAccount, at time.Time) bool {
	beforeFirstUsedWrite()
	if err := providersMu.Take(); err != nil {
		log.Printf("%v; %s's first use isn't kept yet", err, a.user)
		return false
	}
	defer providersMu.Unlock()
	f, err := read()
	if err != nil {
		log.Printf("providers.json: %v; %s's first use isn't kept yet", err, a.user)
		return false
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == a.id })
	if i < 0 {
		return true
	}
	p := &f.Providers[i]
	added := p.AccountAdded[a.user]
	if !addedKnown(added) || addedKnown(p.AccountFirstUsed[a.user]) {
		return true
	}
	if at.Before(added) {
		// a request of the account's life before it was removed and
		// added again, ended after: its new life starts now at the earliest
		at = added
	}
	p.AccountFirstUsed = maps.Clone(p.AccountFirstUsed)
	if p.AccountFirstUsed == nil {
		p.AccountFirstUsed = map[string]time.Time{}
	}
	p.AccountFirstUsed[a.user] = at.UTC().Truncate(time.Second)
	// what agents were handed of the catalog is no different
	if err := storeQuiet(f); err != nil {
		log.Printf("providers.json: %v; %s's first use isn't kept yet", err, a.user)
		return false
	}
	return true
}

// FirstServed is when the ledger's first served request of each account of
// want was, at or after the time want gives it (when it was added: a row
// from before it was removed and added again is another life's); an
// account with none isn't in it. An error is a ledger that couldn't be
// read, never "none served". usage.FirstServed reads it; provider can't.
type FirstServed func(want map[[2]string]time.Time) (map[[2]string]time.Time, error)

// BackfillFirstUsed reads, once, when each account with a known
// AccountAdded was first used (earlier than a first use kept since, if so), from
// the ledger's served rows (served), and marks the read done
// (FirstUsedRead). One with no served row is taken as never used: its days
// start at its first use, day 0. A ledger that can't be read marks
// nothing, and those accounts count from AccountAdded until a later start
// reads it. providers.json is not held while the ledger is read.
func BackfillFirstUsed(served FirstServed) {
	f, err := read()
	if err != nil || f.FirstUsedRead {
		return
	}
	// every account with a day, one a request kept a first use of
	// meanwhile too: the ledger's may be earlier (the read failed before,
	// or another magpie held providers.json), and the earlier is kept
	want := map[[2]string]time.Time{}
	for _, p := range f.Providers {
		for u, added := range p.AccountAdded {
			if addedKnown(added) {
				want[[2]string{p.ID, u}] = added
			}
		}
	}
	found := map[[2]string]time.Time{}
	if len(want) > 0 {
		if found, err = served(want); err != nil {
			log.Printf("usage ledger: %v; the accounts' first use is read at a later start, their days counted from when they were added till then", err)
			return
		}
	}
	if err := providersMu.Take(); err != nil {
		log.Printf("%v; the accounts' first use is read at a later start", err)
		return
	}
	defer providersMu.Unlock()
	if f, err = read(); err != nil || f.FirstUsedRead {
		return
	}
	for i := range f.Providers {
		p := &f.Providers[i]
		for u, added := range p.AccountAdded {
			at, ok := found[[2]string{p.ID, u}]
			// the same account still, and the earlier of the ledger's and
			// one a request kept meanwhile
			if !ok || !added.Equal(want[[2]string{p.ID, u}]) {
				continue
			}
			if kept := p.AccountFirstUsed[u]; addedKnown(kept) && !kept.After(at) {
				continue
			}
			p.AccountFirstUsed = maps.Clone(p.AccountFirstUsed)
			if p.AccountFirstUsed == nil {
				p.AccountFirstUsed = map[string]time.Time{}
			}
			p.AccountFirstUsed[u] = at.UTC().Truncate(time.Second)
		}
	}
	f.FirstUsedRead = true
	if err := store(f); err != nil {
		log.Printf("providers.json: %v; the accounts' first use is read at a later start", err)
	}
}

// earlierFirstUsed is the first uses of here and there, each account's
// earlier; a sync from another computer never takes one away.
func earlierFirstUsed(here, there map[string]time.Time) map[string]time.Time {
	out := maps.Clone(here)
	for u, at := range there {
		if !addedKnown(at) {
			continue
		}
		if had, ok := out[u]; !ok || at.Before(had) {
			if out == nil {
				out = map[string]time.Time{}
			}
			out[u] = at
		}
	}
	return out
}

// FirstUsedKnown says the ledger was read for the accounts' first use
// (BackfillFirstUsed), so one without any has not been used.
func FirstUsedKnown() bool { return storedFile().FirstUsedRead }

// accountAdded is when the account user of the subscription id was added,
// zero when it isn't known (one from before AccountAdded was kept).
func accountAdded(id, user string) time.Time {
	if s, ok := storedPicks(id); ok {
		if at := s.AccountAdded[accountKey(user)]; addedKnown(at) {
			return at
		}
	}
	return time.Time{}
}

// AccountAddedOf is accountAdded, for the GUI and CLI to say how many
// days old an account is.
func AccountAddedOf(id, user string) time.Time { return accountAdded(id, user) }

// normalAccountAdded is m as it is kept: keys in lower case, zero times
// left out; nil for none.
func normalAccountAdded(m map[string]time.Time) map[string]time.Time {
	var out map[string]time.Time
	for u, at := range m {
		u = accountKey(u)
		if u == "" || at.IsZero() {
			continue
		}
		if out == nil {
			out = map[string]time.Time{}
		}
		out[u] = at.UTC()
	}
	return out
}

// loginAccount is how AccountAdded knows a saved login: its provider's id
// and its name in lower case.
type loginAccount struct{ id, user string }

func loginAccountOf(l savedLogin) loginAccount {
	return loginAccount{labelProvider(l.Agent), accountKey(l.User)}
}

// addedIn is what writing next over was adds to AccountAdded: each account
// of next that was had neither by its stable id nor by its provider and
// name (added), and each was had under another name (renamed, old to new),
// whose time goes with it. The agent's own row whose name changed is
// another account the agent signed in to (keepSideLogins writes it over),
// added, unless it is Copilot's host put in the name (copilotRenamed). A
// row hidden (the agent's own, removed in magpie) is no account had: shown
// again, the agent signed in to it anew, it is added.
func addedIn(was, next []savedLogin) (added []loginAccount, renamed map[loginAccount]loginAccount) {
	byID := map[string]savedLogin{}
	had := map[loginAccount]bool{}
	for _, l := range was {
		byID[loginID(l)] = l
		if l.Hidden == "" {
			had[loginAccountOf(l)] = true
		}
	}
	for _, l := range next {
		a := loginAccountOf(l)
		if a.user == "" || had[a] || l.Hidden != "" {
			continue
		}
		if w, ok := byID[loginID(l)]; ok && (!l.own() || l.Agent == "copilot" && copilotRenamed(w.User, l.User)) {
			if old := loginAccountOf(w); old != a {
				if renamed == nil {
					renamed = map[loginAccount]loginAccount{}
				}
				renamed[old] = a
			}
			continue
		}
		if !slices.Contains(added, a) {
			added = append(added, a)
		}
	}
	return added, renamed
}

// leftIn is each account of was (shown) that next doesn't have shown, and
// that wasn't renamed: gone from the list, by the user's removal or a
// sign-in not listed a while.
func leftIn(was, next []savedLogin, renamed map[loginAccount]loginAccount) []loginAccount {
	has := map[loginAccount]bool{}
	for _, l := range next {
		if l.Hidden == "" {
			has[loginAccountOf(l)] = true
		}
	}
	var left []loginAccount
	for _, l := range was {
		a := loginAccountOf(l)
		if _, moved := renamed[a]; a.user == "" || l.Hidden != "" || has[a] || moved || slices.Contains(left, a) {
			continue
		}
		left = append(left, a)
	}
	return left
}

// noteAdded writes now as when each account of added was added, and moves
// each renamed account's time to its new name, in providers.json. An
// account that has a time keeps it: one back in the list after it fell out
// (a plugin's whose sign-ins weren't listed a while) is not new. Each of
// left with no time is from before, and is marked so (addedBefore): back,
// it isn't new either. The user's removal took its time (forgetAdded), so
// one added again is new again.
func noteAdded(added []loginAccount, renamed map[loginAccount]loginAccount, left []loginAccount) {
	if len(added) == 0 && len(renamed) == 0 && len(left) == 0 {
		return
	}
	if err := providersMu.Take(); err != nil {
		log.Printf("%v; when the accounts were added isn't kept", err)
		return
	}
	defer providersMu.Unlock()
	f, err := read()
	if err != nil {
		// unknown is not written over: the accounts stay without a time
		log.Printf("providers.json: %v; when the accounts were added isn't kept", err)
		return
	}
	entry := func(id string) *Provider {
		i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == id })
		if i < 0 {
			// the account's settings alone, as Save keeps them
			f.Providers = append(f.Providers, Provider{ID: id})
			i = len(f.Providers) - 1
		}
		p := &f.Providers[i]
		p.AccountAdded = maps.Clone(p.AccountAdded)
		if p.AccountAdded == nil {
			p.AccountAdded = map[string]time.Time{}
		}
		return p
	}
	changed := false
	for old, now := range renamed {
		i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == old.id })
		if i < 0 {
			continue
		}
		t, ok := f.Providers[i].AccountAdded[old.user]
		if !ok {
			continue
		}
		used, wasUsed := f.Providers[i].AccountFirstUsed[old.user]
		delete(entry(old.id).AccountAdded, old.user)
		entry(now.id).AccountAdded[now.user] = t
		if wasUsed {
			// its first use goes with it
			o := entry(old.id)
			o.AccountFirstUsed = maps.Clone(o.AccountFirstUsed)
			delete(o.AccountFirstUsed, old.user)
			n := entry(now.id)
			n.AccountFirstUsed = maps.Clone(n.AccountFirstUsed)
			if n.AccountFirstUsed == nil {
				n.AccountFirstUsed = map[string]time.Time{}
			}
			n.AccountFirstUsed[now.user] = used
		}
		changed = true
	}
	has := func(a loginAccount) bool {
		i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == a.id })
		if i < 0 {
			return false
		}
		_, ok := f.Providers[i].AccountAdded[a.user]
		return ok
	}
	at := time.Now().UTC().Truncate(time.Second)
	for _, a := range added {
		if !has(a) {
			entry(a.id).AccountAdded[a.user] = at
			changed = true
		}
	}
	for _, a := range left {
		if !has(a) {
			entry(a.id).AccountAdded[a.user] = addedBefore
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := store(f); err != nil {
		log.Printf("providers.json: %v; when the accounts were added isn't kept", err)
	}
	forgetFirstUsedSeen()
}

// forgetAdded drops when each of users of the agent's accounts was added,
// as the user removes them: one signed in to again is new again.
func forgetAdded(agent string, users ...string) {
	id := labelProvider(agent)
	if err := providersMu.Take(); err != nil {
		log.Printf("%v; when the removed accounts were added is kept", err)
		return
	}
	defer providersMu.Unlock()
	f, err := read()
	if err != nil {
		log.Printf("providers.json: %v; when the removed accounts were added is kept", err)
		return
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == id })
	if i < 0 {
		return
	}
	p := &f.Providers[i]
	changed := false
	for _, u := range users {
		if _, ok := p.AccountAdded[accountKey(u)]; ok {
			if !changed {
				p.AccountAdded = maps.Clone(p.AccountAdded)
				p.AccountFirstUsed = maps.Clone(p.AccountFirstUsed)
			}
			delete(p.AccountAdded, accountKey(u))
			// its first use goes with it: added again, it is new again
			delete(p.AccountFirstUsed, accountKey(u))
			changed = true
		}
	}
	if !changed {
		return
	}
	if len(p.AccountFirstUsed) == 0 {
		p.AccountFirstUsed = nil
	}
	if len(p.AccountAdded) == 0 {
		p.AccountAdded = nil
		if reflect.DeepEqual(*p, Provider{ID: id}) {
			// a record noteAdded made for the days alone goes with them
			f.Providers = slices.Delete(f.Providers, i, i+1)
		}
	}
	if err := store(f); err != nil {
		log.Printf("providers.json: %v; when the removed accounts were added is kept", err)
	}
	forgetFirstUsedSeen()
}

// addedBut is every account of the agent's provider with a day (or marked
// from before) but those of but: the ones a sign-out of all its sign-ins,
// listed or not, takes.
func addedBut(agent string, but []string) []string {
	s, ok := storedPicks(labelProvider(agent))
	if !ok {
		return nil
	}
	var out []string
	for u := range s.AccountAdded {
		if !slices.ContainsFunc(but, func(b string) bool { return accountKey(b) == u }) {
			out = append(out, u)
		}
	}
	return out
}

// SignedOutOfPlugin takes the rows of the plugin's sign-ins out of
// logins.json and drops their days, as `magpie plugin logout` signed every
// one out: each signed in to again is new. The agent's own (a CLI's
// sign-in the plugin reads) stays, with its day.
func SignedOutOfPlugin(pp plugin.Provider) error {
	if settings.AccountDiscoveryOff() {
		return nil
	}
	agent := pluginAgent(pp)
	var own []string
	loginsMu.Lock()
	ls := readLogins()
	n := len(ls)
	ls = slices.DeleteFunc(ls, func(l savedLogin) bool {
		if l.Agent == agent && l.own() {
			own = append(own, l.User)
		}
		return l.Agent == agent && !l.own()
	})
	if len(ls) != n {
		if err := writeLogins(ls); err != nil {
			loginsMu.Unlock()
			return err
		}
	}
	loginsMu.Unlock()
	forgetAdded(agent, addedBut(agent, own)...)
	return nil
}
