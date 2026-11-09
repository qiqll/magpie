package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// magpie users held lists the callers held after authentication_failed,
// and --json the same; magpie users release lets one go by what it names.
// A word either doesn't take is refused before anything is read or
// released, and --help shows the commands' lines.
func TestUsersHeldAndRelease(t *testing.T) {
	now := time.Now()
	holds := []gateway.AuthHold{{Kind: "user", Who: "bob", At: now, Until: now.Add(12 * time.Hour), Provider: "claude", Account: "me@example.com"}}
	read, released := 0, []string{}
	was, wasRel := usersHeldRead, usersRelease
	t.Cleanup(func() { usersHeldRead, usersRelease = was, wasRel })
	usersHeldRead = func() ([]gateway.AuthHold, error) { read++; return holds, nil }
	usersRelease = func(who string) error {
		if who != "user:bob" && who != "bob" {
			return errors.New(who + " isn't held")
		}
		released = append(released, who)
		return nil
	}
	call := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := usersTo(&out, args)
		return out.String(), err
	}
	got, err := call("held")
	if err != nil || !strings.Contains(got, "user:bob") || !strings.Contains(got, "claude@me@example.com") {
		t.Fatalf("held: %v %q", err, got)
	}
	got, err = call("held", "--json")
	var j struct{ Holds []gateway.AuthHold }
	if err != nil || json.Unmarshal([]byte(got), &j) != nil || len(j.Holds) != 1 || j.Holds[0].Who != "bob" {
		t.Fatalf("--json: %v %q", err, got)
	}
	if _, err := call("release", "user:bob"); err != nil || len(released) != 1 {
		t.Fatalf("release: %v %v", err, released)
	}
	if _, err := call("release", "alice"); err == nil {
		t.Fatal("released one not held")
	}
	before := read
	for _, args := range [][]string{{}, {"hold"}, {"held", "--jsn"}, {"release"}, {"release", "--all"}, {"release", "a", "b"}} {
		if _, err := call(args...); err == nil {
			t.Errorf("%v taken", args)
		}
	}
	if read != before || len(released) != 1 {
		t.Fatalf("a bad word read %d times, released %v", read-before, released)
	}
	if !helpAsked([]string{"users", "--help"}) || !strings.Contains(helpFor([]string{"users", "--help"}), "magpie users release <who>") {
		t.Fatal("--help doesn't show the commands")
	}
}

// magpie users hours sets how long a caller is held: a number of hours,
// 0 for nobody, default for 12; anything else is refused, nothing saved.
func TestUsersHours(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var saved []*int
	was := usersSaveSettings
	t.Cleanup(func() { usersSaveSettings = was })
	usersSaveSettings = func(s settings.Settings) error { saved = append(saved, s.AuthHoldHours); return nil }
	for _, a := range []string{"6", "0", "default"} {
		var out bytes.Buffer
		if err := usersTo(&out, []string{"hours", a}); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	if len(saved) != 3 || *saved[0] != 6 || *saved[1] != 0 || saved[2] != nil {
		t.Fatalf("saved %v", saved)
	}
	for _, a := range []string{"-1", "169", "twelve", "--json"} {
		if err := usersTo(&bytes.Buffer{}, []string{"hours", a}); err == nil {
			t.Errorf("%s taken", a)
		}
	}
	if len(saved) != 3 {
		t.Fatalf("a bad value saved: %v", saved)
	}
}
