package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gui"
	"github.com/yetone/magpie/internal/settings"
)

// magpie accounts keys lists each gateway key that works now with the
// accounts it may be served by (GET /api/accounts/keys' list): "all" for a
// key held to none, the accounts by provider and name otherwise, "none"
// for one that reaches none; --json is the route's own shape.
func TestAccountsKeysListsEachKeysAccounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := accountsKeysTo(&out, args); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	// no key yet, read for real: says so
	if got := call(); !strings.Contains(got, "no gateway key works now") {
		t.Fatalf("no keys: %q", got)
	}
	// one key held to none, read for real: every account
	if _, err := access.Update("add-key", access.Change{Name: "Laptop"}); err != nil {
		t.Fatal(err)
	}
	got := call()
	if !strings.Contains(got, "Laptop") || !strings.Contains(got, "all") {
		t.Fatalf("a key held to none: %q", got)
	}

	// the rule itself is the route's (gui's TestAccountsKeysFollowTheGatewaysRule);
	// here its list as printed
	was := accountsKeysRead
	t.Cleanup(func() { accountsKeysRead = was })
	accountsKeysRead = func() ([]gui.AccountsKey, error) {
		return []gui.AccountsKey{
			{ID: "k-all", Name: "Laptop", All: true, Accounts: []string{}},
			{ID: "k-ci", Name: "CI", Accounts: []string{"claude/bob@example.com", "codex/ops@example.com"}},
			{ID: "k-none", Name: "Nobody", Accounts: []string{}},
		}, nil
	}
	got = call()
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], "ACCOUNTS") {
		t.Fatalf("table: %q", got)
	}
	for i, want := range []string{"all", "claude/bob@example.com, codex/ops@example.com", "none"} {
		if !strings.HasSuffix(strings.TrimSpace(lines[i+1]), want) {
			t.Errorf("line %d %q, want it to end %q", i+1, lines[i+1], want)
		}
	}
	var j struct{ Keys []gui.AccountsKey }
	if err := json.Unmarshal([]byte(call("--json")), &j); err != nil || len(j.Keys) != 3 || !j.Keys[0].All || len(j.Keys[1].Accounts) != 2 {
		t.Fatalf("--json: %v %+v", err, j)
	}
	// a read that fails is said, not taken for no keys
	accountsKeysRead = func() ([]gui.AccountsKey, error) { return nil, errors.New("keys unreadable") }
	if err := accountsKeysTo(&bytes.Buffer{}, nil); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("unreadable: %v", err)
	}
}

// A word magpie accounts keys doesn't take is refused before anything is
// read, and --help shows its line of the usage instead of running it.
func TestAccountsKeysRefusesABadValue(t *testing.T) {
	read := 0
	was := accountsKeysRead
	t.Cleanup(func() { accountsKeysRead = was })
	accountsKeysRead = func() ([]gui.AccountsKey, error) { read++; return nil, nil }
	for _, args := range [][]string{{"codex"}, {"--jsn"}, {"--json", "x"}} {
		var out bytes.Buffer
		if err := accountsKeysTo(&out, args); err == nil || !strings.Contains(err.Error(), "takes only --json") || out.Len() != 0 {
			t.Errorf("%v: %v %q", args, err, out.String())
		}
	}
	// as typed: magpie accounts keys reaches it, not an agent's list
	if err := run([]string{"accounts", "keys", "--jsn"}); err == nil || !strings.Contains(err.Error(), "takes only --json") {
		t.Errorf("magpie accounts keys --jsn: %v", err)
	}
	if read != 0 {
		t.Fatalf("read the keys %d times for a bad value", read)
	}
	if !helpAsked([]string{"accounts", "keys", "--help"}) || !strings.Contains(helpFor([]string{"accounts", "keys", "--help"}), "magpie accounts keys [--json]") {
		t.Fatal("--help doesn't show the command's line")
	}
}
