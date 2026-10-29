package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// claudeRefuses has the fake Claude Code fail as Claude Code does on a
// sign-in Anthropic refused, on the accounts whose token is one of toks
// (all of them with none), and answer on the others.
func claudeRefuses(t *testing.T, calls string, toks ...string) {
	t.Helper()
	refused := `[ -z "` + strings.Join(toks, "") + `" ]`
	for _, tok := range toks {
		refused += ` || [ "$tok" = "` + tok + `" ]`
	}
	script := `#!/bin/sh
case "$1" in auth) exit 1;; esac
creds="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.credentials.json"
while read -r line; do
  tok=$(grep -o 'tok-[a-z]*' "$creds" | head -1)
  echo "$tok" >> '` + calls + `'
  if ` + refused + `; then
    echo '{"type":"result","is_error":true,"result":"Failed to authenticate: OAuth session expired and could not be refreshed"}'
  else
    echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"healthy account"}}}'
    echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
    echo '{"type":"stream_event","event":{"type":"message_stop"}}'
    echo '{"type":"result","is_error":false,"result":""}'
  fi
done
`
	binary := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	testenv.Program(t, filepath.Join(binary, "claude"), script)
}

func askClaudeModel(s *Server, text string) *httptest.ResponseRecorder {
	body := `{"model":"claude/claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":"` + text + `"}]}`
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	return rec
}

// An account Anthropic refused goes to the next one at once, with no rest
// to wait out and then fail again: its sign-in is not run again, and the
// next request leaves it out, the route saying why (leaveLapsed).
func TestClaudeAuthFailureFallsBackWithoutRetryingTheLogin(t *testing.T) {
	claudeMadeFirst(t, false)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	calls := filepath.Join(t.TempDir(), "calls")
	claudeRefuses(t, calls, "tok-a")
	for _, text := range []string{"first", "second"} {
		if rec := askClaudeModel(s, text); rec.Code != 200 || !strings.Contains(rec.Body.String(), "healthy account") {
			t.Fatalf("%s: %d %s", text, rec.Code, rec.Body.String())
		}
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "tok-a\n") != 1 {
		t.Fatalf("refused login run again: %s", b)
	}
	routes := s.Trace(context.Background(), 0, 0).Routes
	slices.SortFunc(routes, func(a, b Route) int { return int(a.Seq - b.Seq) })
	if len(routes) != 2 {
		t.Fatalf("routes: %d", len(routes))
	}
	if first := routes[0]; len(first.Tries) != 2 || first.Tries[0].Fail != failAuth || first.Tries[0].Rest != nil {
		t.Fatalf("first request: the refusal not told as one, or rested: %+v", first.Tries)
	}
	if !strings.Contains(routes[0].Tries[0].Error, "OAuth session expired") {
		t.Fatalf("error told: %q", routes[0].Tries[0].Error)
	}
	second := routes[1]
	if len(second.Tries) != 1 || second.Tries[0].ID != "claude@b@example.com" {
		t.Fatalf("second request: the refused account asked again: %+v", second.Tries)
	}
	if !slices.ContainsFunc(second.Left, func(w Weighed) bool { return w.Who == "a@example.com" && strings.Contains(w.Lapsed, "sign in again") }) {
		t.Fatalf("second request: not said why it was left out: %+v", second.Left)
	}
}

// With every account refused, the agent is told a limit to try again
// after, nothing of a sign-in, not that nothing is ready; the call's error
// and the log say to sign in again (the owner's decision, 2026-10-29: it
// used to be told so).
func TestClaudeAllRefusedToldAsALimit(t *testing.T) {
	claudeMadeFirst(t, false)
	loginFile := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var logins []map[string]any
	raw, _ := os.ReadFile(loginFile)
	if err := json.Unmarshal(raw, &logins); err != nil {
		t.Fatal(err)
	}
	for _, l := range logins {
		l["on"] = false
	}
	if err := os.WriteFile(loginFile, mustJSON(logins), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeRefuses(t, filepath.Join(t.TempDir(), "calls"))
	s := New()
	t.Cleanup(s.subscription.abortAll)
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	lapseRefusedSaid.Clear()
	for _, text := range []string{"first", "second"} {
		rec := askClaudeModel(s, text)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: not a limit with a time to try again: %d %v %s", text, rec.Code, rec.Header(), rec.Body.String())
		}
		saysNoLapse(t, rec)
		if c := s.Recent(); len(c) == 0 || !strings.Contains(c[0].Error, "sign-in lapsed") {
			t.Fatalf("%s: the usage log doesn't say why: %+v", text, c)
		}
	}
	if l := logged.String(); !strings.Contains(l, "has to be signed in again") || !strings.Contains(l, "sign in again") {
		t.Fatalf("not logged: %s", l)
	}
}

// Another vendor saying a Claude sign-in's words (an OpenCode plugin on
// Anthropic's OAuth: "OAuth access token has been revoked") isn't a Claude
// account Anthropic refused: it rests as any failure does, and isn't told
// as needing a sign-in that magpie would never try again (#716 review).
func TestSignInWordsFromAnotherVendorRest(t *testing.T) {
	fresh(t)
	revoked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"OAuth access token has been revoked"}}`)
	})
	serveOn(t, "a", "ka", []string{"m"}, revoked)
	serveOn(t, "b", "kb", []string{"m"}, &keyed{})
	if err := provider.SaveGroup(provider.Group{Name: "Two", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := postAs(t, s, "", `{"model":"group/two","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from kb") {
		t.Fatalf("%d %s", code, body)
	}
	if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[0].Fail == failAuth || r.Tries[0].Rest == nil {
		t.Fatalf("not rested as before: %+v", r.Tries)
	}
}
