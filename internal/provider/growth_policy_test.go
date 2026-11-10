package provider

import (
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// daysAgo is now less days days.
func daysAgo(now time.Time, days float64) time.Time {
	return now.Add(-time.Duration(days * 24 * float64(time.Hour)))
}

// A ramp holds every window, the 5 hours and the week alike, at
// start+(100-start)×d/N on its day d (whole days, the fraction dropped),
// start on day 0, and nothing from day N on. A clock before the day it
// was added is day 0; an account from before is never held.
func TestRampCaps(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		start, days int
		day         float64
		want        int // 0 for not held
	}{
		{50, 14, 0, 50},
		{50, 14, 0.99, 50},
		{50, 14, 1, 53},
		{50, 14, 7, 75},
		{50, 14, 7.5, 75},
		{50, 14, 13, 96},
		{50, 14, 13.99, 96},
		{50, 14, 14, 0},
		{50, 14, 400, 0},
		{70, 7, 0, 70},
		{70, 7, 3, 82},
		{70, 7, 7, 0},
		{30, 30, 1, 32},
		{30, 30, 29, 97},
		{30, 30, 30, 0},
		{1, 365, 0, 1},
		{1, 365, 364, 99},
		{1, 365, 365, 0},
		{99, 1, 0, 99},
		{99, 1, 1, 0},
		{99, 365, 364, 99},
		{50, 14, -2, 50}, // a clock set back
	} {
		g := rampCaps(c.start, c.days, daysAgo(now, c.day), now)
		if c.want == 0 {
			if g != nil {
				t.Errorf("%d%% over %d days, day %v: %v, want none", c.start, c.days, c.day, g)
			}
			continue
		}
		for _, wins := range growthWindows {
			for _, w := range wins {
				if g[w] != c.want {
					t.Errorf("%d%% over %d days, day %v: %q at %d%%, want %d%%", c.start, c.days, c.day, w, g[w], c.want)
				}
			}
		}
	}
	if g := rampCaps(50, 14, time.Time{}, now); g != nil {
		t.Errorf("an account not known: %v", g)
	}
	if g := rampCaps(50, 14, addedBefore, now); g != nil {
		t.Errorf("an account from before: %v", g)
	}
	// every day of Standard, each share no less than the day before's
	last := 0
	for d := 0; d < 14; d++ {
		share := rampCaps(50, 14, daysAgo(now, float64(d)+0.5), now)["5 hours"]
		if share < last || share != 50+50*d/14 {
			t.Errorf("Standard's day %d at %d%% (the day before %d%%)", d, share, last)
		}
		last = share
	}
}

// Stages set before policies were kept hold every window on every day as
// they did, read before the move and after it (MigrateGrowth): the old
// default's, a user's own, and none saved on purpose.
func TestGrowthMigrationKeepsEveryDay(t *testing.T) {
	now := time.Now()
	for name, stages := range map[string][]settings.GrowthStage{
		"the stages before": settings.GrowthStagesBefore(),
		"the user's own": {
			{From: 0, Window: settings.GrowthFiveHours, Cap: 30},
			{From: 2, Window: settings.GrowthWeek, Cap: 60},
			{From: 5, Window: settings.GrowthFiveHours, Cap: 70},
			{From: 9, Window: settings.GrowthWeek, Cap: 90},
		},
		"none": {},
	} {
		was := settings.Settings{Growth: &stages}
		moved := was
		if !settings.MigrateGrowth(&moved) {
			t.Fatalf("%s: not moved", name)
		}
		for half := 0; half <= 80; half++ {
			added := daysAgo(now, float64(half)/2)
			old := growthCaps(stages, added, now)
			if g := policyCaps(was.GrowthInEffect(), added, now); !reflect.DeepEqual(g, old) {
				t.Errorf("%s, day %v before the move: %v, was %v", name, float64(half)/2, g, old)
			}
			if g := policyCaps(moved.GrowthInEffect(), added, now); !reflect.DeepEqual(g, old) {
				t.Errorf("%s, day %v after the move: %v, was %v", name, float64(half)/2, g, old)
			}
		}
	}
}

// A user who set nothing has a new account held by Standard: 53% of its
// five hours and its week on its second day, nothing from its fourteenth.
// The default made Cautious, Off or a ramp of the user's holds by it, and
// a cap the user set on a window still comes first.
func TestGrowthStandardByDefault(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
	addedAgo(t, "codex", "new@example.com", 36*time.Hour)
	share := func(w string) int {
		t.Helper()
		s, _ := AccountCapsOf("codex", "new@example.com").Of(w)
		return s
	}
	if f, w := share("5 hours"), share("Weekly"); f != 53 || w != 53 {
		t.Fatalf("Standard's second day: five hours %d%%, week %d%%", f, w)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := p.CapsOf("new@example.com").Of("7 days · Opus"); s != 53 {
		t.Fatalf("the gateway's read: %d%%", s)
	}
	def := func(id string, own ...settings.GrowthPolicy) {
		t.Helper()
		if err := settings.EditGrowth(func([]settings.GrowthPolicy, string) ([]settings.GrowthPolicy, string, error) {
			return own, id, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	def(settings.GrowthCautious)
	if f := share("5 hours"); f != 32 {
		t.Fatalf("Cautious's second day: %d%%", f)
	}
	def(settings.GrowthNone)
	if c := AccountCapsOf("codex", "new@example.com"); c.Holds() {
		t.Fatalf("Off holds: %+v", c)
	}
	def("slow", settings.GrowthPolicy{ID: "slow", Name: "Slow", Kind: settings.GrowthRamp, Start: 10, Days: 10})
	if f := share("5 hours"); f != 19 {
		t.Fatalf("the user's ramp on its second day: %d%%", f)
	}
	if err := SetWindowCap("codex", "new@example.com", "5 hours", 100); err != nil {
		t.Fatal(err)
	}
	if f, w := share("5 hours"), share("Weekly"); f != 0 || w != 19 {
		t.Fatalf("the user's 100 on the five hours: five hours %d%%, week %d%%", f, w)
	}
	if err := SetWindowCap("codex", "new@example.com", "5 hours", 0); err != nil {
		t.Fatal(err)
	}
	def("")
	addedAgo(t, "codex", "new@example.com", 14*24*time.Hour+time.Hour)
	if c := AccountCapsOf("codex", "new@example.com"); c.Holds() {
		t.Fatalf("Standard on its fourteenth day: %+v", c)
	}
}
