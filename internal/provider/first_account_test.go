package provider

import (
	"os"
	"strings"
	"testing"
	"time"
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
	if err := keepSignInProxy("claude", user, "socks5://u:p@203.0.113.7:1080"); err != nil {
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

// An account listed already with no time (one from before) signed in
// again stays from before: a lapsed sign-in renewed doesn't start the
// growth policy holding an account the user relies on (review 1).
func TestSignedInAgainStaysFromBefore(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	forgetAccountCaches()
	fakeClaudeLogin(t, fakeClaudeAccount{email: "old@example.com", plan: "max", refresh: "sk-ant-ort01-old"}, "", false)
	signIn := func() {
		t.Helper()
		st, err := StartSignIn("claude")
		if err != nil {
			t.Fatal(err)
		}
		if err := SubmitSignInCallback(st.ID, "the-code#st"); err != nil {
			t.Fatal(err)
		}
		if st = waitDone(t, st.ID); st.State != "done" || st.User != "old@example.com" {
			t.Fatalf("state %+v", st)
		}
	}
	signIn()
	// made one from before as logins.json's first write leaves it: with
	// no time at all (one marked addedBefore is kept by noteAdded anyway)
	if err := providersMu.Take(); err != nil {
		t.Fatal(err)
	}
	f, err := read()
	if err != nil {
		providersMu.Unlock()
		t.Fatal(err)
	}
	for i := range f.Providers {
		if f.Providers[i].ID == "claude" {
			f.Providers[i].AccountAdded = map[string]time.Time{}
		}
	}
	err = store(f)
	providersMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	signIn()
	if s, _ := storedPicks("claude"); addedKnown(s.AccountAdded["old@example.com"]) {
		t.Fatalf("an account from before, signed in again, was made new: %v", s.AccountAdded)
	}
}

// A setup-token's proxy: none picked leaves the proxies; a bad one, or the
// global one where its subscription has its own and other accounts, is
// refused before the account is added; a token saved already keeps its
// account's proxy (review 2, 4).
func TestSetupTokenProxy(t *testing.T) {
	claudeHome(t)
	forgetAccountCaches()
	tok := func(c string) string { return strings.Replace(testSetupToken, "abcdef", c, 1) }
	user, added, err := AddClaudeSetupTokenThrough(tok("aaaaaa"), nil)
	if err != nil || !added {
		t.Fatalf("%q %v %v", user, added, err)
	}
	if p := ProxyOfLogin("claude", user); p != "" {
		t.Fatalf("none picked, its proxy is %q", p)
	}
	sub := "http://10.0.0.9:3128"
	if _, _, err := AddClaudeSetupTokenThrough(tok("aaaaaa"), &sub); err != nil {
		t.Fatal(err)
	}
	if p := ProxyOfLogin("claude", user); p != "" {
		t.Fatalf("a token saved already had its proxy changed to %q", p)
	}
	// its only account's proxy is the subscription's
	if err := keepSignInProxy("claude", user, sub); err != nil {
		t.Fatal(err)
	}
	n := len(Logins("claude"))
	bad := "ftp://x:1"
	if _, _, err := AddClaudeSetupTokenThrough(tok("bbbbbb"), &bad); err == nil {
		t.Fatal("a bad proxy taken")
	}
	global := ""
	if _, _, err := AddClaudeSetupTokenThrough(tok("cccccc"), &global); err == nil {
		t.Fatal("the global one taken beside its subscription's own")
	}
	if got := len(Logins("claude")); got != n {
		t.Fatalf("a refused proxy left its account added: %d accounts, had %d", got, n)
	}
	own := "socks5://u:p@203.0.113.7:1080"
	two, added, err := AddClaudeSetupTokenThrough(tok("dddddd"), &own)
	if err != nil || !added {
		t.Fatalf("%q %v %v", two, added, err)
	}
	if p := ProxyOfLogin("claude", two); p != own {
		t.Fatalf("its proxy is %q", p)
	}
	if p := ProxyOfLogin("claude", user); p != sub {
		t.Fatalf("the other account's proxy moved to %q", p)
	}
}
