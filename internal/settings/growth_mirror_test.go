package settings

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// beforePolicies is settings.json as a magpie from before policies were
// kept (T-129) reads and writes it: growth and what else it knows, with
// growthPolicies and growthDefault dropped on its next save.
type beforePolicies struct {
	Theme  string         `json:"theme,omitempty"`
	Growth *[]GrowthStage `json:"growth,omitempty"`
}

func readBefore(t *testing.T) beforePolicies {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var old beforePolicies
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	return old
}

func saveBefore(t *testing.T, old beforePolicies) {
	t.Helper()
	b, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// T-138: stages migrated to "My policy (migrated)" and still the default
// are kept in growth too, so a magpie from before policies were kept,
// sharing the file or synced with this one, holds by the user's own stages
// and writes them back; this magpie then reads the same stages again. The
// mirror never moves again beside the policies it copies, follows the
// default's stages as they are edited, and is cleared when the default is
// a policy that magpie can't read (a built-in, a ramp, one counting the
// days used).
func TestGrowthMirrorForMagpieBeforePolicies(t *testing.T) {
	growthHome(t)
	stages := []GrowthStage{{From: 0, Window: GrowthFiveHours, Cap: 30}, {From: 4, Window: GrowthWeek, Cap: 60}}
	if err := Save(Settings{Growth: &stages, Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	// the first save of the policies moves the stages, and keeps them in
	// growth
	keep := func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error) { return own, def, nil }
	if err := EditGrowth(keep); err != nil {
		t.Fatal(err)
	}
	s := Load()
	if s.GrowthDefault != GrowthMine || len(s.GrowthPolicies) != 1 || s.Growth == nil || !reflect.DeepEqual(*s.Growth, stages) {
		t.Fatalf("migrated: %+v", s)
	}
	// this magpie reads the policies: the mirror is no stages to move
	if g := s.GrowthInEffect(); g.ID != GrowthMine || !reflect.DeepEqual(g.Stages, stages) {
		t.Fatalf("in effect beside the mirror: %+v", g)
	}
	all := s.GrowthPoliciesAll()
	if len(all) != len(BuiltinGrowth())+1 || s.GrowthKnows("mine-2") {
		t.Fatalf("the mirror is read as a second policy: %+v", all)
	}
	if g := s.GrowthOf("mine-2"); g.ID != GrowthMine {
		t.Fatalf("a pick of the mirror: %+v", g)
	}
	m := s
	m.GrowthPolicies = slices.Clone(s.GrowthPolicies)
	if MigrateGrowth(&m) || m.Growth != nil || len(m.GrowthPolicies) != 1 {
		t.Fatalf("the mirror moved: %+v", m)
	}
	if err := EditGrowth(keep); err != nil {
		t.Fatal(err)
	}
	if s := Load(); len(s.GrowthPolicies) != 1 || s.Growth == nil {
		t.Fatalf("saved again: %+v", s)
	}

	// the magpie from before reads the user's stages, and its save drops
	// the policies
	old := readBefore(t)
	if old.Growth == nil || !reflect.DeepEqual(*old.Growth, stages) {
		t.Fatalf("read by a magpie from before: %+v", old.Growth)
	}
	old.Theme = "light"
	saveBefore(t, old)
	s = Load()
	if g := s.GrowthInEffect(); g.ID != GrowthMine || g.Kind != GrowthStaged || !reflect.DeepEqual(g.Stages, stages) {
		t.Fatalf("read back after a magpie from before saved: %+v", g)
	}
	// and stages it edited are this magpie's once it reads them
	edited := []GrowthStage{{From: 0, Window: GrowthWeek, Cap: 40}}
	old.Growth = &edited
	saveBefore(t, old)
	if g := Load().GrowthInEffect(); !reflect.DeepEqual(g.Stages, edited) {
		t.Fatalf("stages a magpie from before edited: %+v", g)
	}
	if err := EditGrowth(keep); err != nil {
		t.Fatal(err)
	}

	// the default's stages edited here are mirrored as they are now
	again := []GrowthStage{{From: 0, Window: GrowthFiveHours, Cap: 20}}
	if err := EditGrowth(func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error) {
		return WithOwnStages(own, def, again)
	}); err != nil {
		t.Fatal(err)
	}
	if o := readBefore(t); o.Growth == nil || !reflect.DeepEqual(*o.Growth, again) {
		t.Fatalf("mirror after an edit: %+v", o.Growth)
	}
	if s := Load(); len(s.GrowthPolicies) != 1 || !reflect.DeepEqual(s.GrowthInEffect().Stages, again) {
		t.Fatalf("policies after an edit: %+v", s)
	}
	// none on purpose is mirrored as none, not left out
	if err := EditGrowth(func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error) {
		return WithOwnStages(own, def, []GrowthStage{})
	}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(Path()); !strings.Contains(string(b), `"growth": []`) {
		t.Fatalf("none mirrored: %s", b)
	}

	// a default the magpie from before can't read clears the mirror
	ramp := GrowthPolicy{ID: "slow", Name: "Slow", Kind: GrowthRamp, Start: 20, Days: 60}
	used := GrowthPolicy{ID: "use", Name: "By use", Kind: GrowthStaged, Stages: stages, CountDays: GrowthUsedDays}
	for _, def := range []string{"slow", "use", GrowthGentle, ""} {
		if err := EditGrowth(func(own []GrowthPolicy, _ string) ([]GrowthPolicy, string, error) {
			return append(own[:1:1], ramp, used), def, nil
		}); err != nil {
			t.Fatal(err)
		}
		if o := readBefore(t); o.Growth != nil {
			t.Fatalf("default %q mirrored %+v", def, *o.Growth)
		}
		if s := Load(); len(s.GrowthPolicies) != 3 || s.GrowthDefault != def {
			t.Fatalf("default %q: %+v", def, s)
		}
	}
	// and the migrated one made the default again mirrors it again
	if err := EditGrowth(func(own []GrowthPolicy, _ string) ([]GrowthPolicy, string, error) {
		return own, GrowthMine, nil
	}); err != nil {
		t.Fatal(err)
	}
	if o := readBefore(t); o.Growth == nil || len(*o.Growth) != 0 {
		t.Fatalf("migrated default again: %+v", o.Growth)
	}
	// a Settings page save carries the file's growth and keeps the mirror
	s = Load()
	s.Theme = "dark"
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Growth == nil || len(s.GrowthPolicies) != 3 {
		t.Fatalf("after another setting's save: %+v", s)
	}
}

// Stages in growth that aren't the default's are still stages to move, as
// before T-138: a default named beside them keeps them a policy of their
// own, and a save keeps them as they are, never the default's over them.
func TestGrowthNotTheMirrorStillMoves(t *testing.T) {
	growthHome(t)
	stages := []GrowthStage{{From: 0, Window: GrowthFiveHours, Cap: 30}}
	other := GrowthPolicy{ID: "steps", Name: "Steps", Kind: GrowthStaged, Stages: []GrowthStage{{From: 0, Window: GrowthWeek, Cap: 70}}}
	s := Settings{Growth: &stages, GrowthPolicies: []GrowthPolicy{other}, GrowthDefault: "steps"}
	if !s.GrowthKnows(GrowthMine) {
		t.Fatal("stages to move aren't listed")
	}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if o := readBefore(t); o.Growth == nil || !reflect.DeepEqual(*o.Growth, stages) {
		t.Fatalf("a save wrote over stages to move: %+v", o.Growth)
	}
	m := Load()
	if !MigrateGrowth(&m) || len(m.GrowthPolicies) != 2 || m.GrowthDefault != "steps" || !reflect.DeepEqual(m.GrowthPolicies[1].Stages, stages) {
		t.Fatalf("moved: %+v", m)
	}
}

// A default whose stages CheckGrowth refuses (another magpie wrote them)
// isn't mirrored, so a save of another setting isn't refused for it.
func TestGrowthMirrorSkipsStagesItRefuses(t *testing.T) {
	growthHome(t)
	bad := GrowthPolicy{ID: "odd", Name: "Odd", Kind: GrowthStaged, Stages: []GrowthStage{{From: 0, Window: "1d", Cap: 50}}}
	if err := Save(Settings{GrowthPolicies: []GrowthPolicy{bad}, GrowthDefault: "odd", Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if o := readBefore(t); o.Growth != nil || o.Theme != "dark" {
		t.Fatalf("mirrored stages it refuses: %+v", o)
	}
}

// A mirror left beside a default changed outside EditGrowth (by hand, or
// a default gone) is still a mirror: never moved again as "mine-2", and
// cleared by the next save of any setting, as the default isn't one a
// magpie from before can read.
func TestGrowthStaleMirrorIsNoSecondPolicy(t *testing.T) {
	growthHome(t)
	stages := []GrowthStage{{From: 0, Window: GrowthFiveHours, Cap: 30}}
	if err := Save(Settings{Growth: &stages}); err != nil {
		t.Fatal(err)
	}
	if err := EditGrowth(func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error) { return own, def, nil }); err != nil {
		t.Fatal(err)
	}
	for _, def := range []string{GrowthGentle, "gone"} {
		s := Load()
		s.GrowthDefault = def // as a hand edit leaves it, the mirror kept
		s.Growth = &stages
		if s.GrowthKnows("mine-2") || len(s.GrowthPoliciesAll()) != len(BuiltinGrowth())+1 {
			t.Fatalf("default %q: the stale mirror is a second policy: %+v", def, s.GrowthPoliciesAll())
		}
		if err := Save(s); err != nil {
			t.Fatal(err)
		}
		if o := readBefore(t); o.Growth != nil {
			t.Fatalf("default %q: a save kept the stale mirror %+v", def, *o.Growth)
		}
		if err := EditGrowth(func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error) {
			if len(own) != 1 {
				t.Fatalf("default %q: handed %+v", def, own)
			}
			return own, GrowthMine, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// A mirror a later magpie wrote of stages this one refuses (a window it
// doesn't know) never blocks a save: it is a mirror, cleared, not checked.
func TestGrowthMirrorItRefusesBlocksNoSave(t *testing.T) {
	growthHome(t)
	odd := []GrowthStage{{From: 0, Window: "1d", Cap: 50}}
	b, _ := json.Marshal(map[string]any{
		"growth":         odd,
		"growthPolicies": []GrowthPolicy{{ID: "odd", Name: "Odd", Kind: GrowthStaged, Stages: odd}},
		"growthDefault":  "odd",
	})
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	s := Load()
	s.Theme = "dark"
	if err := Save(s); err != nil {
		t.Fatalf("a later magpie's mirror blocked a save: %v", err)
	}
	if o := readBefore(t); o.Growth != nil || o.Theme != "dark" {
		t.Fatalf("saved: %+v", o)
	}
	if s := Load(); len(s.GrowthPolicies) != 1 || s.GrowthDefault != "odd" {
		t.Fatalf("the policies: %+v", s)
	}
}

// withGrowth asks GrowthFor on every request: beside a mirror of many
// stages, a pick gone included, it makes nothing on the heap.
func TestGrowthForBesideAMirrorMakesNothing(t *testing.T) {
	var stages []GrowthStage
	for d := range 12 {
		stages = append(stages, GrowthStage{From: d, Window: GrowthFiveHours, Cap: 20 + d})
	}
	mirror := slices.Clone(stages)
	s := Settings{Growth: &mirror, GrowthPolicies: []GrowthPolicy{{ID: GrowthMine, Name: "Mine", Kind: GrowthStaged, Stages: stages}}, GrowthDefault: GrowthMine}
	if n := testing.AllocsPerRun(100, func() { _ = s.GrowthFor("gone", "gone-too") }); n != 0 {
		t.Fatalf("GrowthFor made %v on the heap", n)
	}
	if g := s.GrowthFor("gone", "gone-too"); g.ID != GrowthMine {
		t.Fatalf("held by %+v", g)
	}
}
