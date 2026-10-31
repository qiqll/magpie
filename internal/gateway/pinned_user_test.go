package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Bound by user, a pin (AccountHeader) holds only to one of its user's
// own accounts (the owner's decision of 2026-10-30, T-009): pinned to
// another user's, the request is served as if unpinned, on its own
// accounts, and the other user's account is never asked.
func TestUserPinnedToAnotherUsersAccountServedOnItsOwn(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC()},
		"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC()},
	}}})
	s := byUser(New())
	for _, pin := range []string{"spare@example.com", "SPARE@example.com", "codex@spare@example.com"} {
		rec := postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`, map[string]string{UserHeader: "alice", AccountHeader: pin})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-1") {
			t.Fatalf("%s: %d %s", pin, rec.Code, rec.Body.String())
		}
		if got := asked(true); !slices.Equal(got, []string{"acct-1"}) {
			t.Fatalf("%s: asked %v", pin, got)
		}
		r := lastRoute(s)
		if r.User == nil || r.User.User != "alice" || r.User.Account != "me@example.com" || r.Pinned != "" {
			t.Fatalf("%s: route user %+v pinned %q", pin, r.User, r.Pinned)
		}
	}
	if b := boundIn(t, "codex"); b["alice"] != "me@example.com" || b["bob"] != "spare@example.com" {
		t.Fatalf("bound %v", b)
	}
}

// A user with no binding yet that pins an account is bound as an
// unpinned request is: to the account fewest users are on, the pin
// holding only when that is the one it named. Another's account alone
// has room for none: a 429 that names no account, nothing sent.
func TestUserPinnedBeforeItsBindingIsBoundAsUnpinned(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	writeTiers(t, `{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":1,"share":1}}}`)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC()},
	}}})
	s := byUser(New())
	rec := postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`, map[string]string{UserHeader: "alice", AccountHeader: "spare@example.com"})
	if rec.Code != 200 || !slices.Equal(asked(true), []string{"acct-1"}) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if b := boundIn(t, "codex"); b["alice"] != "me@example.com" {
		t.Fatalf("bound %v", b)
	}
	// carol: every account has as many users as it may
	rec = postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`, map[string]string{UserHeader: "carol", AccountHeader: "spare@example.com"})
	if rec.Code != http.StatusTooManyRequests || strings.Contains(rec.Body.String(), "spare@example.com") {
		t.Fatalf("full: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("full: asked %v", got)
	}
}

// Pinned to one of its own accounts, only that one is asked: not its
// first, and no spare added when it is out of its allowance.
func TestUserPinnedToItsOwnAccountAsksThatOneAlone(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, map[string]int{"acct-2": http.StatusTooManyRequests})
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(),
			More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC()}}},
	}}})
	s := byUserUpTo(t, New(), 3)
	post := func(pin string) *httptest.ResponseRecorder {
		return postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`, map[string]string{UserHeader: "alice", AccountHeader: pin})
	}
	rec := post("me@example.com")
	if rec.Code != 200 || !slices.Equal(asked(true), []string{"acct-1"}) {
		t.Fatalf("its first: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.Pinned != "me@example.com" || r.User == nil || r.User.User != "alice" {
		t.Fatalf("route pinned %q user %+v", r.Pinned, r.User)
	}
	// refused: told so, asked again on that account alone (a rate
	// limit's retries), never its first or a spare
	rec = post("spare@example.com")
	if got := asked(true); rec.Code != http.StatusTooManyRequests || len(got) == 0 || slices.ContainsFunc(got, func(a string) bool { return a != "acct-2" }) {
		t.Fatalf("its spare out: %d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	if b := boundIn(t, "codex"); b["alice"] != "me@example.com" {
		t.Fatalf("bound %v", b)
	}
	if strings.Contains(mustRead(t, usersPath()), "third@example.com") {
		t.Fatal("a spare was added for a pinned request")
	}
}

// A pinned request with a user can't be placed when user-accounts.json
// doesn't read: a 503, nothing sent, the file left as it was.
func TestUserPinnedWithUnreadableBindingsRefused(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	if err := os.WriteFile(usersPath(), []byte(`{"users": {"codex": {"alice": `), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := postAt(byUser(New()), "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`, map[string]string{UserHeader: "alice", AccountHeader: "spare@example.com"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "user-accounts.json") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
	if b, _ := os.ReadFile(usersPath()); string(b) != `{"users": {"codex": {"alice": ` {
		t.Fatalf("written over: %s", b)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
