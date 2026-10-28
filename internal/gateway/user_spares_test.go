package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// seatsIn is the accounts user-accounts.json binds each user of the
// provider pid to, the first first.
func seatsIn(t *testing.T, pid string) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(usersPath())
	if err != nil {
		t.Fatalf("user-accounts.json: %v", err)
	}
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("user-accounts.json: %v: %s", err, b)
	}
	out := map[string][]string{}
	for u, b := range f.Users[pid] {
		for _, s := range b.seats() {
			out[u] = append(out[u], s.Account)
		}
	}
	return out
}

// sessionPost asks for codex/gpt-5.5 for user, in the conversation
// session.
func sessionPost(s *Server, user, session, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`))
	req.Header.Set("Authorization", "Bearer magpie")
	req.Header.Set("session_id", session)
	if user != "" {
		req.Header.Set(UserHeader, user)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// writeUsers writes user-accounts.json as f.
func writeUsers(t *testing.T, f any) {
	t.Helper()
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	if err := os.WriteFile(usersPath(), mustJSON(f), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The user's one account out of its allowance for hours: in the same
// request a spare is added, the one fewest users are bound to, and it
// answers; the user is bound to both, its first first. The next request
// goes to the spare while the first rests, and nothing more is added.
func TestUserGivenASpareInTheSameRequest(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	out, asked := limitUpstream(t)
	// dave on the spare account as a spare, counted as a first is:
	// alice's first is the first in the plan's order of the two none is
	// on, and her spare the other of them, not the next in the order
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"dave": {Who: "codex@dave@example.com", Account: "dave@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
	}}})
	s := byUserUpTo(t, New(), 2) // no share: as many users as come on an account
	if rec := userPost(s, "codex/gpt-5.5", "alice", "hi"); rec.Code != 200 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	first := asked(true)[0]
	if got := seatsIn(t, "codex")["alice"]; len(got) != 1 {
		t.Fatalf("alice on %v", got)
	}
	out(first, 7200)
	rec := userPost(s, "codex/gpt-5.5", "alice", "again")
	got := asked(true)
	if rec.Code != 200 || len(got) != 2 || got[0] != first || first != "acct-1" || got[1] != "acct-3" {
		t.Fatalf("out for hours: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	if rec.Header().Get(UserLimitHeader) != "" {
		t.Fatalf("answered, yet a limit said: %v", rec.Header())
	}
	seats := seatsIn(t, "codex")["alice"]
	if len(seats) != 2 || seats[1] == seats[0] {
		t.Fatalf("alice's accounts: %v", seats)
	}
	if r := lastRoute(s); r.User == nil || r.User.Why != "added" || r.User.Account != seats[0] || len(r.User.Spares) != 1 || r.User.Spares[0] != seats[1] {
		t.Fatalf("route %+v", r.User)
	}
	spare := got[1]
	for _, in := range []string{"later", "and later"} {
		rec := userPost(s, "codex/gpt-5.5", "alice", in)
		if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != spare {
			t.Fatalf("%s: %d asked %v: %s", in, rec.Code, got, rec.Body.String())
		}
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 2 {
		t.Fatalf("grew again: %v", got)
	}
}

// Every account a user has out before anything is asked (rested by an
// earlier request, when it had no room for a spare): the spare is added
// as the request is routed, and only it is asked.
func TestUserGivenASpareBeforeAsking(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	out, asked := limitUpstream(t)
	one := byUserUpTo(t, New(), 1)
	userPost(one, "codex/gpt-5.5", "alice", "hi")
	first := asked(true)[0]
	out(first, 7200)
	if rec := userPost(one, "codex/gpt-5.5", "alice", "spent"); rec.Code != 429 {
		t.Fatalf("at a max of one: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	// room for a spare: user-tiers.json gone, free's 2 again
	os.Remove(tiersPath())
	s := byUser(New())
	rec := userPost(s, "codex/gpt-5.5", "alice", "again")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] == first {
		t.Fatalf("resting for hours: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 2 {
		t.Fatalf("alice's accounts: %v", got)
	}
}

// A rate limit, or an allowance back within half an hour, adds no spare:
// the error is the user's, its limit said, and nobody else is asked.
func TestUserNotGivenASpareForAShortRest(t *testing.T) {
	t.Run("rate", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		refuse := map[string]int{}
		asked, _ := usersUpstream(t, refuse)
		s := byUser(New())
		userPost(s, "codex/gpt-5.5", "alice", "hi")
		first := asked(true)[0]
		refuse[first] = 429
		rec := userPost(s, "codex/gpt-5.5", "alice", "again")
		if got := asked(true); rec.Code != 429 || slicesHasOther(got, first) {
			t.Fatalf("rate limited: %d asked %v", rec.Code, got)
		}
		if limit, _ := limitOf(t, rec); limit != "rate" {
			t.Fatalf("limit %q", limit)
		}
		if got := seatsIn(t, "codex")["alice"]; len(got) != 1 {
			t.Fatalf("grew on a rate limit: %v", got)
		}
	})
	t.Run("soon", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		out, asked := limitUpstream(t)
		s := byUser(New())
		userPost(s, "codex/gpt-5.5", "alice", "hi")
		first := asked(true)[0]
		out(first, int((userLongRest - 5*time.Minute).Seconds()))
		rec := userPost(s, "codex/gpt-5.5", "alice", "again")
		if got := asked(true); rec.Code != 429 || slicesHasOther(got, first) {
			t.Fatalf("back in 25 minutes: %d asked %v", rec.Code, got)
		}
		if limit, until := limitOf(t, rec); limit != "quota" || !near(until, 25*time.Minute) {
			t.Fatalf("limit %q until %v", limit, until)
		}
		// resting so: refused at once, still no spare
		rec = userPost(s, "codex/gpt-5.5", "alice", "and again")
		if got := asked(true); rec.Code != 429 || len(got) != 0 {
			t.Fatalf("resting 25 minutes: %d asked %v", rec.Code, got)
		}
		if got := seatsIn(t, "codex")["alice"]; len(got) != 1 {
			t.Fatalf("grew on a short rest: %v", got)
		}
	})
}

// slicesHasOther says got names an account other than want.
func slicesHasOther(got []string, want string) bool {
	for _, g := range got {
		if g != want {
			return true
		}
	}
	return false
}

// A user with as many accounts as its tier has (free's 2: no tier sent,
// no user-tiers.json), each out: a 429 that names its accounts, its tier
// and its most, and says when the first of them is back, and for which
// model; the third account isn't asked, nor added.
func TestUserAtItsTierMostIsTold(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	out, asked := limitUpstream(t)
	s := byUser(New())
	userPost(s, "codex/gpt-5.5", "alice", "hi")
	first := asked(true)[0]
	out(first, 7200)
	userPost(s, "codex/gpt-5.5", "alice", "grows")
	spare := asked(true)[1]
	out(spare, 3600)
	rec := userPost(s, "codex/gpt-5.5", "alice", "full")
	got := asked(true)
	if rec.Code != 429 || slicesHasOther(got, spare) {
		t.Fatalf("both out, at the max: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	if limit, until := limitOf(t, rec); limit != "quota" || !near(until, time.Hour) {
		t.Fatalf("the vendor's own: %q until %v: %v", limit, until, rec.Header())
	}
	rec = userPost(s, "codex/gpt-5.5", "alice", "full again")
	if got := asked(true); rec.Code != 429 || len(got) != 0 {
		t.Fatalf("resting, at the max: %d asked %v", rec.Code, got)
	}
	limit, until := limitOf(t, rec)
	if limit != "quota" || !near(until, time.Hour) {
		t.Fatalf("the first back: %q until %v: %v %s", limit, until, rec.Header(), rec.Body.String())
	}
	seats := seatsIn(t, "codex")["alice"]
	// the caller is told a rate limit, as Anthropic's own; why, and the
	// accounts, are the usage log's
	body, why := rec.Body.String(), s.Recent()[0].Error
	if !strings.Contains(body, userLimitSaid) || !strings.Contains(body, "rate_limit_error") || strings.Contains(body, "@") || strings.Contains(body, "tier") {
		t.Fatalf("the caller told more than a rate limit: %s", body)
	}
	if len(seats) != 2 || !strings.Contains(why, "at its tier free's 2 accounts") || !strings.Contains(why, "the first back is "+seats[1]) || !strings.Contains(why, "for every model") {
		t.Fatalf("%v: %s", seats, why)
	}
	if strings.Contains(strings.Join(seats, ","), "third") && first != "acct-3" && spare != "acct-3" {
		t.Fatalf("the third added: %v", seats)
	}
}

// Only accounts past routes.LowShare left: no spare is added, the user's
// error is its own.
func TestUserNotGivenAnAlmostSpentSpare(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	out, asked := limitUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com"},
	}}})
	capUsage(t, map[string]float64{"me@example.com": 10, "spare@example.com": 95})
	s := byUser(New())
	out("acct-1", 7200)
	rec := userPost(s, "codex/gpt-5.5", "alice", "hi")
	if got := asked(true); rec.Code != 429 || slicesHasOther(got, "acct-1") {
		t.Fatalf("only 95%% left: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 1 {
		t.Fatalf("an almost spent spare added: %v", got)
	}
	rec = userPost(s, "codex/gpt-5.5", "alice", "again")
	if got := asked(true); rec.Code != 429 || len(got) != 0 || !strings.Contains(refusedWhy(s), "room") {
		t.Fatalf("resting: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	// with room again, it is added
	capUsage(t, map[string]float64{"me@example.com": 10, "spare@example.com": 50})
	if rec := userPost(s, "codex/gpt-5.5", "alice", "room"); rec.Code != 200 {
		t.Fatalf("with room: %d %s", rec.Code, rec.Body.String())
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 2 || got[1] != "spare@example.com" {
		t.Fatalf("not added with room: %v", got)
	}
}

// Its first back, a user's new conversation goes to it again, while one
// already on its spare stays there.
func TestUserBackOnItsFirst(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	if err := provider.SetAffinity("codex", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	out, asked := limitUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com"},
	}}})
	s := byUser(New())
	out("acct-1", 7200)
	if rec := sessionPost(s, "alice", "s1", "one"); rec.Code != 200 {
		t.Fatalf("s1: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 2 || got[1] != "acct-2" {
		t.Fatalf("s1 asked %v", got)
	}
	// back
	out("acct-1", 0)
	s.UnrestAll("codex@me@example.com")
	if rec := sessionPost(s, "alice", "s1", "one, two"); rec.Code != 200 {
		t.Fatalf("s1 again: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("s1 moved off its spare: %v", got)
	}
	if rec := sessionPost(s, "alice", "s2", "new"); rec.Code != 200 {
		t.Fatalf("s2: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("a new conversation not on the first: %v", got)
	}
}

// A conversation kept strictly on an account the user isn't bound to
// (another user answered it there) goes to the user's own account, not
// refused for want of it.
func TestUserStrictConversationOnAnotherAccount(t *testing.T) {
	b := strictSetup(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com"},
		"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com"},
	}}})
	s := byUser(New())
	for _, c := range []struct{ user, want string }{{"alice", "acct-1"}, {"bob", "acct-2"}, {"alice", "acct-1"}} {
		b.mu.Lock()
		b.tried = nil
		b.mu.Unlock()
		rec := sessionPost(s, c.user, "shared", "ping")
		tried, _ := b.seen()
		if rec.Code != 200 || tried != c.want {
			t.Fatalf("%s: %d tried %q: %s", c.user, rec.Code, tried, rec.Body.String())
		}
	}
}

// A file a magpie before spares wrote reads, its users on their one
// account; one with spares reads, for a magpie before them, as each
// user's first account.
func TestUserFileBothWays(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	os.WriteFile(usersPath(), []byte(`{"users":{"codex":{"alice":{"who":"codex@spare@example.com","account":"spare@example.com","at":"2026-10-20T10:00:00Z"}}}}`), 0o600)
	s := byUser(New())
	if rec := userPost(s, "codex/gpt-5.5", "alice", "hi"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("an old file's user asked %v", got)
	}
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", More: []userSeat{{Who: "codex@me@example.com", Account: "me@example.com"}}},
	}}})
	raw, _ := os.ReadFile(usersPath())
	// user-accounts.json as a magpie before spares reads it
	var old struct {
		Users map[string]map[string]struct {
			Who     string    `json:"who"`
			Account string    `json:"account"`
			At      time.Time `json:"at"`
		} `json:"users"`
	}
	if err := json.Unmarshal(raw, &old); err != nil || old.Users["codex"]["bob"].Who != "codex@spare@example.com" {
		t.Fatalf("read before spares: %+v %v: %s", old, err, raw)
	}
	if strings.Contains(string(mustJSON(userBinding{Who: "x"})), "more") {
		t.Fatal("no spares written as more")
	}
	rows, err := UserBindings()
	if err != nil || len(rows) != 1 || rows[0].Account != "spare@example.com" || len(rows[0].Spares) != 1 || rows[0].Spares[0].Account != "me@example.com" {
		t.Fatalf("%+v %v", rows, err)
	}
}

// Its first account gone (switched off) or turned away for good, a
// user's first spare is its first; a user whose every account goes is
// bound afresh.
func TestUserSpareTakesTheFirstsPlace(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@spare@example.com", Account: "spare@example.com", More: []userSeat{{Who: "codex@me@example.com", Account: "me@example.com"}}},
		"bob":   {Who: "codex@third@example.com", Account: "third@example.com", More: []userSeat{{Who: "codex@me@example.com", Account: "me@example.com"}}},
	}}})
	s := byUser(New())
	// the spare account switched off
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var saved []map[string]any
	raw, _ := os.ReadFile(logins)
	json.Unmarshal(raw, &saved)
	for _, l := range saved {
		if l["user"] == "spare@example.com" {
			l["on"] = false
		}
	}
	os.WriteFile(logins, mustJSON(saved), 0o600)
	provider.ForgetAccounts()
	rec := userPost(s, "codex/gpt-5.5", "alice", "after")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("alice: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 1 || got[0] != "me@example.com" {
		t.Fatalf("the spare isn't the first: %v", got)
	}
	if r := lastRoute(s); r.User == nil || r.User.Why != "gone" {
		t.Fatalf("route %+v", r.User)
	}
	// bob's first restricted: its spare is its first; then that one too,
	// and bob is bound to none
	third := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "third@example.com"}}, rest: "codex@third@example.com"}
	s.unbindTurnedAway(third, failRestricted)
	if got := seatsIn(t, "codex")["bob"]; len(got) != 1 || got[0] != "me@example.com" {
		t.Fatalf("bob: %v", got)
	}
	me := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}, rest: "codex"}
	s.unbindTurnedAway(me, failVerify)
	if got := seatsIn(t, "codex"); len(got["bob"]) != 0 || len(got["alice"]) != 0 {
		t.Fatalf("still bound: %v", got)
	}
}

// A provider routed in turn sends a user's new conversations to its first
// all the same: its accounts go in their order, not round them.
func TestUserFirstThoughRoutedInTurn(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	if err := provider.SetRouting("codex", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("codex", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	asked, _ := usersUpstream(t, nil)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@spare@example.com", Account: "spare@example.com", More: []userSeat{{Who: "codex@me@example.com", Account: "me@example.com"}}},
	}}})
	s := byUser(New())
	for _, session := range []string{"r1", "r2", "r3"} {
		if rec := sessionPost(s, "alice", session, "hi "+session); rec.Code != 200 {
			t.Fatalf("%s: %d %s", session, rec.Code, rec.Body.String())
		}
	}
	if got := strings.Join(asked(true), ","); got != "acct-2,acct-2,acct-2" {
		t.Fatalf("sent round alice's accounts: %s", got)
	}
}
