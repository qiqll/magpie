package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/testenv"
)

// With the interactive mode on, /usage is typed in it, through its
// helper, where -p's /usage tells no window of the plan (Claude Code
// 2.1.316): the helper is told the command, and what it tells is the
// reading. No claude -p runs. Turned off, it runs as -p.
func TestClaudeUsageTUIOn(t *testing.T) {
	plog := fakeWarmClaude(t, `{"type":"result","is_error":false,"result":"Total cost: $0.0000"}`, 0)
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	os.Unsetenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
	log := filepath.Join(t.TempDir(), "log")
	text := "Current session: 99% used · resets 7pm (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
shift 2
{ printf 'args:'; for a in "$@"; do printf '[%s]' "$a"; done; echo
  echo "trust:$`+claudetui.TrustEnv+`"; echo "traffic:$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"; echo "telemetry:$DISABLE_TELEMETRY"
  IFS= read -r line; echo "told:$line"; } > '`+log+`'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"local_command":"usage","result":"Current session: 99% used · resets 7pm (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"}'
cat > /dev/null
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got, err := claudeUsage(ctx, "")
	if err != nil || got != text {
		t.Fatalf("usage %q %v", got, err)
	}
	b, _ := os.ReadFile(log)
	for _, want := range []string{"[-p][--output-format][stream-json][--input-format][stream-json]", "[--tools][]", "[--setting-sources][]",
		"trust:1\n", "traffic:\n", "telemetry:1\n", `told:{"type":"command","command":"/usage"}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the helper's run lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "/usage]") {
		t.Errorf("/usage was given as an argument, not typed:\n%s", b)
	}
	if _, err := os.Stat(plog); err == nil {
		t.Fatal("claude -p ran with the interactive mode on")
	}
	printMode(t, true)
	if _, err := claudeUsage(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(plog); !strings.Contains(string(b), "[-p][/usage]") {
		t.Fatalf("turned off, /usage didn't run as -p:\n%s", b)
	}
}
