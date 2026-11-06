package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cachedUpstream stands in for the ChatGPT backend as usersUpstream does,
// each reply sent a prompt worth keeping (5000 tokens, none said cached:
// the ChatGPT backend keeps a prompt without saying so), so a
// conversation's next turn stays where it was answered under Stays: Auto.
func cachedUpstream(t *testing.T) (asked func(reset bool) []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		acct := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		got = append(got, acct)
		mu.Unlock()
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"from `+acct+`"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":5000,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return func(reset bool) []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(got)
		if reset {
			got = nil
		}
		return out
	}
}

// sessionsAsked sends each session's request for alice in turn and
// returns the accounts asked, failing on anything but a 200.
func sessionsAsked(t *testing.T, s *Server, asked func(bool) []string, sessions ...string) string {
	t.Helper()
	for _, session := range sessions {
		if rec := sessionPost(s, "alice", session, "hi "+session); rec.Code != 200 {
			t.Fatalf("%s: %d %s", session, rec.Code, rec.Body.String())
		}
	}
	return strings.Join(asked(true), ",")
}

// A user bound to two accounts (T-013): each new session goes to the one
// of them the fewest conversations of the provider's model are held on
// (any user's, on an account shared), the first on a tie, and
// each session's later turns stay where it went, its cache read there.
// Another user's account, holding none, is never asked, and the user is
// bound to nothing more.
func TestUserSessionsSpreadOverItsAccounts(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
		"bob":   {Who: "codex@third@example.com", Account: "third@example.com"},
	}}})
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1", "s2", "s3"); got != "acct-1,acct-2,acct-1" {
		t.Fatalf("new sessions asked %s", got)
	}
	if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "first" {
		t.Fatalf("route %+v", r.Affinity)
	}
	if got := sessionsAsked(t, s, asked, "s2", "s1", "s2", "s3"); got != "acct-2,acct-1,acct-2,acct-1" {
		t.Fatalf("later turns asked %s", got)
	}
	if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "cache" || !r.Affinity.Kept {
		t.Fatalf("a later turn not kept: %+v", r.Affinity)
	}
	if got := seatsIn(t, "codex"); len(got["alice"]) != 2 || len(got["bob"]) != 1 {
		t.Fatalf("bound afresh: %v", got)
	}
}

// A user with one account has every session on it, as before T-013, and
// is given no spare for it.
func TestUserOneAccountKeepsEverySession(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com"},
	}}})
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1", "s2", "s3", "s1"); got != "acct-1,acct-1,acct-1,acct-1" {
		t.Fatalf("one account's sessions asked %s", got)
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 1 {
		t.Fatalf("a spare added: %v", got)
	}
}

// Kept within a turn only, a user's sessions aren't spread: each next
// turn routes afresh, to the first, and a first turn spread to the spare
// would only be moved off it, its cache written twice.
func TestUserSessionsWithinATurnStayOnTheFirst(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	if err := provider.SetAffinity("codex", provider.AffinityTurn); err != nil {
		t.Fatal(err)
	}
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
	}}})
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1", "s2", "s1", "s2"); got != "acct-1,acct-1,acct-1,acct-1" {
		t.Fatalf("sessions within a turn asked %s", got)
	}
}

// A user's account all but used up (past provider.SpentShare) takes no new
// session while another of its own has room, though it holds none: it is
// last as ownedBy puts it.
func TestUserSessionsPassAnAlmostSpentAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
	}}})
	capUsage(t, map[string]float64{"me@example.com": 10, "spare@example.com": 99})
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1", "s2", "s3"); got != "acct-1,acct-1,acct-1" {
		t.Fatalf("sessions asked %s", got)
	}
}

// A user's account past lowShare (90%), not spending its credits first,
// takes no new session while another of its own has room, the user's
// first too and with nobody held anywhere, as readyForNew passes it
// unbound (T-052, the owner's choice A); a session answered there stays.
// With every one of them past it, the order stands: each new session goes
// to the user's first, as unbound.
func TestUserSessionsPassAnAccountPastLowShare(t *testing.T) {
	for _, c := range []struct {
		name  string
		used  map[string]float64
		first string
		later string
	}{
		{"first-low", map[string]float64{"me@example.com": 92, "spare@example.com": 10}, "acct-2,acct-2,acct-2", "acct-2,acct-2"},
		{"spare-low", map[string]float64{"me@example.com": 10, "spare@example.com": 92}, "acct-1,acct-1,acct-1", "acct-1,acct-1"},
		{"both-low", map[string]float64{"me@example.com": 92, "spare@example.com": 93}, "acct-1,acct-1,acct-1", "acct-1,acct-1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			asked := cachedUpstream(t)
			writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
				"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
			}}})
			capUsage(t, c.used)
			s := byUser(New())
			if got := sessionsAsked(t, s, asked, "s1", "s2", "s3"); got != c.first {
				t.Fatalf("new sessions asked %s, want %s", got, c.first)
			}
			if got := sessionsAsked(t, s, asked, "s1", "s2"); got != c.later {
				t.Fatalf("later turns asked %s, want %s", got, c.later)
			}
		})
	}
}

// A conversation routed afresh (cold, T-042) passes the user's own account
// past lowShare as a new one does: s2 went cold on acct-2, now at 92%, and
// goes to acct-1, though acct-1 holds two and acct-2 nothing else.
func TestUserColdSessionPassesAnAccountPastLowShare(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
	}}})
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1", "s2", "s3"); got != "acct-1,acct-2,acct-1" {
		t.Fatalf("new sessions asked %s", got)
	}
	capUsage(t, map[string]float64{"me@example.com": 10, "spare@example.com": 92})
	ageStick(t, "s2", chatGPTCold+time.Minute)
	if got := sessionsAsked(t, s, asked, "s2"); got != "acct-1" {
		t.Fatalf("cold session asked %s, want acct-1", got)
	}
	if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "cold" {
		t.Fatalf("route %+v", r.Affinity)
	}
}

// A group's sessions on a member's provider pass the user's own account
// past lowShare the same (groupTurn.spreadOwn, T-052).
func TestUserGroupSessionsPassAnAccountPastLowShare(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := cachedUpstream(t)
	if err := provider.SaveGroup(provider.Group{ID: "solo", Name: "Solo", Members: []string{"codex/gpt-5.5"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
	}}})
	capUsage(t, map[string]float64{"me@example.com": 92, "spare@example.com": 10})
	s := byUser(New())
	for _, session := range []string{"s1", "s2", "s3"} {
		rec := postAt(s, "/v1/responses", `{"model":"group/solo","stream":true,"input":"hi `+session+`"}`,
			map[string]string{UserHeader: "alice", "session_id": session})
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", session, rec.Code, rec.Body.String())
		}
	}
	if got := strings.Join(asked(true), ","); got != "acct-2,acct-2,acct-2" {
		t.Fatalf("the group's new sessions asked %s", got)
	}
}

// ownLeastHeld's lowShare rule is readyForNew's: one spending its credits
// first is still taken past it, and a subagent's stick still counts where
// it is held (ready's own rule isn't brought along), so of two with room
// the one a subagent holds isn't the one held on fewest (T-052).
func TestOwnLeastHeldLowShareAsReadyForNew(t *testing.T) {
	c := func(key string) candidate {
		return candidate{p: provider.Provider{ID: "t052", Key: key}, model: "m", rest: "t052#" + key}
	}
	a, b, d := c("ka"), c("kb"), c("kd")
	t.Cleanup(func() {
		sticks.Lock()
		for _, k := range []string{"t052|s1", "t052|s2", "t052|x|agent:sub"} {
			delete(sticks.m, k)
			delete(sticks.opening, k)
		}
		sticks.Unlock()
	})
	low := Weighed{Known: true, Used: 92}
	if got, _ := ownLeastHeld("t052", "t052|s1", []candidate{a, b}, planned{order: []Weighed{low, {Known: true, Used: 10}}}, false); got[0].who() != b.who() {
		t.Fatalf("past lowShare, the first took a new session: %s", got[0].who())
	}
	credits := Weighed{Known: true, Used: 92, CreditsFirst: true}
	if got, _ := ownLeastHeld("t052", "t052|s2", []candidate{a, b}, planned{order: []Weighed{credits, {Known: true, Used: 10}}}, false); got[0].who() != a.who() {
		t.Fatalf("spending its credits first, the first was passed: %s", got[0].who())
	}
	sticks.Lock()
	for k := range sticks.opening {
		if strings.HasPrefix(k, "t052|") {
			delete(sticks.opening, k)
		}
	}
	sticks.m["t052|x|agent:sub"] = stick{rest: b.rest, who: b.who(), model: "m", at: time.Now()}
	sticks.Unlock()
	if got, _ := ownLeastHeld("t052", "t052|s1", []candidate{b, d, a}, planned{order: []Weighed{{}, {}, low}}, false); got[0].who() != d.who() {
		t.Fatalf("a subagent's stick not counted: went to %s, want %s", got[0].who(), d.who())
	}
	// with nobody held, a first aside (a key for another protocol) is
	// passed too, as it is with somebody held
	t.Cleanup(func() {
		sticks.Lock()
		delete(sticks.opening, "t052b|s")
		sticks.Unlock()
	})
	if got, _ := ownLeastHeld("t052b", "t052b|s", []candidate{a, b}, planned{order: []Weighed{{Aside: true}, {}}}, false); got[0].who() != b.who() {
		t.Fatalf("with nobody held, an aside first took a new session: %s", got[0].who())
	}
}

// Bound by user under a routing that spreads sessions (Session), a
// subagent's first request goes to the account its session's lead is on,
// one of the user's own, not to the one of them the fewest are held on:
// main's withLead, which the merge with --bind=user's own spread
// (ownLeastHeld) had passed over (T-050).
func TestUserSubagentGoesWithItsLead(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
	}}})
	if err := provider.SetAffinity("codex", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1", "s2"); got != "acct-1,acct-2" {
		t.Fatalf("new sessions asked %s", got)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"a subagent's task"}`))
	req.Header.Set("Authorization", "Bearer magpie")
	req.Header.Set("session_id", "s2")
	req.Header.Set("x-claude-code-agent-id", "sub-1")
	req.Header.Set(UserHeader, "alice")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(asked(true), ","); got != "acct-2" {
		t.Fatalf("s2's subagent asked %s, not s2's acct-2", got)
	}
}

// A subagent goes with its lead (ownWithLead) only while the lead's
// account is ready for a new conversation (readyForNew); with it resting
// the subagent is spread over the user's own, not put on the user's
// first: the review of T-050's lead fix.
func TestUserSubagentSpreadWhileItsLeadRests(t *testing.T) {
	c := func(key string) candidate {
		return candidate{p: provider.Provider{ID: "t050", Key: key}, model: "m", rest: "t050#" + key}
	}
	a, b, l := c("ka"), c("kb"), c("kl")
	now := time.Now()
	sticks.Lock()
	sticks.m["t050|s"] = stick{rest: l.rest, who: l.who(), model: "m", at: now}
	sticks.m["t050|x"] = stick{rest: a.rest, who: a.who(), model: "m", at: now}
	sticks.Unlock()
	t.Cleanup(func() {
		sticks.Lock()
		for _, k := range []string{"t050|s", "t050|x", "t050|s|agent:sub"} {
			delete(sticks.m, k)
			delete(sticks.opening, k)
		}
		sticks.Unlock()
	})
	cs := []candidate{a, b, l}
	ready := planned{order: []Weighed{{}, {}, {}}}
	if got, _ := ownWithLead("t050", "t050|s|agent:sub", cs, ready, true, false); got[0].who() != l.who() {
		t.Fatalf("with its lead ready, the subagent went to %s, want %s", got[0].who(), l.who())
	}
	resting := planned{order: []Weighed{{}, {}, {Rest: &Rest{Why: failQuota, Until: now.Add(time.Hour)}}}}
	if got, _ := ownWithLead("t050", "t050|s|agent:sub", cs, resting, true, false); got[0].who() != b.who() {
		t.Fatalf("with its lead resting, the subagent went to %s, want %s (a holds one, kb none)", got[0].who(), b.who())
	}
}
