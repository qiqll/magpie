package provider

import (
	"errors"
	"sync"
	"sync/atomic"
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
	var pending atomic.Int32
	for range 20 {
		wg.Add(1)
		pending.Add(1)
		go func() { defer wg.Done(); NoteAccountUsed("codex", "new@example.com", time.Now()); pending.Add(-1) }()
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
	// the other 19 return while the first is held in its write
	returned := make(chan struct{})
	go func() { wg.Wait(); close(returned) }()
	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		n := writes
		mu.Unlock()
		if n > 1 {
			t.Fatalf("%d writes started", n)
		}
		select {
		case <-returned:
			t.Fatal("every request waited for none, the held write too")
		case <-deadline:
			t.Fatal("the other requests waited on the held write")
		case <-time.After(5 * time.Millisecond):
		}
		if pending.Load() == 1 {
			break
		}
	}
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

// Removed and added again by another magpie while this one runs (the
// command line), an account is looked at again: its new life's first use
// is kept, not taken for seen. One with no day yet is looked at again
// once it has one. A first use is never earlier than the day it was added.
func TestGrowthFirstUseAfterAnotherAddsAgain(t *testing.T) {
	firstUsedFixture(t, 10*24*time.Hour)
	markRead(t)
	NoteAccountUsed("codex", "new@example.com", time.Now().Add(-9*24*time.Hour))
	// as another magpie: removed, and added again an hour ago
	forgetAdded("codex", "new@example.com")
	f, _ := read()
	added := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	// the entry went with its last day (forgetAdded): made again, as noteAdded does
	f.Providers = append(f.Providers, Provider{ID: "codex", AccountAdded: map[string]time.Time{"new@example.com": added}})
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	firstUsedSeen.Lock()
	if firstUsedSeen.m == nil {
		firstUsedSeen.m = map[loginAccount]time.Time{}
	}
	firstUsedSeen.m[loginAccount{"codex", "new@example.com"}] = time.Now().Add(-10 * 24 * time.Hour) // its old life, seen
	firstUsedSeen.Unlock()
	// a request of its old life, ended after the add
	NoteAccountUsed("codex", "new@example.com", added.Add(-2*time.Hour))
	if at := AccountFirstUsedOf("codex", "new@example.com"); !at.Equal(added) {
		t.Fatalf("first use of its new life %v, want its day %v", at, added)
	}
	// an account with no day: not marked, so its day once kept is looked at
	NoteAccountUsed("codex", "later@example.com", time.Now())
	firstUsedSeen.Lock()
	_, marked := firstUsedSeen.m[loginAccount{"codex", "later@example.com"}]
	firstUsedSeen.Unlock()
	if marked {
		t.Fatal("an account with no day marked seen")
	}
}

// A sync from another computer never takes a first use away, and keeps the
// earlier of the two.
func TestGrowthFirstUseKeptThroughASync(t *testing.T) {
	here := map[string]time.Time{"a": time.Unix(2000, 0), "b": time.Unix(1000, 0)}
	there := map[string]time.Time{"b": time.Unix(3000, 0), "c": time.Unix(4000, 0)}
	got := earlierFirstUsed(here, there)
	if !got["a"].Equal(time.Unix(2000, 0)) || !got["b"].Equal(time.Unix(1000, 0)) || !got["c"].Equal(time.Unix(4000, 0)) {
		t.Fatalf("%v", got)
	}
	if got := earlierFirstUsed(here, nil); len(got) != 2 {
		t.Fatalf("the other computer's none took ours away: %v", got)
	}
}

// The backfill reads the ledger for an account a request kept a first use
// of meanwhile too, and keeps the ledger's when it is earlier.
func TestGrowthBackfillKeepsTheEarlier(t *testing.T) {
	firstUsedFixture(t, 30*24*time.Hour)
	NoteAccountUsed("codex", "new@example.com", time.Now())
	at := time.Now().Add(-25 * 24 * time.Hour).UTC().Truncate(time.Second)
	BackfillFirstUsed(func(want map[[2]string]time.Time) (map[[2]string]time.Time, error) {
		if _, ok := want[[2]string{"codex", "new@example.com"}]; !ok {
			t.Error("the ledger isn't asked for an account a request kept meanwhile")
		}
		return map[[2]string]time.Time{{"codex", "new@example.com"}: at}, nil
	})
	if got := AccountFirstUsedOf("codex", "new@example.com"); !got.Equal(at) {
		t.Fatalf("first use %v, want the ledger's %v", got, at)
	}
}

// claudeAddedAgo stores a Claude account added ago and never used, the
// ledger read, Claude kept to the agent (switchOnly) as given.
func claudeAddedAgo(t *testing.T, ago time.Duration, switchOnly bool) {
	t.Helper()
	signIn(t) // a sandbox HOME of its own
	f, _ := read()
	f.FirstUsedRead = true
	f.Providers = []Provider{{ID: "claude", SwitchOnly: switchOnly,
		AccountAdded: map[string]time.Time{"max@x.com": time.Now().Add(-ago).UTC().Truncate(time.Second)}}}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
}

// throughMagpie has Claude Code's requests go to the gateway, or not, for
// the test.
func throughMagpie(t *testing.T, on bool) {
	t.Helper()
	was := ClaudeThroughMagpie
	ClaudeThroughMagpie = func() bool { return on }
	t.Cleanup(func() { ClaudeThroughMagpie = was })
}

// claudeFive is the share of the five hours the account is held to, by the
// gateway's caps (CapsOf) and by the switch's (AccountCapsOf), which must
// agree.
func claudeFive(t *testing.T) int {
	t.Helper()
	a, _ := Provider{ID: "claude"}.CapsOf("max@x.com").Of("5 hours")
	b, _ := AccountCapsOf("claude", "max@x.com").Of("5 hours")
	if a != b {
		t.Fatalf("the gateway holds at %d%%, the switch at %d%%", a, b)
	}
	return a
}

// An account Claude Code uses on its own (not through magpie, or Claude
// kept to the agent) never has its first use seen, so its days count from
// when it was added: added 30 days ago, it isn't held at day 0's 50% and the
// switch doesn't move Claude Code off it there. Routed through magpie, one
// never served is on day 0. The setting flipped, it follows at the next read.
func TestGrowthAgentOnlyCountsFromAdded(t *testing.T) {
	claudeAddedAgo(t, 30*24*time.Hour, false)
	throughMagpie(t, false)
	if s := claudeFive(t); s != 95 {
		t.Fatalf("Claude Code on its own: day 30 by when it was added, 95%%; got %d%%", s)
	}
	if capReached(growthReading(60, 10), AccountCapsOf("claude", "max@x.com"), time.Now()) {
		t.Fatal("switched off at 60% of its five hours on its own")
	}
	throughMagpie(t, true)
	if s := claudeFive(t); s != 50 {
		t.Fatalf("routed through magpie, never served: day 0, 50%%; got %d%%", s)
	}
	claudeAddedAgo(t, 30*24*time.Hour, true) // kept to the agent
	if s := claudeFive(t); s != 95 {
		t.Fatalf("kept to the agent: by when it was added, 95%%; got %d%%", s)
	}
}

// Codex switch-only is the agent's own use too; routed, day 0.
func TestGrowthCodexSwitchOnlyCountsFromAdded(t *testing.T) {
	firstUsedFixture(t, 30*24*time.Hour)
	markRead(t)
	if s := newFiveHours(t); s != 50 {
		t.Fatalf("routed, never served: 50%%, got %d%%", s)
	}
	if err := SetSwitchOnly("codex", true); err != nil {
		t.Fatal(err)
	}
	if s := newFiveHours(t); s != 95 {
		t.Fatalf("switch-only: by when it was added, 95%%; got %d%%", s)
	}
}
