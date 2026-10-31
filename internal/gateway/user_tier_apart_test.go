package gateway

import (
	"context"
	"encoding/json"
	"log"
	"os"
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

// boundOf is user's binding to pid under the gateway key keyID ("" for
// none) as user-accounts.json has it.
func boundOf(t *testing.T, keyID, pid, user string) userBinding {
	t.Helper()
	b, err := os.ReadFile(usersPath())
	if err != nil {
		t.Fatalf("user-accounts.json: %v", err)
	}
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("user-accounts.json: %v: %s", err, b)
	}
	if keyID == "" {
		return f.Users[pid][user]
	}
	return f.Keys[keyID][pid][user]
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
		rec := userPostWith(s, secrets[i+1], "bob", name)
		if got := asked(true); rec.Code != 429 || len(got) != 0 || !strings.Contains(refusedWhy(s), "tier free's 2 accounts") {
			t.Fatalf("%s: %d asked %v: %s (%s)", name, rec.Code, got, rec.Body.String(), refusedWhy(s))
		}
	}

	rec := userPostWith(s, secrets[0], "bob", "its own key's pro")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("its own key: %d asked %v: %s (%s)", rec.Code, got, rec.Body.String(), refusedWhy(s))
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "pro" {
		t.Fatalf("its own key: route %+v", r.User)
	}

	var kept []string
	for _, k := range keys {
		kept = append(kept, boundOf(t, k.ID, "codex", "bob").Tier)
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
// its binding keeps none, is too.
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
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	ask := func(key, user string) ([]candidate, bool, error) {
		return s.usersOwn(context.WithValue(context.Background(), askedForKey{}, askedFor{key, user, ""}), *p, "gpt-5.5", provider.Responses)
	}

	for _, c := range []struct{ key, user string }{{"", "alice"}, {"other-key", "bob"}} {
		// told as a limit, its tier in the log: no spare added
		logged.Reset()
		if own, bound, err := ask(c.key, c.user); err == nil || err.Error() != userLimitSaid || !bound || len(own) != 0 {
			t.Fatalf("%q of key %q: %d accounts, bound %v, err %v", c.user, c.key, len(own), bound, err)
		}
		if !strings.Contains(logged.String(), "at its tier free's 2 accounts") {
			t.Fatalf("%q of key %q: logged %q", c.user, c.key, logged.String())
		}
		if seats := boundOf(t, c.key, "codex", c.user).seats(); len(seats) != 2 {
			t.Fatalf("%q of key %q given a spare: %v", c.user, c.key, seats)
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
