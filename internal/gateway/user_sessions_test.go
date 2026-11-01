package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

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
// of them the fewest of its sessions are held on, the first on a tie, and
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
