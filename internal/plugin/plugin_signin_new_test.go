package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newSignInSandbox is a sandbox with the fake plugin added, and a reader
// of the sign-ins it keeps.
func newSignInSandbox(t *testing.T) (context.Context, func() map[string]map[string]any) {
	t.Helper()
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	return ctx, func() map[string]map[string]any {
		t.Helper()
		var m map[string]map[string]any
		b, _ := os.ReadFile(AuthPath())
		json.Unmarshal(b, &m)
		return m
	}
}

// A sign-in to a new account only, an area's (T-221), to an account
// magpie has is ErrKnown, and that account's tokens stay as they were:
// a sign-in as the owner's (Finish after Authorize) replaces them. Here
// the new sign-in's tokens differ (r-a/u1, not r-a), so it is the account
// the plugin names, not the secret, that tells it is known.
func TestSignInNewLeavesAKnownAccount(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	signIn := func(fresh bool, team string) (Saved, error) {
		t.Helper()
		in := map[string]string{"where": "work", "team": team}
		var a Authorization
		var err error
		if fresh {
			a, err = AuthorizeNew(ctx, "fakeco", 1, in)
		} else {
			a, err = Authorize(ctx, "fakeco", 1, in, NewAccount)
		}
		if err != nil {
			t.Fatal(err)
		}
		return Finish(ctx, a.Session, "good")
	}
	first, err := signIn(false, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(true, "a/u1"); !errors.Is(err, ErrKnown) {
		t.Fatalf("a new sign-in to a@fake: %v, not ErrKnown", err)
	}
	if m := saved(); len(m) != 1 || m[first.Account]["refresh"] != "r-a" {
		t.Fatalf("the known account was changed: %v", m)
	}
	// another account is kept beside it, where the plugin names it
	other, err := signIn(true, "b")
	if err != nil {
		t.Fatal(err)
	}
	if other.Provider != "fakeco" || other.Account == first.Account {
		t.Fatalf("b saved as %+v", other)
	}
	if m := saved(); len(m) != 2 || m[other.Account]["accountId"] != "b@fake" || m[first.Account]["refresh"] != "r-a" {
		t.Fatalf("after b %v", m)
	}
	// the owner's sign-in again still replaces its own
	if again, err := signIn(false, "a/u1"); err != nil || again != first {
		t.Fatalf("a again as the owner: %+v %v, not %+v", again, err, first)
	}
	if m := saved(); m[first.Account]["refresh"] != "r-a/u1" {
		t.Fatalf("the owner's sign-in again didn't replace its own: %v", m)
	}
}

// A page's sign-in to a new account only whose plugin names no account
// is ErrUnnamed and saves nothing: its fresh tokens match no account
// magpie has, so a second record of one it has would be kept.
func TestSignInNewUnnamedSavesNothing(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	// team "=": accountId and uid "", as Grok's plugin names a sign-in
	// whose CLI said no email
	a, err := AuthorizeNew(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "="})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, a.Session, "good"); !errors.Is(err, ErrUnnamed) {
		t.Fatalf("an unnamed sign-in: %v, not ErrUnnamed", err)
	}
	if m := saved(); len(m) != 0 {
		t.Fatalf("an unnamed sign-in was saved: %v", m)
	}
}

// What a plugin writes itself while it signs in to a new account only is
// held: the CodeArts plugin's sign-in writes its account's record by its
// id, over the one magpie has. The owner's sign-in still writes it.
func TestSignInNewHoldsThePluginsOwnWrites(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	kept := map[string]any{"type": "oauth", "refresh": "r-x", "access": "", "expires": 0, "accountId": "x@fake"}
	b, _ := json.Marshal(map[string]any{"fakeco#x": kept})
	os.MkdirAll(filepath.Dir(AuthPath()), 0o700)
	if err := os.WriteFile(AuthPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	in := map[string]string{"where": "work", "team": "write-x"}
	a, err := AuthorizeNew(ctx, "fakeco", 1, in)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Finish(ctx, a.Session, "good")
	if err != nil {
		t.Fatal(err)
	}
	if m := saved(); len(m) != 2 || m["fakeco#x"]["refresh"] != "r-x" || m[s.Account]["accountId"] != "write-x@fake" {
		t.Fatalf("the plugin's own write went through: %v", m)
	}
	a, err = Authorize(ctx, "fakeco", 1, in, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, a.Session, "good"); err != nil {
		t.Fatal(err)
	}
	if m := saved(); m["fakeco#x"]["refresh"] != "r-written" {
		t.Fatalf("the owner's sign-in lost the plugin's write: %v", m)
	}
}

// A key to a new account only that magpie has is ErrKnown and stays as
// it was; another key is kept beside it.
func TestAPIKeyNewLeavesAKnownKey(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	first, err := APIKey(ctx, "fakeco", 0, map[string]string{"note": "owner"}, "sk-one", NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := APIKeyNew(ctx, "fakeco", 0, map[string]string{"note": "area"}, "sk-one"); !errors.Is(err, ErrKnown) {
		t.Fatalf("a key magpie has: %v, not ErrKnown", err)
	}
	m := saved()
	if md, _ := m[first.Account]["metadata"].(map[string]any); len(m) != 1 || md["note"] != "owner" {
		t.Fatalf("the known key was changed: %v", m)
	}
	other, err := APIKeyNew(ctx, "fakeco", 0, nil, "sk-two")
	if err != nil {
		t.Fatal(err)
	}
	if m := saved(); len(m) != 2 || m[other.Account]["key"] != "sk-two" {
		t.Fatalf("after another key %v", m)
	}
}

// A plugin that keeps one account (single) takes no new one while it has
// one: its sign-in goes to that one's place.
func TestAPIKeyNewSingleHasOne(t *testing.T) {
	t.Setenv("FAKE_KEYLESS", "1")
	ctx, saved := newSignInSandbox(t)
	if _, err := APIKeyNew(ctx, "fakeco", 0, map[string]string{"credential": "c-one"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKeyNew(ctx, "fakeco", 0, map[string]string{"credential": "c-two"}, ""); !errors.Is(err, ErrKnown) {
		t.Fatalf("a second account of a single plugin: %v, not ErrKnown", err)
	}
	if m := saved(); len(m) != 1 || m["fakeco"]["key"] != "c-one" {
		t.Fatalf("the single plugin's account was changed: %v", m)
	}
}

// The owner's sign-in and an area's begun side by side are never handed
// one key: the one to finish last wrote over the other's account, and an
// area's claimed key held the owner's.
func TestSignInNewBesideTheOwnersKeepsBoth(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	owner, err := Authorize(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "o"}, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	area, err := AuthorizeNew(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "n"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Finish(ctx, area.Session, "good")
	if err != nil {
		t.Fatal(err)
	}
	o, err := Finish(ctx, owner.Session, "good")
	if err != nil {
		t.Fatal(err)
	}
	if m := saved(); len(m) != 2 || m[a.Account]["accountId"] != "n@fake" || m[o.Account]["accountId"] != "o@fake" {
		t.Fatalf("area %+v owner %+v: %v", a, o, m)
	}
}

// A key to a new account only never takes its name from the answers:
// an area typing another's email would have the owner's sign-in to it
// folded into the area's account.
func TestAPIKeyNewTakesNoNameFromInputs(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	a, err := Authorize(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "boss"}, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := Finish(ctx, a.Session, "good")
	if err != nil {
		t.Fatal(err)
	}
	s, err := APIKeyNew(ctx, "fakeco", 0, map[string]string{"email": "boss@fake", "accountId": "boss@fake", "cli": "true"}, "sk-junk")
	if err != nil {
		t.Fatal(err)
	}
	m := saved()
	if md, _ := m[s.Account]["metadata"].(map[string]any); len(md) != 0 {
		t.Fatalf("the area's key was kept with %v", md)
	}
	// the owner's sign-in again goes to its own account, not the area's
	a, err = Authorize(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "boss"}, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := Finish(ctx, a.Session, "good"); err != nil || again != owner {
		t.Fatalf("the owner's again as %+v %v, not %+v", again, err, owner)
	}
	if m := saved(); m[s.Account]["key"] != "sk-junk" {
		t.Fatalf("the area's account was written over: %v", m)
	}
}

// What a sign-in to a new account only starts is held only until the
// account is kept: then a timer it started renews its own account.
func TestSignInNewLetsItsWritesThroughOnceKept(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	a, err := AuthorizeNew(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "write-later"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Finish(ctx, a.Session, "good")
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); saved()[s.Account]["refresh"] != "r-later"; {
		if time.Now().After(deadline) {
			t.Fatalf("the renewal after the sign-in was dropped: %v", saved())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A key with nothing to tell it by (no key, no name) is unnamed: each one
// would be kept as another account.
func TestAPIKeyNewEmptyIsUnnamed(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	if _, err := APIKeyNew(ctx, "fakeco", 0, nil, ""); !errors.Is(err, ErrUnnamed) {
		t.Fatalf("an empty key: %v, not ErrUnnamed", err)
	}
	if m := saved(); len(m) != 0 {
		t.Fatalf("saved %v", m)
	}
}
