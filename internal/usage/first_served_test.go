package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// FirstServed takes each account's earliest served row from when it was
// added: not a failed one, a probe, an app's report or one from before
// it was added; no ledger is none served, an unreadable one an error.
func TestFirstServedEarliestServedRow(t *testing.T) {
	added := time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second)
	at := func(d int) time.Time { return added.Add(time.Duration(d) * time.Hour) }
	k := [2]string{"codex", "me@example.com"}
	if got, err := FirstServed(map[[2]string]time.Time{k: added}); err != nil || len(got) != 0 {
		t.Fatalf("no ledger: %v %v", got, err)
	}
	for _, r := range []Record{
		{Time: at(-2), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(1), Provider: "codex", ProviderAccount: "me@example.com", Status: 401},
		{Time: at(2), Provider: "codex", ProviderAccount: "me@example.com", Status: 200, Source: SourceProbe},
		{Time: at(3), Provider: "codex", ProviderAccount: "me@example.com", Status: 200, Source: SourceReported},
		{Time: at(4), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(5), Provider: "codex", ProviderAccount: "Me@Example.com", Status: 200},
	} {
		Append(r)
	}
	got, err := FirstServed(map[[2]string]time.Time{k: added})
	if err != nil || !got[k].Equal(at(4)) {
		t.Fatalf("first served %v (%v), want %v", got[k], err, at(4))
	}
	if err := os.Chmod(Path(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(Path(), 0o600) })
	if os.Getuid() != 0 {
		if _, err := FirstServed(map[[2]string]time.Time{k: added}); err == nil {
			t.Fatal("an unreadable ledger read as none served")
		}
	}
}

// ServedDays counts the days, each 24 hours from the first use, that the
// account's served rows fall in: not a failed row, a probe's or one from
// before its first use; no ledger is none, an unreadable one an error.
// The days start at the first use, half an hour before midnight in UTC,
// whatever the clock's zone. One whose first day has no row (the ledger
// doesn't reach back to it) isn't known, so isn't in it.
func TestServedDaysCountsEachDayOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	from := time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC)
	at := func(d int, h time.Duration) time.Time { return from.Add(time.Duration(d)*24*time.Hour + h) }
	k := [2]string{"codex", "me@example.com"}
	if got, err := ServedDays(map[[2]string]time.Time{k: from}); err != nil || len(got) != 0 {
		t.Fatalf("no ledger: %v %v", got, err)
	}
	for _, r := range []Record{
		{Time: at(-1, 0), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(0, 0), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(0, 2*time.Hour), Provider: "codex", ProviderAccount: "Me@Example.com", Status: 200},
		{Time: at(1, time.Hour), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(2, time.Hour), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(4, time.Hour), Provider: "codex", ProviderAccount: "me@example.com", Status: 429},
		{Time: at(5, time.Hour), Provider: "codex", ProviderAccount: "me@example.com", Status: 200, Source: SourceProbe},
		{Time: at(8, 23*time.Hour), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(3, time.Hour), Provider: "codex", ProviderAccount: "other@example.com", Status: 200},
	} {
		Append(r)
	}
	// first used, by another computer, a day before this ledger has it
	k2 := [2]string{"codex", "other@example.com"}
	got, err := ServedDays(map[[2]string]time.Time{k: from, k2: at(2, 0)})
	if c := got[k]; err != nil || c.Count != 4 || !c.From.Equal(from) || !c.Last.Equal(at(8, 0)) || len(got) != 1 {
		t.Fatalf("served days %+v (%v), want 4 to day 8", got, err)
	}
	// and which: days 0, 1, 2 and 8, so a sync takes the union (T-148)
	if want := provider.UsedDaysOf(from, []int{0, 1, 2, 8}); got[k] != want {
		t.Fatalf("served days %+v, want %+v", got[k], want)
	}
	if err := os.Chmod(Path(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(Path(), 0o600) })
	if os.Getuid() != 0 {
		if _, err := ServedDays(map[[2]string]time.Time{k: from}); err == nil {
			t.Fatal("an unreadable ledger read as not used")
		}
	}
}
