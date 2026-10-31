package gateway

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"testing"
)

// An account let go of by hand (the Gateway page's Unbind): the user's
// spare is its first, and its next request goes there; a user left with
// none is bound afresh on its next request; one not bound to it, or of
// another gateway key, is left alone; a file that doesn't read is an
// error and left as it is.
func TestUserUnboundByHand(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
		"bob":   {Who: "codex@me@example.com", Account: "me@example.com"},
	}}})
	s := byUserUpTo(t, New(), 2) // no share: as many users as come on an account
	if rec := userPost(s, "codex/gpt-5.5", "alice", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	onFirst := asked(true)
	if len(onFirst) != 1 {
		t.Fatalf("alice asked %v", onFirst)
	}

	// her first let go of, named in another case: her spare is her first
	if found, err := Unbind("", "codex", "alice", "CODEX@Me@example.com"); !found || err != nil {
		t.Fatalf("alice's first: %v %v", found, err)
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 1 || got[0] != "spare@example.com" {
		t.Fatalf("alice on %v", got)
	}
	rec := userPost(s, "codex/gpt-5.5", "alice", "again")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] == onFirst[0] {
		t.Fatalf("alice after her first was let go: %d asked %v (her first %v)", rec.Code, got, onFirst)
	}
	if r := lastRoute(s); r.User == nil || r.User.Why != "kept" || r.User.Account != "spare@example.com" {
		t.Fatalf("route %+v", r.User)
	}
	// let go of already: nothing found, nothing written
	if found, err := Unbind("", "codex", "alice", "codex@me@example.com"); found || err != nil {
		t.Fatalf("again: %v %v", found, err)
	}
	// of another gateway key, or another provider: not hers
	for _, c := range [][2]string{{"some-key", "codex"}, {"", "claude"}} {
		if found, err := Unbind(c[0], c[1], "alice", "codex@spare@example.com"); found || err != nil {
			t.Fatalf("%v: %v %v", c, found, err)
		}
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 1 {
		t.Fatalf("alice on %v", got)
	}

	// bob's one account let go of: he is bound afresh on his next request
	if found, err := Unbind("", "codex", "bob", "codex@me@example.com"); !found || err != nil {
		t.Fatalf("bob: %v %v", found, err)
	}
	if _, ok := seatsIn(t, "codex")["bob"]; ok {
		t.Fatalf("bob still bound: %v", seatsIn(t, "codex"))
	}
	if rec := userPost(s, "codex/gpt-5.5", "bob", "hi"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.User == nil || r.User.Why != "bound" {
		t.Fatalf("bob not bound afresh: %+v", r.User)
	}
	if got := seatsIn(t, "codex")["bob"]; len(got) != 1 {
		t.Fatalf("bob on %v", got)
	}

	// a file that doesn't read: an error, and left as it is
	bad := []byte(`{"users": {"codex": {"alice": `)
	if err := os.WriteFile(usersPath(), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := Unbind("", "codex", "alice", "codex@spare@example.com"); found || err == nil {
		t.Fatalf("an unreadable file: %v %v", found, err)
	}
	if b, _ := os.ReadFile(usersPath()); !bytes.Equal(b, bad) {
		t.Fatalf("written over: %s", b)
	}
}

// Accounts let go of by hand while users are bound: each change is made
// on the file as it is then, none lost.
func TestUserUnbindsWhileUsersAreBound(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	us := map[string]userBinding{}
	for i := range 12 {
		us[fmt.Sprint("held-", i)] = userBinding{Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}}
	}
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": us}})
	s := byUserUpTo(t, New(), 2)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if found, err := Unbind("", "codex", fmt.Sprint("held-", i), "codex@me@example.com"); !found || err != nil {
				t.Errorf("held-%d: %v %v", i, found, err)
			}
		}()
		go func() {
			defer wg.Done()
			u := fmt.Sprint("new-", i)
			if rec := userPost(s, "codex/gpt-5.5", u, "hi "+u); rec.Code != 200 {
				t.Errorf("%s: %d %s", u, rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	seats := seatsIn(t, "codex")
	for i := range 12 {
		if got := seats[fmt.Sprint("held-", i)]; len(got) != 1 || got[0] != "spare@example.com" {
			t.Errorf("held-%d on %v", i, got)
		}
		if got := seats[fmt.Sprint("new-", i)]; len(got) != 1 {
			t.Errorf("new-%d on %v", i, got)
		}
	}
}
