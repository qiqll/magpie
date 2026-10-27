package gateway

import (
	"strings"
	"testing"
)

// A tier's aliases are taken for it, whatever their case; none may be a
// tier's name or another's alias; the built-in tiers take 1 to 4.
func TestParseTierAliases(t *testing.T) {
	got, err := parseTiers([]byte(`{"default":"1","tiers":{"free":{"max_accounts":2,"share":2,"aliases":["1"," basic "]},"max":{"max_accounts":5,"share":1,"aliases":["4","M"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for alias, want := range map[string]string{"1": "free", "BASIC": "free", "4": "max", "m": "max", "MAX": "max"} {
		if x, ok := got.find(alias); !ok || x.Name != want {
			t.Errorf("%q: %+v %v", alias, x, ok)
		}
	}
	if got.Default != "free" {
		t.Errorf("a default by its alias: %q", got.Default)
	}
	if said := got.said(); said != "free (1, basic), max (4, M)" {
		t.Errorf("said %q", said)
	}
	for _, c := range []struct{ body, says string }{
		{`{"default":"free","tiers":{"free":{"max_accounts":2,"aliases":["max"]},"max":{"max_accounts":5}}}`, `alias "max" is already tier "max"`},
		{`{"default":"free","tiers":{"free":{"max_accounts":2,"aliases":["x"]},"max":{"max_accounts":5,"aliases":["X"]}}}`, `is already tier`},
		{`{"default":"free","tiers":{"free":{"max_accounts":2,"aliases":[" "]}}}`, "empty alias"},
		{`{"default":"free","tiers":{"free":{"max_accounts":2,"aliases":["a\tb"]}}}`, "control"},
	} {
		if got, err := parseTiers([]byte(c.body)); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %+v %v", c.body, got, err)
		}
	}
	for alias, want := range map[string]string{"1": "free", "2": "plus", "3": "pro", "4": "max"} {
		if x, ok := defaultTiers.find(alias); !ok || x.Name != want {
			t.Errorf("built-in %q: %+v %v", alias, x, ok)
		}
	}
}

// A caller that numbers its tiers (X-Magpie-User-Tier: 4) is of the tier
// that number is an alias of: answered, its account its own alone as
// max's, and the tier kept by its name, never the number.
func TestUserTierByItsAlias(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	if rec := tierPost(s, "carol", "4", "hi"); rec.Code != 200 {
		t.Fatalf("carol, 4: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "max" {
		t.Fatalf("route %+v", r.User)
	}
	if got := tierIn(t, "codex")["carol"]; got != "max" {
		t.Fatalf("kept tier %q", got)
	}
	// max has its account alone: a free user goes to the other
	if rec := tierPost(s, "bob", "1", "hi"); rec.Code != 200 {
		t.Fatalf("bob, 1: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	seats := seatsIn(t, "codex")
	if seats["carol"][0] == seats["bob"][0] {
		t.Fatalf("free shares max's account: %v", seats)
	}
	if rec := tierPost(s, "dave", "1", "hi"); rec.Code != 200 {
		t.Fatalf("dave, 1: %d %s", rec.Code, rec.Body.String())
	}
	if rec := tierPost(s, "erin", "5", "hi"); rec.Code != 400 || !strings.Contains(rec.Body.String(), "max (4)") {
		t.Fatalf("erin, 5: %d %s", rec.Code, rec.Body.String())
	}
}
