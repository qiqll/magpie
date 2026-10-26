package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// --bind, in either form, sets the gateway's MAGPIE_BIND and leaves the
// other arguments; a value the gateway can't take, given on the command
// line or found in the environment, refuses to start with why.
func TestBindFlags(t *testing.T) {
	helpHome(t)
	for _, c := range []struct {
		args       []string
		bind, rest string
	}{
		{[]string{"--web", "--bind", "user"}, "user", "--web"},
		{[]string{"--bind=session", "--web"}, "session", "--web"},
		{[]string{"--web"}, "", "--web"},
	} {
		t.Setenv(gateway.BindEnv, "")
		rest, err := bindFlags(c.args)
		if err != nil || strings.Join(rest, " ") != c.rest {
			t.Fatalf("%v: %v %v", c.args, rest, err)
		}
		if os.Getenv(gateway.BindEnv) != c.bind {
			t.Errorf("%v: %s=%q", c.args, gateway.BindEnv, os.Getenv(gateway.BindEnv))
		}
	}
	for _, c := range []struct {
		args    []string
		envBind string
		says    string
	}{
		{[]string{"--bind=users"}, "", "--bind"},
		{[]string{"--bind"}, "", "--bind takes"},
		{[]string{"--bind", "--web"}, "", "--bind takes"},
		{nil, "nope", gateway.BindEnv},
	} {
		t.Setenv(gateway.BindEnv, c.envBind)
		if _, err := bindFlags(c.args); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%v (env %q): %v", c.args, c.envBind, err)
		}
		if os.Getenv(gateway.BindEnv) != c.envBind {
			t.Errorf("%v: set %s to %q on a refusal", c.args, gateway.BindEnv, os.Getenv(gateway.BindEnv))
		}
	}
	// a flag over the environment's
	t.Setenv(gateway.BindEnv, "user")
	if _, err := bindFlags([]string{"--bind=session"}); err != nil || os.Getenv(gateway.BindEnv) != "session" {
		t.Fatalf("%v %q", err, os.Getenv(gateway.BindEnv))
	}
}

// magpie serve, web and daemon install refuse a binding they can't take
// before anything starts, --bind-max among the arguments they don't
// know, and --help prints the flags without starting anything.
func TestBindRefusedBeforeStart(t *testing.T) {
	helpHome(t)
	t.Setenv(gateway.BindEnv, "")
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"serve", "--bind=users"}, "--bind"},
		{[]string{"web", "--no-open", "--bind=x"}, "--bind"},
		{[]string{"serve", "--bind-max=3"}, `unknown "--bind-max=3"`},
		{[]string{"serve", "--bind=user", "--bind-max", "3"}, `unknown "--bind-max"`},
		{[]string{"web", "--no-open", "--bind-max=3"}, `unknown "--bind-max=3"`},
		{[]string{"daemon", "install", "--bind-max=3"}, `unknown "--bind-max=3"`},
	} {
		if _, err := runOut(t, c.args...); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%q: %v", c.args, err)
		}
	}
	t.Setenv(gateway.BindEnv, "sessions")
	if _, err := runOut(t, "serve"); err == nil || !strings.Contains(err.Error(), gateway.BindEnv) {
		t.Errorf("%s=sessions: %v", gateway.BindEnv, err)
	}
	// taken by install: refused for a test build, not for the flag
	t.Setenv(gateway.BindEnv, "")
	if _, err := runOut(t, "daemon", "install", "--bind=user"); err == nil || !strings.Contains(err.Error(), "go run") {
		t.Errorf("daemon install --bind=user: %v", err)
	}
	for _, cmd := range []string{"serve", "web", "daemon"} {
		out, err := runOut(t, cmd, "--help")
		if err != nil || !strings.Contains(out, "--bind=session|user") || strings.Contains(out, "--bind-max") {
			t.Errorf("%s --help: %v\n%s", cmd, err, out)
		}
	}
}

// Bound by user, a user-tiers.json that doesn't read, or holds a value it
// can't, refuses to start with why, from the flag or MAGPIE_BIND; bound
// by session it isn't read. No file starts on the built-in tiers, and
// none is written.
func TestBindRefusedOnBadTiers(t *testing.T) {
	helpHome(t)
	t.Setenv(gateway.BindEnv, "")
	tiers := filepath.Join(filepath.Dir(provider.Path()), "user-tiers.json")
	if _, err := bindFlags([]string{"--bind=user"}); err != nil {
		t.Fatalf("no file: %v", err)
	}
	if _, err := os.Stat(tiers); !os.IsNotExist(err) {
		t.Fatalf("user-tiers.json written: %v", err)
	}
	os.MkdirAll(filepath.Dir(tiers), 0o700)
	for _, c := range []struct{ body, says string }{
		{`{"default":"free","tiers":`, "unexpected end"},
		{`{"default":"free","tiers":{"free":{"max_accounts":0}}}`, "max_accounts"},
		{`{"default":"gold","tiers":{"free":{"max_accounts":2}}}`, "gold"},
	} {
		os.WriteFile(tiers, []byte(c.body), 0o600)
		t.Setenv(gateway.BindEnv, "")
		if _, err := bindFlags([]string{"--bind=user"}); err == nil || !strings.Contains(err.Error(), c.says) || !strings.Contains(err.Error(), "user-tiers.json") {
			t.Errorf("--bind=user, %s: %v", c.body, err)
		}
		if os.Getenv(gateway.BindEnv) != "" {
			t.Errorf("set %s on a refusal", gateway.BindEnv)
		}
		if _, err := runOut(t, "serve", "--bind", "user"); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("serve --bind user, %s: %v", c.body, err)
		}
		t.Setenv(gateway.BindEnv, "user")
		if _, err := bindFlags(nil); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s=user, %s: %v", gateway.BindEnv, c.body, err)
		}
		if _, err := bindFlags([]string{"--bind=session"}); err != nil {
			t.Errorf("--bind=session, %s: %v", c.body, err)
		}
		if b, _ := os.ReadFile(tiers); string(b) != c.body {
			t.Errorf("written over: %s", b)
		}
	}
}
