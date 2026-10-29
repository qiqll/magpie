package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// lapseSaved marks the saved account user as one whose sign-in has to be
// made again, as a refused refresh does (savedRefreshFailed): its access
// token still in date, so only routing's word keeps it from being asked.
func lapseSaved(t *testing.T, user string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var ls []map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &ls); err != nil {
		t.Fatal(err)
	}
	why := user + "'s sign-in has expired — add the account again to use it"
	for _, l := range ls {
		if l["user"] == user {
			l["lapsed"] = why
		}
	}
	if err := os.WriteFile(path, mustJSON(ls), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	return why
}

// An account whose sign-in has to be made again is never asked while
// another of the provider's can be: here the one left is out of its
// allowance, and the agent is told that, not the lapse of an account that
// couldn't have answered. The route says why it was left out.
func TestLapsedAccountLeftOutWhileAnotherCanAnswer(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, map[string]int{"acct-1": http.StatusTooManyRequests})
	why := lapseSaved(t, "spare@example.com")
	s := New()
	rec := codexPostTo(s, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if got := asked(true); slices.Contains(got, "acct-2") || !slices.Contains(got, "acct-1") {
		t.Fatalf("asked %v: the lapsed account was asked", got)
	}
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "has expired") {
		t.Fatalf("told %d %s", rec.Code, rec.Body.String())
	}
	r := lastRoute(s)
	if !slices.ContainsFunc(r.Left, func(w Weighed) bool { return w.Who == "spare@example.com" && w.Lapsed == why }) {
		t.Fatalf("the route doesn't say why it was left out: %+v", r.Left)
	}
	if slices.ContainsFunc(r.Order, func(w Weighed) bool { return w.Who == "spare@example.com" }) {
		t.Fatalf("the lapsed account is in the order: %+v", r.Order)
	}

	// pinned to it, the caller is told it has to be signed in again
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set(AccountHeader, "spare@example.com")
	pinned := httptest.NewRecorder()
	s.Handler().ServeHTTP(pinned, req)
	if pinned.Code != http.StatusForbidden || !strings.Contains(pinned.Body.String(), "has expired") {
		t.Fatalf("pinned: %d %s", pinned.Code, pinned.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("pinned: asked %v", got)
	}

	// signed in again: asked again
	path := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var ls []map[string]any
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &ls)
	for _, l := range ls {
		delete(l, "lapsed")
	}
	os.WriteFile(path, mustJSON(ls), 0o600)
	provider.ForgetAccounts()
	if rec := codexPostTo(New(), `{"model":"gpt-5.5","stream":true,"input":"again"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-2") {
		t.Fatalf("signed in again, not asked: %d %s", rec.Code, rec.Body.String())
	}
}

// A user bound to an account whose sign-in has to be made again is bound
// to one that can answer, the lapsed one not asked (--bind=user).
func TestUserLetGoOfALapsedAccountUnasked(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	os.WriteFile(usersPath(), mustJSON(userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com"},
	}}}), 0o600)
	s := byUser(New())
	if rec := userPost(s, "codex/gpt-5.5", "bob", "ping"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("asked %v", got)
	}
	if b := boundIn(t, "codex"); b["bob"] != "me@example.com" {
		t.Fatalf("not bound afresh: %v", b)
	}
}

// A compaction moved off Codex's paused sign-in, and a web search on a
// ChatGPT account, leave out an account whose sign-in has to be made
// again as routing does.
func TestLapsedAccountNotCompactedOnOrSearchedWith(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", CodexPath+"/responses/compact", nil)
	on := compactOn(r, "gpt-5.5")
	if on == nil || on.Account.User != "third@example.com" {
		t.Fatalf("compacted on %+v", on)
	}
	ons, _ := chatGPTSearchers(httptest.NewRequest("POST", "/v1/responses", nil), "codex/gpt-5.5")
	var users []string
	for _, o := range ons {
		users = append(users, o.Account.User)
	}
	if slices.Contains(users, "spare@example.com") || !slices.Contains(users, "third@example.com") {
		t.Fatalf("searches with %v", users)
	}
	if got := asked(false); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
}
