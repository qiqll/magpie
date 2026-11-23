package provider

import "testing"

// An account's card that can't be used now is told so for the Overview to
// leave out: one marked banned or disabled, and one read as not signed in.
// Usable accounts, a key's card and a remote magpie's card are left as
// they were, and the cards handed in are not written to.
func TestWithUnusableMarksBannedAndSignedOut(t *testing.T) {
	home := signIn(t)
	rememberLogins(true) // me@ saved…
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true) // …and codex is on work@ now
	codexSignIn(t, home, "spare@example.com", "r-spare")
	rememberLogins(true)
	if err := SetLoginBanned("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginDisabled("codex", "WORK@example.com", true); err != nil {
		t.Fatal(err)
	}
	ForgetAccounts()
	in := []SubscriptionQuota{
		{Provider: "codex", User: "ME@example.com"},
		{Provider: "codex", User: "work@example.com"},
		{Provider: "codex", User: "spare@example.com"},
		{Provider: "copilot", User: "gh", Error: errNotSignedIn},
		{Provider: "deepseek", Balance: "$3"},
		{Provider: "codex", User: "me@example.com", From: "office"},
	}
	got := WithUnusable(in)
	want := []bool{true, true, false, true, false, false}
	for i, q := range got {
		if q.Unusable != want[i] {
			t.Errorf("card %d (%s %s from %q): unusable %v, want %v", i, q.Provider, q.User, q.From, q.Unusable, want[i])
		}
	}
	for i, q := range in {
		if q.Unusable {
			t.Fatalf("card %d handed in was written to", i)
		}
	}
}
