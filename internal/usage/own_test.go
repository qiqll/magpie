package usage

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// Filter.Own (T-098) keeps one user's own calls on its seats, and a call
// it leaves out is in nothing of the page: not its rows, its total, nor
// the agents, providers, purposes, keys and accounts listed beside them,
// on the page read from the files' chunks and on the one a replacement
// reader (LogCalls) makes from the ledger alike.
func TestOwnFilterLeavesOthersOutOfThePage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	at := time.Now().Add(-time.Hour)
	call := func(key, user, agent, kind, prov, account, keyID, session string) {
		Append(Record{Time: at, Agent: agent, Kind: kind, Provider: prov, Model: "m1", Status: 200, Input: 10, Output: 1,
			CallerKeyID: key, User: user, ProviderAccount: account, ProviderKeyID: keyID, Session: session})
	}
	call("k1", "alice", "codex", "", "codex", "Me@Example.com", "", "mine-1")
	call("", "alice", "codex", "", "alpha", "", "a1", "mine-2")               // no key: the shared group's
	call("k1", "alice", "codex", "", "codex", "spare@example.com", "", "off") // not her seat
	call("k1", "bob", "claude-code", "title", "codex", "me@example.com", "", "bob")
	call("k2", "alice", "opencode", "", "codex", "me@example.com", "", "k2-alice") // a key not of her group
	call("k1", "", "codex", "", "codex", "me@example.com", "", "nobody")

	own := Own{User: "alice", Keys: "\nk1", Seats: "alpha#a1\ncodex@me@example.com"}
	for _, ledger := range []bool{false, true} {
		if ledger {
			old := LogCalls
			LogCalls = func(time.Time) []sessions.Call { return nil }
			t.Cleanup(func() { LogCalls = old })
		}
		p := QueryPage(All, Filter{Own: own}, 0, 100)
		got := []string{}
		for _, r := range p.Rows {
			got = append(got, r.Session)
		}
		slices.Sort(got)
		if !slices.Equal(got, []string{"mine-1", "mine-2"}) || p.Total != 2 || p.Sum.Calls != 2 {
			t.Errorf("ledger %v: rows %v, total %d, %d calls", ledger, got, p.Total, p.Sum.Calls)
		}
		if !slices.Equal(p.Agents, []string{"codex"}) || !slices.Equal(p.Providers, []string{"alpha", "codex"}) || !slices.Equal(p.Purposes, []string{PurposeOf("")}) {
			t.Errorf("ledger %v: agents %v, providers %v, purposes %v", ledger, p.Agents, p.Providers, p.Purposes)
		}
		if len(p.CallerKeys) != 1 || p.CallerKeys[0].ID != "k1" || p.CallerKeys[0].Calls != 1 {
			t.Errorf("ledger %v: keys %+v", ledger, p.CallerKeys)
		}
		if len(p.Accounts) != 1 || p.Accounts[0].Account != "Me@Example.com" || p.Accounts[0].Calls != 1 {
			t.Errorf("ledger %v: accounts %+v", ledger, p.Accounts)
		}
		for _, s := range p.By["callerKey"] {
			if s.ID != "k1" && s.ID != NoCallerKey {
				t.Errorf("ledger %v: split by key names %q", ledger, s.ID)
			}
		}
		// a pick within it narrows it further
		if p := QueryPage(All, Filter{Own: own, Provider: "alpha"}, 0, 100); p.Total != 1 || p.Rows[0].Session != "mine-2" {
			t.Errorf("ledger %v: alpha's of hers: %d rows", ledger, p.Total)
		}
		// on no seat, nothing; with none, everything
		if p := QueryPage(All, Filter{Own: Own{User: "alice", Keys: "k1"}}, 0, 100); p.Total != 0 || len(p.Agents)+len(p.Accounts)+len(p.CallerKeys) != 0 {
			t.Errorf("ledger %v: on no seat: %d rows, %v %v %v", ledger, p.Total, p.Agents, p.Accounts, p.CallerKeys)
		}
		if p := QueryPage(All, Filter{}, 0, 100); p.Total != 6 {
			t.Errorf("ledger %v: unnarrowed: %d rows", ledger, p.Total)
		}
	}
}
