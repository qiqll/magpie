package gateway

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// Bound by session, a Claude Code run's web search keeps its rows under
// the key of the turn it was made for, as bound by user (T-095); a turn
// without a key keeps them under none. Before, forUser carried nothing
// bound by session, so every such row named no key and the key's limit
// and usage never counted it (T-102).
func TestClaudeRunSearchRowsKeptUnderItsKeyBySession(t *testing.T) {
	keys, _ := dsKeyed(t, provider.Responses)
	callers, _ := newCaller(t, "Tenant")
	s := New()
	for _, c := range []struct{ what, id, name string }{
		{"under Tenant", callers[0].ID, "Tenant"},
		{"under no key", "", ""},
	} {
		from := usageCount()
		turn := httptest.NewRequest("POST", "/v1/messages", nil)
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

// Bound by session, a routing group's classifier that is a decision
// group keeps its routing rows under the turn's gateway key, as bound by
// user (T-095), whether or not the turn names a user; a turn without a
// key keeps them under none. Before, they named none (T-102).
func TestDecisionGroupRowsKeptUnderTheTurnsKeyBySession(t *testing.T) {
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
	for _, c := range []struct{ what, user, secret, id, name string }{
		{"bob under Tenant", "bob", secrets[0], callers[0].ID, "Tenant"},
		{"no user under Tenant", "", secrets[0], callers[0].ID, "Tenant"},
		{"bob under no key", "bob", "", "", ""},
	} {
		from, asked := usageCount(), len(u.asked("/v1/systemone"))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat("add the unit test for the parser "+c.what, nil, 0, "")))
		req.Header.Set("x-session-id", "s "+c.what)
		if c.user != "" {
			req.Header.Set(UserHeader, c.user)
		}
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
		rowsKeptUnder(t, c.what, from, c.id, c.name)
	}
}
