package settings

// The growth policy (T-078): a subscription account added to magpie
// lately is used to a share of its windows that grows with the days since
// it was added, so a new account isn't run hard from its first day. A
// stage starts on a day (From) and holds one window (5 hours, or the week)
// at a share; stages that start on the same day are one stage. Each window
// of an account is held at the share of the latest stage its days have
// reached that lists that window: a window a later stage leaves out keeps
// the share it had (T-128; until then only the latest stage's windows were
// held, and the others let go). provider.CapsOf reads it.

import (
	"fmt"
	"slices"
)

// The windows a growth stage holds: the 5 hours, and the week (each of a
// vendor's 7-day windows, per model too).
const (
	GrowthFiveHours = "5h"
	GrowthWeek      = "7d"
)

// GrowthWindows are the windows a stage can hold.
var GrowthWindows = []string{GrowthFiveHours, GrowthWeek}

// GrowthDaysMost is the latest day a stage can start on.
const GrowthDaysMost = 365

// GrowthStage holds Window of an account at Cap percent (1–99) from day
// From after the account was added, until the next stage that holds the
// same window starts.
type GrowthStage struct {
	From   int    `json:"from"`
	Window string `json:"window"`
	Cap    int    `json:"cap"`
}

// DefaultGrowth is the policy of a user who set none: the 5 hours at 50%
// to day 7, at 90% to day 14 and at 95% after; the week at 80% from day 3
// on.
func DefaultGrowth() []GrowthStage {
	return []GrowthStage{
		{From: 0, Window: GrowthFiveHours, Cap: 50},
		{From: 3, Window: GrowthWeek, Cap: 80},
		{From: 7, Window: GrowthFiveHours, Cap: 90},
		{From: 14, Window: GrowthFiveHours, Cap: 95},
	}
}

// GrowthOf is the policy s sets: its own, or DefaultGrowth when it set
// none. The list is the caller's own.
func (s Settings) GrowthOf() []GrowthStage {
	if s.Growth == nil {
		return DefaultGrowth()
	}
	return slices.Clone(*s.Growth)
}

// CheckGrowth says what is wrong with a policy, nil when nothing is: each
// stage starts on a day from 0 to GrowthDaysMost, holds one of
// GrowthWindows at 1–99%, and no two hold the same window from one day.
func CheckGrowth(stages []GrowthStage) error {
	seen := map[GrowthStage]bool{}
	for _, g := range stages {
		if g.From < 0 || g.From > GrowthDaysMost {
			return fmt.Errorf("a growth stage starts on a day from 0 to %d, not %d", GrowthDaysMost, g.From)
		}
		if !slices.Contains(GrowthWindows, g.Window) {
			return fmt.Errorf("a growth stage holds the %q or %q window, not %q", GrowthFiveHours, GrowthWeek, g.Window)
		}
		if g.Cap < 1 || g.Cap > 99 {
			return fmt.Errorf("a growth stage holds its window at 1%% to 99%%, not %d%%", g.Cap)
		}
		k := GrowthStage{From: g.From, Window: g.Window}
		if seen[k] {
			return fmt.Errorf("two growth stages hold the %s window from day %d", g.Window, g.From)
		}
		seen[k] = true
	}
	return nil
}
