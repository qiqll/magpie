package provider

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// growthDefault makes the policy id the default, the user's own beside it.
func growthDefault(t *testing.T, id string, own ...settings.GrowthPolicy) {
	t.Helper()
	if err := settings.EditGrowth(func([]settings.GrowthPolicy, string) ([]settings.GrowthPolicy, string, error) {
		return own, id, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// heldAt is the share the gateway holds the account's window at, 100 for
// none, as AccountGrowth says it.
func heldAt(t *testing.T, w string) int {
	t.Helper()
	if s, _ := AccountCapsOf("codex", "new@example.com").Of(w); s > 0 {
		return s
	}
	return 100
}

// An account's row says the shares the gateway holds it at, day by day,
// for a ramp, stages and stages that never let go (T-132): AccountGrowthOf
// reads the same withGrowth as AccountCapsOf, never a formula of its own.
func TestAccountGrowthIsTheGatewaysHold(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	for _, p := range []settings.GrowthPolicy{
		{ID: "climb", Name: "Climb", Kind: settings.GrowthRamp, Start: 20, Days: 9},
		{ID: "steps", Name: "Steps", Kind: settings.GrowthStaged, Stages: []settings.GrowthStage{
			{From: 0, Window: settings.GrowthFiveHours, Cap: 40}, {From: 2, Window: settings.GrowthWeek, Cap: 70},
			{From: 5, Window: settings.GrowthFiveHours, Cap: 99}, {From: 5, Window: settings.GrowthWeek, Cap: 99},
		}},
	} {
		growthDefault(t, p.ID, p)
		for d := 0; d <= 12; d++ {
			addedAgo(t, "codex", "new@example.com", time.Duration(d)*24*time.Hour+time.Hour)
			g := AccountGrowthOf("codex", "new@example.com", time.Now())
			f, w, o := heldAt(t, "5 hours"), heldAt(t, "Weekly"), heldAt(t, "7 days · Opus")
			if f == 100 && w == 100 {
				if g != nil {
					t.Errorf("%s day %d: the gateway holds nothing, the row says %+v", p.ID, d, g)
				}
				continue
			}
			if g == nil || g.Day != d || g.FiveHours != f || g.Week != w || g.Week != o || g.Policy != p.ID || g.Name != p.Name {
				t.Errorf("%s day %d: the gateway holds 5h %d%% week %d%% (opus %d%%), the row says %+v", p.ID, d, f, w, o, g)
			}
		}
	}
}

// Its last day, and a stage's next day, are the gateway's too: a ramp's
// Days, no Next; stages' next change with its shares; stages that never
// let go have no last day.
func TestAccountGrowthDays(t *testing.T) {
	firstUsedFixture(t, 36*time.Hour)
	growthDefault(t, settings.GrowthStandard)
	g := AccountGrowthOf("codex", "new@example.com", time.Now())
	if g == nil || g.Day != 1 || g.Days != 14 || g.FiveHours != 53 || g.Week != 53 || g.Next != 0 || !g.Builtin || g.Kind != settings.GrowthRamp {
		t.Fatalf("Standard's second day: %+v", g)
	}
	stagesBefore(t)
	addedAgo(t, "codex", "new@example.com", time.Hour)
	g = AccountGrowthOf("codex", "new@example.com", time.Now())
	if g == nil || g.Day != 0 || g.FiveHours != 50 || g.Week != 100 || g.Next != 3 || g.NextFiveHours != 50 || g.NextWeek != 80 || g.Days != 0 {
		t.Fatalf("the stages from before, day 0: %+v", g)
	}
	addedAgo(t, "codex", "new@example.com", 4*24*time.Hour+time.Hour)
	g = AccountGrowthOf("codex", "new@example.com", time.Now())
	if g == nil || g.Day != 4 || g.FiveHours != 50 || g.Week != 80 || g.Next != 7 || g.NextFiveHours != 90 || g.NextWeek != 80 {
		t.Fatalf("the stages from before, day 4: %+v", g)
	}
	// on the last day it holds, Days is tomorrow
	growthDefault(t, settings.GrowthGentle)
	addedAgo(t, "codex", "new@example.com", 6*24*time.Hour+time.Hour)
	if g = AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.Day != 6 || g.Days != 7 {
		t.Fatalf("Gentle's last day: %+v", g)
	}
	addedAgo(t, "codex", "new@example.com", 7*24*time.Hour+time.Hour)
	if g = AccountGrowthOf("codex", "new@example.com", time.Now()); g != nil {
		t.Fatalf("Gentle past its last day: %+v", g)
	}
	growthDefault(t, settings.GrowthNone)
	addedAgo(t, "codex", "new@example.com", time.Hour)
	if g = AccountGrowthOf("codex", "new@example.com", time.Now()); g != nil {
		t.Fatalf("Off: %+v", g)
	}
}

// One not used yet waits for its first use; one the gateway doesn't serve
// counts from when it was added, and says so.
func TestAccountGrowthHowCounted(t *testing.T) {
	firstUsedFixture(t, 3*24*time.Hour+time.Hour)
	growthDefault(t, settings.GrowthStandard)
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.Day != 3 || g.Waiting || g.ByAdded {
		t.Fatalf("before the ledger is read: %+v", g)
	}
	markRead(t)
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.Day != 0 || !g.Waiting || g.ByAdded || g.FiveHours != 50 {
		t.Fatalf("read, never used: %+v", g)
	}
	if err := editStored("codex", func(q *Provider) bool { q.SwitchOnly = true; return true }); err != nil {
		t.Fatal(err)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.Day != 3 || g.Waiting || !g.ByAdded {
		t.Fatalf("Codex switch-only, from when it was added: %+v", g)
	}
}

// Skipping the warm-up lets the gateway hold the account at the user's
// own caps alone, keeps a window's share the user set, and a share the
// user sets after is theirs: taken off, the policy has the window again.
func TestSkipGrowth(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	growthDefault(t, settings.GrowthStandard)
	if err := SetWindowCap("codex", "new@example.com", "weekly", 70); err != nil {
		t.Fatal(err)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.FiveHours != 50 || g.Week != 50 {
		t.Fatalf("the week's own 70%% leaves the policy the 7 days: %+v", g)
	}
	if err := SkipGrowth("codex", "NEW@example.com"); err != nil {
		t.Fatal(err)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g != nil {
		t.Fatalf("skipped, the row says %+v", g)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"5 hours", "7 days", "7 days · Opus"} {
		if s, _ := p.CapsOf("new@example.com").Of(w); s != 0 {
			t.Errorf("skipped, the gateway holds %s at %d%%", w, s)
		}
	}
	if s := heldAt(t, "Weekly"); s != 70 {
		t.Errorf("the user's own weekly share: %d%%", s)
	}
	// the user's word after the skip
	if err := SetWindowCap("codex", "new@example.com", "5 hours", 60); err != nil {
		t.Fatal(err)
	}
	if s := heldAt(t, "5 hours"); s != 60 {
		t.Errorf("a share set after the skip: %d%%", s)
	}
	if err := SetWindowCap("codex", "new@example.com", "5 hours", 0); err != nil {
		t.Fatal(err)
	}
	if s := heldAt(t, "5 hours"); s != 50 {
		t.Errorf("set back to follow the account, the policy's again: %d%%", s)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.FiveHours != 50 || g.Week != 100 {
		t.Errorf("the five hours the policy's again: %+v", g)
	}
	// a cap of the account's own: the policy holds none, nothing written
	if err := SetAccountCap("codex", "new@example.com", 80); err != nil {
		t.Fatal(err)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g != nil {
		t.Errorf("an account cap: %+v", g)
	}
	before, _ := storedPicks("codex")
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if after, _ := storedPicks("codex"); after.AccountWindowCaps["new@example.com"]["5 hours"] != before.AccountWindowCaps["new@example.com"]["5 hours"] {
		t.Errorf("an account with a cap of its own was written: %v", after.AccountWindowCaps)
	}
	if err := SkipGrowth("codex", "nobody@example.com"); err == nil {
		t.Error("an account the subscription hasn't was skipped")
	}
}

// A window capped at an amount of its own is the user's: the policy
// leaves it, and the skip doesn't write a share over it.
func TestGrowthLeavesAnAmount(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	growthDefault(t, settings.GrowthStandard)
	if err := SetWindowAmount("codex", "new@example.com", []string{"weekly"}, 40); err != nil {
		t.Fatal(err)
	}
	c := AccountCapsOf("codex", "new@example.com")
	if _, ok := c.Windows["weekly"]; ok {
		t.Fatalf("the policy's share beside an amount: %v", c.Windows)
	}
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	s, _ := storedPicks("codex")
	if _, ok := s.AccountWindowCaps["new@example.com"]["weekly"]; ok || s.AccountWindowAmounts["new@example.com"]["weekly"] != 40 {
		t.Fatalf("skipped over an amount: shares %v amounts %v", s.AccountWindowCaps, s.AccountWindowAmounts)
	}
}
