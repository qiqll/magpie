package provider

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// firstUsedFixture is codex with me@ from before (no day) and new@ added
// ago before now, its first use not read from the ledger yet.
func firstUsedFixture(t *testing.T, ago time.Duration) {
	t.Helper()
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
	addedAgo(t, "codex", "new@example.com", ago)
	forgetFirstUsedSeen()
	t.Cleanup(forgetFirstUsedSeen)
}

// markRead marks the ledger read for first uses, as BackfillFirstUsed does.
func markRead(t *testing.T) {
	t.Helper()
	BackfillFirstUsed(func(map[[2]string]time.Time) (map[[2]string]time.Time, error) {
		return map[[2]string]time.Time{}, nil
	})
	if !storedFile().FirstUsedRead {
		t.Fatal("not marked read")
	}
}

func newFiveHours(t *testing.T) int {
	t.Helper()
	s, _ := AccountCapsOf("codex", "new@example.com").Of("5 hours")
	return s
}

// An account added 30 days ago and never used is on its day 0 the day it
// is first used: held at the first stage's 50% of its five hours, where
// counting from AccountAdded held it not at all. Its days then count from
// that use, kept once: a later request doesn't move it.
func TestGrowthCountsFromFirstUse(t *testing.T) {
	firstUsedFixture(t, 30*24*time.Hour)
	if s := newFiveHours(t); s != 95 {
		t.Fatalf("before the ledger was read, day 30 by AccountAdded: %d%%", s)
	}
	markRead(t)
	if s := newFiveHours(t); s != 50 {
		t.Fatalf("added long ago, never used: day 0 is 50%%, got %d%%", s)
	}
	used := time.Now().Add(-4 * 24 * time.Hour) // its fourth day: the week at 80%, the five hours free
	NoteAccountUsed("codex", "NEW@example.com", used)
	if at := AccountFirstUsedOf("codex", "new@example.com"); !at.Equal(used.UTC().Truncate(time.Second)) {
		t.Fatalf("first use kept as %v", at)
	}
	forgetFirstUsedSeen()
	NoteAccountUsed("codex", "new@example.com", time.Now())
	if at := AccountFirstUsedOf("codex", "new@example.com"); !at.Equal(used.UTC().Truncate(time.Second)) {
		t.Fatalf("a later request moved the first use to %v", at)
	}
	c := AccountCapsOf("codex", "new@example.com")
	if s, _ := c.Of("Weekly"); s != 80 {
		t.Fatalf("its fourth day from first use: the week at 80%%, got %d%%", s)
	}
	if s := newFiveHours(t); s != 0 {
		t.Fatalf("its fourth day from first use: the five hours free, got %d%%", s)
	}
	// a first use kept is never written over, asked straight
	writeFirstUsed(loginAccount{"codex", "new@example.com"}, time.Now())
	if at := AccountFirstUsedOf("codex", "new@example.com"); !at.Equal(used.UTC().Truncate(time.Second)) {
		t.Fatalf("written over to %v", at)
	}
	// the account from before has no day, and no first use is kept for it
	NoteAccountUsed("codex", "me@example.com", time.Now())
	if at := AccountFirstUsedOf("codex", "me@example.com"); !at.IsZero() {
		t.Fatalf("the account from before given a first use: %v", at)
	}
	if c := AccountCapsOf("codex", "me@example.com"); c.Holds() {
		t.Fatalf("the account from before held: %+v", c)
	}
}

// The backfill takes the earliest served row from when the account was
// added; a ledger that can't be read marks nothing, so an account isn't
// made new by a failed read, and the next start reads again.
func TestGrowthBackfillFirstUse(t *testing.T) {
	firstUsedFixture(t, 30*24*time.Hour)
	BackfillFirstUsed(func(map[[2]string]time.Time) (map[[2]string]time.Time, error) {
		return nil, errors.New("usage.jsonl: permission denied")
	})
	if storedFile().FirstUsedRead {
		t.Fatal("an unreadable ledger marked the read done")
	}
	if s := newFiveHours(t); s != 95 {
		t.Fatalf("an unreadable ledger made the account new: %d%%", s)
	}
	at := time.Now().Add(-20 * 24 * time.Hour).UTC().Truncate(time.Second)
	var asked map[[2]string]time.Time
	BackfillFirstUsed(func(want map[[2]string]time.Time) (map[[2]string]time.Time, error) {
		asked = want
		return map[[2]string]time.Time{{"codex", "new@example.com"}: at}, nil
	})
	if _, ok := asked[[2]string{"codex", "new@example.com"}]; !ok || len(asked) != 1 {
		t.Fatalf("asked the ledger for %v", asked)
	}
	if got := AccountFirstUsedOf("codex", "new@example.com"); !got.Equal(at) {
		t.Fatalf("backfilled %v, want %v", got, at)
	}
	if s := newFiveHours(t); s != 95 {
		t.Fatalf("first used 20 days ago: day 20, got %d%%", s)
	}
}

// A keep-alive and a usage read keep no first use: only a request the
// gateway records as served does (usage.Record.ServedOn, budget.Append).
func TestGrowthKeepAliveIsNoUse(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	markRead(t)
	RenewLogins(t.Context(), 0)
	usageLogins("codex")
	if at := AccountFirstUsedOf("codex", "new@example.com"); !at.IsZero() {
		t.Fatalf("a keep-alive or usage read kept a first use: %v", at)
	}
}

// Removed and added again, an account is new again: its first use goes
// with its day.
func TestGrowthFirstUseGoesWithRemoval(t *testing.T) {
	firstUsedFixture(t, 10*24*time.Hour)
	markRead(t)
	NoteAccountUsed("codex", "new@example.com", time.Now().Add(-9*24*time.Hour))
	forgetAdded("codex", "new@example.com")
	if at := AccountFirstUsedOf("codex", "new@example.com"); !at.IsZero() {
		t.Fatalf("removed, its first use kept: %v", at)
	}
}

// The user's own cap still comes first: an account with a cap of its own
// isn't held by the policy, whatever its first use.
func TestGrowthFirstUseLeavesTheUsersCap(t *testing.T) {
	firstUsedFixture(t, 30*24*time.Hour)
	markRead(t)
	f, _ := read()
	for i := range f.Providers {
		if f.Providers[i].ID == "codex" {
			f.Providers[i].AccountCaps = map[string]int{"new@example.com": 70}
		}
	}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	if s := newFiveHours(t); s != 70 {
		t.Fatalf("the user's cap: 70%%, got %d%%", s)
	}
}

// Many requests at once of a new account write its first use once and
// wait on nothing but each other's map lock: one claims the write, the
// rest pass at once. Run with -race.
func TestGrowthFirstUseWrittenOnce(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	release := make(chan struct{})
	writes := 0
	var mu sync.Mutex
	was := beforeFirstUsedWrite
	beforeFirstUsedWrite = func() {
		mu.Lock()
		writes++
		mu.Unlock()
		<-release
	}
	t.Cleanup(func() { beforeFirstUsedWrite = was })
	var wg sync.WaitGroup
	done := make(chan struct{})
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); NoteAccountUsed("codex", "new@example.com", time.Now()) }()
	}
	// 19 of them return while the first is held in its write
	go func() {
		for {
			mu.Lock()
			n := writes
			mu.Unlock()
			if n >= 1 {
				close(done)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	<-done
	close(release)
	wg.Wait()
	if writes != 1 {
		t.Fatalf("%d writes", writes)
	}
	if AccountFirstUsedOf("codex", "new@example.com").IsZero() {
		t.Fatal("not kept")
	}
}

// The editor's Save never writes the first use: a body that carries one
// keeps the file's, and one with none takes nothing away.
func TestGrowthSaveKeepsFirstUse(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	used := time.Now().Add(-30 * time.Minute)
	NoteAccountUsed("codex", "new@example.com", used)
	want := used.UTC().Truncate(time.Second)
	p, ok := storedPicks("codex")
	if !ok {
		t.Fatal("no codex entry")
	}
	for _, sent := range []map[string]time.Time{{"new@example.com": time.Now().Add(-90 * 24 * time.Hour)}, nil} {
		q := p
		q.AccountFirstUsed = sent
		if err := Save(q); err != nil {
			t.Fatal(err)
		}
		if at := AccountFirstUsedOf("codex", "new@example.com"); !at.Equal(want) {
			t.Fatalf("Save with %v wrote the first use as %v", sent, at)
		}
	}
}
