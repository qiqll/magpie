package provider

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// fiveHoursAt is the share the gateway holds the account's five hours at
// at now, 100 for none.
func fiveHoursAt(now time.Time) int {
	s, _ := accountCapsAt("codex", "new@example.com", now).Of("5 hours")
	if s == 0 {
		return 100
	}
	return s
}

// fromT134 is providers.json as a magpie of T-134 left it: first uses
// read (usedDaysFixture's markRead, which marks a fresh file done in the
// same write) and the mark not made.
func fromT134(t *testing.T) {
	t.Helper()
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	f.GraduatedRead = false
	if err := store(f); err != nil {
		t.Fatal(err)
	}
}

// usedDaysBeganAt has the built-ins begin counting the days used at at
// (usedDaysBegan) for the test: one whose first uses are on the real clock
// has them before it.
func usedDaysBeganAt(t *testing.T, at time.Time) {
	t.Helper()
	was := usedDaysBegan
	usedDaysBegan = at
	t.Cleanup(func() { usedDaysBegan = was })
}

// graduated is when the account was found done with its ramp-up, zero for
// not.
func graduated() time.Time { return AccountGraduatedOf("codex", "new@example.com") }

// Added 20 days ago and used on 8 of them (T-150's acceptance): under
// Standard by the days used it is on day 8, back at 78%. The upgrade's
// mark finds its ramp-up done by the calendar and keeps it at 100% every
// day after, used or not, its row and the policy page's chart saying it
// isn't held. Run under TZ=UTC and TZ=Pacific/Kiritimati.
func TestGraduatedKeptAtFull(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	for d := range 8 {
		NoteAccountUsed("codex", "new@example.com", day(d, time.Hour))
	}
	upgrade := day(20, 0)
	if g, s := grownAt(t, upgrade); g.Policy != settings.GrowthStandard || g.Day != 8 || !g.UsedDays || s != 78 {
		t.Fatalf("before the mark, by the days used: day %d at %d%% (%+v), want day 8 at 78%%", g.Day, s, g)
	}
	graduateOnce(upgrade)
	if at := graduated(); !at.Equal(upgrade) {
		t.Fatalf("not marked at the upgrade: %v", at)
	}
	if !storedFile().GraduatedRead {
		t.Fatal("the mark isn't recorded as done")
	}
	for d := 20; d <= 40; d++ {
		if d%3 == 0 {
			NoteAccountUsed("codex", "new@example.com", day(d, time.Hour))
		}
		now := day(d, 2*time.Hour)
		if s := fiveHoursAt(now); s != 100 {
			t.Errorf("day %d: the gateway holds it at %d%%", d, s)
		}
		if g := AccountGrowthOf("codex", "new@example.com", now); g != nil {
			t.Errorf("day %d: its row says it ramps up: %+v", d, g)
		}
		if slices.ContainsFunc(GrowthAccountsOf(settings.GrowthStandard, now), func(a GrowthAccount) bool { return a.Account == "new@example.com" }) {
			t.Errorf("day %d: the chart marks it", d)
		}
	}
	// a policy picked for it after doesn't hold it again: it was at 100%
	if err := SetAccountGrowthPolicy("codex", "new@example.com", settings.GrowthCautious); err != nil {
		t.Fatal(err)
	}
	if s := fiveHoursAt(day(41, 0)); s != 100 || graduated().IsZero() {
		t.Fatalf("after a pick: held at %d%%, marked %v", s, graduated())
	}
}

// The mark is made once: an account still ramping up then (used 5 days,
// on its calendar day 5 of Standard's 14) ramps up by the days used after,
// day by day, its calendar's day 14 and day 30 passed, and a later start's
// mark finds nothing to do. So does an account first used after the
// upgrade.
func TestGraduatedOnceOnly(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	for d := range 5 {
		NoteAccountUsed("codex", "new@example.com", day(d, time.Hour))
	}
	graduateOnce(day(5, 0))
	if !graduated().IsZero() || !storedFile().GraduatedRead {
		t.Fatalf("on its calendar day 5: marked %v, done %v", graduated(), storedFile().GraduatedRead)
	}
	usedBefore := 5
	for d := 5; d <= 40; d++ {
		used := d%4 == 0
		if used {
			NoteAccountUsed("codex", "new@example.com", day(d, time.Hour))
		}
		// each start after the upgrade
		graduateOnce(day(d, 90*time.Minute))
		now := day(d, 2*time.Hour)
		want := 100
		if usedBefore < 14 {
			want = 50 + 50*usedBefore/14
		}
		if s := fiveHoursAt(now); s != want {
			t.Errorf("day %d, %d days used before: held at %d%%, want %d%%", d, usedBefore, s, want)
		}
		if used {
			usedBefore++
		}
	}
	if !graduated().IsZero() {
		t.Fatalf("marked at a later start: %v", graduated())
	}
}

// An account first used after the upgrade's mark ramps up by its days used
// alone, its calendar's last day long past.
func TestGraduatedNewAccountByUse(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	graduateOnce(day(0, -30*time.Minute)) // before its first use
	NoteAccountUsed("codex", "new@example.com", day(0, time.Hour))
	NoteAccountUsed("codex", "new@example.com", day(1, time.Hour))
	graduateOnce(day(25, 0))
	if g, s := grownAt(t, day(25, time.Hour)); g.Day != 2 || !g.UsedDays || s != 57 || !graduated().IsZero() {
		t.Fatalf("new after the upgrade, on calendar day 25: day %d at %d%% (%+v), marked %v", g.Day, s, g, graduated())
	}
}

// Nothing is marked, nor the mark done, until the ledger was read for
// first uses; then it is. An account its policy counts on the calendar,
// one the gateway doesn't serve, and one skipped are never marked: none is
// held again by the days used.
func TestGraduatedOnlyByUse(t *testing.T) {
	t.Run("first uses not read", func(t *testing.T) {
		firstUsedFixture(t, time.Hour)
		growthDefault(t, settings.GrowthStandard)
		usedDaysBeganAt(t, time.Now().Add(time.Hour))
		// added an hour ago: its first use is now, on the real clock
		NoteAccountUsed("codex", "new@example.com", time.Now())
		later := time.Now().Add(20 * growthDay)
		graduateOnce(later)
		if !graduated().IsZero() || storedFile().GraduatedRead {
			t.Fatalf("first uses not read: marked %v, done %v", graduated(), storedFile().GraduatedRead)
		}
		markRead(t)
		fromT134(t)
		graduateOnce(later)
		if graduated().IsZero() || !storedFile().GraduatedRead {
			t.Fatalf("first uses read: marked %v, done %v", graduated(), storedFile().GraduatedRead)
		}
	})
	t.Run("calendar policy", func(t *testing.T) {
		usedDaysFixture(t, "cal")
		fromT134(t)
		NoteAccountUsed("codex", "new@example.com", day(0, 0))
		graduateOnce(day(25, 0))
		if !graduated().IsZero() || !storedFile().GraduatedRead {
			t.Fatalf("on the calendar: marked %v", graduated())
		}
	})
	t.Run("not served", func(t *testing.T) {
		usedDaysFixture(t, "")
		fromT134(t)
		NoteAccountUsed("codex", "new@example.com", day(0, 0))
		if err := SetSwitchOnly("codex", true); err != nil {
			t.Fatal(err)
		}
		graduateOnce(day(25, 0))
		if !graduated().IsZero() {
			t.Fatalf("not served: marked %v", graduated())
		}
	})
	t.Run("skipped", func(t *testing.T) {
		usedDaysFixture(t, "")
		fromT134(t)
		NoteAccountUsed("codex", "new@example.com", day(0, 0))
		if err := SkipGrowth("codex", "new@example.com"); err != nil {
			t.Fatal(err)
		}
		graduateOnce(day(25, 0))
		if !graduated().IsZero() {
			t.Fatalf("skipped: marked %v", graduated())
		}
	})
}

// The mark is kept in providers.json under its own keys, beside the old
// fields as they were; the editor's Save keeps it, a rename moves it (a
// Copilot host's and a login's), a sync keeps either computer's, the
// earlier, and leaves the mark's done here as it is; the user removing the
// account drops it.
func TestGraduatedKeptApart(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	upgrade := day(20, 0)
	graduateOnce(upgrade)
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"accountGraduated"`, `"graduatedRead"`, `"accountUsedDays"`, `"accountFirstUsed"`} {
		if !strings.Contains(string(raw), k) {
			t.Fatalf("providers.json has no %s: %s", k, raw)
		}
	}

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	q := *p
	q.AccountGraduated = nil
	if err := Save(q); err != nil {
		t.Fatal(err)
	}
	if !graduated().Equal(upgrade) {
		t.Fatalf("Save of a form without it: %v", graduated())
	}

	if err := moveAccountSettings("codex", map[string]string{"new@example.com": "Renamed@example.com"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); !s.AccountGraduated["renamed@example.com"].Equal(upgrade) || len(s.AccountGraduated) != 1 {
		t.Fatalf("after the rename: %v", s.AccountGraduated)
	}
	noteAdded(nil, map[loginAccount]loginAccount{{"codex", "renamed@example.com"}: {"codex", "new@example.com"}}, nil)
	if s, _ := storedPicks("codex"); !s.AccountGraduated["new@example.com"].Equal(upgrade) || len(s.AccountGraduated) != 1 {
		t.Fatalf("after a login's rename: %v", s.AccountGraduated)
	}

	// the other computer has none: here's stays, and so does the mark's done
	f, _ := read()
	there := slices.Clone(f.Providers)
	for i := range there {
		if there[i].ID == "codex" {
			there[i].AccountGraduated = nil
		}
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if !graduated().Equal(upgrade) || !storedFile().GraduatedRead {
		t.Fatalf("after a sync without it: %v, done %v", graduated(), storedFile().GraduatedRead)
	}
	// and an earlier one there: taken, another account's too
	for i := range there {
		if there[i].ID == "codex" {
			there[i].AccountGraduated = map[string]time.Time{"new@example.com": day(15, 0), "other@example.com": day(16, 0)}
		}
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	// other@ isn't added here or there: its mark isn't kept (review M3)
	if s, _ := storedPicks("codex"); !s.AccountGraduated["new@example.com"].Equal(day(15, 0)) || len(s.AccountGraduated) != 1 {
		t.Fatalf("after a sync with an earlier one: %v", s.AccountGraduated)
	}

	if err := ForgetLogin("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); !s.AccountGraduated["new@example.com"].IsZero() {
		t.Fatalf("removed by the user, its mark stayed: %v", s.AccountGraduated)
	}
}

// A providers.json from before (no accountGraduated, no graduatedRead) is
// read as it is: the mark isn't done, and the account is held by its days
// used until it is.
func TestGraduatedFromBefore(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	if f := storedFile(); f.GraduatedRead {
		t.Fatal("a file from before reads as marked")
	}
	if s, _ := storedPicks("codex"); s.AccountGraduated != nil {
		t.Fatalf("a file from before has marks: %v", s.AccountGraduated)
	}
	if s := fiveHoursAt(day(20, 0)); s != 53 {
		t.Fatalf("before the mark, 1 day used: %d%%", s)
	}
}

// A file whose first uses were never read (a magpie from before T-134) has
// the mark made in the ledger read's own write, so a start writes
// providers.json no more often than before: an account added 30 days ago
// and first served 20 days ago is marked done under Standard, one first
// served 3 days ago isn't, and GraduateOnce after writes nothing.
func TestGraduatedWithTheFirstUseRead(t *testing.T) {
	firstUsedFixture(t, 30*24*time.Hour)
	growthDefault(t, settings.GrowthStandard)
	usedDaysBeganAt(t, time.Now())
	codexSignIn(t, os.Getenv("HOME"), "late@example.com", "r-late")
	rememberLogins(true)
	addedAgo(t, "codex", "late@example.com", 30*24*time.Hour)
	now := time.Now()
	BackfillFirstUsed(func(want map[[2]string]time.Time) (map[[2]string]time.Time, error) {
		return map[[2]string]time.Time{
			{"codex", "new@example.com"}:  now.Add(-20 * growthDay),
			{"codex", "late@example.com"}: now.Add(-3 * growthDay),
		}, nil
	})
	f := storedFile()
	if !f.FirstUsedRead || !f.GraduatedRead {
		t.Fatalf("read %v, marked %v", f.FirstUsedRead, f.GraduatedRead)
	}
	s, _ := storedPicks("codex")
	if s.AccountGraduated["new@example.com"].IsZero() || !s.AccountGraduated["late@example.com"].IsZero() {
		t.Fatalf("marked: %v", s.AccountGraduated)
	}
	before, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	GraduateOnce()
	if after, _ := os.Stat(Path()); !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("GraduateOnce wrote the file again")
	}
}

// A backup from before T-150 (no accountGraduated) restored keeps the
// marks made here: the mark is made once, so one dropped is never made
// again, and the account would be held at 78% (T-150 review H1).
func TestGraduatedKeptThroughARestore(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	for d := range 8 {
		NoteAccountUsed("codex", "new@example.com", day(d, time.Hour))
	}
	upgrade := day(20, 0)
	graduateOnce(upgrade)
	ps, err := Stored()
	if err != nil {
		t.Fatal(err)
	}
	for i := range ps {
		ps[i].AccountGraduated = nil
	}
	if _, _, err := Restore(ps); err != nil {
		t.Fatal(err)
	}
	if !graduated().Equal(upgrade) {
		t.Fatalf("after a restore without it: %v", graduated())
	}
	if s := fiveHoursAt(day(21, 0)); s != 100 {
		t.Fatalf("after a restore: held at %d%%", s)
	}
}

// An account the user removed here isn't marked again by a sync bringing
// the other computer's mark: added again, it is new (review M3).
func TestGraduatedNotBroughtBackForARemoved(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	graduateOnce(day(20, 0))
	f, _ := read()
	there := slices.Clone(f.Providers)
	if err := ForgetLogin("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	// the other computer's record of the account removed here, its mark
	// still on, its AccountAdded gone with this computer's sync of the
	// removal
	for i := range there {
		if there[i].ID == "codex" {
			there[i].AccountAdded = maps.Clone(there[i].AccountAdded)
			delete(there[i].AccountAdded, "new@example.com")
		}
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if !graduated().IsZero() {
		t.Fatalf("a mark of an account not added came back: %v", graduated())
	}
}

// Settings that can't be read are unknown: nothing is marked, the mark
// isn't done, and the next start with them read makes it (review M1).
func TestGraduatedWaitsForSettings(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	NoteAccountUsed("codex", "new@example.com", day(0, 0))
	good, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	// no copy to read it from either
	if err := os.Remove(settings.Path() + ".bak"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.SharedChecked(); err == nil {
		t.Fatal("settings read with no error")
	}
	graduateOnce(day(20, 0))
	if !graduated().IsZero() || storedFile().GraduatedRead {
		t.Fatalf("settings unreadable: marked %v, done %v", graduated(), storedFile().GraduatedRead)
	}
	if err := os.WriteFile(settings.Path(), good, 0o600); err != nil {
		t.Fatal(err)
	}
	graduateOnce(day(20, 0))
	if graduated().IsZero() || !storedFile().GraduatedRead {
		t.Fatalf("settings read again: marked %v, done %v", graduated(), storedFile().GraduatedRead)
	}
}

// An account skipped when the mark was made isn't marked (letGo), so one
// done by the calendar then and resumed after (T-158) would be held again
// by the days it was used: used 8 days of 20, back at Standard's 78%.
// ResumeGrowth marks it as the mark would have, so it stays at 100%.
func TestGraduatedOnResume(t *testing.T) {
	usedDaysFixture(t, "")
	fromT134(t)
	for d := range 8 {
		NoteAccountUsed("codex", "new@example.com", day(d, time.Hour))
	}
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	graduateOnce(day(20, 0))
	if !graduated().IsZero() || !storedFile().GraduatedRead {
		t.Fatalf("skipped at the mark: marked %v, done %v", graduated(), storedFile().GraduatedRead)
	}
	if err := ResumeGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if graduated().IsZero() {
		t.Error("resumed, done by the calendar, not marked")
	}
	if s := fiveHoursAt(time.Now()); s != 100 {
		t.Errorf("resumed, the gateway holds it at %d%%", s)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g != nil {
		t.Errorf("resumed, its row says it ramps up: %+v", g)
	}
}

// One resumed still ramping up by the calendar (used 3 days, first used 5
// days ago) isn't marked: the policy holds it again by its days used.
func TestGraduatedNotOnResumeWhileRamping(t *testing.T) {
	usedDaysFixture(t, "")
	now := time.Now()
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.Providers {
		if f.Providers[i].ID == "codex" {
			f.Providers[i].AccountAdded["new@example.com"] = now.Add(-6 * 24 * time.Hour).UTC()
		}
	}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	for d := range 3 {
		NoteAccountUsed("codex", "new@example.com", now.Add(time.Duration(d-5)*24*time.Hour))
	}
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := ResumeGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if !graduated().IsZero() {
		t.Errorf("resumed while ramping up, marked %v", graduated())
	}
	if s := fiveHoursAt(now); s >= 100 {
		t.Errorf("resumed while ramping up, the gateway holds it at %d%%", s)
	}
}

// Only an account first used before the built-ins counted the days used
// (usedDaysBegan) is marked (T-162): a ledger read for first uses long
// after the upgrade, unreadable till then, finds one first used since
// with its calendar's 14 days past, and leaves it ramping up by its days
// used. One first used a minute before is marked, and so is it when
// resumed (ResumeGrowth asks the same).
func TestGraduatedOnlyFromBefore(t *testing.T) {
	for _, c := range []struct {
		name   string
		began  time.Duration // usedDaysBegan, from the first use
		marked bool
	}{
		{"first used before", time.Minute, true},
		{"first used then", 0, false},
		{"first used after", -2 * growthDay, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			usedDaysFixture(t, "")
			fromT134(t)
			usedDaysBeganAt(t, day(0, time.Hour+c.began))
			for d := range 3 {
				NoteAccountUsed("codex", "new@example.com", day(d, time.Hour))
			}
			graduateOnce(day(25, 0))
			if marked := !graduated().IsZero(); marked != c.marked || !storedFile().GraduatedRead {
				t.Fatalf("marked %v (%v), want %v; done %v", marked, graduated(), c.marked, storedFile().GraduatedRead)
			}
			if c.marked {
				return
			}
			if s := fiveHoursAt(day(25, time.Hour)); s != 60 {
				t.Errorf("not marked, 3 days used: held at %d%%, want 60%%", s)
			}
			if err := SkipGrowth("codex", "new@example.com"); err != nil {
				t.Fatal(err)
			}
			if err := ResumeGrowth("codex", "new@example.com"); err != nil {
				t.Fatal(err)
			}
			if !graduated().IsZero() {
				t.Errorf("resumed, marked %v", graduated())
			}
		})
	}
}
