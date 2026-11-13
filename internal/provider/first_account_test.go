package provider

import (
	"os"
	"testing"
)

// 需求方 2026-11-13: the first account of a new install was never held by
// the growth policy, nor did its row show one: logins.json's first write
// takes every account in it for one from before (writeLogins), so the
// account added by that write had no AccountAdded. One a sign-in in magpie
// adds is new, first or not (noteSignedIn); and the proxy picked for a
// setup-token's account is kept as a sign-in's is (KeepSignInProxy).
func TestFirstAccountIsNew(t *testing.T) {
	claudeHome(t)
	forgetAccountCaches()
	if _, err := os.Stat(loginsPath()); err == nil {
		t.Fatal("the sandbox has a logins.json already")
	}
	user, err := AddClaudeSetupToken(testSetupToken)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := storedPicks("claude")
	if !addedKnown(s.AccountAdded[accountKey(user)]) {
		t.Fatalf("the first account %q has no time it was added (%v): the growth policy never holds it", user, s.AccountAdded)
	}
	if err := KeepSignInProxy("claude", user, "socks5://u:p@203.0.113.7:1080"); err != nil {
		t.Fatal(err)
	}
	if got := ProxyOfLogin("claude", user); got != "socks5://u:p@203.0.113.7:1080" {
		t.Fatalf("its proxy is %q", got)
	}

	// an account listed already, signed in again, keeps its time
	at := s.AccountAdded[accountKey(user)]
	noteSignedIn("claude", user)
	if s, _ := storedPicks("claude"); !s.AccountAdded[accountKey(user)].Equal(at) {
		t.Fatal("signing in again moved the time it was added")
	}
}

// The same for the first account signed in through Claude Code's own
// sign-in, which becomes the one Claude Code itself uses: the account
// the user called the default one.
func TestFirstSignedInAccountIsNew(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	forgetAccountCaches()
	fakeClaudeLogin(t, fakeClaudeAccount{email: "first@example.com", plan: "max", refresh: "sk-ant-ort01-first"}, "", false)
	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, "the-code#st"); err != nil {
		t.Fatal(err)
	}
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "first@example.com" || !st.Using {
		t.Fatalf("state %+v", st)
	}
	if s, _ := storedPicks("claude"); !addedKnown(s.AccountAdded["first@example.com"]) {
		t.Fatalf("the first account signed in has no time it was added (%v)", s.AccountAdded)
	}
}
