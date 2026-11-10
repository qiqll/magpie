package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A Claude account Anthropic answers authentication_failed walls the
// Claude subscription off for the rest of the request: the spare account
// isn't asked (a banned account's request went to the next account in
// the same minute, and that one was banned 19 minutes later). Claude Code says the kind on its assistant message, as it does
// for a revoked token and for any other 401/403. Another failure (529)
// still goes on to the spare, as before.
func TestClaudeAuthFailedWallsTheOtherAccounts(t *testing.T) {
	refused := `    echo '{"type":"assistant","message":{"id":"x","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Failed to authenticate: OAuth token revoked. Please log in again or contact your administrator."}]},"error":"authentication_failed"}'
    echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":403,"result":"Failed to authenticate: OAuth token revoked. Please log in again or contact your administrator."}'
`
	overloaded := `    echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"result":"API Error: 529 overloaded"}'
`
	for _, c := range []struct {
		name, first string
		spare       bool
	}{{"auth", refused, false}, {"overloaded", overloaded, true}} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%v", c.name, stream), func(t *testing.T) {
				log := claudeSubscriptionFirstSays(t, c.first)
				s := New()
				body, _ := json.Marshal(map[string]any{"model": "claude/claude-sonnet-5", "max_tokens": 50, "stream": stream,
					"messages": []map[string]any{{"role": "user", "content": "ping"}}})
				code, reply := sendTo(s, "/v1/messages", string(body))
				b, _ := os.ReadFile(log)
				runs := strings.Join(strings.Fields(string(b)), ",")
				if c.spare {
					if code != 200 || runs != "own,tok-spare" {
						t.Fatalf("a 529 isn't handed on: %d %q %s", code, runs, reply)
					}
					return
				}
				if strings.Contains(runs, "tok-spare") || code == 200 {
					t.Fatalf("the spare was asked after authentication_failed: %d runs %q %s", code, runs, reply)
				}
			})
		}
	}
}

// In a routing group, the walled Claude subscription's spare isn't asked,
// and the group's other provider answers.
func TestClaudeAuthFailedGroupGoesToItsOtherProvider(t *testing.T) {
	refused := `    echo '{"type":"assistant","message":{"id":"x","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Please run /login · API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"Invalid authentication credentials\"}}"}]},"error":"authentication_failed"}'
    echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":401,"result":"Please run /login · API Error: 401"}'
`
	log := claudeSubscriptionFirstSays(t, refused)
	if err := provider.SetRouting("claude", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	other := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "b", provider.Chat, other)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"claude/claude-sonnet-5", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	code, body := postAs(t, New(), "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	b, _ := os.ReadFile(log)
	if runs := strings.Join(strings.Fields(string(b)), ","); code != 200 || other.n != 1 || runs != "own" {
		t.Fatalf("%d %s; the other provider asked %d times; Claude runs %q", code, body, other.n, runs)
	}
}

// claudeAuthFails stands in for Claude Code: tok-a's runs answer
// authentication_failed, Claude Code's kind for a revoked token, and every
// other account's answer "healthy account". Each run's token goes to calls.
func claudeAuthFails(t *testing.T, calls string) {
	t.Helper()
	script := `#!/bin/sh
case "$1" in auth) exit 1;; esac
creds="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.credentials.json"
while read -r line; do
  tok=$(grep -o 'tok-[a-z]*' "$creds" | head -1)
  echo "$tok" >> '` + calls + `'
  if [ "$tok" = tok-a ]; then
    echo '{"type":"assistant","message":{"id":"x","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Failed to authenticate: OAuth token revoked. Please log in again or contact your administrator."}]},"error":"authentication_failed"}'
    echo '{"type":"result","is_error":true,"api_error_status":403,"result":"Failed to authenticate: OAuth token revoked. Please log in again or contact your administrator."}'
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
