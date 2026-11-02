package gateway

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Each call's record keeps the caller's user it named (T-020), for the
// Usage page's Users: bound by user or by session, a request without one,
// or with a value UserHeader can't take, keeps none.
func TestRecordKeepsTheCallersUser(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(servedBy(provider.Chat, "sol"))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: up.URL + "/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for _, c := range []struct{ name, user, want string }{
		{"named", "alice", "alice"},
		{"none", "", ""},
		{"untakeable", "a\tb", ""},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/sol","messages":[{"role":"user","content":"hi"}]}`))
		if c.user != "" {
			req.Header.Set(UserHeader, c.user)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		if got := lastRecord(t); got.User != c.want {
			t.Errorf("%s: the record's user is %q, want %q", c.name, got.User, c.want)
		}
	}

	// bound by user, the user it is bound for
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	s := byUser(New())
	r := httptest.NewRequest("POST", "http://magpie/v1/x", nil)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(UserHeader, "bob")
	w := httptest.NewRecorder()
	s.serve(w, r, provider.Responses, []byte(`{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`))
	if w.Code != 200 {
		t.Fatalf("bound by user: %d %s", w.Code, w.Body)
	}
	if got := lastRecord(t); got.User != "bob" {
		t.Errorf("bound by user: the record's user is %q, want bob", got.User)
	}
}

// A web search's own rows keep the user they were made for too (T-020):
// main's logSearch (a search API, a plan's search service) and
// logNativeSearches write past appendUsage, and the merge had them name
// none (T-050).
func TestSearchRowsKeepTheUser(t *testing.T) {
	fresh(t)
	ctx := context.WithValue(context.Background(), askedForKey{}, askedFor{user: "alice"})
	logSearch(ctx, usage.Record{Requested: "search"}, "q", time.Now(), 1, nil)
	logNativeSearches(context.Background(), 1, time.Now(), "codex", "codex", "chatgpt.com", "gpt-5.5", "bob")
	// unbound, the request's own user, which its search's CallFor carries
	unbound := context.WithValue(context.Background(), searchForKey{}, &CallFor{Agent: "claude", User: "carol"})
	logSearch(unbound, usage.Record{Requested: "search"}, "q", time.Now(), 1, nil)
	rows, text := searchRows(t)
	if len(rows) != 3 || rows[0].User != "alice" || rows[1].User != "bob" || rows[2].User != "carol" {
		t.Fatalf("search rows %+v, want alice's, bob's, then carol's (%s)", rows, text)
	}
}

// routingRowsKeepTheirUser fails unless every routing row (systemOne's,
// magpie's ask of a decision classifier) keeps the user its turn was
// for: an alice turn's words name alice, a bob turn's bob (T-020).
// Before, they named none.
func routingRowsKeepTheirUser(t *testing.T) {
	t.Helper()
	n := 0
	for _, r := range usage.Load(time.Time{}) {
		if r.Kind != "routing" {
			continue
		}
		n++
		if r.User != "alice" && r.User != "bob" {
			t.Errorf("a routing row of %s/%s keeps the user %q, want alice or bob", r.Provider, r.Model, r.User)
		}
	}
	if n == 0 {
		t.Fatal("no routing row in the usage log")
	}
}
