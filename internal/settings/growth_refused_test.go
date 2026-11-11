package settings

import (
	"errors"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/lastgood"
)

// Each refusal of a growth policy says which rule it is and the values
// its words name (T-139), so Settings › Growth policy says it in the
// page's language: a range, a name or id twice, a built-in one changed, a
// default gone, and settings.json that can't be read. The English is as
// it was. Every code in GrowthCodes is one of these.
func TestGrowthRefusalsHaveCodes(t *testing.T) {
	ramp := func(id, name string, start, days int) GrowthPolicy {
		return GrowthPolicy{ID: id, Name: name, Kind: GrowthRamp, Start: start, Days: days}
	}
	staged := func(g ...GrowthStage) []GrowthPolicy {
		return []GrowthPolicy{{ID: "s", Name: "S", Kind: GrowthStaged, Stages: g}}
	}
	seen := map[string]bool{}
	for _, c := range []struct {
		own  []GrowthPolicy
		def  string
		code string
		vals map[string]string
		says string
	}{
		{staged(GrowthStage{From: 366, Window: GrowthWeek, Cap: 50}), "", "stage_day", map[string]string{"got": "366"}, "a growth stage starts on a day from 0 to 365, not 366"},
		{staged(GrowthStage{From: 0, Window: "1h", Cap: 50}), "", "stage_window", map[string]string{"got": "1h"}, `a growth stage holds the "5h" or "7d" window, not "1h"`},
		{staged(GrowthStage{From: 0, Window: GrowthWeek, Cap: 100}), "", "stage_cap", map[string]string{"got": "100"}, "a growth stage holds its window at 1% to 99%, not 100%"},
		{staged(GrowthStage{From: 3, Window: GrowthWeek, Cap: 50}, GrowthStage{From: 3, Window: GrowthWeek, Cap: 60}), "", "stage_twice", map[string]string{"window": "7d", "day": "3"}, "two growth stages hold the 7d window from day 3"},
		{[]GrowthPolicy{ramp(GrowthGentle, "Mine", 60, 14)}, "", "builtin", map[string]string{"id": "gentle"}, `the built-in growth policy "gentle" can't be changed`},
		{[]GrowthPolicy{ramp("a b", "R", 60, 14)}, "", "id", map[string]string{"got": "a b"}, `a growth policy's id is up to 64 letters, digits, - and _, not "a b"`},
		{[]GrowthPolicy{ramp("r", "R", 60, 14), ramp("r", "Q", 60, 14)}, "", "id_twice", map[string]string{"id": "r"}, `two growth policies have the id "r"`},
		{[]GrowthPolicy{ramp("r", " ", 60, 14)}, "", "name_needed", map[string]string{"id": "r"}, `the growth policy "r" needs a name`},
		{[]GrowthPolicy{ramp("r", strings.Repeat("名", 65), 60, 14)}, "", "name_long", map[string]string{"name": strings.Repeat("名", 65)}, "a growth policy's name is up to 64 characters"},
		{[]GrowthPolicy{ramp("r", "Gentle", 60, 14)}, "", "name_twice", map[string]string{"name": "Gentle"}, `two growth policies are named "Gentle"`},
		{[]GrowthPolicy{ramp("r", "R", 60, 14)}, "gone", "default_gone", map[string]string{"id": "gone"}, `no growth policy has the id "gone"`},
		{[]GrowthPolicy{{ID: "r", Name: "R", Kind: GrowthRamp, Start: 60, Days: 14, CountDays: "hours"}}, "", "count_days", map[string]string{"got": "hours"}, `a growth policy counts calendar days ("") or the days used ("used"), not "hours"`},
		{[]GrowthPolicy{ramp("r", "R", 0, 14)}, "", "ramp_start", map[string]string{"got": "0"}, "a growth ramp starts at 1% to 99%, not 0%"},
		{[]GrowthPolicy{ramp("r", "R", 60, 366)}, "", "ramp_days", map[string]string{"got": "366"}, "a growth ramp reaches 100% in 1 to 365 days, not 366"},
		{[]GrowthPolicy{{ID: "r", Name: "R", Kind: GrowthOff}}, "", "kind", map[string]string{"got": "off"}, `a growth policy ramps ("ramp") or has stages ("stages"), not "off"`},
	} {
		err := CheckGrowthPolicies(c.own, c.def)
		var ge *GrowthError
		if !errors.As(err, &ge) {
			t.Errorf("%s: %v (%T), no code", c.code, err, err)
			continue
		}
		seen[ge.Code] = true
		if ge.Code != c.code || ge.Error() != c.says {
			t.Errorf("%s: %q %q", c.code, ge.Code, ge.Error())
		}
		if !maps.Equal(ge.Vals, c.vals) {
			t.Errorf("%s: values %v, want %v", c.code, ge.Vals, c.vals)
		}
	}
	// settings.json that can't be read: a folder in its place
	growthHome(t)
	if err := os.MkdirAll(Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	lastgood.Reset()
	err := EditGrowth(func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error) {
		t.Fatal("edited settings nobody knows")
		return own, def, nil
	})
	var ge *GrowthError
	if !errors.As(err, &ge) || ge.Code != GrowthUnreadable || ge.Vals["path"] != Path() || ge.Vals["why"] == "" || errors.Unwrap(err) == nil {
		t.Fatalf("unreadable: %v %+v", err, ge)
	}
	seen[ge.Code] = true

	for _, code := range GrowthCodes {
		if !seen[code] {
			t.Errorf("no refusal has the code %q", code)
		}
	}
	if got := slices.Sorted(maps.Keys(seen)); len(got) != len(GrowthCodes) {
		t.Errorf("codes %v, GrowthCodes %v", got, GrowthCodes)
	}
}
