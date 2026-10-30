package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// writeTiers writes user-tiers.json as body.
func writeTiers(t *testing.T, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(tiersPath()), 0o700)
	if err := os.WriteFile(tiersPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tierPost asks for codex/gpt-5.5 for user, of tier ("" sends none).
func tierPost(s *Server, user, tier, input string) *httptest.ResponseRecorder {
	h := map[string]string{UserHeader: user}
	if tier != "" {
		h[UserTierHeader] = tier
	}
	return postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`, h)
}

// spend sends user's requests of tier until one is refused, each account
// that answers put out of its allowance for two hours after: the
// accounts that answered, in order, and the refusal of the request after
// (the first is the vendor's own 429, its last account asked), which
// asks nobody.
func spend(t *testing.T, s *Server, user, tier string, out func(string, int), asked func(bool) []string) ([]string, *httptest.ResponseRecorder) {
	t.Helper()
	var answered []string
	for i := range 8 {
		rec := tierPost(s, user, tier, user+" turn "+string(rune('a'+i)))
		got := asked(true)
		if rec.Code != 200 {
			rec = tierPost(s, user, tier, user+" refused")
			if got := asked(true); len(got) != 0 {
				t.Fatalf("%s (%q) refused, then asked %v", user, tier, got)
			}
			return answered, rec
		}
		last := got[len(got)-1]
		answered = append(answered, last)
		out(last, 7200)
	}
	t.Fatalf("%s (%q) never refused: %v", user, tier, answered)
	return nil, nil
}

// tierIn is the tier user-accounts.json keeps for each user of pid.
func tierIn(t *testing.T, pid string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(usersPath())
	if err != nil {
		t.Fatal(err)
	}
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for u, b := range f.Users[pid] {
		out[u] = b.Tier
	}
	return out
}

// user-tiers.json takes tiers named as a user is (no control or format
// character, at most userMax bytes), none twice whatever its case, each
// with max_accounts a whole number 1 or more and credits 0 or more, and a
// default among them; anything else says why.
func TestParseTiers(t *testing.T) {
	got, err := parseTiers([]byte(`{"default":"FREE","tiers":{"pro":{"credits":5000,"max_accounts":4},"free":{"credits":0,"max_accounts":1}}}`))
	if err != nil || got.Default != "free" || len(got.Tiers) != 2 || got.Tiers[0] != (Tier{"free", 0, 1, 0}) || got.Tiers[1] != (Tier{"pro", 5000, 4, 0}) {
		t.Fatalf("%+v %v", got, err)
	}
	if tr, ok := got.find("PrO"); !ok || tr.Name != "pro" {
		t.Fatalf("a tier by another case: %+v %v", tr, ok)
	}
	for _, c := range []struct{ body, says string }{
		{`{"default":"free","tiers":{"free":{"credits":1,"max_accounts":0}}}`, "max_accounts"},
		{`{"default":"free","tiers":{"free":{"credits":1}}}`, "max_accounts"},
		{`{"default":"free","tiers":{"free":{"credits":1,"max_accounts":1.5}}}`, "max_accounts"},
		{`{"default":"free","tiers":{"free":{"credits":1,"max_accounts":"2"}}}`, "max_accounts"},
		{`{"default":"free","tiers":{"free":{"credits":-1,"max_accounts":2}}}`, "credits"},
		{`{"tiers":{"free":{"credits":1,"max_accounts":2}}}`, "default"},
		{`{"default":"gold","tiers":{"free":{"credits":1,"max_accounts":2}}}`, `"gold" is none of its tiers (free)`},
		{`{"default":"free","tiers":{}}`, "no tiers"},
		{`{"default":"free"}`, "no tiers"},
		{`{"default":"free","tiers":{"free":{"max_accounts":2},"Free":{"max_accounts":3}}}`, "named twice"},
		{`{"default":"free","tiers":{"free":{"max_accounts":2},"a` + "\u200b" + `b":{"max_accounts":3}}}`, "format"},
		{`{"default":"free","tiers":{"free":{"max_accounts":2},"a\tb":{"max_accounts":3}}}`, "control"},
		{`{"default":"free","tiers":{"free":{"max_accounts":2},"` + strings.Repeat("t", userMax+1) + `":{"max_accounts":3}}}`, "at most"},
		{`{"default":"free","tiers":{"free":{"max_accounts":2}," ":{"max_accounts":3}}}`, "no name"},
		{`{"default":"free","tiers":`, "unexpected end"},
	} {
		if got, err := parseTiers([]byte(c.body)); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %+v %v", c.body, got, err)
		}
	}
}

// No user-tiers.json: the built-in tiers (free 2, plus 3, pro 4, max 5,
// free the default), and none is written. Without a tier a user has
// free's 2 accounts, the third not added though there is one to add; pro
// grows to 4, and the fifth, though there is one to add, is a 429 that
// names the tier and its most.
// The trace and user-accounts.json say each request's tier.
func TestUserTierSetsItsAccounts(t *testing.T) {
	// seven: alice's 2 and bob's 4, and one left to add that isn't
	codexSignedIn(t, "b@example.com", "c@example.com", "d@example.com", "e@example.com", "f@example.com", "g@example.com")
	out, asked := limitUpstream(t)
	s := byUser(New())
	if got, err := UserTiers(); err != nil || got.Default != "free" || !slices.Equal(got.Tiers, defaultTiers.Tiers) {
		t.Fatalf("no file: %+v %v", got, err)
	}

	got, rec := spend(t, s, "alice", "", out, asked)
	if len(got) != 2 || rec.Code != 429 || !strings.Contains(refusedWhy(s), "at its tier free's 2 accounts") {
		t.Fatalf("no tier: answered by %v, then %d %s", got, rec.Code, rec.Body.String())
	}
	if n := len(seatsIn(t, "codex")["alice"]); n != 2 {
		t.Fatalf("alice on %d accounts", n)
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "free" {
		t.Fatalf("route %+v", r.User)
	}

	got, rec = spend(t, s, "bob", "pro", out, asked)
	if len(got) != 4 || rec.Code != 429 || !strings.Contains(refusedWhy(s), "at its tier pro's 4 accounts") || rec.Header().Get(UserLimitHeader) != "quota" {
		t.Fatalf("pro: answered by %v, then %d %v %s", got, rec.Code, rec.Header(), rec.Body.String())
	}
	if seats := seatsIn(t, "codex")["bob"]; len(seats) != 4 {
		t.Fatalf("bob on %v", seats)
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "pro" || len(r.User.Spares) != 3 {
		t.Fatalf("route %+v", r.User)
	}
	// alice named none: kept with none, the default's
	if tiers := tierIn(t, "codex"); tiers["alice"] != "" || tiers["bob"] != "pro" {
		t.Fatalf("kept tiers %v", tiers)
	}
	if _, err := os.Stat(tiersPath()); !os.IsNotExist(err) {
		t.Fatalf("user-tiers.json written: %v", err)
	}
}

// A request that leaves the tier out is of the tier its user's binding
// keeps: a user bound at pro is given pro's accounts, not the default's,
// and the binding keeps pro. A user bound to none is of the default, as
// is one whose binding keeps a tier user-tiers.json no longer has; and
// neither is written, so the binding follows the default as it changes
// and has its tier again when the file does.
func TestUserTierKeptWithoutTheHeader(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com", "d@example.com", "e@example.com", "f@example.com", "g@example.com")
	out, asked := limitUpstream(t)
	s := byUser(New())
	if rec := tierPost(s, "alice", "pro", "bound"); rec.Code != 200 {
		t.Fatalf("pro: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)

	got, rec := spend(t, s, "alice", "", out, asked)
	if len(got) != 4 || rec.Code != 429 || !strings.Contains(refusedWhy(s), "at its tier pro's 4 accounts") {
		t.Fatalf("no header: answered by %v, then %d %s (%s)", got, rec.Code, rec.Body.String(), refusedWhy(s))
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "pro" {
		t.Fatalf("route %+v", r.User)
	}
	if tiers := tierIn(t, "codex"); tiers["alice"] != "pro" {
		t.Fatalf("kept tiers %v", tiers)
	}

	got, rec = spend(t, s, "bob", "", out, asked)
	if len(got) != 2 || rec.Code != 429 || !strings.Contains(refusedWhy(s), "at its tier free's 2 accounts") {
		t.Fatalf("bob: answered by %v, then %d %s", got, rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "free" {
		t.Fatalf("bob's route %+v", r.User)
	}
	if tiers := tierIn(t, "codex"); tiers["bob"] != "" {
		t.Fatalf("the default written: %v", tiers)
	}

	// the default raised: bob, who never named one, is given plus's third
	writeTiers(t, `{"default": "plus", "tiers": {"free": {"max_accounts": 2, "share": 2}, "plus": {"max_accounts": 3, "share": 1}, "pro": {"max_accounts": 4, "share": 1}}}`)
	if rec := tierPost(s, "bob", "", "default raised"); rec.Code != 200 {
		t.Fatalf("default raised: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if r := lastRoute(s); r.User == nil || r.User.Tier != "plus" || len(r.User.Spares) != 2 {
		t.Fatalf("default raised: route %+v", r.User)
	}

	// pro gone from the file: alice is of the default, pro kept for when
	// it is back
	writeTiers(t, `{"default": "free", "tiers": {"free": {"max_accounts": 2}}}`)
	if rec := tierPost(s, "alice", "", "pro gone"); rec.Code != 429 || !strings.Contains(refusedWhy(s), "past its tier free's 2 accounts") {
		t.Fatalf("pro gone: %d %s (%s)", rec.Code, rec.Body.String(), refusedWhy(s))
	}
	if tiers := tierIn(t, "codex"); tiers["alice"] != "pro" {
		t.Fatalf("pro gone: kept tiers %v", tiers)
	}
}

// A tier that isn't user-tiers.json's, or one the header can't take, is
// a 400 that lists the tiers there are, and asks nobody, on each route
// that needs a user, a pinned request's too; another case of one is that
// one.
func TestUserTierRefusedBad(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	for _, c := range []struct{ tier, says string }{
		{"gold", `gold\" is none of user-tiers.json's tiers: free (1), plus (2), pro (3), max (4)`},
		{strings.Repeat("t", userMax+1), "at most"},
		{"a\tb", "control"},
		{"a\u200bb", "format"},
	} {
		if rec := tierPost(s, "alice", c.tier, "hi"); rec.Code != 400 || !strings.Contains(rec.Body.String(), c.says) || !strings.Contains(rec.Body.String(), UserTierHeader) {
			t.Errorf("%q: %d %s", c.tier, rec.Code, rec.Body.String())
		}
		chat := postAt(s, "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, map[string]string{UserHeader: "alice", UserTierHeader: c.tier})
		if chat.Code != 400 || !strings.Contains(chat.Body.String(), c.says) {
			t.Errorf("chat %q: %d %s", c.tier, chat.Code, chat.Body.String())
		}
	}
	// a request pinned to an account isn't bound, and still needs a tier
	// there is
	pinned := postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`, map[string]string{UserHeader: "alice", UserTierHeader: "gold", AccountHeader: "spare@example.com"})
	if pinned.Code != 400 || !strings.Contains(pinned.Body.String(), "free (1), plus (2), pro (3), max (4)") {
		t.Errorf("pinned: %d %s", pinned.Code, pinned.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
	if _, err := os.Stat(usersPath()); err == nil {
		t.Fatal("bound with a bad tier")
	}
	if rec := tierPost(s, "alice", "PRO", "hi"); rec.Code != 200 {
		t.Fatalf("PRO: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "pro" {
		t.Fatalf("route %+v", r.User)
	}
}

// Bound by session, the tier is read nowhere: one that isn't a tier, or
// a user-tiers.json that doesn't read, is routed as a request without
// one, nothing refused; neither the tier nor the user goes upstream.
func TestSessionBindingIgnoresTheTier(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, heads := usersUpstream(t, nil)
	s := New()
	var with, without []string
	for i, tier := range []string{"gold", "pro", "a\u200bb", strings.Repeat("t", userMax+1)} {
		in := "hello " + string(rune('a'+i))
		if rec := tierPost(s, "alice", tier, in); rec.Code != 200 {
			t.Fatalf("%q: %d %s", tier, rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); r.User != nil {
			t.Fatalf("%q: bound %+v", tier, r.User)
		}
		with = append(with, asked(true)...)
	}
	sticks.Lock()
	sticks.m, sticks.opening = map[string]stick{}, map[string]stick{}
	sticks.Unlock()
	for i := range 4 {
		in := "hello " + string(rune('a'+i))
		if rec := userPost(s, "codex/gpt-5.5", "", in); rec.Code != 200 {
			t.Fatalf("%d: %d %s", i, rec.Code, rec.Body.String())
		}
		without = append(without, asked(true)...)
	}
	if strings.Join(with, ",") != strings.Join(without, ",") {
		t.Fatalf("routed apart by the tier: %v, without %v", with, without)
	}
	// a user-tiers.json that doesn't read refuses nothing
	writeTiers(t, `{"default":`)
	for _, tier := range []string{"gold", "pro"} {
		if rec := tierPost(s, "alice", tier, "unread "+tier); rec.Code != 200 {
			t.Fatalf("%q, the file unread: %d %s", tier, rec.Code, rec.Body.String())
		}
	}
	for _, h := range *heads {
		if h.Get(UserTierHeader) != "" || h.Get(UserHeader) != "" {
			t.Fatalf("went upstream: %v", h)
		}
	}
	if _, err := os.Stat(usersPath()); err == nil {
		t.Fatal("user-accounts.json written bound by session")
	}
}

// The tier goes no further than magpie: not to the vendor of a request
// routed, nor through Codex's relay (copyHeaders), nor to a remote MCP
// server.
func TestUserTierNotUpstream(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	_, heads := usersUpstream(t, nil)
	s := byUser(New())
	h := map[string]string{UserHeader: "alice", UserTierHeader: "pro"}
	if rec := postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`, h); rec.Code != 200 {
		t.Fatalf("routed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postAt(s, CodexPath+"/responses", `{"model":"gpt-5.5","stream":true,"input":"hi"}`, h); rec.Code != 200 {
		t.Fatalf("relay: %d %s", rec.Code, rec.Body.String())
	}
	if len(*heads) < 2 {
		t.Fatalf("asked %d times", len(*heads))
	}
	for _, h := range *heads {
		if h.Get(UserTierHeader) != "" || h.Get(UserHeader) != "" {
			t.Fatalf("went upstream: %v", h)
		}
	}
	dst := http.Header{}
	copyHeaders(dst, http.Header{"X-Magpie-User-Tier": {"pro"}, "x-magpie-user-tier": {"pro"}, "Accept": {"text/event-stream"}})
	if dst.Get(UserTierHeader) != "" || len(dst["x-magpie-user-tier"]) != 0 || dst.Get("Accept") == "" {
		t.Fatalf("relay copied %v", dst)
	}

	f := mcpauthtest.New(t)
	mcpauthtest.SignIn(t, "exa", f.URL)
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	req, _ := http.NewRequest("POST", gw.URL+"/mcp/exa", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(UserHeader, "alice")
	req.Header.Set(UserTierHeader, "pro")
	req.Header.Set("X-Exa-Source", "magpie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("mcp: %d %s", resp.StatusCode, b)
	}
	if f.Seen.Get(UserTierHeader) != "" || f.Seen.Get(UserHeader) != "" || f.Seen.Get("X-Exa-Source") != "magpie" {
		t.Fatalf("the MCP server got %v", f.Seen)
	}
}

// user-tiers.json edited holds from the next request: a tier given more
// accounts lets its user have them. Edited so it doesn't read, or holds
// a value it can't, the tiers read before still hold, said once in the
// log a change, and the file is left as it is.
func TestUserTiersReadAgain(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com")
	out, asked := limitUpstream(t)
	var logs bytes.Buffer
	was := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(was) })
	s := byUserUpTo(t, New(), 1)
	got, rec := spend(t, s, "alice", "", out, asked)
	if len(got) != 1 || !strings.Contains(refusedWhy(s), "at its tier free's 1 account") {
		t.Fatalf("at 1: %v %d %s", got, rec.Code, rec.Body.String())
	}
	// edited so it doesn't read: still 1
	for _, bad := range []string{`{"default":"free","tiers":{"free":{"max_accounts":3}}`, `{"default":"free","tiers":{"free":{"max_accounts":0}}}`} {
		writeTiers(t, bad)
		for range 2 {
			rec := tierPost(s, "alice", "", "again")
			if got := asked(true); rec.Code != 429 || len(got) != 0 || !strings.Contains(refusedWhy(s), "at its tier free's 1 account") {
				t.Fatalf("%s: %d asked %v %s", bad, rec.Code, got, rec.Body.String())
			}
		}
		if b, _ := os.ReadFile(tiersPath()); string(b) != bad {
			t.Fatalf("written over: %s", b)
		}
	}
	if n := strings.Count(logs.String(), "user-tiers.json can't be read"); n != 2 {
		t.Fatalf("logged %d times: %s", n, logs.String())
	}
	// mended, with room for 3: grown
	writeTiers(t, `{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":3}}}`)
	got, rec = spend(t, s, "alice", "", out, asked)
	if len(got) != 2 || !strings.Contains(refusedWhy(s), "at its tier free's 3 accounts") {
		t.Fatalf("at 3: %v %d %s", got, rec.Code, rec.Body.String())
	}
	if n := len(seatsIn(t, "codex")["alice"]); n != 3 {
		t.Fatalf("alice on %d", n)
	}
}

// A user-tiers.json that doesn't read with none read before (a gateway
// started on it outside magpie serve): a user's request is a 503 that
// says why, nothing asked nor bound.
func TestUserTiersNeverReadRefuse(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	writeTiers(t, `{"default":"gold","tiers":{"free":{"max_accounts":2}}}`)
	if err := CheckUserTiers(); err == nil || !strings.Contains(err.Error(), "gold") {
		t.Fatalf("checked: %v", err)
	}
	s := byUser(New())
	if rec := tierPost(s, "alice", "", "hi"); rec.Code != 503 || !strings.Contains(rec.Body.String(), "user-tiers.json") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
	if _, err := os.Stat(usersPath()); err == nil {
		t.Fatal("bound")
	}
	// one that can't be read for a moment (its mode) is tried again, its
	// mtime and size as they were
	writeTiers(t, `{"default":"free","tiers":{"free":{"max_accounts":2}}}`)
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		os.Chmod(tiersPath(), 0)
		if rec := tierPost(s, "alice", "", "unreadable"); rec.Code != 503 {
			t.Fatalf("unreadable: %d %s", rec.Code, rec.Body.String())
		}
		os.Chmod(tiersPath(), 0o600)
		if rec := tierPost(s, "alice", "", "readable again"); rec.Code != 200 {
			t.Fatalf("readable again: %d %s", rec.Code, rec.Body.String())
		}
	}
	os.Remove(tiersPath())
	if err := CheckUserTiers(); err != nil {
		t.Fatalf("no file: %v", err)
	}
}

// A user's tier lowered: the accounts it has stay, in their order, and
// none is added while it has as many as the tier lets it or more; raised
// again, it is given one.
func TestUserTierLoweredKeepsItsAccounts(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com", "d@example.com", "e@example.com")
	out, asked := limitUpstream(t)
	s := byUser(New())
	got, _ := spend(t, s, "alice", "pro", out, asked)
	if len(got) != 4 {
		t.Fatalf("pro: %v", got)
	}
	// three of its four: free (2) and plus (3) both at or under it
	seats := seatsIn(t, "codex")["alice"]
	userBinds.Lock()
	b := userBinds.m[""]["codex"]["alice"]
	userBinds.m[""]["codex"]["alice"] = b.withSeats(b.seats()[:3])
	saveUsersLogged()
	userBinds.Unlock()
	for _, tier := range []string{"free", "plus"} {
		rec := tierPost(s, "alice", tier, "lowered "+tier)
		if got := asked(true); rec.Code != 429 || len(got) != 0 {
			t.Fatalf("%q: %d asked %v %s", tier, rec.Code, got, rec.Body.String())
		}
		want := "past its tier free's 2 accounts"
		if tier == "plus" {
			want = "at its tier plus's 3 accounts"
		}
		if !strings.Contains(refusedWhy(s), want) {
			t.Fatalf("%q: %s", tier, refusedWhy(s))
		}
		if now := seatsIn(t, "codex")["alice"]; !slices.Equal(now, seats[:3]) {
			t.Fatalf("%q: %v, was %v", tier, now, seats[:3])
		}
	}
	rec := tierPost(s, "alice", "max", "raised")
	if got := asked(true); rec.Code != 200 || len(got) != 1 {
		t.Fatalf("max: %d asked %v %s", rec.Code, got, rec.Body.String())
	}
	if now := seatsIn(t, "codex")["alice"]; len(now) != 4 || !slices.Equal(now[:3], seats[:3]) {
		t.Fatalf("raised: %v", now)
	}
	if got := tierIn(t, "codex")["alice"]; got != "max" {
		t.Fatalf("a spare added, the tier kept: %q", got)
	}
}

// The tier a user's binding keeps is written when a request names
// another, never for one that names the same: a user's every request
// doesn't write the file.
func TestUserTierWrittenWhenItChanges(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	s := byUser(New())
	if rec := tierPost(s, "alice", "pro", "one"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := tierIn(t, "codex")["alice"]; got != "pro" {
		t.Fatalf("bound with %q", got)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	still := func() bool {
		fi, err := os.Stat(usersPath())
		return err == nil && fi.ModTime().Equal(old)
	}
	os.Chtimes(usersPath(), old, old)
	for _, tier := range []string{"pro", "PRO"} {
		if rec := tierPost(s, "alice", tier, "same "+tier); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if !still() {
			t.Fatalf("%q written again", tier)
		}
	}
	// none named: the binding's tier, kept and not written again
	for _, c := range []struct {
		tier, want string
		written    bool
	}{{"plus", "plus", true}, {"", "plus", false}, {"free", "free", true}} {
		if rec := tierPost(s, "alice", c.tier, "other "+c.tier); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if still() == c.written || tierIn(t, "codex")["alice"] != c.want {
			t.Fatalf("%q: written %v, kept %q", c.tier, !still(), tierIn(t, "codex")["alice"])
		}
		os.Chtimes(usersPath(), old, old)
	}
	// a file from before tiers reads, and is given one
	writeUsers(t, map[string]any{"users": map[string]any{"codex": map[string]any{"bob": map[string]any{"who": "codex@me@example.com", "account": "me@example.com"}}}})
	if rows, err := UserBindings(); err != nil || len(rows) != 1 || rows[0].Tier != "" {
		t.Fatalf("%+v %v", rows, err)
	}
	// named none, it is left as it is; named, it is given that one
	if rec := tierPost(s, "bob", "", "bob"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rows, _ := UserBindings(); len(rows) != 1 || rows[0].Tier != "" {
		t.Fatalf("%+v", rows)
	}
	if rec := tierPost(s, "bob", "free", "bob free"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rows, _ := UserBindings(); len(rows) != 1 || rows[0].Tier != "free" {
		t.Fatalf("%+v", rows)
	}
}

// The gateway's / tells this computer how it binds and, bound by user,
// the tiers it holds to, for the Gateway page of another magpie; bound
// by session it says none, user-tiers.json not read.
func TestInfoSaysTheTiers(t *testing.T) {
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
	writeTiers(t, `{"default":"basic","tiers":{"basic":{"credits":10,"max_accounts":1},"team":{"credits":0,"max_accounts":6}}}`)
	out := info(byUser(New()))
	var tiers Tiers
	if json.Unmarshal(out["tiers"], &tiers) != nil || string(out["bind"]) != `"user"` || tiers.Default != "basic" || len(tiers.Tiers) != 2 || tiers.Tiers[1] != (Tier{"team", 0, 6, 0}) {
		t.Fatalf("by user: %s", mustJSON(out))
	}
	if string(out["idle_hours"]) != "72" {
		t.Fatalf("by user, idle hours: %s", out["idle_hours"])
	}
	writeTiers(t, `{"default":`)
	if out := info(New()); string(out["bind"]) != `"session"` || out["tiers"] != nil || out["idle_hours"] != nil {
		t.Fatalf("by session: %s", mustJSON(out))
	}
}

// The tiers `magpie serve --bind=user` checked as it started are the
// ones it holds to: the file broken before the first request keeps them,
// never a 503.
func TestUserTiersCheckedHold(t *testing.T) {
	codexSignedIn(t)
	usersUpstream(t, nil)
	writeTiers(t, `{"default":"basic","tiers":{"basic":{"credits":0,"max_accounts":1}}}`)
	if err := CheckUserTiers(); err != nil {
		t.Fatal(err)
	}
	writeTiers(t, `{"default":`)
	s := byUser(New())
	if rec := tierPost(s, "alice", "", "hi"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.User == nil || r.User.Tier != "basic" {
		t.Fatalf("route %+v", r.User)
	}
}
