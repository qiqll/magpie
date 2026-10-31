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

	"github.com/yetone/magpie/internal/provider"
)

// keyWho is the account a key k of provider pid is to a binding and to a
// pin (candidate.who).
func keyWho(pid, k string) string { return pid + "#" + provider.KeyID(k) }

// pinnedGroupChat asks the group pair for user, pinned to pin.
func pinnedGroupChat(s *Server, user, pin string) *httptest.ResponseRecorder {
	return postAt(s, "/v1/chat/completions", `{"model":"group/pair","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{UserHeader: user, AccountHeader: pin})
}

// Bound by user, a routing group's turn pinned (AccountHeader) to another
// user's key is served as if unpinned, on its user's own (T-009's
// decision, T-036): bob's key is never asked for alice. Before, a pinned
// group turn skipped the user's binding and went on the key named.
func TestUserGroupPinnedToAnotherUsersKeyServedOnItsOwn(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	f := &chatFleet{status: map[string]int{}}
	chatGroup(t, f)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{
		"ca": {
			"alice": {Who: keyWho("ca", "kc1"), Account: "kc1", At: time.Now().UTC()},
			"bob":   {Who: keyWho("ca", "kc2"), Account: "kc2", At: time.Now().UTC()},
		},
		"cb": {"bob": {Who: keyWho("cb", "kd1"), Account: "kd1", At: time.Now().UTC()}},
	}})
	s := byUserUpTo(t, New(), 1)
	for _, pin := range []string{keyWho("ca", "kc2"), keyWho("cb", "kd1")} {
		if rec := pinnedGroupChat(s, "alice", pin); rec.Code != 200 {
			t.Fatalf("%s: %d %s", pin, rec.Code, rec.Body.String())
		}
		if got := f.took(); !slices.Equal(got, []string{"kc1"}) {
			t.Fatalf("%s: asked %v, want alice's kc1 alone", pin, got)
		}
		if r := lastRoute(s); r.Pinned != "" || r.User == nil || r.User.User != "alice" {
			t.Fatalf("%s: route pinned %q user %+v", pin, r.Pinned, r.User)
		}
	}
	if b := boundIn(t, "cb"); b["alice"] != "" {
		t.Fatalf("alice bound on cb %v, before any turn went to it", b)
	}
}

// Pinned to one of its own keys, a group's turn asks that one alone: not
// the user's other own, not the next member's, and no spare is added for
// it, on that provider or another.
func TestUserGroupPinnedToItsOwnKeyAsksThatOneAlone(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	f := &chatFleet{status: map[string]int{}}
	chatGroup(t, f)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{
		"ca": {"alice": {Who: keyWho("ca", "kc1"), Account: "kc1", At: time.Now().UTC(), Used: time.Now().UTC().Add(-48 * time.Hour),
			More: []userSeat{{Who: keyWho("ca", "kc2"), Account: "kc2", At: time.Now().UTC()}}}},
	}})
	s := byUserUpTo(t, New(), 3)
	pin := keyWho("ca", "kc2")
	if rec := pinnedGroupChat(s, "alice", pin); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := f.took(); !slices.Equal(got, []string{"kc2"}) {
		t.Fatalf("asked %v, want kc2 alone", got)
	}
	if r := lastRoute(s); r.Pinned != pin || r.User == nil || r.User.User != "alice" || len(r.Order) == 0 {
		t.Fatalf("route pinned %q user %+v order %+v", r.Pinned, r.User, r.Order)
	}
	// its use is kept, as an unpinned turn's is, so it isn't let go as idle
	if used := boundOf(t, "", "ca", "alice").Used; time.Since(used) > time.Minute {
		t.Fatalf("used %v, not stamped by a pinned turn", used)
	}
	// out of capacity: told so, never sent on to kc1 or the next member
	f.set("kc2", 503)
	rec := pinnedGroupChat(s, "alice", pin)
	if got := f.took(); rec.Code == 200 || len(got) == 0 || slices.ContainsFunc(got, func(k string) bool { return k != "kc2" }) {
		t.Fatalf("its pin out: %d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	if b := boundIn(t, "cb"); len(b) != 0 {
		t.Fatalf("bound on cb %v for a pinned turn", b)
	}
	if s := mustRead(t, usersPath()); strings.Count(s, `"who"`) != 2 {
		t.Fatalf("a seat was added for a pinned turn: %s", s)
	}
}

// A pinned group turn whose user-accounts.json doesn't read is told so, a
// 503, nothing sent, the pin to a key it can't tell is the user's never
// asked.
func TestUserGroupPinnedWithUnreadableBindingsRefused(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	f := &chatFleet{status: map[string]int{}}
	chatGroup(t, f)
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	if err := os.WriteFile(usersPath(), []byte(`{"users": {"ca": {"alice": `), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := pinnedGroupChat(byUserUpTo(t, New(), 1), "alice", keyWho("ca", "kc1"))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "user-accounts.json") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
}

// A group's turn pinned to another user's account whose sign-in has to be
// made again goes as an unpinned one too: not cut down to that account's
// provider, nor refused for its lapse. Before, the lapse was judged first,
// on the whole group, and alice's turn went to bob's provider alone.
func TestUserGroupPinnedToAnotherUsersLapsedAccountServedOnItsOwn(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	codex, _ := usersUpstream(t, nil)
	noRests()
	t.Cleanup(noRests)
	f := &chatFleet{status: map[string]int{}}
	srv := f.serve(t)
	if err := provider.Save(provider.Provider{ID: "ca", Name: "CA", Chat: srv.URL + "/v1", Key: "kc1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"ca/m", "codex/gpt-5.5"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	lapseSaved(t, "spare@example.com")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{
		"ca":    {"alice": {Who: keyWho("ca", "kc1"), Account: "kc1", At: time.Now().UTC()}},
		"codex": {"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC()}},
	}})
	s := byUserUpTo(t, New(), 1)
	rec := pinnedGroupChat(s, "alice", "spare@example.com")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := f.took(); !slices.Equal(got, []string{"kc1"}) {
		t.Fatalf("asked %v, want alice's kc1, the group's first", got)
	}
	if got := codex(true); len(got) != 0 {
		t.Fatalf("codex asked %v", got)
	}
	if r := lastRoute(s); r.Pinned != "" {
		t.Fatalf("route pinned %q", r.Pinned)
	}
}
