package usage

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
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

// A ledger read that stops partway is an error to FirstServed and
// ServedDays, never the rows before it taken for all (T-149): the
// account's served row after the break would read as never used (day 0)
// or as not used on its days. The read that failed isn't kept as the
// ledger either: the next one, read through, has the row.
func TestServedReadPartwayIsUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restartLog()
	t.Cleanup(restartLog)
	from := time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC)
	k := [2]string{"codex", "me@example.com"}
	Append(Record{Time: from.Add(-time.Hour), Provider: "codex", ProviderAccount: "other@example.com", Status: 200})
	Append(Record{Time: from, Provider: "codex", ProviderAccount: "me@example.com", Status: 200})
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	first := int64(bytes.IndexByte(b, '\n') + 1)
	was := logReadFrom
	t.Cleanup(func() { logReadFrom = was })
	logReadFrom = func(f *os.File, n int64) io.Reader {
		return io.MultiReader(io.LimitReader(f, min(n, first)), iotest.ErrReader(errors.New("the disk went away")))
	}
	restartLog()
	if got, err := FirstServed(map[[2]string]time.Time{k: from}); err == nil {
		t.Errorf("a read that stopped after the first line: first served %v, no error", got)
	}
	restartLog()
	if got, err := ServedDays(map[[2]string]time.Time{k: from}); err == nil {
		t.Errorf("a read that stopped after the first line: served days %v, no error", got)
	}
	logReadFrom = was
	if got, err := FirstServed(map[[2]string]time.Time{k: from}); err != nil || !got[k].Equal(from) {
		t.Fatalf("read through after: first served %v (%v), want %v", got[k], err, from)
	}
	if got, err := ServedDays(map[[2]string]time.Time{k: from}); err != nil || got[k].Count != 1 {
		t.Fatalf("read through after: served days %+v (%v), want 1", got[k], err)
	}
}

// A read that continues the last one and stops partway still hands Visit
// every row read before (T-149 review M1): the last block, taken off to be
// continued, among them. It isn't kept as the ledger, so the next read,
// through, has the rows after the break too; LogVersion says the stopped
// one isn't complete.
func TestLogReadPartwayKeepsTheRowsBefore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restartLog()
	t.Cleanup(restartLog)
	from := time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC)
	for i := range 3 {
		Append(Record{Time: from.Add(time.Duration(i) * time.Hour), Provider: "codex", ProviderAccount: "me@example.com", Status: 200})
	}
	count := func() int {
		n := 0
		Visit(time.Time{}, func(Record) { n++ })
		return n
	}
	if n := count(); n != 3 {
		t.Fatalf("read through: %d rows, want 3", n)
	}
	Append(Record{Time: from.Add(3 * time.Hour), Provider: "codex", ProviderAccount: "me@example.com", Status: 200})
	Append(Record{Time: from.Add(4 * time.Hour), Provider: "codex", ProviderAccount: "me@example.com", Status: 200})
	// the fourth line is read, the fifth isn't
	was := logReadFrom
	t.Cleanup(func() { logReadFrom = was })
	logReadFrom = func(f *os.File, n int64) io.Reader {
		// the rest of the log from where the read continues: its first line
		// whole, then a failure
		rest := make([]byte, n)
		k, _ := io.ReadFull(f, rest)
		first := int64(bytes.IndexByte(rest[:k], '\n') + 1)
		return io.MultiReader(bytes.NewReader(rest[:first]), iotest.ErrReader(errors.New("the disk went away")))
	}
	if n := count(); n != 4 {
		t.Errorf("a read that stopped after the fourth row: %d rows, want the 4 before the break", n)
	}
	if _, complete := LogVersion(); complete {
		t.Error("a read that stopped partway is complete")
	}
	logReadFrom = was
	if n := count(); n != 5 {
		t.Fatalf("read through after: %d rows, want 5", n)
	}
	// a log that can't be looked at is no log read through either (L2)
	if os.Getuid() != 0 {
		dir := filepath.Dir(Path())
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		if _, complete := LogVersion(); complete {
			t.Error("a log that can't be looked at is complete")
		}
		os.Chmod(dir, 0o700)
	}
}
