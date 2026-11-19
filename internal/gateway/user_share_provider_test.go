package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// providerShares saves Settings' shares by provider and tier, nil for
// none (T-243).
func providerShares(t *testing.T, m map[string]map[string]int) {
	t.Helper()
	s := settings.Load()
	s.ProviderShares = m
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// noShareTiers is a user-tiers.json whose tiers share an account with any
// number of users, free (alias 1) the default.
const noShareTiers = `{"default":"free","tiers":{"free":{"max_accounts":2,"aliases":["1"]},"plus":{"max_accounts":3}}}`

// A provider's own shares stand in for the file's tiers' (T-243). The
// owner's provider A: plus has an account alone and free shares one two to
// an account, though the file lets any number share. The owner's provider
// B: plus shares one two to an account, free ten; a third plus user and an
// eleventh free one are refused, and neither tier joins the other's
// account.
func TestTierSharesOfAProviderStandInForTheFiles(t *testing.T) {
	t.Run("A", func(t *testing.T) {
		codexSignedIn(t, "b@example.com", "c@example.com")
		asked, _ := usersUpstream(t, nil)
		writeTiers(t, noShareTiers)
		providerShares(t, map[string]map[string]int{"codex": {"free": 2, "PLUS": 1}})
		s := byUser(New())
		for _, u := range []struct{ user, tier string }{{"alice", "plus"}, {"carol", "free"}, {"dave", "free"}, {"bob", "plus"}} {
			if rec := tierPost(s, u.user, u.tier, "hi"); rec.Code != 200 {
				t.Fatalf("%s: %d %s", u.user, rec.Code, rec.Body.String())
			}
		}
		asked(true)
		seats := seatsIn(t, "codex")
		a, b, c, d := seats["alice"], seats["bob"], seats["carol"], seats["dave"]
		if len(a) != 1 || len(b) != 1 || len(c) != 1 || len(d) != 1 || a[0] == b[0] || c[0] != d[0] || c[0] == a[0] || c[0] == b[0] {
			t.Fatalf("plus alone, free two to one: %v", seats)
		}
		rec := tierPost(s, "erin", "free", "hi")
		if limit, _ := limitOf(t, rec); rec.Code != 429 || limit != "seats" || !strings.Contains(refusedWhy(s), "tier free shares one with one other user at most") {
			t.Fatalf("a third free user: %d %q %s", rec.Code, limit, refusedWhy(s))
		}
		if got := asked(true); len(got) != 0 {
			t.Fatalf("asked: %v", got)
		}
	})
	t.Run("B", func(t *testing.T) {
		codexSignedIn(t, "b@example.com")
		asked, _ := usersUpstream(t, nil)
		providerShares(t, map[string]map[string]int{"codex": {"free": 10, "plus": 2, "pro": 2, "max": 2}})
		s := byUser(New())
		for _, u := range []string{"p1", "p2"} {
			if rec := tierPost(s, u, "plus", "hi"); rec.Code != 200 {
				t.Fatalf("%s: %d %s", u, rec.Code, rec.Body.String())
			}
		}
		for i := range 10 {
			if rec := tierPost(s, fmt.Sprint("f", i), "free", "hi"); rec.Code != 200 {
				t.Fatalf("free %d: %d %s", i, rec.Code, rec.Body.String())
			}
		}
		asked(true)
		seats := seatsIn(t, "codex")
		plus, free := seats["p1"], seats["f0"]
		if len(plus) != 1 || !slices.Equal(seats["p2"], plus) || len(free) != 1 || free[0] == plus[0] {
			t.Fatalf("plus two to one, free apart: %v", seats)
		}
		for i := range 10 {
			if f := seats[fmt.Sprint("f", i)]; !slices.Equal(f, free) {
				t.Fatalf("free ten to one: %v", seats)
			}
		}
		for _, u := range []struct{ user, tier string }{{"f10", "free"}, {"p3", "plus"}} {
			rec := tierPost(s, u.user, u.tier, "hi")
			if limit, _ := limitOf(t, rec); rec.Code != 429 || limit != "seats" {
				t.Fatalf("%s: %d %q %s", u.user, rec.Code, limit, rec.Body.String())
			}
		}
		if got := asked(true); len(got) != 0 {
			t.Fatalf("asked: %v", got)
		}
	})
}

// A provider's shares are its own: Settings naming another provider
// leaves codex at the file's tiers, free two to an account, and so does a
// tier user-tiers.json doesn't have.
func TestTierSharesOfAnotherProvider(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	providerShares(t, map[string]map[string]int{"claude": {"free": 10}, "codex": {"gold": 10}})
	s := byUser(New())
	for _, u := range []string{"alice", "bob"} {
		if rec := tierPost(s, u, "free", "hi"); rec.Code != 200 {
			t.Fatalf("%s: %d %s", u, rec.Code, rec.Body.String())
		}
	}
	if rec := tierPost(s, "carol", "free", "hi"); rec.Code != 429 {
		t.Fatalf("a third free user on codex: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
}

// Only users of one tier share an account (T-243): with tiers that share
// with any number, a plus user doesn't join the account a free one is on,
// and is told why; another free one does, named by the tier's alias.
func TestSharedAccountTakesOneTier(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	writeTiers(t, noShareTiers)
	s := byUser(New())
	if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	rec := tierPost(s, "bob", "plus", "hi")
	if limit, _ := limitOf(t, rec); rec.Code != 429 || limit != "seats" || !strings.Contains(refusedWhy(s), "shared by users of another tier") {
		t.Fatalf("plus beside free: %d %q %s", rec.Code, limit, refusedWhy(s))
	}
	if rec := tierPost(s, "carol", "1", "hi"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	seats := seatsIn(t, "codex")
	if !slices.Equal(seats["alice"], seats["carol"]) || len(seats["bob"]) != 0 {
		t.Fatalf("free together, plus none: %v", seats)
	}
}

// A user given a spare isn't given one users of another tier are on: free
// alice, out for hours, has none of plus bob's.
func TestSpareTakesOneTier(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	out, asked := limitUpstream(t)
	writeTiers(t, noShareTiers)
	s := byUser(New())
	if rec := tierPost(s, "bob", "plus", "hi"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	bobs := asked(true)[0]
	// alice takes the account nobody is on: bob's is another tier's
	if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	alices := asked(true)[0]
	if alices == bobs {
		t.Fatalf("alice on bob's %s", bobs)
	}
	out(alices, 7200)
	if rec := tierPost(s, "alice", "free", "spent"); rec.Code != 429 {
		t.Fatalf("alice out: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	rec := tierPost(s, "alice", "free", "again")
	if rec.Code != 429 || !strings.Contains(refusedWhy(s), "shared by users of another tier") {
		t.Fatalf("alice again: %d %s", rec.Code, refusedWhy(s))
	}
	if got := asked(true); slices.Contains(got, bobs) {
		t.Fatalf("bob's account asked for alice: %v", got)
	}
}

// Users of two tiers bound to one account before (T-243) keep it and are
// answered, a binding keeping a tier's alias by hand counted as its tier;
// nobody new joins them, of either tier.
func TestMixedTiersBoundBeforeKept(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	writeTiers(t, noShareTiers)
	s := byUser(New())
	at := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: at, Tier: "1"},
		"bob":   {Who: "codex@me@example.com", Account: "me@example.com", At: at, Tier: "plus"}}}})
	for _, u := range []struct{ user, tier string }{{"alice", ""}, {"bob", "plus"}} {
		if rec := tierPost(s, u.user, u.tier, "hi"); rec.Code != 200 {
			t.Fatalf("%s: %d %s", u.user, rec.Code, rec.Body.String())
		}
	}
	for _, tier := range []string{"free", "plus"} {
		if rec := tierPost(s, "carol", tier, "hi"); rec.Code != 429 {
			t.Fatalf("carol, %s: %d %s", tier, rec.Code, rec.Body.String())
		}
	}
	asked(true)
	if seats := seatsIn(t, "codex"); len(seats["alice"]) != 1 || len(seats["bob"]) != 1 || len(seats["carol"]) != 0 {
		t.Fatalf("kept: %v", seats)
	}
}

// forProvider puts a provider's shares on the tiers they name, by name or
// alias whatever the case, leaving a tier not named, one not in the file
// and a share below 0 alone, and the tiers it was given unchanged.
func TestTiersForProvider(t *testing.T) {
	got := defaultTiers.forProvider(map[string]int{"FREE": 10, "2": 3, "gold": 4, "max": -1})
	if got.shareOf("free") != 10 || got.shareOf("plus") != 3 || got.shareOf("pro") != 1 || got.shareOf("max") != 1 {
		t.Fatalf("%+v", got)
	}
	if defaultTiers.shareOf("free") != 2 || defaultTiers.shareOf("plus") != 1 {
		t.Fatalf("the built-in tiers changed: %+v", defaultTiers)
	}
	if got.nameOf("1") != "free" || got.nameOf("gold") != "free" || (Tiers{}).nameOf("free") != "" {
		t.Fatalf("names: %q %q", got.nameOf("1"), got.nameOf("gold"))
	}
	// a tier named twice by hand takes the fewer users, whatever the
	// map's order, 0 (any number) the most
	for range 20 {
		got = defaultTiers.forProvider(map[string]int{"free": 3, "FREE": 2, "1": 5, "plus": 0, "2": 4})
		if got.shareOf("free") != 2 || got.shareOf("plus") != 4 {
			t.Fatalf("named twice: free %d, plus %d", got.shareOf("free"), got.shareOf("plus"))
		}
	}
}

// A user that keeps another tier under each gateway key of its group (its
// last request under each named one) is of either: a user of one of
// them joins its account, though the user's other tier is another.
func TestUserOfTwoTiersUnderTwoKeysTakesEither(t *testing.T) {
	_, ask, _ := sharedAcrossKeys(t, noShareTiers,
		map[string]map[string][]string{"other": {"x": {"kaAAw@post.com"}}, "qmcloud": {"x": {"kaAAw@post.com"}}},
		map[string]string{"other/x": "plus", "qmcloud/x": "free"})
	usersUpstream(t, nil)
	if rec := ask("y"); rec.Code != 200 {
		t.Fatalf("y, free, beside x: %d %s", rec.Code, rec.Body.String())
	}
}

// A user bound to an account that names another tier keeps the account
// (as users of two tiers bound to one before T-243 do): free alice and
// carol share me@, alice comes back as plus and is served there; the
// account, now of two tiers, takes no new free user.
func TestTierChangedKeepsItsAccount(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	writeTiers(t, noShareTiers)
	s := byUser(New())
	for _, u := range []string{"alice", "carol"} {
		if rec := tierPost(s, u, "free", "hi"); rec.Code != 200 {
			t.Fatalf("%s: %d %s", u, rec.Code, rec.Body.String())
		}
	}
	if rec := tierPost(s, "alice", "plus", "again"); rec.Code != 200 {
		t.Fatalf("alice as plus: %d %s", rec.Code, rec.Body.String())
	}
	if tiers := tierIn(t, "codex"); tiers["alice"] != "plus" || tiers["carol"] != "free" {
		t.Fatalf("tiers kept: %v", tiers)
	}
	if rec := tierPost(s, "dave", "free", "hi"); rec.Code != 429 {
		t.Fatalf("dave: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if seats := seatsIn(t, "codex"); !slices.Equal(seats["alice"], []string{"me@example.com"}) || len(seats["dave"]) != 0 {
		t.Fatalf("seats: %v", seats)
	}
}

// A settings.json that doesn't read is unknown, never "no shares of the
// provider's own": the shares read before hold (sharesFrom), and with
// none read before, a new user isn't bound (503) while one bound is still
// served.
func TestTierSharesUnread(t *testing.T) {
	forget := func() {
		sharesRead.Lock()
		sharesRead.m, sharesRead.ok = nil, false
		sharesRead.Unlock()
	}
	forget()
	t.Cleanup(forget)
	bad := errors.New("could not read settings")
	if m, err := sharesFrom(settings.Settings{}, bad, "codex"); err == nil || m != nil {
		t.Fatalf("never read: %v %v", m, err)
	}
	if m, err := sharesFrom(settings.Settings{ProviderShares: map[string]map[string]int{"codex": {"free": 1}}}, nil, "codex"); err != nil || m["free"] != 1 {
		t.Fatalf("read: %v %v", m, err)
	}
	if m, err := sharesFrom(settings.Settings{}, bad, "codex"); err != nil || m["free"] != 1 {
		t.Fatalf("read before: %v %v", m, err)
	}
	if m, err := sharesFrom(settings.Settings{}, nil, "codex"); err != nil || m != nil {
		t.Fatalf("read again, none: %v %v", m, err)
	}

	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	was := providerSharesOf
	providerSharesOf = func(string) (map[string]int, error) { return nil, bad }
	t.Cleanup(func() { providerSharesOf = was })
	s := byUser(New())
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": bound("me@example.com", "free", ago(time.Minute))}}})
	if rec := tierPost(s, "carol", "free", "hi"); rec.Code != 503 || !strings.Contains(rec.Body.String(), "settings.json can") {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	if rec := tierPost(s, "bob", "free", "hi"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if _, b := seatsIn(t, "codex")["carol"]; b {
		t.Fatal("carol bound")
	}
}

// The gateway's / tells this computer, bound by user, each provider's own
// shares by tier as Settings gives them, by the tiers' own names (an
// alias or another case its tier's, a tier named twice the fewer users),
// a provider naming none of the tiers left out; that they can't be read
// when settings.json doesn't read, never none; and that an account takes
// users of one tier (T-246). With none of its own, the tiers alone, as
// before; by session, none of it.
func TestInfoSaysTierSharesByProvider(t *testing.T) {
	codexSignedIn(t)
	info := func(s *Server) map[string]json.RawMessage {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		s.Handler().ServeHTTP(rec, req)
		var out map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return out
	}
	writeTiers(t, noShareTiers)
	providerShares(t, nil)
	out := info(byUser(New()))
	if out["tiers"] == nil || out["provider_shares"] != nil || out["provider_shares_unknown"] != nil || string(out["one_tier"]) != "true" {
		t.Fatalf("none of their own: %s", mustJSON(out))
	}
	providerShares(t, map[string]map[string]int{
		"codex":  {"FREE": 10, "1": 3, "plus": 0, "nope": 2},
		"claude": {"nope": 1},
		"kimi":   {"plus": 1},
	})
	out = info(byUser(New()))
	var shares map[string]map[string]int
	if err := json.Unmarshal(out["provider_shares"], &shares); err != nil || len(shares) != 2 || !maps.Equal(shares["codex"], map[string]int{"free": 3, "plus": 0}) || !maps.Equal(shares["kimi"], map[string]int{"plus": 1}) {
		t.Fatalf("their own: %s", mustJSON(out))
	}
	// the shares never read and settings.json not reading: unknown
	was := allProviderShares
	allProviderShares = func() (map[string]map[string]int, error) { return nil, errors.New("could not read settings") }
	t.Cleanup(func() { allProviderShares = was })
	out = info(byUser(New()))
	if string(out["provider_shares_unknown"]) != "true" || out["provider_shares"] != nil || out["tiers"] == nil {
		t.Fatalf("unread: %s", mustJSON(out))
	}
	allProviderShares = was
	if out := info(New()); out["provider_shares"] != nil || out["provider_shares_unknown"] != nil || out["one_tier"] != nil {
		t.Fatalf("by session: %s", mustJSON(out))
	}
}

// ServedBy reads what the serving magpie says of its shares by provider
// and one tier to an account, for another magpie's Gateway page (T-246).
func TestServedByTellsTierSharesByProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"magpie","version":"t","bind":"user","tiers":{"default":"free","tiers":[{"name":"free","max_accounts":2,"share":2}]},"provider_shares":{"codex":{"free":10}},"provider_shares_unknown":true,"one_tier":true}`))
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	o := ServedBy()
	if !o.Running || o.Tiers == nil || o.ProviderShares["codex"]["free"] != 10 || !o.SharesUnknown || !o.OneTier {
		t.Fatalf("%+v", o)
	}
}
