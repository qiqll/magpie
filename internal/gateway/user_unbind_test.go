package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
)

// An account let go of by hand (the Gateway page's Unbind): the user's
// spare is its first, and its next request goes there; a user left with
// none is bound afresh on its next request; one not bound to it, or of
// another provider, is left alone; a file that doesn't read is an error
// and left as it is.
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
	// of another provider: not hers (a gateway key's is hers under every
	// key: TestUserUnboundUnderEveryKey)
	if found, err := Unbind("", "claude", "alice", "codex@spare@example.com"); found || err != nil {
		t.Fatalf("claude: %v %v", found, err)
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

// Another magpie writing the file between this one's read and its write
// (the window's Unbind while the gateway binds, in another process, whose
// userBinds isn't this one's): this one's write is refused, the other's
// change kept, and the next read takes it.
func TestUserFileNotWrittenOverAnotherMagpiesChange(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	two := userBinding{Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}}
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {"alice": two}}})
	s := byUserUpTo(t, New(), 2)

	userBinds.Lock()
	if err := loadUsers(); err != nil {
		userBinds.Unlock()
		t.Fatal(err)
	}
	// the other magpie lets go of alice's first meanwhile
	theirs := mustJSON(userFile{Users: map[string]map[string]userBinding{"codex": {"alice": bindingOf(two.seats()[1:])}}})
	if err := os.WriteFile(usersPath(), theirs, 0o600); err != nil {
		userBinds.Unlock()
		t.Fatal(err)
	}
	// this one keeps her tier, on what it read before
	b := userBinds.m[""]["codex"]["alice"]
	b.Tier = "free"
	userBinds.m[""]["codex"]["alice"] = b
	err := saveUsers()
	userBinds.Unlock()
	if !errors.Is(err, errUsersChanged) {
		t.Fatalf("written over another magpie's change: %v", err)
	}
	if got, _ := os.ReadFile(usersPath()); !bytes.Equal(got, theirs) {
		t.Fatalf("the file: %s", got)
	}
	// read again: alice is on her spare alone
	rec := userPost(s, "codex/gpt-5.5", "alice", "hi")
	if r := lastRoute(s); rec.Code != 200 || r.User == nil || r.User.Account != "spare@example.com" || len(r.User.Spares) != 0 {
		t.Fatalf("alice: %d %+v asked %v", rec.Code, r.User, asked(false))
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 1 || got[0] != "spare@example.com" {
		t.Fatalf("alice on %v", got)
	}
}

// A file that can't be written: Unbind says why, and the binding stays
// as the file has it, in memory too, so it can be let go of once it can.
// Another magpie's write between its read and its write is read again
// and the change made on it.
func TestUserUnbindWriteFails(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com"},
		"bob":   {Who: "codex@me@example.com", Account: "me@example.com"},
	}}})
	was := writeUsersFile
	t.Cleanup(func() { writeUsersFile = was })
	writeUsersFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	if found, err := Unbind("", "codex", "alice", "codex@me@example.com"); found || err == nil || err.Error() != "disk full" {
		t.Fatalf("unwritable: %v %v", found, err)
	}
	writeUsersFile = was
	if found, err := Unbind("", "codex", "alice", "codex@me@example.com"); !found || err != nil {
		t.Fatalf("written once it can: %v %v", found, err)
	}
	// bob's written by another magpie as Unbind writes: read again
	writes := 0
	writeUsersFile = func(path string, b []byte, mode os.FileMode) error {
		if writes++; writes == 1 {
			other := mustJSON(userFile{Users: map[string]map[string]userBinding{"codex": {
				"bob":   {Who: "codex@me@example.com", Account: "me@example.com"},
				"carol": {Who: "codex@spare@example.com", Account: "spare@example.com"},
			}}})
			if err := os.WriteFile(usersPath(), other, 0o600); err != nil {
				return err
			}
			return errUsersChanged
		}
		return was(path, b, mode)
	}
	if found, err := Unbind("", "codex", "bob", "codex@me@example.com"); !found || err != nil || writes != 2 {
		t.Fatalf("bob: %v %v after %d writes", found, err, writes)
	}
	if got := seatsIn(t, "codex"); len(got) != 1 || len(got["carol"]) != 1 {
		t.Fatalf("the other magpie's carol lost, or bob kept: %v", got)
	}
}
