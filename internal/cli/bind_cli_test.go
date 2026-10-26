package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// --bind and --bind-max, in either form, set the gateway's MAGPIE_BIND
// and MAGPIE_BIND_MAX and leave the other arguments; a value the gateway
// can't take, given on the command line or found in the environment,
// refuses to start with why.
func TestBindFlags(t *testing.T) {
	for _, c := range []struct {
		args      []string
		bind, max string
		rest      string
	}{
		{[]string{"--web", "--bind", "user", "--bind-max", "3"}, "user", "3", "--web"},
		{[]string{"--bind=session", "--web", "--bind-max=1"}, "session", "1", "--web"},
		{[]string{"--web"}, "", "", "--web"},
	} {
		t.Setenv(gateway.BindEnv, "")
		t.Setenv(gateway.BindMaxEnv, "")
		rest, err := bindFlags(c.args)
		if err != nil || strings.Join(rest, " ") != c.rest {
			t.Fatalf("%v: %v %v", c.args, rest, err)
		}
		if os.Getenv(gateway.BindEnv) != c.bind || os.Getenv(gateway.BindMaxEnv) != c.max {
			t.Errorf("%v: %s=%q %s=%q", c.args, gateway.BindEnv, os.Getenv(gateway.BindEnv), gateway.BindMaxEnv, os.Getenv(gateway.BindMaxEnv))
		}
	}
	for _, c := range []struct {
		args            []string
		envBind, envMax string
		says            string
	}{
		{[]string{"--bind=users"}, "", "", "--bind"},
		{[]string{"--bind"}, "", "", "--bind takes"},
		{[]string{"--bind", "--web"}, "", "", "--bind takes"},
		{[]string{"--bind-max=0"}, "", "", "--bind-max"},
		{[]string{"--bind-max", "two"}, "", "", "--bind-max"},
		{[]string{"--bind-max", "-1"}, "", "", "--bind-max takes"},
		{nil, "nope", "", gateway.BindEnv},
		{nil, "user", "0", gateway.BindMaxEnv},
		{[]string{"--bind=user"}, "", "x", gateway.BindMaxEnv},
	} {
		t.Setenv(gateway.BindEnv, c.envBind)
		t.Setenv(gateway.BindMaxEnv, c.envMax)
		if _, err := bindFlags(c.args); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%v (env %q %q): %v", c.args, c.envBind, c.envMax, err)
		}
		if os.Getenv(gateway.BindEnv) != c.envBind {
			t.Errorf("%v: set %s to %q on a refusal", c.args, gateway.BindEnv, os.Getenv(gateway.BindEnv))
		}
	}
	// a flag over the environment's
	t.Setenv(gateway.BindEnv, "user")
	t.Setenv(gateway.BindMaxEnv, "")
	if _, err := bindFlags([]string{"--bind=session"}); err != nil || os.Getenv(gateway.BindEnv) != "session" {
		t.Fatalf("%v %q", err, os.Getenv(gateway.BindEnv))
	}
}

// magpie serve, web and daemon install refuse a binding they can't take
// before anything starts, and --help prints the flags without starting
// anything.
func TestBindRefusedBeforeStart(t *testing.T) {
	helpHome(t)
	t.Setenv(gateway.BindEnv, "")
	t.Setenv(gateway.BindMaxEnv, "")
	for _, args := range [][]string{
		{"serve", "--bind=users"},
		{"serve", "--bind-max=0"},
		{"web", "--no-open", "--bind=x"},
		{"daemon", "install", "--bind-max=none"},
	} {
		if _, err := runOut(t, args...); err == nil || !strings.Contains(err.Error(), "--bind") {
			t.Errorf("%q: %v", args, err)
		}
	}
	t.Setenv(gateway.BindEnv, "sessions")
	if _, err := runOut(t, "serve"); err == nil || !strings.Contains(err.Error(), gateway.BindEnv) {
		t.Errorf("%s=sessions: %v", gateway.BindEnv, err)
	}
	// taken by install: refused for a test build, not for the flags
	t.Setenv(gateway.BindEnv, "")
	if _, err := runOut(t, "daemon", "install", "--bind=user", "--bind-max", "3"); err == nil || !strings.Contains(err.Error(), "go run") {
		t.Errorf("daemon install --bind=user: %v", err)
	}
	for _, cmd := range []string{"serve", "web", "daemon"} {
		out, err := runOut(t, cmd, "--help")
		if err != nil || !strings.Contains(out, "--bind=session|user") || !strings.Contains(out, "--bind-max=N") {
			t.Errorf("%s --help: %v\n%s", cmd, err, out)
		}
	}
}
