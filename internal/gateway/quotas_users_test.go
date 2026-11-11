package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// T-090: bound by user, a request naming its user (X-Magpie-User) was
// told every account's card, its email and what is left of it, from the
// quotas, their kept report, their history and the cards another magpie
// reads, and could have any of them read again. It is told, and has read
// again, the cards of the accounts and keys it is on alone, as its group
// shares them (T-089): alice of a key that doesn't share its users is not
// the alice of another key. No user named is DefaultUser's (T-163), told
// its own cards alone as any user; bound by session: every card, as
// before.
func TestBoundUserToldItsOwnQuotas(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	seedKeptBalances(t) // alpha's keys One and Two, beta's one key
	// and a key of beta's that is off: beta's card is still named by no
	// one (cardKeys), and of its first key
	b, err := provider.Find("beta")
	if err != nil {
		t.Fatal(err)
	}
	b.Keys = []provider.KeyAccount{{Name: "Old", Key: "beta-old", Off: true}}
	if err := provider.Save(*b); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Hour).Format(time.RFC3339)
	seed := fmt.Sprintf(`{"alpha|One":{"Daily":[{"at":%[1]q,"left":90}]},"alpha|Two":{"Daily":[{"at":%[1]q,"left":80}]},
		"beta|":{"Daily":[{"at":%[1]q,"left":70}]},"codex|me@example.com":{"5 hours":[{"at":%[1]q,"left":60}]},
		"codex|spare@example.com":{"5 hours":[{"at":%[1]q,"left":50}]}}`, at)
	if err := provider.MergeQuotaHistory([]byte(seed), now); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Full", "Other")
	one, two, beta := "alpha#"+provider.KeyID("alpha-one"), "alpha#"+provider.KeyID("alpha-two"), "beta#"+provider.KeyID("beta-key")
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {
			"alpha": {"alice": {Who: one, Account: "One", At: now}, "bob": {Who: two, Account: "Two", At: now}},
			"beta":  {"alice": {Who: beta, Account: provider.Mask("beta-key"), At: now}},
			// me@ her spare, beside an account that is gone
			"codex": {"alice": {Who: "codex@gone@example.com", Account: "gone@example.com", At: now,
				More: []userSeat{{Who: "codex@me@example.com", Account: "me@example.com", At: now}}},
				DefaultUser: {Who: "codex@spare@example.com", Account: "spare@example.com", At: now}},
		},
		// another key's alice, apart from Full's: its users aren't shared
		keys[1].ID: {"alpha": {"alice": {Who: two, Account: "Two", At: now}}},
	}})
	bound := lanGuard(byUserUpTo(t, New(), 1).Handler())
	bySession := lanGuard(New().Handler())
	secret := secrets[0]
	do := func(h http.Handler, method, path, user string) (int, string) {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "192.168.1.9:5000"
		r.Header.Set("Authorization", "Bearer "+secret)
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	told := func(path, b string) []string {
		t.Helper()
		var out struct {
			Data []struct{ Provider, User string }
		}
		if err := json.Unmarshal([]byte(b), &out); err != nil {
			t.Fatal(path, err, b)
		}
		cards := []string{}
		for _, q := range out.Data {
			cards = append(cards, q.Provider+":"+strings.ToLower(q.User))
		}
		slices.Sort(cards)
		return cards
	}
	// the report read afresh last: alpha and beta's upstream answers no
	// balance, so it reads their cards away
	reads := []string{"/v1/magpie/quotas?kept=1", provider.RemoteCardsPath, "/v1/magpie/quotas/history", "/v1/magpie/quotas"}
	kept := func(path string) bool { return path == "/v1/magpie/quotas?kept=1" || path == provider.RemoteCardsPath }
	// what each is told: the cards of alpha and beta's keys, and the
	// history of codex's accounts beside them
	want := map[string][]string{
		"alice": {"alpha:one", "beta:", "codex:me@example.com"},
		"bob":   {"alpha:two"},
		"carol": {}, // bound to nothing
		// no user named: DefaultUser's, as naming it
		"":          {"codex:spare@example.com"},
		DefaultUser: {"codex:spare@example.com"},
	}
	every := []string{"alpha:one", "alpha:two", "beta:", "codex:me@example.com", "codex:spare@example.com"}
	for _, path := range reads {
		for _, user := range []string{"alice", "bob", "carol", "", DefaultUser} {
			c, b := do(bound, "GET", path, user)
			if c != 200 {
				t.Fatalf("%s %s: %d %s", user, path, c, b)
			}
			got := told(path, b)
			for _, card := range got {
				if slices.Contains(every, card) && !slices.Contains(want[user], card) {
					t.Errorf("%s %s: told %s, may be told only %v", user, path, card, want[user])
				}
			}
			if path == "/v1/magpie/quotas/history" && !slices.Equal(got, want[user]) {
				t.Errorf("%s %s: told %v, want %v", user, path, got, want[user])
			}
			if user == "alice" && kept(path) && (!slices.Contains(got, "alpha:one") || !slices.Contains(got, "beta:")) {
				t.Errorf("alice %s: not told her own cards: %v", path, got)
			}
		}
		// bound by session: every card
		for _, c := range []struct {
			h    http.Handler
			user string
		}{{bySession, "alice"}, {bySession, ""}} {
			code, b := do(c.h, "GET", path, c.user)
			if code != 200 {
				t.Fatalf("%q %s: %d %s", c.user, path, code, b)
			}
			got := told(path, b)
			if !kept(path) {
				if path == "/v1/magpie/quotas/history" && !slices.Equal(got, every) {
					t.Errorf("%q %s: told %v, want %v", c.user, path, got, every)
				}
				continue
			}
			for _, card := range []string{"alpha:one", "alpha:two", "beta:"} {
				if !slices.Contains(got, card) {
					t.Errorf("%q %s: no %s card in %v", c.user, path, card, got)
				}
			}
		}
	}
	// Other's alice is its own: told Two's history alone
	secret = secrets[1]
	if c, b := do(bound, "GET", "/v1/magpie/quotas/history", "alice"); c != 200 || !slices.Equal(told("history", b), []string{"alpha:two"}) {
		t.Errorf("Other's alice: %d %s", c, b)
	}
	secret = secrets[0]
	// a card of another user's isn't read again for alice, nor one of
	// another key's alice
	for _, card := range []string{"provider=alpha&user=Two", "provider=codex&user=spare@example.com", "provider=nobody"} {
		if c, b := do(bound, "POST", provider.RemoteRefreshPath+"?"+card, "alice"); c != http.StatusForbidden || strings.Contains(b, "alpha-two") {
			t.Errorf("alice refreshing %s: %d %s", card, c, b)
		}
	}
	for _, card := range []string{"provider=alpha&user=One", ""} {
		c, b := do(bound, "POST", provider.RemoteRefreshPath+"?"+card, "alice")
		if c != 200 {
			t.Fatalf("alice refreshing her own %q: %d %s", card, c, b)
		}
		for _, got := range told("refresh", b) {
			if slices.Contains(every, got) && !slices.Contains(want["alice"], got) {
				t.Errorf("alice refreshing %q: told %s", card, got)
			}
		}
	}
	// nor for a request naming no user, DefaultUser's (T-163)
	if c, b := do(bound, "POST", provider.RemoteRefreshPath+"?provider=alpha&user=Two", ""); c != http.StatusForbidden || strings.Contains(b, "alpha-two") {
		t.Errorf("no user refreshing Two: %d %s", c, b)
	}
	if c, b := do(bound, "GET", "/v1/magpie/quotas?kept=1", "al ice\x00"); c != http.StatusBadRequest {
		t.Errorf("a user that can't be taken: %d %s", c, b)
	}
	// user-accounts.json written over with what doesn't read: which
	// accounts alice is on is unknown, so she is told none
	if err := os.WriteFile(usersPath(), []byte(`{"keys":`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range reads {
		for _, user := range []string{"alice", ""} {
			if c, b := do(bound, "GET", path, user); c != http.StatusServiceUnavailable || strings.Contains(b, "alpha:") {
				t.Errorf("unread %s, %q: %d %s", path, user, c, b)
			}
		}
	}
}

// main's /v1/magpie/statusline (0e1054ce) is made of the kept quotas and
// the session's route. Bound by user, a request naming its user is told
// the cards of its own seats alone, as the quotas are (T-090), and the
// model of its own session's route alone, as /v1/magpie/route is
// (T-099): another user who names the session is told no model.
func TestBoundUserToldItsOwnStatusline(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	seedKeptPlans(t) // alpha's keys One and Two, beta's one key, each a plan's windows
	now := time.Now().UTC().Truncate(time.Second)
	keys, secrets := newCaller(t, "Full")
	one, two := "alpha#"+provider.KeyID("alpha-one"), "alpha#"+provider.KeyID("alpha-two")
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"alpha": {"alice": {Who: one, Account: "One", At: now}, "bob": {Who: two, Account: "Two", At: now}}},
	}})
	s := byUserUpTo(t, New(), 1)
	h := lanGuard(s.Handler())
	tr := s.trace.begin(Route{Session: "sess-a", Model: "alpha/m-alice", caller: routeCaller{user: "alice", key: keys[0].ID}})
	s.trace.update(tr, func(r *Route) { r.Done = true })
	do := func(user string) (cards []string, model string) {
		t.Helper()
		r := httptest.NewRequest("GET", StatuslinePath+"?format=json&session=sess-a", nil)
		r.RemoteAddr = "192.168.1.9:5000"
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%q: %d %s", user, w.Code, w.Body)
		}
		var out struct {
			Model    string
			Accounts []struct{ Provider, User string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err, w.Body)
		}
		cards = []string{}
		for _, q := range out.Accounts {
			cards = append(cards, q.Provider+":"+strings.ToLower(q.User))
		}
		slices.Sort(cards)
		return cards, out.Model
	}
	if cards, model := do("alice"); !slices.Equal(cards, []string{"alpha:one"}) || model != "alpha/m-alice" {
		t.Errorf("alice: told %v and model %q, want her own card alpha:one alone and her own model", cards, model)
	}
	if cards, model := do("bob"); slices.Contains(cards, "alpha:one") || slices.Contains(cards, "beta:") || model != "" {
		t.Errorf("bob: told %v and model %q, want none of alice's cards and no model", cards, model)
	}
	// no user named: DefaultUser's cards, none here (T-163), and the
	// session's model as /v1/magpie/route tells it, as before
	if cards, model := do(""); len(cards) != 0 || model != "alpha/m-alice" {
		t.Errorf("no user: told %v and model %q, want no card and the route's model", cards, model)
	}
}
