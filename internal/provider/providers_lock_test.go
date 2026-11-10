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
	// set and read under providersMu, as store is called under it
	setWriteHook(func() {
		beforeProvidersWrite = func() {}
		started = true
		go func() {
			defer close(done)
			second()
		}()
		select {
		case <-done:
			t.Error("the second change wrote while the first held providers.json")
		case <-time.After(2 * time.Second):
		}
	})
	t.Cleanup(func() { setWriteHook(func() {}) })
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

func setWriteHook(f func()) {
	if err := providersMu.Take(); err != nil {
		panic(err)
	}
	beforeProvidersWrite = f
	providersMu.Unlock()
}

// providersLockHome is a home with two codex accounts, looked at just now,
// so no change comes but the test's.
func providersLockHome(t *testing.T) {
	t.Helper()
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
}

// Caps the user sets on two accounts of one subscription, one
// while the other is being written, are both kept.
func TestProvidersTwoCapsBothKept(t *testing.T) {
	providersLockHome(t)
	interleave(t, func() {
		if err := SetAccountCap("codex", "me@example.com", 70); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := SetAccountCap("codex", "new@example.com", 50); err != nil {
			t.Error(err)
		}
	})
	if s, _ := AccountCapsOf("codex", "me@example.com").Of("5 hours"); s != 70 {
		t.Errorf("me@example.com's 70%% cap read as %d%%: written over", s)
	}
	if s, _ := AccountCapsOf("codex", "new@example.com").Of("5 hours"); s != 50 {
		t.Errorf("new@example.com's 50%% cap read as %d%%: written over", s)
	}
}

// A provider the user adds while a cap is written to another is
// kept, and so is the cap.
func TestProvidersAddKeptWhileCapWritten(t *testing.T) {
	providersLockHome(t)
	interleave(t, func() {
		if err := SetAccountCap("codex", "me@example.com", 70); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-relay", Models: []string{"m"}}); err != nil {
			t.Error(err)
		}
	})
	if !stored("relay") {
		t.Error("the provider added meanwhile is gone from providers.json: written over")
	}
	if s, _ := AccountCapsOf("codex", "me@example.com").Of("5 hours"); s != 70 {
		t.Errorf("me@example.com's 70%% cap read as %d%%: written over", s)
	}
}

// Settings moved to an account's new name while another's cap is set:
// both stay.
func TestProvidersMoveKeepsACapSetMeanwhile(t *testing.T) {
	providersLockHome(t)
	if err := SetAccountCap("codex", "new@example.com", 60); err != nil {
		t.Fatal(err)
	}
	interleave(t, func() {
		if err := moveAccountSettings("codex", map[string]string{"new@example.com": "renamed@example.com"}); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := SetAccountCap("codex", "me@example.com", 70); err != nil {
			t.Error(err)
		}
	})
	if s, _ := AccountCapsOf("codex", "me@example.com").Of("5 hours"); s != 70 {
		t.Errorf("me@example.com's 70%% cap read as %d%%: written over", s)
	}
	if s, _ := AccountCapsOf("codex", "renamed@example.com").Of("5 hours"); s != 60 {
		t.Errorf("new@example.com's 60%% cap didn't move to its new name: %d%%", s)
	}
}
