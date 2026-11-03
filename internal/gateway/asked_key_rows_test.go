package gateway

import (
	"cmp"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// rowsKeptUnder fails unless the usage log's rows written since it had
// from rows are there and are each kept under the gateway key id (name
// too), the key of the turn magpie asked them for (rowCaller, T-095);
// "" for a turn without one. by_use and a user's own usage count a row
// in its CallerKeyID's group, so a row kept under none went to the
// shared group's user of the name.
func rowsKeptUnder(t *testing.T, what string, from int, id, name string) {
	t.Helper()
	rows := usage.Load(time.Time{})[from:]
	if len(rows) == 0 {
		t.Fatalf("%s: no row in the usage log", what)
	}
	for _, r := range rows {
		if r.CallerKeyID != id || r.CallerKeyName != name {
			t.Errorf("%s: the %s row of %s/%s is kept under %q (%q), want %q (%q)", what, cmp.Or(r.Kind, "turn"), r.Provider, r.Model, r.CallerKeyID, r.CallerKeyName, id, name)
		}
	}
}

// Bound by user, a Claude Code run's web search, asked back through its
// MCP callback, which carries no key, keeps its rows under the key of
// the turn it was made for, as the turn's own row is; a turn without a
// key keeps them under none, as before. Before, every such row named no
// key, though the search was asked on the key's user's accounts.
func TestClaudeRunSearchRowsKeptUnderItsKey(t *testing.T) {
	keys, _ := dsKeyed(t, provider.Responses)
	callers, _ := newCaller(t, "Tenant")
	s := byUserUpTo(t, New(), 1)
	for _, c := range []struct{ what, id, name string }{
		{"under Tenant", callers[0].ID, "Tenant"},
		{"under no key", "", ""},
	} {
		from := usageCount()
		turn := httptest.NewRequest("POST", "/v1/messages", nil)
		turn.Header.Set(UserHeader, "bob")
		if c.id != "" {
			turn = turn.WithContext(access.WithIdentity(turn.Context(), access.Identity{KeyID: c.id, KeyName: c.name}))
		}
		if _, _, err := s.runSearch(turn)(context.Background(), "weather in Hangzhou"); err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		if got := keys("deepseek-flash"); len(got) == 0 {
			t.Fatalf("%s: never searched", c.what)
		}
		rowsKeptUnder(t, c.what, from, c.id, c.name)
	}
}

// Bound by user, a routing group's classifier that is a decision group
// (asked through System One in a context of its own, askDecideTries)
// keeps its routing rows under the turn's gateway key, as the turn's own
// row is; a turn without a key keeps them under none. Before, they named
// none, and by_use counted a key's user's classifier calls in the shared
// group's user of the name.
func TestDecisionGroupRowsKeptUnderTheTurnsKey(t *testing.T) {
	s, _, _, _ := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
	u := newRoutesUp(t)
	if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "kA", Decide: u.URL + "/v1", Models: []string{"jev-latest"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "judges", Name: "Judges", Members: []string{"jev/jev-latest"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Classifier = "group/judges"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	callers, secrets := newCaller(t, "Tenant")
	s = byUser(s)
	for _, c := range []struct{ what, secret, id, name string }{
		{"under Tenant", secrets[0], callers[0].ID, "Tenant"},
		{"under no key", "", "", ""},
	} {
		from, asked := usageCount(), len(u.asked("/v1/systemone"))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat("add the unit test for bob's parser "+c.what, nil, 0, "")))
		req.Header.Set("x-session-id", "bob "+c.what)
		req.Header.Set(UserHeader, "bob")
		if c.secret != "" {
			req.Header.Set("Authorization", "Bearer "+c.secret)
		}
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.what, rec.Code, rec.Body.String())
		}
		if len(u.asked("/v1/systemone")) <= asked {
			t.Fatalf("%s: the decision group wasn't asked", c.what)
		}
		routingRowsKeepTheirUser(t, from, "bob")
		rowsKeptUnder(t, c.what, from, c.id, c.name)
	}
}

// A System One ask made for a turn under a key with a limit counts
// against that limit at once, as the turn's own row does (budget.Append):
// a window already read is added to, not only read again from
// usage.jsonl. And a row's caller is the ask's own caller first, the
// turn's key only when it has none (rowCaller).
func TestSystemOneCountsForTheTurnsKey(t *testing.T) {
	fresh(t)
	budget.Forget()
	t.Cleanup(budget.Forget)
	u := newRoutesUp(t)
	p := provider.Provider{ID: "jev", Name: "Jev", Key: "kA", Decide: u.URL + "/v1", Models: []string{"jev-latest"}}
	save(t, p)
	keys, _ := newCaller(t, "Tenant", "Other")
	l := &access.Limit{Period: "day", Tokens: 12} // one ask's 10 in, 2 out
	if _, err := access.Update("limit-key", access.Change{Key: keys[0].ID, Limit: l}); err != nil {
		t.Fatal(err)
	}
	who := access.Identity{KeyID: keys[0].ID, KeyName: "Tenant", Limit: l}
	release, refused := budget.Reserve(who, 0, "", time.Now()) // the window, read
	if refused != nil {
		t.Fatalf("refused before the ask: %v", refused)
	}
	release()
	turn := context.WithValue(context.Background(), askedKeyKey{}, askedKey{id: keys[0].ID, name: "Tenant"})
	s := New()
	from := usageCount()
	if _, err := s.systemOne(turn, p, "jev-latest", []byte(`{"model":"jev-latest"}`)); err != nil {
		t.Fatal(err)
	}
	rowsKeptUnder(t, "the turn's ask", from, keys[0].ID, "Tenant")
	if release, refused := budget.Reserve(who, 0, "", time.Now()); refused == nil {
		release()
		t.Fatal("the key's limit didn't count its turn's System One ask")
	}

	from = usageCount()
	own := access.WithIdentity(turn, access.Identity{KeyID: keys[1].ID, KeyName: "Other"})
	if _, err := s.systemOne(own, p, "jev-latest", []byte(`{"model":"jev-latest"}`)); err != nil {
		t.Fatal(err)
	}
	rowsKeptUnder(t, "an ask with a caller of its own", from, keys[1].ID, "Other")
}
