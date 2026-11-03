package provider

// The growth policy (T-078, settings/growth.go): a subscription account
// added lately is held at the share of its windows the policy's stage for
// its days gives, as a cap the user set on it would hold it — routing
// takes it for used up there and goes on to the next account
// (gateway/fallback.go capHeld, codex_switch.go). The days are counted
// from AccountAdded, written as logins.json is written with an account it
// didn't have (noteAdded): Claude's and Codex's sign-ins, a plugin's, the
// side logins', an agent's own read the first time. An account added
// before magpie kept this has none, and is not held. The time stays while
// the account is gone from the list and back (a plugin's, when its
// sign-ins can't be read a while, or the plugin is installed again): only
// the user removing it (forgetAdded) lets it go, and one removed and
// added again is new again.
//
// The user's word comes first: an account with a cap of its own
// (AccountCaps) holds every window at it, and a window with a share of its
// own (AccountWindowCaps, 100 among them) keeps it. The policy fills in
// only the windows the user left alone.

import (
	"log"
	"maps"
	"slices"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// growthWindows are the windows (WindowCapID) each of the policy's windows
// holds: the 5 hours, and the week as each vendor names it, per model too.
var growthWindows = map[string][]string{
	settings.GrowthFiveHours: {"5 hours"},
	settings.GrowthWeek:      {"weekly", "7 days", "7 days · opus", "7 days · sonnet", "7 days · fable"},
}

// growthCaps is the share of each window (WindowCapID) the policy stages
// holds an account added at added to at now: the windows of the stage of
// the latest start its days have reached, alone. nil when no stage has
// started, or added is zero (not known: an account from before).
func growthCaps(stages []settings.GrowthStage, added, now time.Time) map[string]int {
	if added.IsZero() || len(stages) == 0 {
		return nil
	}
	days := now.Sub(added).Hours() / 24
	from := -1
	for _, g := range stages {
		if float64(g.From) <= days && g.From > from {
			from = g.From
		}
	}
	if from < 0 {
		return nil
	}
	out := map[string]int{}
	for _, g := range stages {
		if g.From != from {
			continue
		}
		for _, w := range growthWindows[g.Window] {
			out[w] = g.Cap
		}
	}
	return out
}

// withGrowth is c with the policy's shares for an account added at added,
// at now, on the windows the user set nothing on; c as it is when the
// account has a cap of its own, which holds every window. c's map is
// never written: one with growth is a copy.
func (c WindowCaps) withGrowth(added, now time.Time) WindowCaps {
	if added.IsZero() || c.All > 0 {
		return c
	}
	g := growthCaps(settings.Shared().GrowthOf(), added, now)
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

// accountAdded is when the account user of the subscription id was added,
// zero when it isn't known (one from before AccountAdded was kept).
func accountAdded(id, user string) time.Time {
	if s, ok := storedPicks(id); ok {
		return s.AccountAdded[accountKey(user)]
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

// noteAdded writes now as when each account of added was added, and moves
// each renamed account's time to its new name, in providers.json. An
// account that has a time keeps it: one back in the list after it fell out
// (a plugin's whose sign-ins weren't listed a while) is not new. The user's
// removal took its time (forgetAdded), so one added again is new again.
func noteAdded(added []loginAccount, renamed map[loginAccount]loginAccount) {
	if len(added) == 0 && len(renamed) == 0 {
		return
	}
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
		delete(entry(old.id).AccountAdded, old.user)
		entry(now.id).AccountAdded[now.user] = t
		changed = true
	}
	at := time.Now().UTC().Truncate(time.Second)
	for _, a := range added {
		if i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == a.id }); i >= 0 {
			if _, ok := f.Providers[i].AccountAdded[a.user]; ok {
				continue
			}
		}
		entry(a.id).AccountAdded[a.user] = at
		changed = true
	}
	if !changed {
		return
	}
	if err := store(f); err != nil {
		log.Printf("providers.json: %v; when the accounts were added isn't kept", err)
	}
}

// forgetAdded drops when each of users of the agent's accounts was added,
// as the user removes them: one signed in to again is new again.
func forgetAdded(agent string, users ...string) {
	id := labelProvider(agent)
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
			}
			delete(p.AccountAdded, accountKey(u))
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := store(f); err != nil {
		log.Printf("providers.json: %v; when the removed accounts were added is kept", err)
	}
}
