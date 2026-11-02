package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// T-066: bound by user, a conversation set to stay strictly with the
// account that answered it moved to the user's other account once that
// one rested: the user's accounts left it out, and the conversation was
// routed as a new one. It is refused there instead, as unbound, nobody
// else asked.

// strictUserUpstream stands in for the ChatGPT backend, an account set in
// fail answering 429 so: "quota" out of its allowance for two hours,
// "rate" rate limited for 20 seconds. asked is who was asked since last.
func strictUserUpstream(t *testing.T) (fail func(acct, how string), asked func() []string) {
	t.Helper()
	var mu sync.Mutex
	how := map[string]string{}
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		acct := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		got = append(got, acct)
		h := how[acct]
		mu.Unlock()
		switch h {
		case "quota":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
		case "rate":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"rate_limit_exceeded","code":"rate_limit_exceeded","message":"Rate limit reached for gpt-5.5"}}`)
		default:
			io.WriteString(w, sse(
				`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
				`data: {"type":"response.output_text.delta","delta":"from `+acct+`"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		}
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return func(acct, h string) {
			mu.Lock()
			defer mu.Unlock()
			how[acct] = h
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			out := got
			got = nil
			return out
		}
}

// strictUserSetup signs Codex in to two accounts, routed in order, its
// conversations staying as stays says, with no conversation remembered,
// bound by user, two accounts each, alice on both.
func strictUserSetup(t *testing.T, stays string) (*Server, func(acct, how string), func() []string) {
	t.Helper()
	codexSignedIn(t, "spare@example.com")
	sticks.Lock()
	sticks.m, sticks.opening, sticks.from = map[string]stick{}, map[string]stick{}, ""
	sticks.Unlock()
	t.Cleanup(func() {
		sticks.Lock()
		sticks.m, sticks.opening, sticks.from = map[string]stick{}, map[string]stick{}, ""
		sticks.Unlock()
	})
	fail, asked := strictUserUpstream(t)
	// a rate limit's 20s isn't waited out: refused at once
	was := singlePatience
	singlePatience = 5 * time.Second
	t.Cleanup(func() { singlePatience = was })
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("codex", stays); err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 2)
	// alice on both, as a spare added for her once makes her
	now := time.Now().UTC()
	more := []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com", At: now}}
	if err := os.WriteFile(usersPath(), mustJSON(userFile{Users: map[string]map[string]userBinding{"codex": {"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: now, More: more}}}}), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, fail, asked
}

// strictUserPost posts a turn of the Codex conversation session of user.
func strictUserPost(s *Server, user, session string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping `+session+`"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("session_id", session)
	req.Header.Set(UserHeader, user)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// A strict conversation of a bound user whose account rests, out of its
// allowance or rate limited, stays on it: the next turn is refused with
// the user's limit and when it is back, nobody asked, a Rejected row and
// a line in magpie's log, where it went to the user's other account. A
// new conversation of the user's goes to that one.
func TestStrictUserStaysWhileItsAccountRests(t *testing.T) {
	for _, c := range []struct {
		how, limit string
		back       time.Duration
	}{
		{"quota", "accounts", 2 * time.Hour}, // at its tier's most, as userLimitedAfter says
		{"rate", "rate", 20 * time.Second},
	} {
		t.Run(c.how, func(t *testing.T) {
			s, fail, asked := strictUserSetup(t, provider.AffinityStrict)
			lines := refusedLog(t)
			if rec := strictUserPost(s, "alice", "s1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-1") {
				t.Fatalf("first turn: %d %s", rec.Code, rec.Body.String())
			}
			asked()
			fail("acct-1", c.how)
			if rec := strictUserPost(s, "alice", "s1"); rec.Code != 429 {
				t.Fatalf("its account out: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
			}
			if got := asked(); !slices.Equal(got, []string{"acct-1"}) {
				t.Fatalf("its account out, asked %v", got)
			}
			if why := lastWhy(t, s); why != "strict" {
				t.Fatalf("traced as %q", why)
			}
			// resting now: refused, not moved
			rec := strictUserPost(s, "alice", "s1")
			limit, until := limitOf(t, rec)
			if rec.Code != 429 || limit != c.limit || !near(until, c.back) || !strings.Contains(rec.Body.String(), userLimitSaid) {
				t.Fatalf("resting: %d %q until %v %v %s", rec.Code, limit, until, rec.Header(), rec.Body.String())
			}
			if got := asked(); len(got) != 0 {
				t.Fatalf("resting, asked %v", got)
			}
			row := rejectedOf(t, "alice")
			if row.Provider != "codex" || row.Status != 429 || !strings.Contains(row.Error, "rests until") || !strings.Contains(row.Error, "Stays: Strict") {
				t.Fatalf("alice's row: %+v", row)
			}
			if got := lines(); len(got) != 1 || !strings.Contains(got[0], fmt.Sprintf("user alice refused (%s, 429) by codex", c.limit)) {
				t.Fatalf("log: %q", got)
			}
			// another conversation of the user's goes on its other account
			if rec := strictUserPost(s, "alice", "s3"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-2") {
				t.Fatalf("a new conversation: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Stays: Session moves it, as before: to the user's other account.
func TestSessionUserMovesOffItsRestingAccount(t *testing.T) {
	s, fail, asked := strictUserSetup(t, provider.AffinitySession)
	if rec := strictUserPost(s, "alice", "s1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-1") {
		t.Fatalf("first turn: %d %s", rec.Code, rec.Body.String())
	}
	fail("acct-1", "quota")
	strictUserPost(s, "alice", "s1") // acct-1 rests now, the turn going on to acct-2
	asked()
	if rec := strictUserPost(s, "alice", "s1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-2") {
		t.Fatalf("session, its account resting: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(); !slices.Equal(got, []string{"acct-2"}) {
		t.Fatalf("asked %v", got)
	}
}

// A routing group's strict conversation of a bound user, its account
// resting, stays on it too: refused with the user's limit and when it is
// back, nobody asked, a Rejected row, where it was told as none of the
// user's accounts able to answer (a 503 with no limit). A new
// conversation goes to the user's other account.
func TestStrictUserGroupStaysWhileItsAccountRests(t *testing.T) {
	for _, c := range []struct {
		how, limit string
	}{
		{"quota", "accounts"},
		{"rate", "rate"},
	} {
		t.Run(c.how, func(t *testing.T) {
			s, fail, asked := strictUserSetup(t, provider.AffinitySession)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5"}, Routing: provider.Ordered,
				Affinity: provider.AffinityStrict, Patience: provider.PatienceOff}); err != nil {
				t.Fatal(err)
			}
			post := func(session string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"G","stream":true,"input":"ping `+session+`"}`))
				req.Header.Set("Authorization", "Bearer magpie")
				req.Header.Set("session_id", session)
				req.Header.Set(UserHeader, "alice")
				s.Handler().ServeHTTP(rec, req)
				return rec
			}
			if rec := post("s1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-1") {
				t.Fatalf("first turn: %d %s", rec.Code, rec.Body.String())
			}
			asked()
			fail("acct-1", c.how)
			if rec := post("s1"); strings.Contains(rec.Body.String(), "from acct-") {
				t.Fatalf("its account out: %d %s", rec.Code, rec.Body.String())
			}
			if got := asked(); slices.ContainsFunc(got, func(a string) bool { return a != "acct-1" }) {
				t.Fatalf("its account out, asked %v", got)
			}
			rec := post("s1")
			limit, until := limitOf(t, rec)
			if rec.Code != 429 || limit != c.limit || until.IsZero() || !strings.Contains(rec.Body.String(), userLimitSaid) {
				t.Fatalf("resting: %d %q until %v %v %s", rec.Code, limit, until, rec.Header(), rec.Body.String())
			}
			if got := asked(); len(got) != 0 {
				t.Fatalf("resting, asked %v", got)
			}
			// (a group's Rejected row names no provider, as T-062 wrote it)
			if row := rejectedOf(t, "alice"); row.Status != 429 || !strings.Contains(row.Error, "rests until") {
				t.Fatalf("alice's row: %+v", row)
			}
			if rec := post("s2"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-2") {
				t.Fatalf("a new conversation: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
