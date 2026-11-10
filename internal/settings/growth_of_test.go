package settings

import "testing"

// A subscription's pick (T-131) is the policy of that id: the user's own,
// a built-in one, or the stages from before policies were kept as the
// page lists them. "" follows the default, and an id no policy has (one
// deleted since, another magpie's) is held by the default too. Only a
// policy there is can be picked.
func TestGrowthOf(t *testing.T) {
	own := []GrowthPolicy{{ID: "slow", Name: "Slow", Kind: GrowthRamp, Start: 20, Days: 60}}
	s := Settings{GrowthPolicies: own, GrowthDefault: GrowthGentle}
	for id, want := range map[string]string{
		"":             GrowthGentle,
		GrowthCautious: GrowthCautious,
		GrowthNone:     GrowthNone,
		"slow":         "slow",
		"gone":         GrowthGentle,
		GrowthMine:     GrowthGentle, // no stages from before: no such policy
	} {
		if got := s.GrowthOf(id); got.ID != want {
			t.Errorf("picked %q: held by %q, want %q", id, got.ID, want)
		}
	}
	if got := s.GrowthOf("slow"); got.Start != 20 || got.Days != 60 {
		t.Errorf("the user's own read as %+v", got)
	}
	for id, want := range map[string]bool{"": false, "gone": false, GrowthMine: false, GrowthCautious: true, "slow": true} {
		if s.GrowthKnows(id) != want {
			t.Errorf("knows %q: %v", id, !want)
		}
	}

	// stages from before policies were kept, not moved yet, while another
	// policy is the default: picked by the id the page lists them under
	stages := []GrowthStage{{From: 0, Window: GrowthFiveHours, Cap: 40}}
	s = Settings{Growth: &stages, GrowthPolicies: own, GrowthDefault: "slow"}
	if got := s.GrowthOf(GrowthMine); got.ID != GrowthMine || got.Kind != GrowthStaged || len(got.Stages) != 1 || got.Stages[0].Cap != 40 {
		t.Errorf("the stages from before, picked: %+v", got)
	}
	if !s.GrowthKnows(GrowthMine) {
		t.Error("the stages from before can't be picked")
	}
	listed := false
	for _, p := range s.GrowthPoliciesAll() {
		listed = listed || p.ID == GrowthMine
	}
	if !listed {
		t.Error("the page doesn't list the stages from before under the id they are picked by")
	}
	if got := s.GrowthOf(""); got.ID != "slow" {
		t.Errorf("following the default: %q", got.ID)
	}
}

// An account's pick (T-133) comes before its subscription's, which comes
// before the default: each level taken away hands the account to the
// next. An account's pick that is gone falls to its subscription's.
func TestGrowthForAccountThenSubscriptionThenDefault(t *testing.T) {
	own := []GrowthPolicy{{ID: "slow", Name: "Slow", Kind: GrowthRamp, Start: 20, Days: 60}}
	s := Settings{GrowthPolicies: own, GrowthDefault: GrowthGentle}
	for _, c := range []struct{ account, sub, want string }{
		{"slow", GrowthCautious, "slow"},         // the account's own
		{"", GrowthCautious, GrowthCautious},     // none: the subscription's
		{"", "", GrowthGentle},                   // neither: the default
		{"gone", GrowthCautious, GrowthCautious}, // the account's gone: the subscription's
		{"gone", "", GrowthGentle},               // both gone or none: the default
		{GrowthNone, GrowthCautious, GrowthNone}, // Off picked for the account alone
		{GrowthCautious, GrowthNone, GrowthCautious},
	} {
		if got := s.GrowthFor(c.account, c.sub); got.ID != c.want {
			t.Errorf("account %q, subscription %q: held by %q, want %q", c.account, c.sub, got.ID, c.want)
		}
	}
}
