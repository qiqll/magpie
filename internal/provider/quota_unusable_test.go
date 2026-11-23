package provider

import (
	"testing"
	"time"
)

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

	// read as signed out by the card itself; not Codex's own token, which
	// Codex renews as it runs
	got = WithUnusable([]SubscriptionQuota{
		{Provider: "codex", User: "spare@example.com", Error: "spare@example.com's sign-in was revoked; sign in again"},
		{Provider: "codex", User: "spare@example.com", Error: "spare@example.com's sign-in has expired — add the account again to use it"},
		{Provider: "copilot", User: "gh", Error: "GitHub refused this Copilot sign-in; sign in again"},
		{Provider: "codex", User: "spare@example.com", Error: "Codex's sign-in has expired; Codex renews it as it runs"},
	})
	for i, want := range []bool{true, true, true, false} {
		if got[i].Unusable != want {
			t.Errorf("signed-out card %d (%q): unusable %v, want %v", i, got[i].Error, got[i].Unusable, want)
		}
	}

}

// A name is unusable only when every saved entry of it is marked: two
// entries of one name (a plugin's two slots, an agent's own beside
// magpie's) with one banned leave the card in sight (43a65950).
func TestMarkedAllNeedsEveryEntry(t *testing.T) {
	banned := savedLogin{Agent: "plugin:acme", User: "Me", Banned: true, Home: "a"}
	other := savedLogin{Agent: "plugin:acme", User: "me", Home: "b"}
	elsewhere := savedLogin{Agent: "plugin:else", User: "me"}
	for _, c := range []struct {
		ls   []savedLogin
		want bool
	}{
		{[]savedLogin{banned}, true},
		{[]savedLogin{banned, other}, false},
		{[]savedLogin{other, banned}, false},
		{[]savedLogin{banned, {Agent: "plugin:acme", User: "ME", Disabled: true}}, true},
		{[]savedLogin{banned, {Agent: "plugin:acme", User: "me", AuthFailedAt: time.Unix(1, 0)}}, true},
		{[]savedLogin{elsewhere}, false},
		{nil, false},
	} {
		if got := markedAll(c.ls, "plugin:acme", "me"); got != c.want {
			t.Errorf("%+v: %v, want %v", c.ls, got, c.want)
		}
	}
}
