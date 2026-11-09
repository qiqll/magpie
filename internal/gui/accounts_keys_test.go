package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// Models › Accounts filters by a gateway key by the gateway's own rule
// (access.AllowsAccount, HeldModels, heldByGroup): a key held to none
// reaches every account; one naming an account of a provider reaches that
// one alone of it, and the rest of the providers its models reach; a model
// glob reaches only providers with such a model; a routing group the key
// may use ("group/<id>", "group/*") reaches its members' providers, held
// to the group's own accounts there; a group gone reaches nothing; a
// provider not in the catalog (turned off) by its id.
func TestAccountsKeysFollowTheGatewaysRule(t *testing.T) {
	sandboxHome(t)
	ps := []provider.Provider{
		{ID: "claude", Name: "Claude", Account: &provider.Account{Agent: "claude", User: "Alice@Example.com"}},
		{ID: "codex", Name: "Codex", Account: &provider.Account{Agent: "codex", User: "ops@example.com"}},
		{ID: "sleepy", Name: "Sleepy", Account: &provider.Account{Agent: "sleepy", User: "s@example.com"}},
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1"},
	}
	alice := provider.LoginID("claude", "Alice@Example.com")
	member := func(pid, model string) provider.Member {
		return provider.Member{Provider: provider.Provider{ID: pid}, Model: model}
	}
	// sleepy is in no catalog: a provider turned off
	c := accountsCatalog{
		models: map[string][]string{"claude": {"claude/claude-opus"}, "codex": {"codex/gpt-5"}},
		groups: []provider.Group{
			{ID: "team", Name: "Team", Accounts: []string{"claude/nobody"}},
			{ID: "open", Name: "Open"},
		},
		members: map[string][]provider.Member{"team": {member("claude", "claude-opus")}, "open": {member("codex", "gpt-5")}},
	}
	keys := []access.Identity{
		{KeyID: "all", KeyName: "All"},
		// an account and no model: that account's provider alone
		{KeyID: "one", KeyName: "One", Accounts: []string{"claude/" + alice}, Models: access.HeldModels(nil, []string{"claude/" + alice})},
		// models of Codex only
		{KeyID: "codex", KeyName: "Codex only", Models: []string{"codex/*"}},
		// every model, and an account of Claude: Claude's that one, Codex's all
		{KeyID: "mixed", KeyName: "Mixed", Models: []string{"*/*"}, Accounts: []string{"claude/" + alice}},
		// an account of Claude that isn't Alice: none of Claude's
		{KeyID: "none", KeyName: "None", Models: []string{"*"}, Accounts: []string{"claude/nobody"}},
		// any provider's claude models: Claude's, not Codex's
		{KeyID: "glob", KeyName: "Glob", Models: []string{"*/claude-*"}},
		// a group held to an account Claude hasn't: none of Claude's
		{KeyID: "team", KeyName: "Team", Models: []string{"group/team"}},
		// every group: Open's Codex, and Team's Claude held to nobody
		{KeyID: "groups", KeyName: "Groups", Models: []string{"group/*"}},
		// a group removed reaches nothing
		{KeyID: "gone", KeyName: "Gone", Models: []string{"group/gone"}},
		// a provider turned off, by its id
		{KeyID: "sleepy", KeyName: "Sleepy", Models: []string{"sleepy/*"}},
	}
	got := map[string][]string{}
	for _, k := range accountsKeysOf(keys, ps, c) {
		if k.All {
			got[k.ID] = []string{"*"}
			continue
		}
		got[k.ID] = k.Accounts
	}
	want := map[string][]string{
		"all":    {"*"},
		"one":    {"claude/alice@example.com"},
		"codex":  {"codex/ops@example.com"},
		"mixed":  {"claude/alice@example.com", "codex/ops@example.com", "sleepy/s@example.com"},
		"none":   {"codex/ops@example.com", "sleepy/s@example.com"},
		"glob":   {"claude/alice@example.com", "sleepy/s@example.com"},
		"team":   {},
		"groups": {"codex/ops@example.com"},
		"gone":   {},
		"sleepy": {"sleepy/s@example.com"},
	}
	for id, w := range want {
		if !slices.Equal(got[id], w) {
			t.Errorf("%s: %v, want %v", id, got[id], w)
		}
	}
}

// The Accounts page's card says when each account was added: the
// providers' view carries magpie's AccountAdded, by the account's name
// in lower case, and leaves out one from before it was kept.
func TestProviderInfoSaysWhenAccountsWereAdded(t *testing.T) {
	sandboxHome(t)
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	// as magpie keeps it beside the Codex subscription's picks (growth.go)
	b, _ := json.Marshal(map[string]any{"providers": []map[string]any{{"id": "codex", "accountAdded": map[string]time.Time{"ops@example.com": at, "old@example.com": time.Unix(0, 0).UTC()}}}})
	if err := os.MkdirAll(filepath.Dir(provider.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got := provider.Provider{ID: "codex", Name: "Codex", Account: &provider.Account{Agent: "codex", User: "Ops@Example.com"}}
	info := providerInfo(got, nil)
	if !info.AccountAdded["ops@example.com"].Equal(at) {
		t.Fatalf("added: %v, want %v", info.AccountAdded, at)
	}
	got.Account.User = "old@example.com"
	if info := providerInfo(got, nil); info.AccountAdded != nil {
		t.Fatalf("one from before AccountAdded was kept is said as added: %v", info.AccountAdded)
	}
}

// An account the user marked banned is used by no key: a key whose models
// reach its provider lists the provider's other accounts and not it.
func TestAccountsKeysLeaveBannedOut(t *testing.T) {
	sandboxHome(t)
	path := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	b, _ := json.Marshal([]map[string]any{
		{"agent": "codex", "user": "Gone@example.com", "banned": true, "seen": time.Now()},
		{"agent": "codex", "user": "off@example.com", "disabled": true, "seen": time.Now()},
		{"agent": "codex", "user": "ops@example.com", "seen": time.Now()},
	})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ps := []provider.Provider{
		{ID: "codex", Name: "Codex", Account: &provider.Account{Agent: "codex", User: "ops@example.com"}},
	}
	c := accountsCatalog{models: map[string][]string{"codex": {"codex/gpt-5"}}}
	keys := []access.Identity{{KeyID: "codex", KeyName: "Codex", Models: []string{"*/gpt-5"}}}
	got := accountsKeysOf(keys, ps, c)
	if len(got) != 1 || !slices.Equal(got[0].Accounts, []string{"codex/ops@example.com"}) {
		t.Fatalf("%+v", got)
	}
}
