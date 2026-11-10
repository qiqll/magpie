package provider

import (
	"bytes"
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
	want := UsedDays{From: usedDaysT0, Last: day(1, 0), Count: 2}
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
