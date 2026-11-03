package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
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

// Pinned to its own key, a user's request is never sent on to a
// fallback's key of the same name: that one may be another user's, and
// a fallback's accounts aren't counted in the user's binding.
func TestUserPinnedToItsOwnKeyNotOnToAFallbacksOfTheSameName(t *testing.T) {
	codexSignedIn(t)
	busy := &scripted{replies: []reply{{503, "", `{"error":{"message":"busy","type":"server_error"}}`}}}
	first := httptest.NewServer(busy)
	t.Cleanup(first.Close)
	other := &scripted{replies: []reply{{200, "", `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"from fb2"},"finish_reason":"stop"}]}`}}}
	second := httptest.NewServer(other)
	t.Cleanup(second.Close)
	if err := provider.Save(provider.Provider{ID: "fb2", Name: "FB2", Chat: second.URL + "/v1", Models: []string{"m"},
		KeyName: "main", Key: "k2", Keys: []provider.KeyAccount{{Name: "other2", Key: "k2b"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fb", Name: "FB", Chat: first.URL + "/v1", Models: []string{"m"}, Fallback: []string{"fb2/m"},
		KeyName: "main", Key: "k1", Keys: []provider.KeyAccount{{Name: "other1", Key: "k1b"}}}); err != nil {
		t.Fatal(err)
	}
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{
		"fb":  {"alice": {Who: "fb#" + provider.KeyID("k1"), Account: "main", At: time.Now().UTC()}},
		"fb2": {"bob": {Who: "fb2#" + provider.KeyID("k2"), Account: "main", At: time.Now().UTC()}},
	}})
	s := byUser(New())
	rec := postAt(s, "/v1/chat/completions", `{"model":"fb/m","messages":[{"role":"user","content":"hi"}]}`, map[string]string{UserHeader: "alice", AccountHeader: "main"})
	if other.n != 0 || busy.n == 0 || strings.Contains(rec.Body.String(), "from fb2") {
		t.Fatalf("%d %s: fb asked %d times, fb2 %d", rec.Code, rec.Body.String(), busy.n, other.n)
	}
	if r := lastRoute(s); r.Pinned != "main" || r.User == nil || r.User.User != "alice" {
		t.Fatalf("route pinned %q user %+v", r.Pinned, r.User)
	}
}

// Bound by user, a pin to one of the user's own accounts named in other
// letters is sent percent-encoded (plugins#615, pinnedName), and holds as
// the name written would: that account alone is asked, the trace naming
// it decoded. Before, the user's binding compared the encoded value with
// its seats, found none, and served the request as if unpinned.
func TestUserPinnedToItsOwnAccountPercentEncoded(t *testing.T) {
	codexSignedIn(t, "北冥")
	asked, _ := usersUpstream(t, nil)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(),
			More: []userSeat{{Who: "codex@北冥", Account: "北冥", At: time.Now().UTC()}}},
	}}})
	s := byUserUpTo(t, New(), 3)
	rec := postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`, map[string]string{UserHeader: "alice", AccountHeader: url.PathEscape("北冥")})
	if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-2"}) {
		t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	if r := lastRoute(s); r.Pinned != "北冥" || r.User == nil || r.User.User != "alice" {
		t.Fatalf("route pinned %q user %+v", r.Pinned, r.User)
	}
}
