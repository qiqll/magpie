package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// The keeper runs while the gateway serves, and the gateway waits for it
// when it stops (a restart, a quit; T-206): a look the gateway's end cuts
// begins no restart of the daemon, and a start under way stops waiting
// for the daemon to say it runs, rather than holding the gateway down for
// scienceWait.
func TestClaudeScienceKeeperEndsWithTheGateway(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := &fakeScience{dir: t.TempDir(), installed: "0.1.62"}
	oldRun, oldBin, oldWait, oldDir := scienceRun, scienceBin, scienceWait, scienceDir
	t.Cleanup(func() { scienceRun, scienceBin, scienceWait, scienceDir = oldRun, oldBin, oldWait, oldDir })
	scienceBin = func() string { return "/bin/claude-science" }
	scienceDir = func() string { return f.dir }
	// long enough that a wait not ended by ctx outlasts the test's
	scienceWait = time.Hour
	// the gateway stops while the keeper runs `on`: ctx ends, and what
	// runs after it fails as CommandContext's does
	var stop context.CancelFunc
	on := ""
	scienceRun = func(ctx context.Context, env []string, bin string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			f.calls = append(f.calls, "cut "+args[0])
			return nil, err
		}
		out, err := f.run(ctx, env, bin, args...)
		if args[0] == on {
			stop()
		}
		return out, err
	}

	t.Run("no restart begun", func(t *testing.T) {
		resetScienceKeep(t)
		// a daemon magpie didn't start, idle: one the keeper restarts
		writeScienceMark(scienceMark{URL: gateway.URL(), PID: 1})
		f.running, f.pid, f.port, f.calls = true, 40, 8123, nil
		f.daemon = map[string]any{"active_frames": 0, "active_conversations": 0, "launch_context": "terminal"}
		f.lock()
		var ctx context.Context
		ctx, stop = context.WithCancel(context.Background())
		defer stop()
		on = "status"
		keepScienceOnce(ctx)
		if !slices.Equal(f.calls, []string{"status"}) || !f.running || f.pid != 40 {
			t.Errorf("the keeper went on past the gateway's end: ran %q, running %v pid %d", f.calls, f.running, f.pid)
		}
	})

	t.Run("a start stops waiting", func(t *testing.T) {
		resetScienceKeep(t)
		writeScienceMark(scienceMark{URL: gateway.URL(), PID: 1})
		f.running, f.calls = false, nil
		f.lock()
		var ctx context.Context
		ctx, stop = context.WithCancel(context.Background())
		defer stop()
		// started, and its status never says it runs
		on = "serve"
		scienceRun = func(ctx context.Context, env []string, bin string, args ...string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				f.calls = append(f.calls, "cut "+args[0])
				return nil, err
			}
			if args[0] == "status" {
				f.calls = append(f.calls, "status")
				return []byte(`{"running":false}`), nil
			}
			out, err := f.run(ctx, env, bin, args...)
			if args[0] == on {
				stop()
			}
			return out, err
		}
		done := make(chan struct{})
		go func() { defer close(done); keepScienceOnce(ctx) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second): // a wait not cut takes scienceWait/60, a minute
			t.Fatalf("the start waited on past the gateway's end: ran %q", f.calls)
		}
		if i := slices.Index(f.calls, "serve --detached --no-browser"); i < 0 || slices.Contains(f.calls[i+1:], "status") {
			t.Errorf("asked status after the gateway's end: %q", f.calls)
		}
	})
}
