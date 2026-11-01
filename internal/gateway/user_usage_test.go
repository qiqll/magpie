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
	rows, text := searchRows(t)
	if len(rows) != 2 || rows[0].User != "alice" || rows[1].User != "bob" {
		t.Fatalf("search rows %+v, want alice's then bob's (%s)", rows, text)
	}
}
