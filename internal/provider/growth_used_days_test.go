package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// usedDaysT0 is the account's first use in these tests: half an hour
// before midnight in UTC, so a day counted by any clock's date and not
// from the first use is seen.
var usedDaysT0 = time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC)

// usedDaysFixture is codex's new@ added an hour before usedDaysT0, the
// ledger read for first uses, with two policies of the user's alike but
// for how they count days: "use" the days used, "cal" the calendar's; the
// one named is the default.
func usedDaysFixture(t *testing.T, def string) {
	t.Helper()
	firstUsedFixture(t, time.Hour)
	markRead(t)
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.Providers {
		if f.Providers[i].ID == "codex" {
			f.Providers[i].AccountAdded["new@example.com"] = usedDaysT0.Add(-time.Hour)
		}
	}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	growthDefault(t, def,
		settings.GrowthPolicy{ID: "use", Name: "By use", Kind: settings.GrowthRamp, Start: 10, Days: 20, CountDays: settings.GrowthUsedDays},
		settings.GrowthPolicy{ID: "cal", Name: "By calendar", Kind: settings.GrowthRamp, Start: 10, Days: 20})
}

// day is a time on the account's day d (from usedDaysT0), h hours in.
func day(d int, h time.Duration) time.Time {
	return usedDaysT0.Add(time.Duration(d)*24*time.Hour + h)
}

// grownAt is the account's day and the share of its five hours, by its
// row (AccountGrowthOf) and by the gateway's hold (accountCapsAt) at now,
// which must agree.
func grownAt(t *testing.T, now time.Time) (*AccountGrowth, int) {
	t.Helper()
	g := AccountGrowthOf("codex", "new@example.com", now)
	if g == nil {
		t.Fatalf("at %v: not held", now)
	}
	s, _ := accountCapsAt("codex", "new@example.com", now).Of("5 hours")
	if s != g.FiveHours {
		t.Fatalf("at %v: the gateway holds the five hours at %d%%, the row says %d%%", now, s, g.FiveHours)
	}
	return g, s
}

// Used 3 days, idle 5, used 1 more (the acceptance's account): on the
// calendar it is on its day 8 (its 9th), by use on its day 3 (its 4th),
// the day before it is used and after, and the next day, used or not,
// day 4. A day is the 24 hours from its first use, whatever the clock's
// zone (run under TZ=UTC and TZ=Pacific/Kiritimati). Each day is written
// once, by its first request, never by a later one of the day or one of
// a day before.
func TestGrowthUsedDaysAgainstCalendar(t *testing.T) {
	usedDaysFixture(t, "use")
	var writes atomic.Int32
	was := beforeUsedDayWrite
	beforeUsedDayWrite = func() { writes.Add(1) }
	t.Cleanup(func() { beforeUsedDayWrite = was })
	for _, at := range []time.Time{
		day(0, 0), day(0, 2*time.Hour), // past midnight in UTC: still its first day
		day(1, time.Hour), day(1, 5*time.Hour), day(1, 20*time.Hour),
		day(2, 3*time.Hour), day(2, 4*time.Hour),
		day(1, 6*time.Hour), // a request of a day before, ended late
	} {
		NoteAccountUsed("codex", "new@example.com", at)
	}
	if n := writes.Load(); n != 2 {
		t.Fatalf("days 1 and 2 written %d times, want 2", n)
	}
	// idle from day 3 to day 7
	check := func(now time.Time, wantUse, wantCal int) {
		t.Helper()
		growthDefault(t, "use", settings.GrowthPolicy{ID: "use", Name: "By use", Kind: settings.GrowthRamp, Start: 10, Days: 20, CountDays: settings.GrowthUsedDays},
			settings.GrowthPolicy{ID: "cal", Name: "By calendar", Kind: settings.GrowthRamp, Start: 10, Days: 20})
		g, s := grownAt(t, now)
		if g.Day != wantUse || !g.UsedDays || g.UsedDaysUnknown || s != 10+90*wantUse/20 {
			t.Errorf("by use at %v: day %d (%d%%, %+v), want day %d", now, g.Day, s, g, wantUse)
		}
		// the policy page's chart marks it on the same day
		a := GrowthAccountsOf("use", now)
		if i := slices.IndexFunc(a, func(a GrowthAccount) bool { return a.Account == "new@example.com" }); i < 0 || a[i].Day != wantUse {
			t.Errorf("by use at %v: the chart marks %+v, want day %d", now, a, wantUse)
		}
		growthDefault(t, "cal", settings.GrowthPolicy{ID: "use", Name: "By use", Kind: settings.GrowthRamp, Start: 10, Days: 20, CountDays: settings.GrowthUsedDays},
			settings.GrowthPolicy{ID: "cal", Name: "By calendar", Kind: settings.GrowthRamp, Start: 10, Days: 20})
		g, s = grownAt(t, now)
		if g.Day != wantCal || g.UsedDays || s != 10+90*wantCal/20 {
			t.Errorf("on the calendar at %v: day %d (%d%%, %+v), want day %d", now, g.Day, s, g, wantCal)
		}
	}
	check(day(2, 23*time.Hour), 2, 2)
	check(day(5, 0), 3, 5)
	check(day(8, 10*time.Minute), 3, 8) // before its day-8 request
	NoteAccountUsed("codex", "new@example.com", day(8, 30*time.Minute))
	NoteAccountUsed("codex", "new@example.com", day(8, 2*time.Hour))
	if n := writes.Load(); n != 3 {
		t.Fatalf("day 8 written %d times in all, want 3", n)
	}
	check(day(8, 40*time.Minute), 3, 8)
	check(day(9, time.Hour), 4, 9) // its next day, unused so far
	s, _ := storedPicks("codex")
	if c := s.AccountUsedDays["new@example.com"]; c.Count != 4 || !c.From.Equal(usedDaysT0) || !c.Last.Equal(day(8, 0)) {
		t.Fatalf("kept as %+v", c)
	}
}

// A count of days used that isn't known (an account first used before it
// was kept, or one counted against another first use a sync brought) is
// the calendar's, never none: its request writes nothing, and the
// ledger's read at a start counts it. A ledger that can't be read counts
// nothing and says so in the log.
func TestGrowthUsedDaysUnknownIsCalendar(t *testing.T) {
	usedDaysFixture(t, "use")
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	forget := func(c *UsedDays) {
		t.Helper()
		if err := editStored("codex", func(q *Provider) bool {
			if c == nil {
				q.AccountUsedDays = nil
			} else {
				q.AccountUsedDays = map[string]UsedDays{"new@example.com": *c}
			}
			return true
		}); err != nil {
			t.Fatal(err)
		}
	}
	forget(nil)
	if g, _ := grownAt(t, day(8, time.Hour)); g.Day != 8 || g.UsedDays || !g.UsedDaysUnknown {
		t.Fatalf("not counted: the calendar's day 8, got %+v", g)
	}
	NoteAccountUsed("codex", "new@example.com", day(8, 2*time.Hour))
	if s, _ := storedPicks("codex"); len(s.AccountUsedDays) != 0 {
		t.Fatalf("a request counted on an unknown count: %+v", s.AccountUsedDays)
	}

	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prev) })
	BackfillUsedDays(func(map[[2]string]time.Time) (map[[2]string]UsedDays, error) {
		return nil, errors.New("usage.jsonl: permission denied")
	})
	if g, _ := grownAt(t, day(8, 3*time.Hour)); g.Day != 8 || g.UsedDays {
		t.Fatalf("an unreadable ledger: the calendar's day 8, got %+v", g)
	}
	if !strings.Contains(logged.String(), "permission denied") || !strings.Contains(logged.String(), "calendar") {
		t.Fatalf("an unreadable ledger not logged: %q", logged.String())
	}

	var asked map[[2]string]time.Time
	BackfillUsedDays(func(want map[[2]string]time.Time) (map[[2]string]UsedDays, error) {
		asked = want
		return map[[2]string]UsedDays{{"codex", "new@example.com"}: {From: usedDaysT0, Last: day(8, 0), Count: 4}}, nil
	})
	if at, ok := asked[[2]string{"codex", "new@example.com"}]; !ok || !at.Equal(usedDaysT0) || len(asked) != 1 {
		t.Fatalf("asked the ledger for %v", asked)
	}
	if g, _ := grownAt(t, day(8, 4*time.Hour)); g.Day != 3 || !g.UsedDays || g.UsedDaysUnknown {
		t.Fatalf("counted from the ledger: day 3, got %+v", g)
	}
	// a count known isn't asked again
	BackfillUsedDays(func(want map[[2]string]time.Time) (map[[2]string]UsedDays, error) {
		t.Errorf("asked again for %v", want)
		return nil, nil
	})

	// one against another first use (an earlier one a sync brought) is
	// not known either; a ledger that doesn't reach back to its first use
	// (usage.ServedDays leaves it out) leaves it unknown, never one day
	forget(&UsedDays{From: usedDaysT0.Add(time.Hour), Last: day(8, time.Hour), Count: 6})
	if g, _ := grownAt(t, day(8, 5*time.Hour)); g.Day != 8 || g.UsedDays || !g.UsedDaysUnknown {
		t.Fatalf("counted against another first use: the calendar's day 8, got %+v", g)
	}
	logged.Reset()
	BackfillUsedDays(func(map[[2]string]time.Time) (map[[2]string]UsedDays, error) {
		return map[[2]string]UsedDays{}, nil
	})
	if g, _ := grownAt(t, day(8, 6*time.Hour)); g.Day != 8 || g.UsedDays || !g.UsedDaysUnknown {
		t.Fatalf("the ledger without its first day: the calendar's day 8, got %+v", g)
	}
	if !strings.Contains(logged.String(), "doesn't reach back") {
		t.Fatalf("not logged: %q", logged.String())
	}
}

// The editor's Save keeps the days used whatever it sends; a Copilot
// rename (moveAccountSettings) moves them; a sync keeps the count against
// the first use it keeps, the larger.
func TestGrowthUsedDaysKeptBySaveMoveAndSync(t *testing.T) {
	usedDaysFixture(t, "use")
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	NoteAccountUsed("codex", "new@example.com", day(1, 0))
	want := UsedDaysOf(usedDaysT0, []int{0, 1}) // days 0 and 1, named (T-148)
	days := func() map[string]UsedDays {
		s, _ := storedPicks("codex")
		return s.AccountUsedDays
	}
	p, _ := storedPicks("codex")
	for _, sent := range []map[string]UsedDays{{"new@example.com": {From: usedDaysT0, Last: day(9, 0), Count: 9}}, nil} {
		q := p
		q.AccountUsedDays = sent
		if err := Save(q); err != nil {
			t.Fatal(err)
		}
		if got := days()["new@example.com"]; got != want {
			t.Fatalf("Save with %v wrote %+v", sent, got)
		}
	}
	if err := moveAccountSettings("codex", map[string]string{"new@example.com": "Renamed@example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := days(); got["renamed@example.com"] != want || len(got) != 1 {
		t.Fatalf("moved: %+v", got)
	}
	if err := moveAccountSettings("codex", map[string]string{"renamed@example.com": "new@example.com"}); err != nil {
		t.Fatal(err)
	}

	// the other computer used it on fewer days: here's count stays
	f, _ := read()
	there := slices.Clone(f.Providers)
	for i := range there {
		if there[i].ID == "codex" {
			there[i].AccountUsedDays = map[string]UsedDays{"new@example.com": {From: usedDaysT0, Last: usedDaysT0, Count: 1}}
		}
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if got := days()["new@example.com"]; got != want {
		t.Fatalf("after a sync with fewer days: %+v", got)
	}
	// and on four days from the same first use: its count is taken
	for i := range there {
		if there[i].ID == "codex" {
			there[i].AccountUsedDays = map[string]UsedDays{"new@example.com": {From: usedDaysT0, Last: day(5, 0), Count: 4}}
		}
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if got := days()["new@example.com"]; got.Count != 4 || !got.Last.Equal(day(5, 0)) {
		t.Fatalf("after a sync with more days: %+v", got)
	}
	for i := range there {
		if there[i].ID == "codex" {
			there[i].AccountFirstUsed = map[string]time.Time{"new@example.com": usedDaysT0.Add(-time.Hour)}
			there[i].AccountUsedDays = map[string]UsedDays{"new@example.com": {From: usedDaysT0.Add(-time.Hour), Count: 1, Last: usedDaysT0.Add(-time.Hour)}}
		}
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	// the earlier first use is kept, and its count with it
	if got := days()["new@example.com"]; got.Count != 1 || !got.From.Equal(usedDaysT0.Add(-time.Hour)) {
		t.Fatalf("after a sync with an earlier first use: %+v", got)
	}
}

// An account the gateway doesn't serve counts calendar days from when it
// was added whatever its policy counts, and its row says so.
func TestGrowthUsedDaysNeedTheGateway(t *testing.T) {
	usedDaysFixture(t, "use")
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	if err := SetSwitchOnly("codex", true); err != nil {
		t.Fatal(err)
	}
	g, _ := grownAt(t, day(4, 0))
	if g.Day != 4 || g.UsedDays || !g.ByAdded || !g.CalendarOnly {
		t.Fatalf("switch-only under a policy by use: %+v", g)
	}
	growthDefault(t, "cal", settings.GrowthPolicy{ID: "cal", Name: "By calendar", Kind: settings.GrowthRamp, Start: 10, Days: 20})
	if g, _ := grownAt(t, day(4, 0)); g.CalendarOnly {
		t.Fatalf("a calendar policy isn't calendar only: %+v", g)
	}
}

// The days used go with the account: removed, it is new; renamed, they
// move; a sync keeps the count against the first use kept, the larger.
func TestGrowthUsedDaysGoWithTheAccount(t *testing.T) {
	usedDaysFixture(t, "use")
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	NoteAccountUsed("codex", "new@example.com", day(1, 0))
	noteAdded(nil, map[loginAccount]loginAccount{{"codex", "new@example.com"}: {"codex", "renamed@example.com"}}, nil)
	s, _ := storedPicks("codex")
	if c := s.AccountUsedDays["renamed@example.com"]; c.Count != 2 || len(s.AccountUsedDays) != 1 {
		t.Fatalf("renamed: %+v", s.AccountUsedDays)
	}
	forgetAdded("codex", "renamed@example.com")
	if s, _ := storedPicks("codex"); len(s.AccountUsedDays) != 0 {
		t.Fatalf("removed, its days kept: %+v", s.AccountUsedDays)
	}

	fu := map[string]time.Time{"a": usedDaysT0, "b": usedDaysT0}
	here := map[string]UsedDays{"a": {From: usedDaysT0, Last: day(3, 0), Count: 3}, "b": {From: usedDaysT0.Add(-time.Hour), Count: 9}}
	there := map[string]UsedDays{"a": {From: usedDaysT0, Last: day(5, 0), Count: 5}, "b": {From: usedDaysT0, Last: day(1, 0), Count: 2}, "c": {From: usedDaysT0, Count: 1}}
	got := keptUsedDays(here, there, fu)
	if got["a"].Count != 5 || got["b"].Count != 2 || len(got) != 2 {
		t.Fatalf("kept through a sync: %+v", got)
	}
}

// A day used starts 24 hours on from the first use, before it for a time
// before it; the days used never start before the first use.
func TestUsedDay(t *testing.T) {
	for _, c := range []struct {
		at   time.Time
		want time.Time
	}{
		{usedDaysT0, usedDaysT0},
		{day(0, 23*time.Hour), usedDaysT0},
		{day(3, 0), day(3, 0)},
		{day(3, time.Minute), day(3, 0)},
		{usedDaysT0.Add(-time.Minute), day(-1, 0)},
		{day(-1, 0), day(-1, 0)},
	} {
		if got := usedDay(usedDaysT0, c.at); !got.Equal(c.want) {
			t.Errorf("usedDay(%v) = %v, want %v", c.at, got, c.want)
		}
	}
	c := UsedDays{From: usedDaysT0, Last: day(5, 0), Count: 3}
	if got := usedFrom(c, day(2, 0)); !got.Equal(usedDaysT0) {
		t.Errorf("a clock set back past a day counted: from %v, want the first use", got)
	}
}

// Magpie's own ramps count the days used (需求方 2026-11-11), so a user
// who set nothing, or picked Standard, Gentle or Cautious, has an idle
// account stay on its day: used on days 0 to 2, idle 3 to 7, used on day 8,
// on each day d it is on the day of the days it was used on before d, its
// share that day's of the built-in's ramp, by its row and by the
// gateway's hold. On the calendar (each built-in until then) it would be on
// day d. Run under TZ=UTC and TZ=Pacific/Kiritimati.
func TestGrowthBuiltinsCountDaysUsed(t *testing.T) {
	ramps := map[string][2]int{settings.GrowthStandard: {50, 14}, settings.GrowthGentle: {70, 7}, settings.GrowthCautious: {30, 30}}
	for _, id := range []string{"", settings.GrowthStandard, settings.GrowthGentle, settings.GrowthCautious} {
		name := id
		if id == "" {
			name, id = "nothing set", settings.GrowthStandard
		}
		t.Run(name, func(t *testing.T) {
			usedDaysFixture(t, "")
			if name != "nothing set" {
				growthDefault(t, id)
			}
			r := ramps[id]
			usedBefore := 0
			for d := 0; d <= 10; d++ {
				used := d <= 2 || d == 8
				if used {
					NoteAccountUsed("codex", "new@example.com", day(d, time.Hour))
				}
				g, s := grownAt(t, day(d, 2*time.Hour))
				if g.Policy != id || g.Day != usedBefore || !g.UsedDays || g.UsedDaysUnknown || s != r[0]+(100-r[0])*usedBefore/r[1] {
					t.Errorf("day %d: on day %d at %d%% (%+v), want day %d at %d%%", d, g.Day, s, g, usedBefore, r[0]+(100-r[0])*usedBefore/r[1])
				}
				if used {
					usedBefore++
				}
			}
		})
	}
}

// Under the built-in default (days used), an account the gateway doesn't
// serve counts calendar days and its row says so, and a served one whose
// count isn't known yet counts calendar days and says that.
func TestGrowthBuiltinDefaultCalendarCases(t *testing.T) {
	t.Run("not served", func(t *testing.T) {
		usedDaysFixture(t, "")
		NoteAccountUsed("codex", "new@example.com", day(0, 0))
		if err := SetSwitchOnly("codex", true); err != nil {
			t.Fatal(err)
		}
		g, _ := grownAt(t, day(4, 0))
		if g.Policy != settings.GrowthStandard || g.Day != 4 || g.UsedDays || !g.ByAdded || !g.CalendarOnly {
			t.Fatalf("switch-only under the default: %+v", g)
		}
	})
	t.Run("count unknown", func(t *testing.T) {
		usedDaysFixture(t, "")
		NoteAccountUsed("codex", "new@example.com", day(0, 0))
		if err := editStored("codex", func(q *Provider) bool {
			q.AccountUsedDays = nil
			return true
		}); err != nil {
			t.Fatal(err)
		}
		g, _ := grownAt(t, day(4, 0))
		if g.Policy != settings.GrowthStandard || g.Day != 4 || g.UsedDays || !g.UsedDaysUnknown {
			t.Fatalf("a count not known under the default: %+v", g)
		}
	})
}

// Two computers that each served the account on days of their own count
// both's days after a sync, each once (T-148): here days 0 to 2, there 0,
// 5 and 6, so 5 days, not the larger 3. A sync again counts no day twice,
// and the account is on the day of the days used in all.
func TestGrowthUsedDaysSyncTakesTheUnion(t *testing.T) {
	usedDaysFixture(t, "use")
	for d := range 3 {
		NoteAccountUsed("codex", "new@example.com", day(d, 0))
	}
	f, _ := read()
	there := slices.Clone(f.Providers)
	for i := range there {
		if there[i].ID == "codex" {
			there[i].AccountUsedDays = map[string]UsedDays{"new@example.com": UsedDaysOf(usedDaysT0, []int{0, 5, 6})}
		}
	}
	want := UsedDaysOf(usedDaysT0, []int{0, 1, 2, 5, 6})
	for range 2 {
		if err := Mirror(there, nil); err != nil {
			t.Fatal(err)
		}
		s, _ := storedPicks("codex")
		if got := s.AccountUsedDays["new@example.com"]; got != want {
			t.Fatalf("after a sync: %+v, want %+v", got, want)
		}
	}
	if g, _ := grownAt(t, day(7, time.Hour)); g.Day != 5 || !g.UsedDays {
		t.Fatalf("on day 7, used on 5 days before it: %+v", g)
	}
	// the other way, the same
	here := map[string]UsedDays{"new@example.com": UsedDaysOf(usedDaysT0, []int{0, 1, 2})}
	fu := map[string]time.Time{"new@example.com": usedDaysT0}
	if got := keptUsedDays(there[slices.IndexFunc(there, func(p Provider) bool { return p.ID == "codex" })].AccountUsedDays, here, fu)["new@example.com"]; got != want {
		t.Fatalf("there's first: %+v, want %+v", got, want)
	}
	// a day each counts on after is counted once more, named
	NoteAccountUsed("codex", "new@example.com", day(9, time.Hour))
	s, _ := storedPicks("codex")
	if got, want := s.AccountUsedDays["new@example.com"], UsedDaysOf(usedDaysT0, []int{0, 1, 2, 5, 6, 9}); got != want {
		t.Fatalf("day 9 counted: %+v, want %+v", got, want)
	}
}

// A count kept before the days were named ({from, last, count}, T-134)
// is still read, and merged: it names its first day and its last only,
// so beside one that names its days the union counts those it names and
// as many as the larger count at least, never more than either could
// have; a day counted on it after names its last too.
func TestGrowthUsedDaysOldCountMerges(t *testing.T) {
	var old map[string]UsedDays
	if err := json.Unmarshal([]byte(`{"a":{"from":"2026-03-01T23:30:00Z","last":"2026-03-07T23:30:00Z","count":3}}`), &old); err != nil {
		t.Fatal(err)
	}
	if c := old["a"]; c.Count != 3 || !c.Last.Equal(day(6, 0)) || c.Days != "" {
		t.Fatalf("read as %+v", c)
	}
	fu := map[string]time.Time{"a": usedDaysT0}
	for _, c := range []struct {
		name  string
		other UsedDays
		count int
		named []int
	}{
		// days 0, 1, 2 here; 0, 6 and one not known there: 4 known, 4 at least
		{"three named", UsedDaysOf(usedDaysT0, []int{0, 1, 2}), 4, []int{0, 1, 2, 6}},
		// day 6 named on both: the larger count, 3
		{"same last", UsedDaysOf(usedDaysT0, []int{0, 6}), 3, []int{0, 6}},
		// more named than the old count: all of them
		{"more named", UsedDaysOf(usedDaysT0, []int{0, 1, 2, 3, 4}), 6, []int{0, 1, 2, 3, 4, 6}},
		// two old counts: the larger, with both lasts named
		{"both old", UsedDays{From: usedDaysT0, Last: day(4, 0), Count: 5}, 5, []int{0, 4, 6}},
	} {
		for _, order := range [][2]map[string]UsedDays{{old, {"a": c.other}}, {{"a": c.other}, old}} {
			got := keptUsedDays(order[0], order[1], fu)["a"]
			named := UsedDaysOf(usedDaysT0, c.named)
			if got.Count != c.count || got.Days != named.Days || !got.Last.Equal(named.Last) {
				t.Errorf("%s: %+v, want %d days naming %v", c.name, got, c.count, c.named)
			}
		}
	}

	// an old count a request counts on: its last stays named
	usedDaysFixture(t, "use")
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	if err := editStored("codex", func(q *Provider) bool {
		q.AccountUsedDays = map[string]UsedDays{"new@example.com": old["a"]}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	NoteAccountUsed("codex", "new@example.com", day(8, time.Hour))
	s, _ := storedPicks("codex")
	got := s.AccountUsedDays["new@example.com"]
	if named := UsedDaysOf(usedDaysT0, []int{0, 6, 8}); got.Count != 4 || got.Days != named.Days || !got.Last.Equal(day(8, 0)) {
		t.Fatalf("an old count, day 8 counted on it: %+v", got)
	}
	// days that can't be read are none named, the count kept
	bad := UsedDays{From: usedDaysT0, Last: day(2, 0), Count: 3, Days: "%%"}
	if got := keptUsedDays(map[string]UsedDays{"a": bad}, map[string]UsedDays{"a": UsedDaysOf(usedDaysT0, []int{0, 1})}, fu)["a"]; got.Count != 3 || got.Days != UsedDaysOf(usedDaysT0, []int{0, 1, 2}).Days {
		t.Fatalf("days unreadable: %+v", got)
	}
	// a synced file's days past its Last, or more than its count, aren't
	// taken: the account is never counted more days than it could have
	// been used on (review M, T-148)
	past := UsedDays{From: usedDaysT0, Last: day(1, 0), Count: 2, Days: "//////8"}
	if got := keptUsedDays(map[string]UsedDays{"a": past}, map[string]UsedDays{"a": UsedDaysOf(usedDaysT0, []int{0})}, fu)["a"]; got.Count != 2 || got.Days != UsedDaysOf(usedDaysT0, []int{0, 1}).Days {
		t.Fatalf("days past Last: %+v", got)
	}
	many := UsedDays{From: usedDaysT0, Last: day(39, 0), Count: 2, Days: "//////8"}
	if got := keptUsedDays(map[string]UsedDays{"a": many}, map[string]UsedDays{"a": UsedDaysOf(usedDaysT0, []int{0})}, fu)["a"]; got.Count != 2 || got.Days != UsedDaysOf(usedDaysT0, []int{0, 39}).Days {
		t.Fatalf("more days than counted: %+v", got)
	}
	// days longer than usedDaysSpan names are not read at all
	huge := UsedDays{From: usedDaysT0, Last: day(400, 0), Count: 300, Days: strings.Repeat("AQ", 1000)}
	if got := knownDays(huge); daysNamed(got) != 2 {
		t.Fatalf("days too long read: %d named", daysNamed(got))
	}
	// day 0 is always one, and a day named twice counted once
	if c := UsedDaysOf(usedDaysT0, []int{3, 3}); c.Count != 2 || c.Days != UsedDaysOf(usedDaysT0, []int{0, 3}).Days {
		t.Fatalf("UsedDaysOf without day 0: %+v", c)
	}
	// a day past usedDaysSpan is counted, not named, so no file grows large
	if c := UsedDaysOf(usedDaysT0, []int{0, usedDaysSpan + 5}); c.Count != 2 || len(c.Days) > 4 {
		t.Fatalf("a day far on: %+v", c)
	}
}
