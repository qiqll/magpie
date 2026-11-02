package gateway

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
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

// usageCount is how many rows the usage log has, for a turn's own rows
// after it (routingRowsKeepTheirUser).
func usageCount() int { return len(usage.Load(time.Time{})) }

// routingRowsKeepTheirUser fails unless the routing rows written since the
// log had from rows (magpie's ask of a classifier, through System One or
// serve) are there and keep user, the user of the turn they were asked
// for (T-020). Before, they named none.
func routingRowsKeepTheirUser(t *testing.T, from int, user string) {
	t.Helper()
	n := 0
	for _, r := range usage.Load(time.Time{})[from:] {
		if r.Kind != "routing" {
			continue
		}
		n++
		if r.User != user {
			t.Errorf("%s's turn: a routing row of %s/%s keeps the user %q", user, r.Provider, r.Model, r.User)
		}
	}
	if n == 0 {
		t.Fatalf("%s's turn: no routing row in the usage log", user)
	}
}

// Bound by session, a classifier's ask keeps the user its turn named in
// X-Magpie-User too, as the turn's own row does (appendUsage): asked
// through System One (askDecideTries) or through serve (askChat), where
// no header carries it on (T-020, review M1).
func TestRoutingRowsKeepTheUserBoundBySession(t *testing.T) {
	t.Run("decision", func(t *testing.T) {
		s, _, _, _ := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
		u := newRoutesUp(t)
		if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "kA", Decide: u.URL + "/v1", Models: []string{"jev-latest"}}); err != nil {
			t.Fatal(err)
		}
		g, _, _ := provider.FindGroup("group/r")
		g.Classifier = "jev/jev-latest"
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
		routingTurns(t, s)
	})
	t.Run("chat", func(t *testing.T) {
		s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
		c.set("1", 0)
		up, _, users := keysSeen(t, c)
		if err := provider.Save(provider.Provider{ID: "c", Name: "C", Key: "kc", Models: []string{"cls"}, Chat: up.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive("c", up.URL+"/v1", []catalog.Model{{ID: "cls", Context: 32000}}); err != nil {
			t.Fatal(err)
		}
		routingTurns(t, s)
		if n := users(); n != 0 {
			t.Fatalf("%d classifier asks took the user header to the vendor", n)
		}
	})
}

// routingTurns sends alice's then bob's turn to group/r, bound by session,
// and checks each turn's routing rows keep its user.
func routingTurns(t *testing.T, s *Server) {
	t.Helper()
	for _, user := range []string{"alice", "bob"} {
		rows := usageCount()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat("add the unit test for "+user+"'s parser", nil, 0, "")))
		req.Header.Set("x-session-id", user)
		req.Header.Set(UserHeader, user)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
		}
		routingRowsKeepTheirUser(t, rows, user)
	}
}
