package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/lastgood"
)

func growthHome(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	lastgood.Reset()
	t.Cleanup(lastgood.Reset)
}

// A user who set nothing is held by Standard, a ramp from 50% to 100% in
// 14 days (需求方 2026-11-10); the built-ins are Standard, Gentle,
// Cautious and Off, and a default that names a policy gone falls back to
// Standard.
func TestGrowthDefaultIsStandard(t *testing.T) {
	g := Settings{}.GrowthInEffect()
	if g.ID != GrowthStandard || g.Kind != GrowthRamp || g.Start != 50 || g.Days != 14 || !g.Builtin {
		t.Fatalf("no settings: %+v", g)
	}
	want := map[string][2]int{GrowthStandard: {50, 14}, GrowthGentle: {70, 7}, GrowthCautious: {30, 30}}
	b := BuiltinGrowth()
	if len(b) != 4 || b[3].ID != GrowthNone || b[3].Kind != GrowthOff {
		t.Fatalf("built-ins: %+v", b)
	}
	for _, p := range b[:3] {
		if w := want[p.ID]; p.Kind != GrowthRamp || p.Start != w[0] || p.Days != w[1] || !p.Builtin {
			t.Errorf("%s: %+v", p.ID, p)
		}
	}
	if g := (Settings{GrowthDefault: "gone"}).GrowthInEffect(); g.ID != GrowthStandard {
		t.Fatalf("a default gone: %+v", g)
	}
	if g := (Settings{GrowthDefault: GrowthNone}).GrowthInEffect(); g.Kind != GrowthOff {
		t.Fatalf("off: %+v", g)
	}
	own := GrowthPolicy{ID: "slow", Name: "Slow", Kind: GrowthRamp, Start: 10, Days: 90}
	if g := (Settings{GrowthPolicies: []GrowthPolicy{own}, GrowthDefault: "slow"}).GrowthInEffect(); !reflect.DeepEqual(g, own) {
		t.Fatalf("the user's own: %+v", g)
	}
	// a built-in a later magpie adds under the id of the user's own
	// doesn't take its place
	own.ID = GrowthGentle
	if g := (Settings{GrowthPolicies: []GrowthPolicy{own}, GrowthDefault: GrowthGentle}).GrowthInEffect(); !reflect.DeepEqual(g, own) {
		t.Fatalf("the user's own beside a built-in of its id: %+v", g)
	}
}

// Save doesn't check the growth policies: a settings.json with one a later
// magpie wrote (a kind this one doesn't know) or a default gone still
// takes every other setting's save, and is read as holding nothing or as
// Standard.
func TestSaveTakesPoliciesItDoesntKnow(t *testing.T) {
	growthHome(t)
	later := Settings{Theme: "dark", GrowthPolicies: []GrowthPolicy{{ID: "c", Name: "C", Kind: "curve"}}, GrowthDefault: "c"}
	if err := Save(later); err != nil {
		t.Fatalf("a kind a later magpie wrote: %v", err)
	}
	if s := Load(); s.Theme != "dark" || len(s.GrowthPolicies) != 1 || s.GrowthInEffect().Kind != "curve" {
		t.Fatalf("read as %+v", s)
	}
	if err := Save(Settings{GrowthDefault: "gone"}); err != nil {
		t.Fatalf("a default gone: %v", err)
	}
	if g := Load().GrowthInEffect(); g.ID != GrowthStandard {
		t.Fatalf("a default gone read as %+v", g)
	}
}

// CheckGrowthPolicies refuses a ramp out of its bounds, a name empty or
// had already (in any case, a built-in's too), an id had or not of its
// characters, a built-in policy, a kind it doesn't know, bad stages and a
// default that names no policy; and takes each bound itself.
func TestCheckGrowthPolicies(t *testing.T) {
	ramp := func(start, days int) GrowthPolicy {
		return GrowthPolicy{ID: "r", Name: "R", Kind: GrowthRamp, Start: start, Days: days}
	}
	for _, c := range []struct {
		name string
		own  []GrowthPolicy
		def  string
		bad  string // part of the error, "" for none
	}{
		{"start 1", []GrowthPolicy{ramp(1, 14)}, "", ""},
		{"start 99", []GrowthPolicy{ramp(99, 14)}, "", ""},
		{"start 0", []GrowthPolicy{ramp(0, 14)}, "", "1% to 99%"},
		{"start 100", []GrowthPolicy{ramp(100, 14)}, "", "1% to 99%"},
		{"days 1", []GrowthPolicy{ramp(50, 1)}, "", ""},
		{"days 365", []GrowthPolicy{ramp(50, 365)}, "", ""},
		{"days 0", []GrowthPolicy{ramp(50, 0)}, "", "1 to 365 days"},
		{"days 366", []GrowthPolicy{ramp(50, 366)}, "", "1 to 365 days"},
		{"no name", []GrowthPolicy{{ID: "r", Name: "  ", Kind: GrowthRamp, Start: 50, Days: 14}}, "", "needs a name"},
		{"a long name", []GrowthPolicy{{ID: "r", Name: strings.Repeat("名", 65), Kind: GrowthRamp, Start: 50, Days: 14}}, "", "up to 64"},
		{"a built-in's name", []GrowthPolicy{{ID: "r", Name: "standard", Kind: GrowthRamp, Start: 50, Days: 14}}, "", "two growth policies are named"},
		{"two names", []GrowthPolicy{ramp(50, 14), {ID: "s", Name: " r ", Kind: GrowthRamp, Start: 50, Days: 14}}, "", "two growth policies are named"},
		{"two ids", []GrowthPolicy{ramp(50, 14), {ID: "r", Name: "S", Kind: GrowthRamp, Start: 50, Days: 14}}, "", "the id"},
		{"an id with a space", []GrowthPolicy{{ID: "a b", Name: "S", Kind: GrowthRamp, Start: 50, Days: 14}}, "", "letters, digits"},
		{"no id", []GrowthPolicy{{Name: "S", Kind: GrowthRamp, Start: 50, Days: 14}}, "", "letters, digits"},
		{"a built-in's id", []GrowthPolicy{{ID: GrowthStandard, Name: "Mine", Kind: GrowthRamp, Start: 60, Days: 14}}, "", "can't be changed"},
		{"off is built in only", []GrowthPolicy{{ID: "r", Name: "R", Kind: GrowthOff}}, "", "ramps"},
		{"bad stages", []GrowthPolicy{{ID: "r", Name: "R", Kind: GrowthStaged, Stages: []GrowthStage{{From: 0, Window: "1h", Cap: 50}}}}, "", "window"},
		{"no stages", []GrowthPolicy{{ID: "r", Name: "R", Kind: GrowthStaged}}, "", ""},
		{"a default of the user's", []GrowthPolicy{ramp(50, 14)}, "r", ""},
		{"a built-in default", nil, GrowthCautious, ""},
		{"a default gone", []GrowthPolicy{ramp(50, 14)}, "gone", "no growth policy has the id"},
	} {
		err := CheckGrowthPolicies(c.own, c.def)
		switch {
		case c.bad == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("%s: %v, want %q", c.name, err, c.bad)
		}
	}
}

// Stages set before policies were kept are read as the user's own stages
// policy, the default, before a save moves them and after: moved, Growth
// is gone and they are "My policy (migrated)". A policy named so already
// gives the moved one a name of its own, and a default named already
// stays the default. None saved on purpose moves as none.
func TestMigrateGrowth(t *testing.T) {
	stages := []GrowthStage{{From: 0, Window: GrowthFiveHours, Cap: 30}, {From: 4, Window: GrowthWeek, Cap: 60}}
	s := Settings{Growth: &stages}
	before := s.GrowthInEffect()
	if before.ID != GrowthMine || before.Kind != GrowthStaged || !reflect.DeepEqual(before.Stages, stages) {
		t.Fatalf("read before the move: %+v", before)
	}
	if !MigrateGrowth(&s) || s.Growth != nil || s.GrowthDefault != GrowthMine || len(s.GrowthPolicies) != 1 {
		t.Fatalf("moved: %+v", s)
	}
	if p := s.GrowthPolicies[0]; p.Name != "My policy (migrated)" || !reflect.DeepEqual(p.Stages, stages) || p.Builtin {
		t.Fatalf("the moved policy: %+v", p)
	}
	if after := s.GrowthInEffect(); !reflect.DeepEqual(after, before) {
		t.Fatalf("in effect after the move %+v, before %+v", after, before)
	}
	if err := CheckGrowthPolicies(s.GrowthPolicies, s.GrowthDefault); err != nil {
		t.Fatal(err)
	}
	if MigrateGrowth(&s) {
		t.Fatal("moved twice")
	}

	taken := GrowthPolicy{ID: GrowthMine, Name: "My policy (migrated)", Kind: GrowthRamp, Start: 40, Days: 20}
	s = Settings{Growth: &stages, GrowthPolicies: []GrowthPolicy{taken}, GrowthDefault: GrowthGentle}
	if g := s.GrowthInEffect(); g.ID != GrowthGentle {
		t.Fatalf("a default named, before the move: %+v", g)
	}
	MigrateGrowth(&s)
	if len(s.GrowthPolicies) != 2 || s.GrowthPolicies[1].ID != "mine-2" || s.GrowthPolicies[1].Name != "My policy (migrated) 2" || s.GrowthDefault != GrowthGentle {
		t.Fatalf("beside one taken: %+v", s)
	}
	if err := CheckGrowthPolicies(s.GrowthPolicies, s.GrowthDefault); err != nil {
		t.Fatal(err)
	}

	s = Settings{GrowthPolicies: []GrowthPolicy{taken}, Growth: &stages}
	if g := s.GrowthInEffect(); g.ID != "mine-2" || !reflect.DeepEqual(g.Stages, stages) {
		t.Fatalf("read before the move, beside one taken: %+v", g)
	}

	none := []GrowthStage{}
	s = Settings{Growth: &none}
	MigrateGrowth(&s)
	if g := s.GrowthInEffect(); g.Kind != GrowthStaged || g.Stages == nil || len(g.Stages) != 0 {
		t.Fatalf("none on purpose: %+v", g)
	}
}

// EditGrowth moves the stages from before first, and saves what the edit
// returns once it checks; a built-in policy is refused, and a
// settings.json that can't be read is left as it is, with the error.
func TestEditGrowth(t *testing.T) {
	growthHome(t)
	stages := []GrowthStage{{From: 0, Window: GrowthFiveHours, Cap: 30}}
	if err := Save(Settings{Growth: &stages, Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := EditGrowth(func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error) {
		if len(own) != 1 || own[0].ID != GrowthMine || def != GrowthMine {
			t.Fatalf("handed %+v %q", own, def)
		}
		return append(own, GrowthPolicy{ID: "slow", Name: " Slow ", Kind: GrowthRamp, Start: 20, Days: 60, Stages: stages}), "slow", nil
	}); err != nil {
		t.Fatal(err)
	}
	s := Load()
	if s.Growth != nil || s.GrowthDefault != "slow" || len(s.GrowthPolicies) != 2 || s.Theme != "dark" {
		t.Fatalf("saved: %+v", s)
	}
	if p := s.GrowthPolicies[1]; p.Name != "Slow" || p.Stages != nil {
		t.Fatalf("kept as %+v", p)
	}
	if err := EditGrowth(func(own []GrowthPolicy, _ string) ([]GrowthPolicy, string, error) {
		return append(own, BuiltinGrowth()[0]), "", nil
	}); err == nil {
		t.Fatal("a built-in policy saved")
	}
	if err := EditGrowth(func(own []GrowthPolicy, _ string) ([]GrowthPolicy, string, error) {
		return own[1:], GrowthMine, nil
	}); err == nil {
		t.Fatal("a default removed saved")
	}
	if Load().GrowthDefault != "slow" {
		t.Fatal("a refused edit saved")
	}

	// a settings.json that can't be read at all: a folder in its place
	if err := os.Remove(Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	lastgood.Reset()
	if err := EditGrowth(func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error) {
		t.Fatal("edited settings nobody knows")
		return own, def, nil
	}); err == nil {
		t.Fatal("no error on settings that can't be read")
	}
	if fi, err := os.Stat(Path()); err != nil || !fi.IsDir() {
		t.Fatalf("written over: %v %v", fi, err)
	}
}

// A policy counts its days on the calendar ("") or by use
// (GrowthUsedDays, T-134): every built-in one and one saved before counts
// the calendar's, as before; one of the user's keeps what it was saved
// with; any other way is refused.
func TestGrowthCountDays(t *testing.T) {
	growthHome(t)
	for _, p := range BuiltinGrowth() {
		if p.CountDays != "" {
			t.Errorf("%s counts %q", p.ID, p.CountDays)
		}
	}
	if g := (Settings{}).GrowthInEffect(); g.CountDays != "" {
		t.Fatalf("a user who set nothing counts %q", g.CountDays)
	}
	use := GrowthPolicy{ID: "use", Name: "By use", Kind: GrowthStaged, Stages: []GrowthStage{{From: 0, Window: GrowthWeek, Cap: 60}}, CountDays: GrowthUsedDays}
	if err := CheckGrowthPolicies([]GrowthPolicy{use, {ID: "climb", Name: "Climb", Kind: GrowthRamp, Start: 40, Days: 9, CountDays: GrowthUsedDays}}, "use"); err != nil {
		t.Fatal(err)
	}
	bad := use
	bad.CountDays = "weekdays"
	if err := CheckGrowthPolicies([]GrowthPolicy{bad}, ""); err == nil || !strings.Contains(err.Error(), "weekdays") {
		t.Fatalf("a way to count no magpie has: %v", err)
	}
	if err := EditGrowth(func([]GrowthPolicy, string) ([]GrowthPolicy, string, error) {
		return []GrowthPolicy{use}, "use", nil
	}); err != nil {
		t.Fatal(err)
	}
	if g := Load().GrowthInEffect(); g.CountDays != GrowthUsedDays {
		t.Fatalf("saved by use, read back %+v", g)
	}
	b, _ := os.ReadFile(Path())
	if !strings.Contains(string(b), `"countDays": "used"`) {
		t.Fatalf("settings.json: %s", b)
	}
}
