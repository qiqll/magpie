package provider

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// usePolicy makes p, a policy of the user's, the default.
func usePolicy(t *testing.T, p settings.GrowthPolicy) {
	t.Helper()
	err := settings.EditGrowth(func([]settings.GrowthPolicy, string) ([]settings.GrowthPolicy, string, error) {
		if p.Builtin {
			return nil, p.ID, nil
		}
		return []settings.GrowthPolicy{p}, p.ID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Settings › Growth policy's chart (T-130) is each day's shares as the
// gateway holds an account to them: every point of GrowthCurve is what
// AccountCapsOf gives an account on that day under the policy in effect,
// for each built-in policy and the stages from before, read to the curve's
// last day. A day's table pins the numbers themselves.
func TestGrowthCurveIsTheGatewaysHold(t *testing.T) {
	firstUsedFixture(t, 0)
	before := settings.GrowthPolicy{ID: "before", Name: "Before", Kind: settings.GrowthStaged, Stages: settings.GrowthStagesBefore()}
	type day struct{ d, five, week int }
	for _, c := range []struct {
		p     settings.GrowthPolicy
		last  int
		table []day
	}{
		{settings.BuiltinGrowth()[0], 18, []day{{0, 50, 50}, {1, 53, 53}, {7, 75, 75}, {13, 96, 96}, {14, 100, 100}, {18, 100, 100}}},
		{settings.BuiltinGrowth()[1], 9, []day{{0, 70, 70}, {3, 82, 82}, {7, 100, 100}}},
		{settings.BuiltinGrowth()[2], 38, []day{{0, 30, 30}, {1, 32, 32}, {29, 97, 97}, {30, 100, 100}}},
		{settings.BuiltinGrowth()[3], 7, []day{{0, 100, 100}, {7, 100, 100}}},
		{before, 18, []day{{0, 50, 100}, {2, 50, 100}, {3, 50, 80}, {7, 90, 80}, {14, 95, 80}, {18, 95, 80}}},
	} {
		curve := GrowthCurve(c.p, GrowthHorizon(c.p))
		if len(curve) != c.last+1 || curve[0].Day != 0 || curve[c.last].Day != c.last {
			t.Fatalf("%s: %d days, want 0 to %d", c.p.ID, len(curve), c.last)
		}
		for _, w := range c.table {
			if g := curve[w.d]; g.FiveHours != w.five || g.Week != w.week {
				t.Errorf("%s day %d: 5 hours %d%%, week %d%%; want %d%%, %d%%", c.p.ID, w.d, g.FiveHours, g.Week, w.five, w.week)
			}
		}
		usePolicy(t, c.p)
		for _, g := range curve {
			// half a day in, as an account is on its day d all day
			addedAgo(t, "codex", "new@example.com", time.Duration(g.Day)*24*time.Hour+12*time.Hour)
			caps := AccountCapsOf("codex", "new@example.com")
			five, _ := caps.Of("5 hours")
			week, _ := caps.Of("weekly")
			if five == 0 {
				five = 100
			}
			if week == 0 {
				week = 100
			}
			if five != g.FiveHours || week != g.Week {
				t.Errorf("%s day %d: the gateway holds 5 hours %d%%, week %d%%; the chart says %d%%, %d%%", c.p.ID, g.Day, five, week, g.FiveHours, g.Week)
			}
		}
	}
}

// The chart reads a ramp to a quarter past its last day, 7 days at least
// and 365 at most.
func TestGrowthHorizon(t *testing.T) {
	for _, c := range []struct {
		p    settings.GrowthPolicy
		want int
	}{
		{settings.GrowthPolicy{Kind: settings.GrowthRamp, Start: 50, Days: 1}, 7},
		{settings.GrowthPolicy{Kind: settings.GrowthRamp, Start: 50, Days: 14}, 18},
		{settings.GrowthPolicy{Kind: settings.GrowthRamp, Start: 50, Days: 365}, 365},
		{settings.GrowthPolicy{Kind: settings.GrowthStaged, Stages: []settings.GrowthStage{{From: 40, Window: "7d", Cap: 90}}}, 50},
		{settings.GrowthPolicy{Kind: settings.GrowthStaged, Stages: []settings.GrowthStage{}}, 7},
		{settings.GrowthPolicy{Kind: settings.GrowthOff}, 7},
	} {
		if got := GrowthHorizon(c.p); got != c.want {
			t.Errorf("%+v: %d, want %d", c.p, got, c.want)
		}
	}
}

// The chart's accounts are those with a day, by the gateway's count: an
// account added 3 days ago is on day 3 until the ledger is read; read and
// not used, it waits on day 0. One from before has no day and isn't
// listed. Nothing of a sign-in goes out: the fields are the subscription,
// its name, the account's name and its day.
func TestGrowthAccounts(t *testing.T) {
	firstUsedFixture(t, 3*24*time.Hour+time.Hour)
	// the fixture's Copilot sign-in has a day too; this is about Codex's
	codex := func() []GrowthAccount {
		return slices.DeleteFunc(GrowthAccounts(time.Now()), func(a GrowthAccount) bool { return a.Provider != "codex" })
	}
	got := codex()
	if len(got) != 1 || got[0].Provider != "codex" || got[0].Account != "new@example.com" || got[0].Day != 3 || got[0].Waiting || got[0].Name == "" {
		t.Fatalf("before the ledger is read: %+v", got)
	}
	markRead(t)
	got = codex()
	if len(got) != 1 || got[0].Day != 0 || !got[0].Waiting {
		t.Fatalf("read, not used: %+v", got)
	}
	NoteAccountUsed("codex", "new@example.com", time.Now().Add(-49*time.Hour))
	got = codex()
	if len(got) != 1 || got[0].Day != 2 || got[0].Waiting {
		t.Fatalf("used 49 hours ago: %+v", got)
	}
	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if keys := slices.Sorted(maps.Keys(m)); !slices.Equal(keys, []string{"account", "day", "name", "provider"}) {
		t.Errorf("an account says %v", keys)
	}

	// a cap of its own holds every window, the policy none: not marked
	if err := SetAccountCap("codex", "new@example.com", 80); err != nil {
		t.Fatal(err)
	}
	if got := codex(); len(got) != 0 {
		t.Errorf("an account with a cap of its own: %+v", got)
	}
	if err := SetAccountCap("codex", "new@example.com", 0); err != nil {
		t.Fatal(err)
	}
	if got := codex(); len(got) != 1 {
		t.Fatalf("its cap taken off: %+v", got)
	}
	// a day kept for an account not in the list (a sign-in not listed a
	// while, a plugin removed): not marked either
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == "codex" })
	f.Providers[i].AccountAdded["gone@example.com"] = time.Now().Add(-time.Hour).UTC()
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	if got := codex(); len(got) != 1 || got[0].Account != "new@example.com" {
		t.Errorf("an account not listed is marked: %+v", got)
	}
}
