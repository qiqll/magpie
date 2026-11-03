package provider

import (
	"testing"
	"time"
)

// interleave runs first, and as it is about to write providers.json, runs
// second in another goroutine: second writes then if it can; with the lock
// held by first it waits for first's write instead.
func interleave(t *testing.T, first, second func()) {
	t.Helper()
	done := make(chan struct{})
	started := false
	beforeProvidersWrite = func() {
		beforeProvidersWrite = func() {}
		started = true
		go func() {
			defer close(done)
			second()
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	t.Cleanup(func() { beforeProvidersWrite = func() {} })
	first()
	if !started {
		t.Fatal("the first change wrote nothing")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the second change never ended")
	}
}

// providersLockHome is a home with a codex account added (its day noted)
// and accounts looked at just now, so no change comes but the test's.
func providersLockHome(t *testing.T) {
	t.Helper()
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
	if accountAdded("codex", "new@example.com").IsZero() {
		t.Fatal("the new account has no day")
	}
}

func checkCapAndDay(t *testing.T) {
	t.Helper()
	if s, _ := AccountCapsOf("codex", "new@example.com").Of("5 hours"); s != 70 {
		t.Errorf("the user's 70%% cap read as %d%%: written over", s)
	}
	if accountAdded("codex", "other@example.com").IsZero() {
		t.Error("the day noted for other@example.com was written over")
	}
	if accountAdded("codex", "new@example.com").IsZero() {
		t.Error("new@example.com's day was lost")
	}
}

// T-079: a cap the user sets while magpie notes an account's day (as
// logins.json is written with it) keeps both: the day noted between the
// cap's read of providers.json and its write isn't written over.
func TestProvidersCapKeepsADayNotedMeanwhile(t *testing.T) {
	providersLockHome(t)
	interleave(t, func() {
		if err := SetAccountCap("codex", "new@example.com", 70); err != nil {
			t.Error(err)
		}
	}, func() {
		noteAdded([]loginAccount{{"codex", "other@example.com"}}, nil, nil)
	})
	checkCapAndDay(t)
}

// And the other way: a day noted while the user sets a cap keeps the cap.
func TestProvidersDayNotedKeepsACapSetMeanwhile(t *testing.T) {
	providersLockHome(t)
	interleave(t, func() {
		noteAdded([]loginAccount{{"codex", "other@example.com"}}, nil, nil)
	}, func() {
		if err := SetAccountCap("codex", "new@example.com", 70); err != nil {
			t.Error(err)
		}
	})
	checkCapAndDay(t)
}

// An account's settings moved to its new name (moveAccountSettings, as a
// Copilot account's host is put in its name) while another's day is
// noted: both stay.
func TestProvidersMoveKeepsACapSetMeanwhile(t *testing.T) {
	providersLockHome(t)
	interleave(t, func() {
		if err := moveAccountSettings("codex", map[string]string{"new@example.com": "renamed@example.com"}); err != nil {
			t.Error(err)
		}
	}, func() {
		noteAdded([]loginAccount{{"codex", "other@example.com"}}, nil, nil)
	})
	if accountAdded("codex", "other@example.com").IsZero() {
		t.Error("the day noted for other@example.com was written over")
	}
	if accountAdded("codex", "renamed@example.com").IsZero() {
		t.Error("new@example.com's day didn't move to its new name")
	}
}
