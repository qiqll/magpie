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
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
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

// GrowthStagesBefore are the stages a user who set none was held by
// before policies were kept (T-129; Standard since, 需求方 2026-11-10):
// the 5 hours at 50% to day 7, at 90% to day 14 and at 95% after; the week
// at 80% from day 3 on.
func GrowthStagesBefore() []GrowthStage {
	return []GrowthStage{
		{From: 0, Window: GrowthFiveHours, Cap: 50},
		{From: 3, Window: GrowthWeek, Cap: 80},
		{From: 7, Window: GrowthFiveHours, Cap: 90},
		{From: 14, Window: GrowthFiveHours, Cap: 95},
	}
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

// The growth policies (T-129): the user keeps policies of their own
// (GrowthPolicies) beside magpie's built-in ones (BuiltinGrowth), and one
// of them is the default (GrowthDefault), the one every account is held
// by. A policy ramps (GrowthRamp): each window of an account is held at
// Start% on its day 0, rising day by day to 100% on day Days, the 5 hours
// and the week alike; or it is stages (GrowthStaged), read as Growth's
// stages always were (growthCaps, T-128); or it holds nothing
// (GrowthOff). A user who set nothing is held by GrowthStandard (需求方
// 2026-11-10); one who set stages of their own before policies were kept
// (Growth) has them as a stages policy of their own, the default
// (MigrateGrowth), so they are held day by day as before.

// The kinds of growth policy.
const (
	GrowthRamp   = "ramp"
	GrowthStaged = "stages"
	GrowthOff    = "off"
)

// The built-in policies' ids, which no policy of the user's takes.
const (
	GrowthStandard = "standard"
	GrowthGentle   = "gentle"
	GrowthCautious = "cautious"
	GrowthNone     = "off"
)

// GrowthMine is the id the stages a user set before policies were kept
// are moved to (MigrateGrowth), and that Settings › Growth policy's
// stages are saved to while its page edits stages alone.
const GrowthMine = "mine"

// GrowthNameMost is the longest a policy's id or name is, in characters.
const GrowthNameMost = 64

// GrowthPolicy is a growth policy: a ramp from Start% on day 0 to 100% on
// day Days, or stages, or none. Builtin is set on magpie's own, which are
// never saved and can't be changed or removed.
type GrowthPolicy struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Kind    string        `json:"kind"`
	Start   int           `json:"start,omitempty"`
	Days    int           `json:"days,omitempty"`
	Stages  []GrowthStage `json:"stages,omitempty"`
	Builtin bool          `json:"builtin,omitempty"`
}

// BuiltinGrowth are magpie's own policies, read only: Standard (50% to
// 100% in 14 days), Gentle (70% in 7), Cautious (30% in 30) and Off.
func BuiltinGrowth() []GrowthPolicy {
	return []GrowthPolicy{
		builtinGrowth(GrowthStandard), builtinGrowth(GrowthGentle),
		builtinGrowth(GrowthCautious), builtinGrowth(GrowthNone),
	}
}

// builtinGrowth is the built-in policy id, or no policy (no ID). It
// makes nothing on the heap, as withGrowth asks on every request.
func builtinGrowth(id string) GrowthPolicy {
	switch id {
	case GrowthStandard:
		return GrowthPolicy{ID: id, Name: "Standard", Kind: GrowthRamp, Start: 50, Days: 14, Builtin: true}
	case GrowthGentle:
		return GrowthPolicy{ID: id, Name: "Gentle", Kind: GrowthRamp, Start: 70, Days: 7, Builtin: true}
	case GrowthCautious:
		return GrowthPolicy{ID: id, Name: "Cautious", Kind: GrowthRamp, Start: 30, Days: 30, Builtin: true}
	case GrowthNone:
		return GrowthPolicy{ID: id, Name: "Off", Kind: GrowthOff, Builtin: true}
	}
	return GrowthPolicy{}
}

// GrowthInEffect is the policy accounts are held by: the one the user made
// the default (GrowthDefault), their own before a built-in of the same id
// a later magpie may add; their stages from before policies were kept
// (Growth) while they named none; Standard otherwise, and when the one
// named is gone. Its stages are read only: withGrowth asks it on every
// request, of Shared's settings. A settings.json holding what CheckGrowthPolicies
// refuses (a kind a later magpie wrote, a default gone) is read, never
// refused: Save doesn't check the policies, so it never blocks another
// setting's save or a restore; EditGrowth does.
func (s Settings) GrowthInEffect() GrowthPolicy {
	if s.GrowthDefault == "" && s.Growth != nil {
		// stages from before policies were kept, as MigrateGrowth makes
		// them the default
		id, name := migratedID(s.GrowthPolicies)
		return GrowthPolicy{ID: id, Name: name, Kind: GrowthStaged, Stages: *s.Growth}
	}
	if s.GrowthDefault != "" {
		for _, p := range s.GrowthPolicies {
			if p.ID == s.GrowthDefault {
				return p
			}
		}
		if p := builtinGrowth(s.GrowthDefault); p.ID != "" {
			return p
		}
	}
	return builtinGrowth(GrowthStandard)
}

// GrowthOf is the policy a subscription that picked id is held by (T-131):
// the user's own of that id, before a built-in one a later magpie may
// add; the built-in one; the default (GrowthInEffect) for "" (follow the
// default) and for an id no policy has, one deleted since it was picked or
// written by another magpie. withGrowth asks it on every request: it makes
// nothing on the heap.
func (s Settings) GrowthOf(id string) GrowthPolicy {
	if p, ok := s.growthPicked(id); ok {
		return p
	}
	if s.Growth != nil && id != "" {
		// the stages from before policies were kept, picked as
		// GrowthPoliciesAll lists them before MigrateGrowth has run
		if mine, name := migratedID(s.GrowthPolicies); id == mine {
			return GrowthPolicy{ID: mine, Name: name, Kind: GrowthStaged, Stages: *s.Growth}
		}
	}
	return s.GrowthInEffect()
}

// GrowthKnows says a policy has the id, so a subscription can pick it: a
// built-in one, the user's own, or their stages from before policies were
// kept (as GrowthPoliciesAll lists them).
func (s Settings) GrowthKnows(id string) bool {
	if _, ok := s.growthPicked(id); ok {
		return true
	}
	if s.Growth != nil {
		mine, _ := migratedID(s.GrowthPolicies)
		return id == mine
	}
	return false
}

func (s Settings) growthPicked(id string) (GrowthPolicy, bool) {
	if id == "" {
		return GrowthPolicy{}, false
	}
	for _, p := range s.GrowthPolicies {
		if p.ID == id {
			return p, true
		}
	}
	if p := builtinGrowth(id); p.ID != "" {
		return p, true
	}
	return GrowthPolicy{}, false
}

// GrowthPoliciesAll are the built-in policies, then the user's own, as
// MigrateGrowth would keep them. The list is the caller's own.
func (s Settings) GrowthPoliciesAll() []GrowthPolicy {
	m := s
	m.GrowthPolicies = slices.Clone(s.GrowthPolicies)
	MigrateGrowth(&m)
	out := BuiltinGrowth()
	for _, p := range m.GrowthPolicies {
		p.Stages = slices.Clone(p.Stages)
		out = append(out, p)
	}
	return out
}

// migratedName is the name the stages from before policies were kept are
// given; the user can rename it.
const migratedName = "My policy (migrated)"

func migratedGrowth(stages []GrowthStage, id, name string) GrowthPolicy {
	st := slices.Clone(stages)
	if st == nil {
		st = []GrowthStage{} // none on purpose: holds nothing, as before
	}
	return GrowthPolicy{ID: id, Name: name, Kind: GrowthStaged, Stages: st}
}

// MigrateGrowth moves the stages s set before policies were kept (Growth)
// to a stages policy of the user's own, which becomes the default unless
// another is already: each account is held day by day as it was. Growth
// is cleared. false when there was nothing to move. s.GrowthPolicies is
// appended to: a caller holding a shared list clones it first.
func MigrateGrowth(s *Settings) bool {
	if s.Growth == nil {
		return false
	}
	id, name := migratedID(s.GrowthPolicies)
	s.GrowthPolicies = append(s.GrowthPolicies, migratedGrowth(*s.Growth, id, name))
	if s.GrowthDefault == "" {
		s.GrowthDefault = id
	}
	s.Growth = nil
	return true
}

// migratedID is the id and name the stages from before policies were
// kept are moved to: GrowthMine and migratedName, or the first of
// "mine-2", "mine-3"… (and "… 2") no policy of own or magpie's has.
func migratedID(own []GrowthPolicy) (string, string) {
	if !growthTaken(own, GrowthMine, migratedName) {
		return GrowthMine, migratedName // made nothing on the heap
	}
	for n := 2; ; n++ {
		id, name := fmt.Sprintf("%s-%d", GrowthMine, n), fmt.Sprintf("%s %d", migratedName, n)
		if !growthTaken(own, id, name) {
			return id, name
		}
	}
}

// growthTaken says a policy of own, or a built-in one, has the id or the
// name (in any case).
func growthTaken(own []GrowthPolicy, id, name string) bool {
	if builtinGrowth(id).ID != "" {
		return true
	}
	for _, p := range own {
		if p.ID == id || strings.EqualFold(strings.TrimSpace(p.Name), name) {
			return true
		}
	}
	return false
}

// growthIDChars are the characters a policy's id is made of.
var growthIDChars = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// CheckGrowthPolicies says what is wrong with the user's own policies and
// the default named, nil when nothing is: each has an id of letters,
// digits, - and _ no built-in one has, and a name, neither longer than
// GrowthNameMost nor had by another policy (a name in any case); a ramp
// starts at 1–99% and reaches 100% in 1–GrowthDaysMost days; stages pass
// CheckGrowth; none is built in. The default is "" or a policy's id.
func CheckGrowthPolicies(own []GrowthPolicy, def string) error {
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, b := range BuiltinGrowth() {
		ids[b.ID] = true
		names[strings.ToLower(b.Name)] = true
	}
	for _, p := range own {
		if builtinGrowth(p.ID).ID != "" {
			return fmt.Errorf("the built-in growth policy %q can't be changed", p.ID)
		}
		if !growthIDChars.MatchString(p.ID) || utf8.RuneCountInString(p.ID) > GrowthNameMost {
			return fmt.Errorf("a growth policy's id is up to %d letters, digits, - and _, not %q", GrowthNameMost, p.ID)
		}
		if ids[p.ID] {
			return fmt.Errorf("two growth policies have the id %q", p.ID)
		}
		ids[p.ID] = true
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return fmt.Errorf("the growth policy %q needs a name", p.ID)
		}
		if utf8.RuneCountInString(name) > GrowthNameMost {
			return fmt.Errorf("a growth policy's name is up to %d characters", GrowthNameMost)
		}
		if names[strings.ToLower(name)] {
			return fmt.Errorf("two growth policies are named %q", name)
		}
		names[strings.ToLower(name)] = true
		if err := CheckGrowthShape(p); err != nil {
			return err
		}
	}
	if def != "" && !ids[def] {
		return fmt.Errorf("no growth policy has the id %q", def)
	}
	return nil
}

// CheckGrowthShape says what is wrong with how p holds an account, nil
// when nothing is: a ramp starts at 1–99% and reaches 100% in
// 1–GrowthDaysMost days, and stages pass CheckGrowth. A user's policy is
// one or the other; Off is magpie's alone.
func CheckGrowthShape(p GrowthPolicy) error {
	switch p.Kind {
	case GrowthRamp:
		if p.Start < 1 || p.Start > 99 {
			return fmt.Errorf("a growth ramp starts at 1%% to 99%%, not %d%%", p.Start)
		}
		if p.Days < 1 || p.Days > GrowthDaysMost {
			return fmt.Errorf("a growth ramp reaches 100%% in 1 to %d days, not %d", GrowthDaysMost, p.Days)
		}
	case GrowthStaged:
		return CheckGrowth(p.Stages)
	default:
		return fmt.Errorf("a growth policy ramps (%q) or has stages (%q), not %q", GrowthRamp, GrowthStaged, p.Kind)
	}
	return nil
}

// NormalGrowthPolicies is own as it is kept: names trimmed, and each
// with only what its kind reads (a ramp no stages, stages no ramp), never
// marked built in. The list is new.
func NormalGrowthPolicies(own []GrowthPolicy) []GrowthPolicy {
	var out []GrowthPolicy
	for _, p := range own {
		p.Name, p.Builtin = strings.TrimSpace(p.Name), false
		switch p.Kind {
		case GrowthRamp:
			p.Stages = nil
		case GrowthStaged:
			p.Start, p.Days = 0, 0
			p.Stages = slices.Clone(p.Stages)
			if p.Stages == nil {
				p.Stages = []GrowthStage{}
			}
		}
		out = append(out, p)
	}
	return out
}

// EditGrowth changes the user's own growth policies and the default alone,
// as edit returns them from what the file holds now, the stages from
// before policies were kept moved first (MigrateGrowth), under the
// settings file's lock. (A Settings page save carries the policies from
// the file as it read them before its own lock, as it carries every field
// it doesn't send.) A settings file that
// can't be read is left as it is, with the error: what it holds is
// unknown, never "no policies". edit is given a copy; what it returns is
// kept as NormalGrowthPolicies has it once CheckGrowthPolicies passes, and
// an error from either saves nothing.
func EditGrowth(edit func(own []GrowthPolicy, def string) ([]GrowthPolicy, string, error)) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	s.GrowthPolicies = slices.Clone(s.GrowthPolicies)
	MigrateGrowth(&s)
	own := NormalGrowthPolicies(s.GrowthPolicies)
	own, def, err := edit(own, s.GrowthDefault)
	if err != nil {
		return err
	}
	own = NormalGrowthPolicies(own)
	if err := CheckGrowthPolicies(own, def); err != nil {
		return err
	}
	s.GrowthPolicies, s.GrowthDefault = own, def
	return save(s, false)
}

// WithOwnStages is own and def with stages saved as the user's own stages
// policy, made the default: the default's, when it is a stages policy of
// the user's; GrowthMine's, when it has stages; a new one named as the
// migrated stages are (migratedID) otherwise. own is not written.
func WithOwnStages(own []GrowthPolicy, def string, stages []GrowthStage) ([]GrowthPolicy, string, error) {
	own = slices.Clone(own)
	at := slices.IndexFunc(own, func(p GrowthPolicy) bool { return p.ID == def && p.Kind == GrowthStaged })
	if at < 0 {
		at = slices.IndexFunc(own, func(p GrowthPolicy) bool { return p.ID == GrowthMine && p.Kind == GrowthStaged })
	}
	if at < 0 {
		id, name := migratedID(own)
		own = append(own, GrowthPolicy{ID: id, Name: name, Kind: GrowthStaged})
		at = len(own) - 1
	}
	own[at].Stages = slices.Clone(stages)
	return own, own[at].ID, nil
}
