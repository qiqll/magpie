package gateway

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// lapsedAt is a binding of a user to spare@ and third@, both to be
// signed in again, keeping tier: at free's 2 accounts it has no room for
// a spare, at pro's 4 it is given me@example.com beside them.
func lapsedAt(tier string) userBinding {
	now := time.Now().UTC()
	return userBinding{Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Used: now, Tier: tier,
		More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}}
}

// keyPost asks for codex/gpt-5.5 for user with the gateway key secret,
// naming no tier.
func keyPost(s *Server, secret, user, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set(UserHeader, user)
	lanGuard(s.Handler()).ServeHTTP(rec, req)
	return rec
}

// A request that names no tier is of the tier its user's binding keeps
// under its own gateway key, to its own provider: bob bound at pro under
// one key is of the default (free) under another, and so under a key
// where bob is bound at pro to another provider only. Each of those is
// refused at free's 2 accounts and asks nobody; under the first key bob
// is given pro's spare.
func TestUserTierKeptOfItsKeyAndProvider(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	lapseSaved(t, "third@example.com")
	keys, secrets := newCaller(t, "pro", "plain", "elsewhere")
	now := time.Now().UTC()
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"codex": {"bob": lapsedAt("pro")}},
		keys[1].ID: {"codex": {"bob": lapsedAt("")}},
		keys[2].ID: {
			"codex":  {"bob": lapsedAt("")},
			"claude": {"bob": {Who: "claude@bob-key", Account: "bob-key", At: now, Used: now, Tier: "pro"}},
		},
	}})
	s := byUser(New())

	for i, name := range []string{"another key's pro", "another provider's pro"} {
		rec := keyPost(s, secrets[i+1], "bob", name)
		if got := asked(true); rec.Code != 429 || len(got) != 0 || !strings.Contains(refusedWhy(s), "tier free's 2 accounts") {
			t.Fatalf("%s: %d asked %v: %s (%s)", name, rec.Code, got, rec.Body.String(), refusedWhy(s))
		}
	}

	rec := keyPost(s, secrets[0], "bob", "its own key's pro")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("its own key: %d asked %v: %s (%s)", rec.Code, got, rec.Body.String(), refusedWhy(s))
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "pro" {
		t.Fatalf("its own key: route %+v", r.User)
	}

	userBinds.Lock()
	err := loadUsers()
	var kept []string
	for _, k := range keys {
		kept = append(kept, userBinds.m[k.ID]["codex"]["bob"].Tier)
	}
	userBinds.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept, []string{"pro", "", ""}) {
		t.Fatalf("tiers kept by key: %q", kept)
	}
}

// magpie's own ask for a user (usersOwn: Codex's web.run, a Kimi Code
// plan's search) carries no tier of its own when the user's request
// named none, and is of the tier the user's binding keeps, as its turn
// is: bob, bound at pro to two accounts to be signed in again, is given
// a spare beside them, keeping pro. alice, bound so with none, is of the
// default and refused at free's 2; bob under another gateway key, where
// his binding keeps none, is too.
func TestUsersOwnKeepsItsTierWithoutTheHeader(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	lapseSaved(t, "third@example.com")
	writeUsers(t, userFile{
		Users: map[string]map[string]userBinding{"codex": {"bob": lapsedAt("pro"), "alice": lapsedAt("")}},
		Keys:  map[string]map[string]map[string]userBinding{"other-key": {"codex": {"bob": lapsedAt("")}}},
	})
	s := byUser(New())
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	ask := func(key, user string) ([]candidate, bool, error) {
		return s.usersOwn(context.WithValue(context.Background(), askedForKey{}, askedFor{key, user, ""}), *p, "gpt-5.5", provider.Responses)
	}

	for _, c := range []struct{ key, user string }{{"", "alice"}, {"other-key", "bob"}} {
		// told as a limit (a 429), its tier not named: no spare added
		if own, bound, err := ask(c.key, c.user); err == nil || !bound || len(own) != 0 {
			t.Fatalf("%q of key %q: %d accounts, bound %v, err %v", c.user, c.key, len(own), bound, err)
		}
		userBinds.Lock()
		err := loadUsers()
		seats := userBinds.m[c.key]["codex"][c.user].seats()
		userBinds.Unlock()
		if err != nil || len(seats) != 2 {
			t.Fatalf("%q of key %q given a spare: %v %v", c.user, c.key, seats, err)
		}
	}

	own, bound, err := ask("", "bob")
	if err != nil || !bound || len(own) == 0 || own[0].p.Account.User != "me@example.com" {
		t.Fatalf("bob: %d accounts, bound %v, err %v", len(own), bound, err)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com", "third@example.com", "me@example.com"}) {
		t.Fatalf("bob's seats %v", got)
	}
	if tiers := tierIn(t, "codex"); tiers["bob"] != "pro" || tiers["alice"] != "" {
		t.Fatalf("kept tiers %v", tiers)
	}
}
