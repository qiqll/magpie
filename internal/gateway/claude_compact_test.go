package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// compactHarness is a harness whose interactive Claude Code reports a
// prompt of FAKE_PROMPT tokens read from the cache with each reply, and
// answers /compact as Claude Code 2.1.233 writes one to its transcript: a
// compact_boundary row, then the summary.
func compactHarness(t *testing.T) *resumeHarness {
	t.Helper()
	h := tuiScriptHarness(t, `[ -f "$file" ] && cp "$file" "$file.seen"
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
while read -r line; do
  case $line in
  *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue ;;
  *'"command":"/compact"'*)
    echo "told $line" >> $log
    echo '{"type":"system","subtype":"compact_boundary","sessionId":"'$sid'"}' >> "$file"
    echo '{"type":"user","isCompactSummary":true,"sessionId":"'$sid'","message":{"role":"user","content":"compacted summary"}}' >> "$file"
    echo '{"type":"result","subtype":"success","is_error":false,"local_command":"compact","session_id":"'$sid'"}'
    continue ;;
  esac
  echo "told $line" >> $log
  if [ -n "$file" ]; then echo "$line" >> "$file"; fi
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":3,"cache_read_input_tokens":'$FAKE_PROMPT'}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
exit 0`)
	h.p = provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "a@example.com"}, AccountCaps: map[string]int{"a@example.com": 70}}
	return h
}

// A conversation of 100K tokens or more, moved off an account because it
// reached the usage cap the user set on it, is compacted with Claude
// Code's own /compact on that account first, and the next account goes on
// from the compacted session (--resume), typed the turn alone: not the
// whole conversation in a transcript (replaySession), all of it written to
// the new account's cache (T-075: ~19.6M tokens so). Under 100K, or where
// Anthropic itself said the account is used up, nothing is compacted.
func TestClaudeCompactsBeforeTheCapMovesIt(t *testing.T) {
	for _, c := range []struct {
		name    string
		prompt  string
		used    float64
		rests   bool
		compact bool
	}{
		{"at the cap", "150000", 75, false, true},
		{"small", "60000", 75, false, false},
		{"used up", "150000", 100, false, false},
		{"under the cap", "150000", 40, false, false},
		{"resting", "150000", 75, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := compactHarness(t)
			t.Setenv("FAKE_PROMPT", c.prompt)
			capUsage(t, map[string]float64{"a@example.com": 10})
			if c.rests {
				// refused by Anthropic (a 429): it rests
				restingUntil.Lock()
				if restingUntil.m == nil {
					restingUntil.m = map[string]time.Time{}
				}
				restingUntil.m["claude@a@example.com"] = time.Now().Add(time.Hour)
				restingUntil.Unlock()
				t.Cleanup(func() {
					restingUntil.Lock()
					delete(restingUntil.m, "claude@a@example.com")
					restingUntil.Unlock()
				})
			}
			a := h.p
			h.ask("rules", "a1", "a2")
			first := h.runs()
			if len(first) != 1 {
				t.Fatalf("%d runs", len(first))
			}
			// the account reaches its cap, and routing moves the
			// conversation's next turn to another
			capUsage(t, map[string]float64{"a@example.com": c.used})
			h.p = provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "b@example.com"}, AccountCaps: a.AccountCaps}
			h.ask("rules", "a1", "a2", "a3")
			runs := h.runs()
			if len(runs) != 2 {
				t.Fatalf("%d runs", len(runs))
			}
			var old, next string
			for pid, l := range runs {
				if _, ok := first[pid]; ok {
					old = l
				} else {
					next = l
				}
			}
			compacted := strings.Contains(old, `"command":"/compact"`)
			if compacted != c.compact {
				t.Fatalf("compacted %v, want %v:\n%s", compacted, c.compact, old)
			}
			args, _, _ := strings.Cut(next, "\n")
			_, sid, ok := strings.Cut(args, "--resume ")
			if !ok {
				t.Fatalf("the next account's run resumed nothing: %s", args)
			}
			sid, _, _ = strings.Cut(sid, " ")
			seen, _ := filepath.Glob(filepath.Join(h.config, "projects", "*", sid+".jsonl.seen"))
			if len(seen) != 1 {
				t.Fatalf("no session %s as the run started", sid)
			}
			b, _ := os.ReadFile(seen[0])
			told := next[strings.Index(next, "\ntold "):]
			if !strings.Contains(told, "a3") || strings.Contains(told, "a2") {
				t.Fatalf("the next account was typed more than the turn:\n%s", told)
			}
			if !c.compact {
				// told whole in a transcript, as before
				if strings.Contains(string(b), "isCompactSummary") || !strings.Contains(string(b), "a2") {
					t.Fatalf("the session the run went on from:\n%s", b)
				}
				return
			}
			if !strings.Contains(string(b), `"isCompactSummary":true`) || !strings.Contains(string(b), `"sessionId":"`+sid+`"`) {
				t.Fatalf("the next account didn't go on from the compacted session:\n%s", b)
			}
		})
	}
}

// A /compact that fails, or never ends, leaves the turn to go on as
// before: the next account is told the conversation whole, and the old
// account's run ends.
func TestClaudeCompactFailedTellsItWhole(t *testing.T) {
	for _, answer := range []string{"failed", "never"} {
		t.Run(answer, func(t *testing.T) { compactGoesWrong(t, answer) })
	}
}

func compactGoesWrong(t *testing.T, answer string) {
	if answer == "never" {
		old := compactLongest
		compactLongest = 300 * time.Millisecond
		t.Cleanup(func() { compactLongest = old })
	}
	t.Setenv("FAKE_ANSWER", answer)
	h := tuiScriptHarness(t, `[ -f "$file" ] && cp "$file" "$file.seen"
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
while read -r line; do
  case $line in
  *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue ;;
  *'"command":"/compact"'*)
    echo "told $line" >> $log
    [ "$FAKE_ANSWER" = failed ] && echo '{"type":"result","subtype":"error_during_execution","is_error":true,"local_command":"compact","result":"Error during compaction"}'
    continue ;;
  esac
  echo "told $line" >> $log
  if [ -n "$file" ]; then echo "$line" >> "$file"; fi
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":3,"cache_read_input_tokens":150000}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
exit 0`)
	caps := map[string]int{"a@example.com": 70}
	h.p = provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "a@example.com"}, AccountCaps: caps}
	capUsage(t, map[string]float64{"a@example.com": 10})
	h.ask("rules", "a1", "a2")
	first := h.runs()
	capUsage(t, map[string]float64{"a@example.com": 75})
	h.p = provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "b@example.com"}, AccountCaps: caps}
	h.ask("rules", "a1", "a2", "a3")
	runs := h.runs()
	if len(runs) != 2 {
		t.Fatalf("%d runs", len(runs))
	}
	for pid, l := range runs {
		if _, ok := first[pid]; ok {
			if !strings.Contains(l, `"command":"/compact"`) {
				t.Fatalf("no /compact was asked: %s", l)
			}
			continue
		}
		// a transcript of the whole conversation, typed the turn
		_, sid, _ := strings.Cut(l, "--resume ")
		sid, _, _ = strings.Cut(sid, "\n")
		seen, _ := filepath.Glob(filepath.Join(h.config, "projects", "*", strings.TrimSpace(sid)+".jsonl.seen"))
		if len(seen) != 1 {
			t.Fatalf("the next run: %s", l)
		}
		b, _ := os.ReadFile(seen[0])
		if strings.Contains(string(b), "isCompactSummary") || !strings.Contains(string(b), "a2") {
			t.Fatalf("the session the next run went on from:\n%s", b)
		}
	}
	eventually(t, "the old account's run still waits", func() bool {
		h.s.subscription.mu.Lock()
		defer h.s.subscription.mu.Unlock()
		for _, r := range h.s.subscription.idle {
			if u, _ := ownerAccount(r.owned()); u == "a@example.com" {
				return false
			}
		}
		return true
	})
}

func TestMovedSessions(t *testing.T) {
	got := movedSessions([]string{"/a/projects/-w", "/a/projects/-private-w"}, "/b")
	if want := []string{filepath.Join("/b", "projects", "-w"), filepath.Join("/b", "projects", "-private-w")}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%v, want %v", got, want)
	}
}
