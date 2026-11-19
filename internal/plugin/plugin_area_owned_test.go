package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// areaOwns has AreaOwned give keys (ok false: can't be told) for the
// test, as internal/provider gives what areas.json owns.
func areaOwns(t *testing.T, ok bool, keys ...string) {
	t.Helper()
	was := AreaOwned
	t.Cleanup(func() { AreaOwned = was })
	AreaOwned = func() ([]string, bool) { return keys, ok }
}

// The owner's sign-in to an account an area owns is ErrAreaOwned (T-228):
// the area's tokens stay as they were and nothing of the owner's is left
// beside them. With the account not the area's, the same sign-in folds
// into it as it always has, so it is the area's ownership that holds it.
func TestSignInOwnerLeavesAnAreasAccount(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	a, err := AuthorizeNew(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "a"})
	if err != nil {
		t.Fatal(err)
	}
	area, err := Finish(ctx, a.Session, "good")
	if err != nil {
		t.Fatal(err)
	}
	owner := func() (Saved, error) {
		t.Helper()
		a, err := Authorize(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "a/u1"}, NewAccount)
		if err != nil {
			t.Fatal(err)
		}
		return Finish(ctx, a.Session, "good")
	}
	areaOwns(t, true, area.Account)
	if s, err := owner(); !errors.Is(err, ErrAreaOwned) {
		t.Fatalf("the owner's sign-in to the area's account: %+v %v, not ErrAreaOwned", s, err)
	}
	if m := saved(); len(m) != 1 || m[area.Account]["refresh"] != "r-a" {
		t.Fatalf("the area's account was changed: %v", m)
	}
	// areas.json unread: every account is kept
	areaOwns(t, false)
	if _, err := owner(); !errors.Is(err, ErrAreaOwned) {
		t.Fatalf("with areas unknown: %v, not ErrAreaOwned", err)
	}
	if m := saved(); len(m) != 1 || m[area.Account]["refresh"] != "r-a" {
		t.Fatalf("with areas unknown the account was changed: %v", m)
	}
	// no area owns it: the owner's sign-in replaces it, as before
	areaOwns(t, true, "fakeco#other")
	if s, err := owner(); err != nil || s != area {
		t.Fatalf("the owner's sign-in to an account no area owns: %+v %v, not %+v", s, err, area)
	}
	if m := saved(); len(m) != 1 || m[area.Account]["refresh"] != "r-a/u1" {
		t.Fatalf("the owner's sign-in didn't replace it: %v", m)
	}
}

// The owner's key to an account an area owns, the same key, is
// ErrAreaOwned, the area's record as it was.
func TestSignInOwnerKeyLeavesAnAreasKey(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	area, err := APIKeyNew(ctx, "fakeco", 0, map[string]string{"note": "area"}, "sk-one")
	if err != nil {
		t.Fatal(err)
	}
	areaOwns(t, true, area.Account)
	if s, err := APIKey(ctx, "fakeco", 0, map[string]string{"note": "owner"}, "sk-one", NewAccount); !errors.Is(err, ErrAreaOwned) {
		t.Fatalf("the owner's key that is the area's: %+v %v, not ErrAreaOwned", s, err)
	}
	m := saved()
	if md, _ := m[area.Account]["metadata"].(map[string]any); len(m) != 1 || md["note"] != "area" {
		t.Fatalf("the area's key was changed: %v", m)
	}
	// another key is the owner's own, beside it
	if s, err := APIKey(ctx, "fakeco", 0, nil, "sk-two", NewAccount); err != nil || s.Account == area.Account {
		t.Fatalf("another key: %+v %v", s, err)
	}
}

// What a plugin writes itself while the owner signs in (the CodeArts
// plugin's record by its id) leaves an account an area owns alone.
func TestSignInOwnerWritesNoAreasRecord(t *testing.T) {
	ctx, saved := newSignInSandbox(t)
	kept := map[string]any{"type": "oauth", "refresh": "r-x", "access": "", "expires": 0, "accountId": "x@fake"}
	b, _ := json.Marshal(map[string]any{"fakeco#x": kept})
	os.MkdirAll(filepath.Dir(AuthPath()), 0o700)
	if err := os.WriteFile(AuthPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	areaOwns(t, true, "fakeco#x")
	a, err := Authorize(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "write-x"}, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Finish(ctx, a.Session, "good")
	if err != nil {
		t.Fatal(err)
	}
	if m := saved(); len(m) != 2 || m["fakeco#x"]["refresh"] != "r-x" || m[s.Account]["accountId"] != "write-x@fake" {
		t.Fatalf("the plugin's own write took the area's account: %v", m)
	}
}

// A plugin that keeps one account (single) whose one an area owns: the
// owner's key would go to its place, and is ErrAreaOwned at once.
func TestSignInOwnerSingleLeavesTheAreas(t *testing.T) {
	t.Setenv("FAKE_KEYLESS", "1")
	ctx, saved := newSignInSandbox(t)
	area, err := APIKeyNew(ctx, "fakeco", 0, map[string]string{"credential": "c-area"}, "")
	if err != nil {
		t.Fatal(err)
	}
	areaOwns(t, true, area.Account)
	if _, err := APIKey(ctx, "fakeco", 0, map[string]string{"credential": "c-owner"}, "", NewAccount); !errors.Is(err, ErrAreaOwned) {
		t.Fatalf("the owner's key to a single plugin an area has: %v, not ErrAreaOwned", err)
	}
	if m := saved(); len(m) != 1 || m[area.Account]["key"] != "c-area" {
		t.Fatalf("the area's one account was changed: %v", m)
	}
}
