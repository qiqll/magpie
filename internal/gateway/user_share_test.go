package gateway

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// share is read from user-tiers.json: 0 when absent (no limit, as a file
// before shares), a negative one says why.
func TestParseTiersShare(t *testing.T) {
	got, err := parseTiers([]byte(`{"default":"free","tiers":{"free":{"max_accounts":2,"share":2},"pro":{"max_accounts":4,"share":1},"team":{"max_accounts":6}}}`))
	if err != nil || got.shareOf("free") != 2 || got.shareOf("PRO") != 1 || got.shareOf("team") != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// a binding of a tier gone from the file, or kept before tiers, is the
	// default's
	if got.shareOf("gold") != 2 || got.shareOf("") != 2 {
		t.Fatalf("gone: %d, none: %d", got.shareOf("gold"), got.shareOf(""))
	}
	if (Tiers{}).shareOf("free") != 1 {
		t.Fatal("tiers not known: not the least share")
	}
	for _, body := range []string{
		`{"default":"free","tiers":{"free":{"max_accounts":2,"share":-1}}}`,
		`{"default":"free","tiers":{"free":{"max_accounts":2,"share":1.5}}}`,
	} {
		if got, err := parseTiers([]byte(body)); err == nil || !strings.Contains(err.Error(), "share") {
			t.Errorf("%s: %+v %v", body, got, err)
		}
	}
	if defaultTiers.shareOf("free") != 2 || defaultTiers.shareOf("plus") != 1 || defaultTiers.shareOf("pro") != 1 || defaultTiers.shareOf("max") != 1 {
		t.Fatalf("built-in shares: %+v", defaultTiers)
	}
}

// With the built-in tiers, a user of a tier with its account alone (pro)
// is bound to one nobody else is on, and nobody joins it; free users
// share one, two to an account at most. With every account taken, a new
// user, free or pro, is a 429 that says the pool is full ("quota", with
// no time it is back), and nobody is asked.
func TestPaidUserHasItsAccountAlone(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	for _, u := range []struct{ user, tier string }{{"alice", "pro"}, {"bob", "pro"}, {"carol", ""}, {"dave", "free"}} {
		if rec := tierPost(s, u.user, u.tier, "hi"); rec.Code != 200 {
			t.Fatalf("%s: %d %s", u.user, rec.Code, rec.Body.String())
		}
	}
	asked(true)
	seats := seatsIn(t, "codex")
	a, b, c, d := seats["alice"], seats["bob"], seats["carol"], seats["dave"]
	if len(a) != 1 || len(b) != 1 || len(c) != 1 || len(d) != 1 || a[0] == b[0] || c[0] != d[0] || c[0] == a[0] || c[0] == b[0] {
		t.Fatalf("pro alone, free two to one: %v", seats)
	}
	for _, u := range []struct{ user, tier string }{{"erin", ""}, {"frank", "pro"}} {
		rec := tierPost(s, u.user, u.tier, "hi")
		limit, until := limitOf(t, rec)
		if rec.Code != 429 || limit != "quota" || !until.IsZero() || !strings.Contains(rec.Body.String(), "no account of the provider is free") {
			t.Fatalf("%s, every account taken: %d %q until %v %s", u.user, rec.Code, limit, until, rec.Body.String())
		}
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked with the pool full: %v", got)
	}
	if _, bound := seatsIn(t, "codex")["erin"]; bound {
		t.Fatal("erin bound with the pool full")
	}
	// the users bound are answered as before
	if rec := tierPost(s, "dave", "free", "again"); rec.Code != 200 {
		t.Fatalf("dave again: %d %s", rec.Code, rec.Body.String())
	}
}

// Free users share the account one is on already before taking one
// nobody is on, whichever that account is, so a pro user that comes
// after still finds one alone.
func TestSharingUsersLeaveAnAccountEmpty(t *testing.T) {
	for _, on := range []string{"me@example.com", "b@example.com", "c@example.com"} {
		t.Run(on, func(t *testing.T) {
			codexSignedIn(t, "b@example.com", "c@example.com")
			asked, _ := usersUpstream(t, nil)
			s := byUser(New())
			writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
				"carol": {Who: "codex@" + on, Account: on, At: time.Now().UTC(), Tier: "free"}}}})
			for _, u := range []struct{ user, tier string }{{"dave", "free"}, {"alice", "pro"}, {"bob", "pro"}} {
				if rec := tierPost(s, u.user, u.tier, "hi"); rec.Code != 200 {
					t.Fatalf("%s: %d %s", u.user, rec.Code, rec.Body.String())
				}
			}
			asked(true)
			seats := seatsIn(t, "codex")
			if d, a, b := seats["dave"], seats["alice"], seats["bob"]; !slices.Equal(d, []string{on}) || len(a) != 1 || len(b) != 1 || a[0] == on || b[0] == on || a[0] == b[0] {
				t.Fatalf("free packed on %s, pro alone: %v", on, seats)
			}
		})
	}
}

// A free user whose account is out for hours is given no spare that is
// another's alone: the 429 says every other account is taken, and the
// pro user's account is never asked for it.
func TestUserGivenNoSpareThatIsAnothersAlone(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	out, asked := limitUpstream(t)
	s := byUser(New())
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	alices := asked(true)[0]
	if rec := tierPost(s, "bob", "free", "hi"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	bobs := asked(true)[0]
	if bobs == alices {
		t.Fatalf("bob on alice's account %s", alices)
	}
	out(bobs, 7200)
	if rec := tierPost(s, "bob", "free", "spent"); rec.Code != 429 {
		t.Fatalf("bob out: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); slices.Contains(got, alices) {
		t.Fatalf("alice's account asked for bob: %v", got)
	}
	rec := tierPost(s, "bob", "free", "again")
	if limit, until := limitOf(t, rec); rec.Code != 429 || limit != "quota" || !near(until, 2*time.Hour) || !strings.Contains(rec.Body.String(), "every other account of the provider is taken") {
		t.Fatalf("bob again: %d %q until %v %s", rec.Code, limit, until, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
	if n := len(seatsIn(t, "codex")["bob"]); n != 1 {
		t.Fatalf("bob on %d accounts", n)
	}
}

// With the accounts left that a new free user could share resting, and
// the others taken, it is told when the first of them is back, and
// nobody is asked.
func TestFullPoolSaysWhenASharedOneIsBack(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	out, asked := limitUpstream(t)
	s := byUser(New())
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if rec := tierPost(s, "bob", "free", "hi"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	out(asked(true)[0], 7200)
	if rec := tierPost(s, "bob", "free", "spent"); rec.Code != 429 {
		t.Fatalf("bob out: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	rec := tierPost(s, "carol", "free", "hi")
	if limit, until := limitOf(t, rec); rec.Code != 429 || limit != "quota" || !near(until, 2*time.Hour) || !strings.Contains(rec.Body.String(), "can share rest") {
		t.Fatalf("carol: %d %q until %v %s", rec.Code, limit, until, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
}
