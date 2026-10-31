package usageapi

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
	"github.com/yetone/magpie/internal/usage"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// A provider the usage log names but that isn't set up now is still named
// as magpie knows it (@ByronVon1 on X): the Claude Code subscription,
// signed out of since, showed as "claude" in the Requests page's Provider
// filter, between providers named by their names. A preset's removed
// since is named by the preset; a custom one magpie can't know stays its
// id. Every place the page names a provider from the log says the same:
// the filter, each row, the split-by legends, and the summary's models
// and accounts.
func TestProvidersNotSetUpAreNamed(t *testing.T) {
	for _, p := range provider.All() {
		if p.ID == "claude" || p.ID == "deepseek" {
			t.Fatalf("the test home has %s set up", p.ID)
		}
	}
	now := time.Now()
	for i, pid := range []string{"claude", "deepseek", "gone-custom"} {
		usage.Append(usage.Record{Time: now.Add(-time.Duration(i+1) * time.Minute), Agent: "claude", Provider: pid, Model: "m-" + pid, Input: 10, Output: 2, ProviderAccount: "me@example.com"})
	}
	want := map[string]string{"claude": "Claude Code", "deepseek": "DeepSeek", "gone-custom": "gone-custom"}

	l := LedgerPage(usage.All, usage.Filter{}, 0, 100)
	if len(l.Providers) != len(want) {
		t.Fatalf("providers: %+v", l.Providers)
	}
	for _, p := range l.Providers {
		if p.Name != want[p.ID] {
			t.Errorf("the Provider filter names %s %q, want %q", p.ID, p.Name, want[p.ID])
		}
	}
	for _, r := range l.Rows {
		if r.ProviderName != want[r.Provider] {
			t.Errorf("a row names %s %q, want %q", r.Provider, r.ProviderName, want[r.Provider])
		}
	}
	for _, s := range l.By["provider"] {
		if s.Name != want[s.ID] {
			t.Errorf("the split by provider names %s %q, want %q", s.ID, s.Name, want[s.ID])
		}
	}
	for _, s := range l.By["modelAt"] {
		if s.Name == "m-claude · claude" {
			t.Errorf("the split by model at provider names %q", s.Name)
		}
	}
	if len(l.Accounts) != 1 || len(l.Accounts[0].Providers) != 3 {
		t.Errorf("accounts: %+v", l.Accounts)
	}
	for _, a := range l.Accounts {
		for _, n := range a.Providers {
			if n == "claude" || n == "deepseek" {
				t.Errorf("the Account filter names a provider %q", n)
			}
		}
	}

	s := State(usage.All)
	for _, g := range append(s.Models, s.Accounts...) {
		if g.Sub == "claude" || g.Sub == "deepseek" {
			t.Errorf("the summary names %q's provider %q", g.Name, g.Sub)
		}
	}
	if len(s.Models) != 3 {
		t.Errorf("models: %+v", s.Models)
	}
}

// cybercaius on Discord: the Usage page's Provider filter lists a
// provider deleted and the one added under its id since apart, the
// deleted one by its own name with when it was deleted.
func TestDeletedProviderListedApartAndMarked(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Old relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-9"}}); err != nil {
		t.Fatal(err)
	}
	usage.Append(usage.Record{Time: time.Now().Add(-time.Hour), Agent: "codex", Provider: "relay", Model: "gpt-9", Input: 10, Output: 2})
	if err := provider.Delete("relay"); err != nil {
		t.Fatal(err)
	}
	// while no other has its id, its calls stay under it, named as it was
	if ps := LedgerPage(usage.All, usage.Filter{}, 0, 100).Providers; len(ps) != 1 || ps[0].ID != "relay" || ps[0].Name != "Old relay" || ps[0].Deleted == nil {
		t.Fatalf("deleted, its id not taken: %+v", ps)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "New relay", Key: "k2", Chat: "http://127.0.0.1:2/v1", Models: []string{"gpt-9"}}); err != nil {
		t.Fatal(err)
	}
	usage.Append(usage.Record{Time: time.Now().Add(time.Second), Agent: "codex", Provider: "relay", Model: "gpt-9", Input: 10, Output: 2})

	got := map[string]Named{}
	for _, p := range LedgerPage(usage.All, usage.Filter{}, 0, 100).Providers {
		got[p.Name] = p
	}
	if n := got["New relay"]; n.ID != "relay" || n.Deleted != nil {
		t.Errorf("the new one: %+v", n)
	}
	if n := got["Old relay"]; !strings.HasPrefix(n.ID, "relay~") || n.Deleted == nil {
		t.Errorf("the deleted one: %+v (all %+v)", n, got)
	}
}

// CyberCaius on Discord: a deleted provider can be shown by a name of the
// user's, and once merged into another it is listed as that one, which
// names it among its merged ones so the page can part them again.
func TestDeletedProviderNamedAndMerged(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-9"}},
		{ID: "main", Name: "Main", Key: "k2", Chat: "http://127.0.0.1:2/v1", Models: []string{"gpt-9"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	usage.Append(usage.Record{Time: time.Now().Add(-time.Hour), Agent: "codex", Provider: "relay", Model: "gpt-9", Input: 10, Output: 2})
	usage.Append(usage.Record{Time: time.Now().Add(-time.Minute), Agent: "codex", Provider: "main", Model: "gpt-9", Input: 10, Output: 2})
	if err := provider.Delete("relay"); err != nil {
		t.Fatal(err)
	}
	faces := func() map[string]Named {
		got := map[string]Named{}
		for _, p := range LedgerPage(usage.All, usage.Filter{}, 0, 100).Providers {
			got[p.ID] = p
		}
		return got
	}
	if err := provider.NameDeleted("relay", "  Leee (old key)  "); err != nil {
		t.Fatal(err)
	}
	if n := faces()["relay"]; n.Name != "Leee (old key)" || n.Deleted == nil {
		t.Fatalf("named: %+v", n)
	}
	if err := provider.NameDeleted("relay", ""); err != nil {
		t.Fatal(err)
	}
	if n := faces()["relay"]; n.Name != "relay" {
		t.Fatalf("its own name again: %+v", n)
	}
	if err := provider.NameDeleted("relay", "Leee"); err != nil {
		t.Fatal(err)
	}
	if err := provider.MergeDeleted("relay", "main"); err != nil {
		t.Fatal(err)
	}
	got := faces()
	if _, ok := got["relay"]; ok || len(got) != 1 {
		t.Fatalf("merged, still listed: %+v", got)
	}
	if m := got["main"].Merged; len(m) != 1 || m[0].ID != "relay" || m[0].Name != "Leee" {
		t.Fatalf("main's merged: %+v", got["main"])
	}
	if err := provider.MergeDeleted("relay", ""); err != nil {
		t.Fatal(err)
	}
	if got := faces(); got["relay"].Name != "Leee" || got["main"].Merged != nil {
		t.Fatalf("parted: %+v", got)
	}
	if err := provider.NameDeleted("main", "x"); err == nil {
		t.Fatal("named a provider that is not deleted")
	}
}

// T-019: each row carries the name the user gave its account at its own
// provider, so one address named at one provider and not at another is
// shown by that name only in the first's rows; a key's row and a session
// file's (whose account made the session, not the call) carry none.
func TestRowsNameTheirAccountAtTheirProvider(t *testing.T) {
	for _, id := range []string{"named-relay", "plain-relay"} {
		p := provider.Provider{ID: id, Name: id, Chat: "http://127.0.0.1:1/v1", Key: "sk-test"}
		if id == "named-relay" {
			p.AccountNames = map[string]string{"ann@example.com": "工作号"}
		}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	usage.Append(usage.Record{Time: now.Add(-1 * time.Minute), Agent: "codex", Provider: "named-relay", Model: "m", Input: 1, ProviderAccount: "Ann@example.com"})
	usage.Append(usage.Record{Time: now.Add(-2 * time.Minute), Agent: "codex", Provider: "plain-relay", Model: "m", Input: 1, Host: "plain-relay.example as ann@example.com"})
	usage.Append(usage.Record{Time: now.Add(-3 * time.Minute), Agent: "codex", Provider: "named-relay", Model: "m", Input: 1})
	usage.Append(usage.Record{Time: now.Add(-4 * time.Minute), Agent: "codex", Provider: "named-relay", Model: "m", Input: 1, Source: "log", SessionAccount: "ann@example.com"})

	l := LedgerPage(usage.All, usage.Filter{}, 0, 100)
	got := map[string]string{}
	for _, r := range l.Rows {
		if strings.HasSuffix(r.Provider, "-relay") { // the package's other tests write the log too
			got[r.Provider+" "+r.Account()+" "+r.Source] = r.AccountName
		}
	}
	want := map[string]string{
		"named-relay Ann@example.com ": "工作号",
		"plain-relay ann@example.com ": "",
		"named-relay  ":                "",
		"named-relay  log":             "",
	}
	if len(got) != len(want) {
		t.Fatalf("rows: %v", got)
	}
	for k, v := range want {
		if n, ok := got[k]; !ok || n != v {
			t.Errorf("row %q names its account %q, want %q (all: %v)", k, n, v, got)
		}
	}
}

// The Account filter offers one choice per address, and a name is given at
// a provider: an address named 工作号 at one relay and 私人号 at another was
// offered as the first name found at both (T-033). Each provider's name now
// comes with it, in Providers' order, and the choice is named only by a
// name every naming provider agrees on.
func TestAccountFilterNamesEachProviderApart(t *testing.T) {
	names := map[string]map[string]string{
		"work-relay":  {"dan@example.com": "工作号", "eve@example.com": "同名"},
		"home-relay":  {"dan@example.com": "私人号", "eve@example.com": "同名"},
		"blank-relay": nil,
	}
	for id, an := range names {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Chat: "http://127.0.0.1:1/v1", Key: "sk-test", AccountNames: an}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for i, c := range []struct{ prov, acct string }{
		{"work-relay", "dan@example.com"}, {"home-relay", "dan@example.com"}, {"blank-relay", "dan@example.com"},
		{"work-relay", "eve@example.com"}, {"home-relay", "eve@example.com"},
	} {
		usage.Append(usage.Record{Time: now.Add(-time.Duration(i+1) * time.Minute), Agent: "codex", Provider: c.prov, Model: "m", Input: 1, ProviderAccount: c.acct})
	}

	l := LedgerPage(usage.All, usage.Filter{}, 0, 100)
	got := map[string]Account{}
	for _, a := range l.Accounts {
		got[a.ID] = a
	}
	dan := got["dan@example.com"]
	if dan.Name != "" {
		t.Errorf("dan is named %q by one provider's name, at three that name it apart", dan.Name)
	}
	if len(dan.Names) != len(dan.Providers) {
		t.Fatalf("dan's names %q don't go with its providers %q", dan.Names, dan.Providers)
	}
	own := map[string]string{}
	for i, p := range dan.Providers {
		own[p] = dan.Names[i]
	}
	for p, want := range map[string]string{"work-relay": "工作号", "home-relay": "私人号", "blank-relay": ""} {
		if n, ok := own[p]; !ok || n != want {
			t.Errorf("dan at %s is named %q, want %q (all: %v)", p, n, want, own)
		}
	}
	eve := got["eve@example.com"]
	if eve.Name != "同名" || eve.Names != nil {
		t.Errorf("eve, named alike at both, is %q with names %q, want 同名 and none apart", eve.Name, eve.Names)
	}
}

// /api/usage's Users name each caller's user by itself (T-020), the calls
// that named none a group of Name "" the page says "No user" for, and a
// summary with no calls an empty list, never null.
func TestUsersNamedByTheirUser(t *testing.T) {
	s := usage.Summary{Users: []usage.Group{
		{ID: "alice", User: "alice", Totals: usage.Totals{Calls: 2}},
		{ID: "", Totals: usage.Totals{Calls: 1}},
	}}
	got := stateOf(s).Users
	if len(got) != 2 || got[0].Name != "alice" || got[0].User != "alice" || got[0].Calls != 2 || got[1].Name != "" || got[1].Calls != 1 || got[0].Icon != "generic" {
		t.Fatalf("users: %+v", got)
	}
	if got := stateOf(usage.Summary{}).Users; got == nil || len(got) != 0 {
		t.Fatalf("no calls: %#v", got)
	}
}
