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
// A policy counts those days on the calendar, or by use (T-134,
// settings.GrowthUsedDays): only the days the gateway served a request of
// the account (AccountUsedDays, a count kept a day at a time as requests
// are served, never read from the ledger on a request), so an idle account
// doesn't move on. One the gateway doesn't serve counts calendar days.
//
// The user's word comes first: an account with a cap of its own
// (AccountCaps) holds every window at it, and a window with a share of its
// own (AccountWindowCaps, 100 among them) or an amount (AccountWindowAmounts)
// keeps it. The policy fills in only the windows the user left alone.
// Skipping the warm-up (SkipGrowth, T-132) is a share of 100 on each of
// those, so a share the user sets on one later is theirs as any other.

import (
	"cmp"
	"fmt"
	"log"
	"maps"
	"reflect"
	"slices"
	"strings"
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

// withGrowth is c with the shares of the policy the account user of the
// subscription id is held by (growthPolicyFor) for an account added at
// added, at now, on the windows the user set nothing on (no share, no
// amount); c as it is when the account has a cap of its own, which holds
// every window. c's map is never written: one with growth is a copy.
func (c WindowCaps) withGrowth(id, user string, added, now time.Time) WindowCaps {
	if !addedKnown(added) || c.All > 0 {
		return c
	}
	return c.withPolicy(growthPolicyFor(id, user), added, now)
}

// growthPolicyOf is the policy the accounts of the subscription id are
// held by (T-131): the one it picked (Provider.GrowthPolicy), the default
// when it picked none or one no policy has (settings.GrowthOf). The
// gateway's holds (CapsOf), switching (AccountCapsOf), an account's row
// (AccountGrowthOf) and the policy page's chart (GrowthAccounts) all read
// it, so none says another policy than the one that holds.
func growthPolicyOf(id string) settings.GrowthPolicy {
	s, _ := storedPicks(id)
	return settings.Shared().GrowthOf(s.GrowthPolicy)
}

// growthPolicyFor is the policy the account user of the subscription id
// is held by (T-133): the one it picked itself (AccountGrowthPolicy),
// before its subscription's (growthPolicyOf), before the default
// (settings.GrowthFor). Every hold, switch, row and chart reads it.
func growthPolicyFor(id, user string) settings.GrowthPolicy {
	s, _ := storedPicks(id)
	return settings.Shared().GrowthFor(s.AccountGrowthPolicy[accountKey(user)], s.GrowthPolicy)
}

// SetAccountGrowthPolicy has the account ref of the subscription id held
// by the growth policy policy (T-133), or by its subscription's for "":
// one no policy has is refused, as SetGrowthPolicy refuses it. Written on
// the record as it is under the lock (editFound). A pick is cleared ("")
// of an account the subscription no longer lists too, by its name: one
// gone from outside magpie would keep its policy from being deleted, and
// of a subscription the Providers page doesn't list (clearStoredPick).
func SetAccountGrowthPolicy(id, ref, policy string) error {
	p, err := Find(id)
	if err != nil {
		if strings.TrimSpace(policy) == "" {
			return clearStoredPick(id, err, func(q *Provider) bool {
				_, had := q.AccountGrowthPolicy[accountKey(ref)]
				m := maps.Clone(q.AccountGrowthPolicy)
				delete(m, accountKey(ref))
				q.AccountGrowthPolicy = normalAccountGrowthPolicy(m)
				return had
			})
		}
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts for a growth policy to hold", p.Name)
	}
	policy = strings.TrimSpace(policy)
	k := accountKey(ref)
	if r, ok := p.accountRef(ref); ok {
		k = accountKey(r)
	} else if policy != "" {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	if policy != "" && !settings.Shared().GrowthKnows(policy) {
		return fmt.Errorf("no growth policy has the id %q", policy)
	}
	return editFound(p, func(q *Provider) error {
		// asked again under the lock, as SetGrowthPolicy does
		if policy != "" && !settings.Shared().GrowthKnows(policy) {
			return fmt.Errorf("no growth policy has the id %q", policy)
		}
		m := maps.Clone(q.AccountGrowthPolicy)
		if policy == "" {
			delete(m, k)
		} else {
			if m == nil {
				m = map[string]string{}
			}
			m[k] = policy
		}
		q.AccountGrowthPolicy = normalAccountGrowthPolicy(m)
		return nil
	})
}

// clearStoredPick clears a growth policy's pick (clear, which says
// whether it had one) on the record providers.json keeps of the
// subscription id that Find doesn't find (notFound, its error): one signed
// out or hidden keeps its picks, and GrowthPicks counts them, so a policy
// it picked can't be deleted until it is cleared (T-145). A record there
// is none of, or one with no pick to clear, is notFound; nothing is made.
func clearStoredPick(id string, notFound error, clear func(q *Provider) bool) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f, err := read()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(f.Providers, func(q Provider) bool { return strings.ToLower(strings.TrimSpace(q.ID)) == id }) {
		return notFound
	}
	return editRecord(id, false, func(q *Provider) (bool, error) {
		if !clear(q) {
			return false, notFound
		}
		return true, nil
	})
}

// normalAccountGrowthPolicy is m as it is kept: keys in lower case, ids
// trimmed, accounts with none left out; nil for none.
func normalAccountGrowthPolicy(m map[string]string) map[string]string {
	var out map[string]string
	for u, id := range m {
		u, id = accountKey(u), strings.TrimSpace(id)
		if u == "" || id == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[u] = id
	}
	return out
}

// SetGrowthPolicy has the subscription id's accounts held by the growth
// policy policy (T-131), or by the default for "": one no policy has is
// refused. Written on the record as it is under the lock (editFound), so
// what another change wrote since stays. "" clears the pick of a
// subscription the Providers page doesn't list too (clearStoredPick).
func SetGrowthPolicy(id, policy string) error {
	p, err := Find(id)
	if err != nil {
		if strings.TrimSpace(policy) == "" {
			return clearStoredPick(id, err, func(q *Provider) bool {
				had := q.GrowthPolicy != ""
				q.GrowthPolicy = ""
				return had
			})
		}
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts for a growth policy to hold", p.Name)
	}
	policy = strings.TrimSpace(policy)
	if policy != "" && !settings.Shared().GrowthKnows(policy) {
		return fmt.Errorf("no growth policy has the id %q", policy)
	}
	return editFound(p, func(q *Provider) error {
		// asked again under providers.json's lock: a policy deleted since
		// the check above isn't written (review L2). Settings are only
		// read here, so no lock is taken the other way round.
		if policy != "" && !settings.Shared().GrowthKnows(policy) {
			return fmt.Errorf("no growth policy has the id %q", policy)
		}
		q.GrowthPolicy = policy
		return nil
	})
}

// GrowthPick is a growth policy picked: by the subscription Provider for
// its accounts (T-131), or by its account Account alone (T-133), with the
// policy's id as it was picked.
type GrowthPick struct {
	Provider string
	Account  string
	Policy   string
}

// GrowthPicks is each pick of a growth policy, by a subscription or by one
// of its accounts, one no policy has among them. A policy picked is not
// deleted until it is picked no more (Settings › Growth policy). A
// providers.json that can't be read is an error, never "none picked".
func GrowthPicks() ([]GrowthPick, error) {
	f, err := read()
	if err != nil {
		return nil, err
	}
	var out []GrowthPick
	for _, s := range f.Providers {
		if s.GrowthPolicy != "" {
			out = append(out, GrowthPick{Provider: s.ID, Policy: s.GrowthPolicy})
		}
		for u, id := range s.AccountGrowthPolicy {
			if id != "" {
				out = append(out, GrowthPick{Provider: s.ID, Account: u, Policy: id})
			}
		}
	}
	slices.SortFunc(out, func(a, b GrowthPick) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Account, b.Account))
	})
	return out, nil
}

// withPolicy is withGrowth by the policy p, read once by a caller that
// asks it of many days (AccountGrowthOf).
func (c WindowCaps) withPolicy(p settings.GrowthPolicy, added, now time.Time) WindowCaps {
	if !addedKnown(added) || c.All > 0 {
		return c
	}
	g := policyCaps(p, added, now)
	if len(g) == 0 {
		return c
	}
	wins := maps.Clone(c.Windows)
	if wins == nil {
		wins = map[string]int{}
	}
	for w, share := range g {
		if _, own := wins[w]; !own && c.Amounts[w] <= 0 {
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

// GrowthAccounts are the accounts the policy holds at now, each with its
// day, by subscription and name: those its subscription lists now (as the
// Providers page does, AccountRefs), on a subscription that isn't off,
// with no cap of their own (which holds every window, withGrowth). A day
// is kept for an account gone from the list a while, so the day alone
// doesn't say it is held. One from before AccountAdded was kept has no
// day and isn't listed.
func GrowthAccounts(now time.Time) []GrowthAccount {
	return growthAccounts(now, nil)
}

// GrowthAccountsOf is GrowthAccounts the policy id holds (growthPolicyFor:
// the account's own pick, its subscription's, the default): the accounts
// its chart marks (T-131, T-133).
func GrowthAccountsOf(id string, now time.Time) []GrowthAccount {
	return growthAccounts(now, func(s *Provider, user string) bool {
		return settings.Shared().GrowthFor(s.AccountGrowthPolicy[user], s.GrowthPolicy).ID == id
	})
}

// growthAccounts is GrowthAccounts of the accounts held reports true of,
// every one for nil; held is given the record read once (growthPolicyFor's
// pick, without a read of the file per account).
func growthAccounts(now time.Time, held func(s *Provider, user string) bool) []GrowthAccount {
	all, f := sharedAll(), storedFile()
	var out []GrowthAccount
	for _, s := range f.Providers {
		p := lookIn(all, s.ID)
		if p == nil || p.Account == nil || p.Off || len(s.AccountAdded) == 0 {
			continue
		}
		name := cmp.Or(p.Name, s.ID)
		listed := map[string]bool{}
		for _, r := range p.AccountRefs() {
			listed[accountKey(r)] = true
			if u := p.PluginUser(r); u != "" {
				listed[accountKey(u)] = true
			}
		}
		for u := range s.AccountAdded {
			from := growthFrom(s.ID, u, now)
			if !listed[u] || s.AccountCaps[u] > 0 || !addedKnown(from) || held != nil && !held(&s, u) {
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

// GrowthUse is a subscription a growth policy holds the accounts of, as
// Settings › Growth policy's In use lists it (T-131): its id and name,
// whether it picked the policy (else it follows the default, or picked one
// no policy has), and how many of its accounts the policy counts the days
// of now (GrowthAccounts'). AccountsPicked is how many of its accounts
// picked the policy themselves (T-133); AccountsOnly says the
// subscription itself is held by another, and is listed for those alone.
//
// A pick that holds nothing is listed too, as GrowthPicks counts it and
// the delete is refused by it (T-145): Off is a subscription turned off,
// Unlisted one the Providers page doesn't list (signed out, hidden), and
// Gone the accounts (as providers.json keeps them) that picked it which
// the subscription no longer lists, every account of one Unlisted.
type GrowthUse struct {
	Provider       string   `json:"provider"`
	Name           string   `json:"name"`
	Picked         bool     `json:"picked,omitempty"`
	Accounts       int      `json:"accounts"`
	AccountsPicked int      `json:"accountsPicked,omitempty"`
	AccountsOnly   bool     `json:"accountsOnly,omitempty"`
	Off            bool     `json:"off,omitempty"`
	Unlisted       bool     `json:"unlisted,omitempty"`
	Gone           []string `json:"gone,omitempty"`
}

// GrowthUses are the subscriptions the policy id holds the accounts of,
// by name: each signed-in subscription that isn't off held by it
// (growthPolicyOf), with or without accounts of a day yet, and each
// record of providers.json that picked it, for itself or for an account
// (T-133), whether it holds any or not (T-145): what GrowthPicks counts,
// so the In use the page shows is what keeps the policy from being
// deleted.
func GrowthUses(id string, now time.Time) []GrowthUse {
	counts := map[string]int{}
	for _, a := range GrowthAccountsOf(id, now) {
		counts[a.Provider]++
	}
	all, f := sharedAll(), storedFile()
	var out []GrowthUse
	for _, p := range all {
		if p.Account == nil || slices.ContainsFunc(out, func(u GrowthUse) bool { return u.Provider == p.ID }) {
			continue
		}
		s, _ := storedPicks(p.ID)
		listed := map[string]bool{}
		for _, r := range p.AccountRefs() {
			listed[accountKey(r)] = true
			if u := p.PluginUser(r); u != "" {
				listed[accountKey(u)] = true
			}
		}
		mine, gone := accountPicksOf(s, id, listed)
		held := !p.Off && growthPolicyOf(p.ID).ID == id
		if !held && mine == 0 && s.GrowthPolicy != id {
			continue
		}
		out = append(out, GrowthUse{Provider: p.ID, Name: cmp.Or(p.Name, p.ID), Picked: s.GrowthPolicy == id, Accounts: counts[p.ID],
			AccountsPicked: mine, AccountsOnly: !held && s.GrowthPolicy != id, Off: p.Off, Gone: gone})
	}
	// a record the Providers page has no row of keeps its picks, and
	// GrowthPicks counts them
	for _, s := range f.Providers {
		if slices.ContainsFunc(out, func(u GrowthUse) bool { return u.Provider == s.ID }) || slices.ContainsFunc(all, func(p Provider) bool { return p.ID == s.ID }) {
			continue
		}
		mine, gone := accountPicksOf(s, id, nil)
		if mine == 0 && s.GrowthPolicy != id {
			continue
		}
		out = append(out, GrowthUse{Provider: s.ID, Name: cmp.Or(s.Name, s.ID), Picked: s.GrowthPolicy == id,
			AccountsPicked: mine, AccountsOnly: s.GrowthPolicy != id, Unlisted: true, Gone: gone})
	}
	slices.SortFunc(out, func(a, b GrowthUse) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Provider, b.Provider))
	})
	return out
}

// accountPicksOf is how many accounts of the record s picked the policy
// id themselves, as GrowthPicks counts them, and those of them listed
// doesn't have, by name.
func accountPicksOf(s Provider, id string, listed map[string]bool) (int, []string) {
	n, gone := 0, []string(nil)
	for u, pick := range s.AccountGrowthPolicy {
		if pick != id {
			continue
		}
		n++
		if !listed[u] {
			gone = append(gone, u)
		}
	}
	slices.Sort(gone)
	return n, gone
}

// AccountGrowth is how the growth policy holds an account at a time, for
// its row on Models › Accounts to say (T-132): the policy in effect, the
// account's day (as GrowthAccounts counts it), Days the first day it holds
// nothing (0 when that is past GrowthDaysMost), and the share of the 5
// hours and of the week it holds the account to, 100 where it holds
// neither. Next is the next day before Days a stage changes a share, with
// the shares from then (stages only: a ramp rises every day). Waiting is
// one not used yet, on day 0 until its first use; ByAdded one whose days
// count from when it was added, the gateway not serving it
// (gatewayServed). UsedDays says its days are the days it was used on
// (T-134); CalendarOnly that its policy counts those, but the gateway
// doesn't serve it, so its days are the calendar's; UsedDaysUnknown that
// its policy counts those, but how many isn't known yet (BackfillUsedDays),
// so its days are the calendar's till then.
type AccountGrowth struct {
	Policy          string `json:"policy"`
	Name            string `json:"name"`
	Builtin         bool   `json:"builtin,omitempty"`
	Kind            string `json:"kind"`
	Day             int    `json:"day"`
	Days            int    `json:"days,omitempty"`
	FiveHours       int    `json:"fiveHours"`
	Week            int    `json:"week"`
	Next            int    `json:"next,omitempty"`
	NextFiveHours   int    `json:"nextFiveHours,omitempty"`
	NextWeek        int    `json:"nextWeek,omitempty"`
	Waiting         bool   `json:"waiting,omitempty"`
	ByAdded         bool   `json:"byAdded,omitempty"`
	UsedDays        bool   `json:"usedDays,omitempty"`
	CalendarOnly    bool   `json:"calendarOnly,omitempty"`
	UsedDaysUnknown bool   `json:"usedDaysUnknown,omitempty"`
}

// AccountGrowthOf is how the growth policy the account is held by
// (growthPolicyFor: its own pick, its subscription's, the default) holds
// the account user of the subscription id at now, nil when it holds none of its windows: one from
// before, one past its last day, a policy that is Off, a subscription
// turned off, a cap of the account's own, or every window it has the
// user's (a ramp-up skipped). Its shares are those AccountCapsOf adds to
// the user's own caps, through the same withGrowth (withPolicy, the
// policy read once) and growthFrom the gateway's holds read, on the
// windows the account was last read with (growthNamesOf), so the row
// never says what the gateway doesn't hold. Only the days a share can
// change on are asked: a ramp's each day to its last, a stage's start.
func AccountGrowthOf(id, user string, now time.Time) *AccountGrowth {
	s, ok := storedPicks(id)
	if !ok || s.Off {
		return nil
	}
	from, byUse := growthCount(id, user, now)
	if !addedKnown(from) {
		return nil
	}
	p := growthPolicyFor(id, user)
	own, names := ownCapsOf(id, user), growthNamesOf(id, user)
	// the policy's shares on the windows it holds at the time at, and
	// whether it holds any
	held := func(at time.Time) (fiveHours, week int, any bool) {
		c := own.withPolicy(p, from, at)
		least := func(win string) int {
			share := 100
			for _, w := range names[win] {
				if _, mine := own.Windows[w]; mine {
					continue
				}
				if v, ok := c.Windows[w]; ok {
					share, any = min(share, v), true
				}
			}
			return share
		}
		return least(settings.GrowthFiveHours), least(settings.GrowthWeek), any
	}
	f, w, any := held(now)
	if !any {
		return nil
	}
	day := max(int(now.Sub(from).Hours()/24), 0)
	g := &AccountGrowth{
		Policy: p.ID, Name: p.Name, Builtin: p.Builtin, Kind: p.Kind,
		Day: day, FiveHours: f, Week: w,
		Waiting:      gatewayServed(s) && storedFile().FirstUsedRead && !addedKnown(s.AccountFirstUsed[accountKey(user)]),
		ByAdded:      !gatewayServed(s),
		UsedDays:     byUse,
		CalendarOnly: !gatewayServed(s) && p.CountDays == settings.GrowthUsedDays,
		UsedDaysUnknown: !byUse && gatewayServed(s) && p.CountDays == settings.GrowthUsedDays &&
			addedKnown(s.AccountFirstUsed[accountKey(user)]),
	}
	var days []int
	switch p.Kind {
	case settings.GrowthRamp:
		for d := day + 1; d <= min(p.Days, settings.GrowthDaysMost+1); d++ {
			days = append(days, d)
		}
	case settings.GrowthStaged:
		for _, st := range p.Stages {
			if st.From > day {
				days = append(days, st.From)
			}
		}
		slices.Sort(days)
		days = slices.Compact(days)
	}
	for _, d := range days {
		nf, nw, more := held(from.Add(time.Duration(d) * 24 * time.Hour))
		if !more {
			g.Days = d
			break
		}
		if p.Kind == settings.GrowthStaged && g.Next == 0 && (nf != f || nw != w) {
			g.Next, g.NextFiveHours, g.NextWeek = d, nf, nw
		}
	}
	return g
}

// growthNamesOf are the policy's windows (growthWindows) the account user
// of the subscription id has, by its last reading kept (lastReading): a
// Codex account's week is "7 days", where a plugin's may be "weekly", and
// the share a user set on the one it has is the one the gateway holds by.
// Every name of growthWindows for an account never read.
func growthNamesOf(id, user string) map[string][]string {
	q, ok := lastReading(id + "/" + accountKey(user))
	if !ok || len(q.Windows) == 0 {
		return growthWindows
	}
	has := map[string]bool{}
	for _, w := range q.Windows {
		has[WindowCapID(w.Name)] = true
	}
	out := map[string][]string{}
	for win, ws := range growthWindows {
		for _, w := range ws {
			if has[w] {
				out[win] = append(out[win], w)
			}
		}
	}
	return out
}

// SkipGrowth lets the growth policy go of the account ref of the
// subscription id (T-132), as the backlog's 需求方 asked: a share of 100,
// none, on each window it would hold that the account has (growthNamesOf)
// and the user set nothing on, so the gateway holds it at the user's own
// caps alone. A window with a share or an amount of the user's keeps it,
// and a share the user sets later is theirs, as on any window
// (SetWindowCaps); one set back to follow the account's cap is the
// policy's again. Those 100s are a window's own, so an account cap set
// later leaves those windows out, as a 100 the user set does (Of); the
// Skip menu says so. An account with a cap of its own, which the policy
// never holds, is left as it is.
func SkipGrowth(id, ref string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts with usage windows to cap", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	k, names := accountKey(r), growthNamesOf(p.ID, r)
	// on the caps as they are stored then, not as they were read: another
	// set meanwhile stays (T-079)
	return editStored(p.ID, func(q *Provider) bool {
		if q.AccountCaps[k] > 0 {
			return false
		}
		all := map[string]map[string]int{}
		for u, m := range q.AccountWindowCaps {
			all[u] = maps.Clone(m)
		}
		if all[k] == nil {
			all[k] = map[string]int{}
		}
		changed := false
		for _, wins := range names {
			for _, w := range wins {
				if _, mine := all[k][w]; mine || q.AccountWindowAmounts[k][w] > 0 {
					continue
				}
				all[k][w], changed = 100, true
			}
		}
		if changed {
			q.AccountWindowCaps = normalWindowCaps(all)
		}
		return changed
	})
}

// growthFrom is when the growth policy counts the days of the account
// user of the subscription id from: its first use; now (day 0) for one not
// used yet, once the ledger was read for it (FirstUsedRead); its
// AccountAdded until then, as before. An account the gateway doesn't serve
// (gatewayServed) counts from AccountAdded always: its agent asks its
// vendor itself, so its first use is never seen, and day 0 would hold it at
// the first stage for good. Zero for one whose AccountAdded isn't known
// (from before), which is never held. For a policy that counts the days
// used (settings.GrowthUsedDays) it is as many days before today as the
// account was used on before it (usedFrom), so a day's count is the days
// used; calendar days while that count isn't known (BackfillUsedDays).
// CapsOf (the gateway's holds) and AccountCapsOf (switching the agent's
// own sign-in) both read it, so the two never disagree.
func growthFrom(id, user string, now time.Time) time.Time {
	from, _ := growthCount(id, user, now)
	return from
}

// growthCount is growthFrom, and whether its days are the days used.
func growthCount(id, user string, now time.Time) (time.Time, bool) {
	s, ok := storedPicks(id)
	if !ok {
		return time.Time{}, false
	}
	k := accountKey(user)
	added := s.AccountAdded[k]
	if !addedKnown(added) {
		return time.Time{}, false
	}
	if !gatewayServed(s) {
		return added, false
	}
	if used := s.AccountFirstUsed[k]; addedKnown(used) {
		if c, ok := s.AccountUsedDays[k]; ok && c.From.Equal(used) && growthPolicyFor(id, user).CountDays == settings.GrowthUsedDays {
			return usedFrom(c, now), true
		}
		return used, false
	}
	if storedFile().FirstUsedRead {
		return now, false
	}
	return added, false
}

// UsedDays is how many days an account was used on (T-134): Count days,
// each the 24 hours from From (its first use) plus a whole number of days
// that a request of it was served in, the latest starting at Last. A
// day's start is from its first use, as a calendar day's is (growthCaps
// counts 24 hours from it), so no clock's zone moves it.
type UsedDays struct {
	From  time.Time `json:"from"`
	Last  time.Time `json:"last"`
	Count int       `json:"count"`
}

// usedDaysMost is the most days used are counted to: past it, no policy
// holds an account (no stage starts after GrowthDaysMost, no ramp is
// longer), so its count is no longer written.
const usedDaysMost = settings.GrowthDaysMost + 1

const growthDay = 24 * time.Hour

// usedDay is the start of the day, counted from from, that at is in: from
// and a whole number of days, before from for a time before it.
func usedDay(from, at time.Time) time.Time {
	d := at.Sub(from)
	n := d / growthDay
	if d < 0 && d%growthDay != 0 {
		n--
	}
	return from.Add(n * growthDay)
}

// usedFrom is when the days of an account used on the days c counts start
// at now: as many days before today as it was used on before today, so
// growthCaps' day is the days used. Never before its first use, for a
// clock set back past a day counted.
func usedFrom(c UsedDays, now time.Time) time.Time {
	today := usedDay(c.From, now)
	d := c.Count
	if !today.After(c.Last) {
		d-- // today is counted already
	}
	from := today.Add(-time.Duration(max(d, 0)) * growthDay)
	if from.Before(c.From) {
		return c.From
	}
	return from
}

// normalUsedDays is m as it is kept: keys in lower case, a count against
// no first use left out; nil for none.
func normalUsedDays(m map[string]UsedDays) map[string]UsedDays {
	var out map[string]UsedDays
	for u, c := range m {
		u = accountKey(u)
		if u == "" || !addedKnown(c.From) || c.Count <= 0 {
			continue
		}
		if out == nil {
			out = map[string]UsedDays{}
		}
		c.From, c.Last = c.From.UTC(), c.Last.UTC()
		out[u] = c
	}
	return out
}

// keptUsedDays are the days used of here and there, as a sync brings
// there's: for each account, the count against its first use as it is
// kept (firstUsed), the larger of the two when both are; one against
// another first use is left out, and counted again from the ledger
// (BackfillUsedDays). Days each computer served on its own are not
// added: the account counts fewer days than it was used on, so is held
// longer, never shorter.
func keptUsedDays(here, there map[string]UsedDays, firstUsed map[string]time.Time) map[string]UsedDays {
	var out map[string]UsedDays
	for _, m := range []map[string]UsedDays{here, there} {
		for u, c := range m {
			if !addedKnown(c.From) || !c.From.Equal(firstUsed[u]) {
				continue
			}
			if had, ok := out[u]; ok && (had.Count > c.Count || had.Count == c.Count && !c.Last.After(had.Last)) {
				continue
			}
			if out == nil {
				out = map[string]UsedDays{}
			}
			out[u] = c
		}
	}
	return out
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
	usedDaySeen.Lock()
	usedDaySeen.m = nil
	usedDaySeen.Unlock()
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
	if used := s.AccountFirstUsed[a.user]; addedKnown(used) {
		noteUsedDay(a, s.AccountUsedDays[a.user], used, at)
		return
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
	if !writeFirstUsed(a, at) {
		// not written: the next request of the account tries again
		firstUsedSeen.Lock()
		delete(firstUsedSeen.m, a)
		firstUsedSeen.Unlock()
	}
}

// usedDaySeen is, for each account, the latest day of its use (usedDay)
// this magpie is writing or wrote: a request beside the first of a day
// writes nothing.
var usedDaySeen struct {
	sync.Mutex
	m map[loginAccount]time.Time
}

// noteUsedDay counts the day at is in as a day account a was used on
// (T-134), c being its count as stored and used its first use: written
// once a day, by the day's first request, so every other request reads
// only providers.json's memo. A count against another first use, or none,
// is not known and is left to the ledger's read at a start
// (BackfillUsedDays); one past every policy's last day isn't written.
func noteUsedDay(a loginAccount, c UsedDays, used, at time.Time) {
	if !c.From.Equal(used) || c.Count >= usedDaysMost {
		return
	}
	day := usedDay(c.From, at)
	if !day.After(c.Last) {
		return // counted already, or a request of a day before
	}
	usedDaySeen.Lock()
	if seen, ok := usedDaySeen.m[a]; ok && !day.After(seen) {
		usedDaySeen.Unlock()
		return
	}
	if usedDaySeen.m == nil {
		usedDaySeen.m = map[loginAccount]time.Time{}
	}
	usedDaySeen.m[a] = day // claimed: a request beside this one writes nothing
	usedDaySeen.Unlock()
	if !writeUsedDay(a, used, at) {
		// not written: the next request of the day tries again
		usedDaySeen.Lock()
		if seen := usedDaySeen.m[a]; seen.Equal(day) {
			delete(usedDaySeen.m, a)
		}
		usedDaySeen.Unlock()
	}
}

// writeUsedDay counts the day at is in for account a, whose first use is
// used, when its count is against it and hasn't that day; false when
// providers.json couldn't be read or written, which keeps nothing.
func writeUsedDay(a loginAccount, used, at time.Time) bool {
	beforeUsedDayWrite()
	if err := providersMu.Take(); err != nil {
		log.Printf("%v; a day %s was used on isn't counted yet", err, a.user)
		return false
	}
	defer providersMu.Unlock()
	f, err := read()
	if err != nil {
		log.Printf("providers.json: %v; a day %s was used on isn't counted yet", err, a.user)
		return false
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == a.id })
	if i < 0 {
		return true
	}
	p := &f.Providers[i]
	c, ok := p.AccountUsedDays[a.user]
	if !ok || !p.AccountFirstUsed[a.user].Equal(used) || !c.From.Equal(used) || c.Count >= usedDaysMost {
		return true
	}
	day := usedDay(c.From, at)
	if !day.After(c.Last) {
		return true
	}
	c.Count, c.Last = c.Count+1, day
	p.AccountUsedDays = maps.Clone(p.AccountUsedDays)
	p.AccountUsedDays[a.user] = c
	// what agents were handed of the catalog is no different
	if err := storeQuiet(f); err != nil {
		log.Printf("providers.json: %v; a day %s was used on isn't counted yet", err, a.user)
		return false
	}
	return true
}

// beforeUsedDayWrite is called as a day used is about to be written, for
// a test to count the writes.
var beforeUsedDayWrite = func() {}

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
	// its first day of use, the day its count of days used starts on
	p.AccountUsedDays = maps.Clone(p.AccountUsedDays)
	if p.AccountUsedDays == nil {
		p.AccountUsedDays = map[string]UsedDays{}
	}
	fu := p.AccountFirstUsed[a.user]
	p.AccountUsedDays[a.user] = UsedDays{From: fu, Last: fu, Count: 1}
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

// ServedDays is, for each account of want, the days used (UsedDays) the
// ledger's served rows of it fall on since the time want gives it (its
// first use), an account whose first day the ledger doesn't have left
// out. An error is a ledger that couldn't be read, never "not used".
// usage.ServedDays reads it.
type ServedDays func(want map[[2]string]time.Time) (map[[2]string]UsedDays, error)

// BackfillUsedDays counts, from the ledger's served rows (served), the
// days each account with a first use was used on (T-134) when its count
// isn't known: none kept (an account first used before the count was
// kept), or one kept against another first use (an earlier one the
// ledger or a sync brought since). A ledger that can't be read counts
// nothing, logged: those accounts' days are the calendar's (growthFrom)
// until a later start reads it, never none; so is one whose first day the
// ledger doesn't have (trimmed, or a first use from another computer),
// whose days aren't known. Run at the gateway's start
// after BackfillFirstUsed; providers.json is not held while the ledger is
// read. A day first used in the moment between the read and the write is
// counted by its next request.
func BackfillUsedDays(served ServedDays) {
	f, err := read()
	if err != nil {
		return
	}
	want := map[[2]string]time.Time{}
	for _, p := range f.Providers {
		for u, used := range p.AccountFirstUsed {
			if c, ok := p.AccountUsedDays[u]; !addedKnown(used) || ok && c.From.Equal(used) {
				continue
			}
			want[[2]string{p.ID, u}] = used
		}
	}
	if len(want) == 0 {
		return
	}
	found, err := served(want)
	if err != nil {
		log.Printf("usage ledger: %v; the days %d accounts were used on are read at a later start, their days counted on the calendar till then", err, len(want))
		return
	}
	if err := providersMu.Take(); err != nil {
		log.Printf("%v; the days the accounts were used on are read at a later start", err)
		return
	}
	defer providersMu.Unlock()
	if f, err = read(); err != nil {
		log.Printf("providers.json: %v; the days the accounts were used on are read at a later start", err)
		return
	}
	changed, unknown := false, 0
	for i := range f.Providers {
		p := &f.Providers[i]
		for u, used := range p.AccountFirstUsed {
			k := [2]string{p.ID, u}
			if w, ok := want[k]; !ok || !w.Equal(used) {
				continue // first used again meanwhile: counted from then
			}
			if c, ok := p.AccountUsedDays[u]; ok && c.From.Equal(used) {
				continue // counted meanwhile, as its first use was written
			}
			c, ok := found[k]
			if !ok || c.Count <= 0 || !c.From.Equal(used) {
				unknown++
				continue
			}
			c.Count = min(c.Count, usedDaysMost)
			p.AccountUsedDays = maps.Clone(p.AccountUsedDays)
			if p.AccountUsedDays == nil {
				p.AccountUsedDays = map[string]UsedDays{}
			}
			p.AccountUsedDays[u] = c
			changed = true
		}
	}
	if unknown > 0 {
		log.Printf("usage ledger: it doesn't reach back to the first use of %d accounts; the days they were used on aren't known, their days counted on the calendar", unknown)
	}
	if !changed {
		return
	}
	if err := storeQuiet(f); err != nil {
		log.Printf("providers.json: %v; the days the accounts were used on are read at a later start", err)
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
		days, hadDays := f.Providers[i].AccountUsedDays[old.user]
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
		if hadDays {
			// and its days used
			o := entry(old.id)
			o.AccountUsedDays = maps.Clone(o.AccountUsedDays)
			delete(o.AccountUsedDays, old.user)
			n := entry(now.id)
			n.AccountUsedDays = maps.Clone(n.AccountUsedDays)
			if n.AccountUsedDays == nil {
				n.AccountUsedDays = map[string]UsedDays{}
			}
			n.AccountUsedDays[now.user] = days
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
	changed, cloned := false, false
	for _, u := range users {
		// the growth policy it picked goes with it too (T-133)
		if _, ok := p.AccountGrowthPolicy[accountKey(u)]; ok {
			p.AccountGrowthPolicy = maps.Clone(p.AccountGrowthPolicy)
			delete(p.AccountGrowthPolicy, accountKey(u))
			if len(p.AccountGrowthPolicy) == 0 {
				p.AccountGrowthPolicy = nil
			}
			changed = true
		}
		if _, ok := p.AccountAdded[accountKey(u)]; ok {
			if !cloned {
				cloned = true
				p.AccountAdded = maps.Clone(p.AccountAdded)
				p.AccountFirstUsed = maps.Clone(p.AccountFirstUsed)
				p.AccountUsedDays = maps.Clone(p.AccountUsedDays)
			}
			delete(p.AccountAdded, accountKey(u))
			// its first use goes with it, and its days used: added again,
			// it is new again
			delete(p.AccountFirstUsed, accountKey(u))
			delete(p.AccountUsedDays, accountKey(u))
			changed = true
		}
	}
	if !changed {
		return
	}
	if len(p.AccountFirstUsed) == 0 {
		p.AccountFirstUsed = nil
	}
	if len(p.AccountUsedDays) == 0 {
		p.AccountUsedDays = nil
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
