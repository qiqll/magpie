package gateway

import (
	"errors"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// sessionPostWith asks for codex/gpt-5.5 as user, in the conversation
// session, with the gateway key secret.
func sessionPostWith(s *Server, secret, user, session, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("session_id", session)
	req.Header.Set(UserHeader, user)
	lanGuard(s.Handler()).ServeHTTP(rec, req)
	return rec
}

// keySeats is the accounts user is bound to for codex under the gateway
// key keyID, as user-accounts.json has them, the first first.
func keySeats(t *testing.T, keyID, user string) []string {
	t.Helper()
	var out []string
	for _, s := range boundOf(t, keyID, "codex", user).seats() {
		out = append(out, s.Account)
	}
	return out
}

// The reporter's case (T-075): one user's conversation comes in under two
// gateway keys, turn by turn. Bound by user, each turn goes to the one
// account it was bound to under the first key, never to the account
// another key's binding would have picked (fewest users on it), so its
// cache stays warm.
func TestUserOneSessionUnderTwoKeysOneAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	keys, secrets := newCaller(t, "affd", "6acb")
	shareUsers(t, keys[0].ID, keys[1].ID) // the owner's front ends, trusted to name users (T-089)
	// no share: a user new to a key's bindings went to the account fewest
	// users are on, which its own seat under the other key made another
	s := byUserUpTo(t, New(), 2)
	for i, secret := range []string{secrets[0], secrets[1], secrets[0], secrets[1]} {
		if rec := sessionPostWith(s, secret, "kai", "sess-1", "turn"); rec.Code != 200 {
			t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	got := asked(true)
	if len(got) != 4 || slices.ContainsFunc(got, func(a string) bool { return a != got[0] }) {
		t.Fatalf("kai's turns went to %v, not one account", got)
	}
	if r := lastRoute(s); r.User == nil || r.User.Why != "kept" || len(r.User.Spares) != 0 {
		t.Fatalf("route %+v", r.User)
	}
}

// Bindings a magpie before wrote apart, one a key, are read as one: the
// first is the first of the one used last, the others' accounts after
// it as spares, each once, and none added past the tier's one account
// (they are kept). The next write (its use, an hour old) writes it so
// under both keys, each keeping its own tier.
func TestUserBindingsOfTwoKeysMergedByLastUse(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	keys, secrets := newCaller(t, "affd", "6acb")
	shareUsers(t, keys[0].ID, keys[1].ID)
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"codex": {"kai": {Who: "codex@me@example.com", Account: "me@example.com", At: ago(days(2)), Used: ago(5 * time.Hour), Tier: "pro",
			More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: ago(days(1))}}}}},
		keys[1].ID: {"codex": {"kai": {Who: "codex@spare@example.com", Account: "spare@example.com", At: ago(days(1)), Used: ago(2 * time.Hour)}}},
	}})
	s := byUser(New())
	if rec := userPostWith(s, secrets[0], "kai", "hi"); rec.Code != 200 {
		t.Fatalf("kai: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("kai asked %v, not spare@ (acct-2), used last", got)
	}
	r := lastRoute(s).User
	if r == nil || r.Why != "kept" || r.Account != "spare@example.com" || !slices.Equal(r.Spares, []string{"me@example.com", "third@example.com"}) {
		t.Fatalf("route %+v", r)
	}
	want := []string{"spare@example.com", "me@example.com", "third@example.com"}
	for i, k := range keys {
		if got := keySeats(t, k.ID, "kai"); !slices.Equal(got, want) {
			t.Fatalf("under %s: %v", k.Name, got)
		}
		b := boundOf(t, k.ID, "codex", "kai")
		if !near(b.Used, 0) {
			t.Fatalf("under %s used %v", k.Name, b.Used)
		}
		if tier := []string{"pro", ""}[i]; b.Tier != tier {
			t.Fatalf("under %s tier %q", k.Name, b.Tier)
		}
	}
}

// Two bindings last used alike: the first is the one bound earliest's,
// whatever order the keys come in.
func TestUserBindingsUsedAlikeMergedByEarliest(t *testing.T) {
	codexSignedIn(t)
	used := ago(time.Hour)
	for _, keys := range [][2]string{{"a", "b"}, {"b", "a"}} {
		userBinds.Lock()
		was, wasSharers := userBinds.m, userBinds.sharers
		userBinds.sharers = map[string]bool{"a": true, "b": true} // both share their users (T-089)
		userBinds.m = map[string]map[string]map[string]userBinding{
			keys[0]: {"codex": {"kai": {Who: "codex@late@x.com", Account: "late@x.com", At: ago(time.Hour), Used: used}}},
			keys[1]: {"codex": {"kai": {Who: "codex@early@x.com", Account: "early@x.com", At: ago(days(1)), Used: used}}},
		}
		b, ok := sharedOf(keys[0], "codex", "kai")
		userBinds.m, userBinds.sharers = was, wasSharers
		userBinds.Unlock()
		if seats := accountsOf(b.seats()); !ok || !slices.Equal(seats, []string{"early@x.com", "late@x.com"}) {
			t.Fatalf("%v: %v %v", keys, ok, seats)
		}
	}
}

// An account let go of by hand is let go of under every gateway key the
// user is bound under that shares its users, and without one: it is one
// user.
func TestUserUnboundUnderEveryKey(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	sharing(t, "k1")
	two := func() userBinding {
		return userBinding{Who: "codex@me@example.com", Account: "me@example.com", At: ago(time.Hour), Used: ago(time.Hour),
			More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com", At: ago(time.Hour)}}}
	}
	writeUsers(t, userFile{
		Users: map[string]map[string]userBinding{"codex": {"alice": two()}},
		Keys:  map[string]map[string]map[string]userBinding{"k1": {"codex": {"alice": two()}}},
	})
	if found, err := Unbind("", "codex", "alice", "codex@me@example.com"); !found || err != nil {
		t.Fatalf("%v %v", found, err)
	}
	for _, k := range []string{"", "k1"} {
		if got := keySeats(t, k, "alice"); !slices.Equal(got, []string{"spare@example.com"}) {
			t.Fatalf("under %q: %v", k, got)
		}
	}
}

// A file that can't be written leaves the user bound under every key as
// it was, in memory too, its only account kept under each: the next
// Unbind, once it can be written, finds it and lets go under both.
func TestUserUnbindUnderEveryKeyWriteFails(t *testing.T) {
	codexSignedIn(t)
	sharing(t, "k1")
	one := func() userBinding {
		return userBinding{Who: "codex@me@example.com", Account: "me@example.com", At: ago(time.Hour), Used: ago(time.Hour)}
	}
	writeUsers(t, userFile{
		Users: map[string]map[string]userBinding{"codex": {"alice": one()}},
		Keys:  map[string]map[string]map[string]userBinding{"k1": {"codex": {"alice": one()}}},
	})
	was := writeUsersFile
	t.Cleanup(func() { writeUsersFile = was })
	writeUsersFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	if found, err := Unbind("", "codex", "alice", "codex@me@example.com"); found || err == nil {
		t.Fatalf("unwritable: %v %v", found, err)
	}
	userBinds.Lock()
	for _, k := range []string{"", "k1"} {
		if b, ok := userBinds.m[k]["codex"]["alice"]; !ok || b.Who != "codex@me@example.com" {
			userBinds.Unlock()
			t.Fatalf("under %q after a failed write: %+v %v", k, b, ok)
		}
	}
	userBinds.Unlock()
	writeUsersFile = was
	if found, err := Unbind("", "codex", "alice", "codex@me@example.com"); !found || err != nil {
		t.Fatalf("written once it can: %v %v", found, err)
	}
	f := usersFile(t)
	if len(f.Users) != 0 || len(f.Keys) != 0 {
		t.Fatalf("left under a key, or an empty provider kept: %+v", f)
	}
}

// A binding a magpie before wrote with no Used, beside one used an hour
// ago under another key: another user's request (sweepIdle) stamps it
// with the user's latest use, not now, so its first stays the one used
// last and the conversation doesn't move.
func TestUserUnstampedBindingKeepsTheFirst(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	keys, secrets := newCaller(t, "affd", "6acb")
	shareUsers(t, keys[0].ID, keys[1].ID)
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"codex": {"kai": {Who: "codex@me@example.com", Account: "me@example.com", At: ago(days(2))}}},
		keys[1].ID: {"codex": {"kai": {Who: "codex@spare@example.com", Account: "spare@example.com", At: ago(days(1)), Used: ago(time.Hour)}}},
	}})
	s := byUser(New())
	if rec := userPostWith(s, secrets[0], "dave", "dave's"); rec.Code != 200 {
		t.Fatalf("dave: %d %s", rec.Code, rec.Body.String())
	}
	userBinds.Lock()
	b, _ := sharedOf(keys[0].ID, "codex", "kai")
	userBinds.Unlock()
	if b.Account != "spare@example.com" {
		t.Fatalf("kai's first after the sweep: %+v", b)
	}
}
