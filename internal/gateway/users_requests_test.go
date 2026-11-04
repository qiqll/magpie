package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// T-098: bound by user, GET /v1/magpie/usage/requests told a request
// naming its user (X-Magpie-User) every row of the log: every account,
// key, user and session, and the filter lists drawn from them. It is told
// its own calls on the accounts and keys it is on alone, as the summary
// (T-093): another user's, another key's alice's (T-089), its own on an
// account it isn't on and a call naming no user are left out, of the
// rows and of every list beside them. A held key's user is narrowed the
// same, within the key's own calls. No user named, or bound by session:
// everything, as before.
func TestBoundUserToldItsOwnRequests(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	now := time.Now().UTC().Truncate(time.Second)
	keys, secrets := newCaller(t, "Full", "Other", "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[2].ID, Models: []string{"codex/*"}}); err != nil {
		t.Fatal(err)
	}
	one, two, beta := "alpha#"+provider.KeyID("alpha-one"), "alpha#"+provider.KeyID("alpha-two"), "beta#"+provider.KeyID("beta-key")
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {
			"alpha": {"alice": {Who: one, Account: "One", At: now}, "bob": {Who: two, Account: "Two", At: now}},
			"beta":  {"alice": {Who: beta, Account: provider.Mask("beta-key"), At: now}},
			"codex": {"alice": {Who: "codex@gone@example.com", Account: "gone@example.com", At: now,
				More: []userSeat{{Who: "codex@me@example.com", Account: "me@example.com", At: now}}}},
		},
		// another key's alice, apart from Full's: its users aren't shared
		keys[1].ID: {"alpha": {"alice": {Who: two, Account: "Two", At: now}}},
		// a held key's alice and bob, on the codex account each
		keys[2].ID: {"codex": {"alice": {Who: "codex@held@example.com", Account: "held@example.com", At: now},
			"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now}}},
	}})
	bound := lanGuard(byUserUpTo(t, New(), 1).Handler())
	bySession := lanGuard(New().Handler())
	at := time.Now().Add(-time.Hour)
	call := func(key, user, agent, prov, keyID, account, session string) {
		usage.Append(usage.Record{Time: at, Agent: agent, Provider: prov, Model: "m1", Status: 200, Input: 10, Output: 1,
			CallerKeyID: key, User: user, ProviderKeyID: keyID, ProviderAccount: account, Session: session})
	}
	call(keys[0].ID, "alice", "codex", "alpha", provider.KeyID("alpha-one"), "", "alice-1")
	call(keys[0].ID, "alice", "codex", "codex", "", "me@example.com", "alice-2")
	call(keys[0].ID, "bob", "claude-code", "alpha", provider.KeyID("alpha-two"), "", "bob-1")
	call(keys[0].ID, "alice", "codex", "codex", "", "spare@example.com", "alice-off-seat")
	call(keys[1].ID, "alice", "codex", "alpha", provider.KeyID("alpha-two"), "", "other-alice")
	call(keys[0].ID, "", "opencode", "codex", "", "me@example.com", "nobody-1")
	call(keys[2].ID, "alice", "codex", "codex", "", "held@example.com", "held-alice")
	call(keys[2].ID, "bob", "codex", "codex", "", "spare@example.com", "held-bob")

	secret := secrets[0]
	do := func(h http.Handler, user string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/magpie/usage/requests?period=all&limit=500", nil)
		r.RemoteAddr = "192.168.1.9:5000"
		r.Header.Set("Authorization", "Bearer "+secret)
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	page := func(h http.Handler, user string) (usageapi.Ledger, string) {
		t.Helper()
		c, b := do(h, user)
		var out usageapi.Ledger
		if c != 200 || json.Unmarshal([]byte(b), &out) != nil {
			t.Fatalf("%q requests: %d %s", user, c, b)
		}
		return out, b
	}
	sessionsOf := func(l usageapi.Ledger) []string {
		got := []string{}
		for _, r := range l.Rows {
			got = append(got, r.Session)
		}
		slices.Sort(got)
		return got
	}
	everyone := []string{"alice-1", "alice-2", "alice-off-seat", "bob-1", "held-alice", "held-bob", "nobody-1", "other-alice"}
	for user, want := range map[string][]string{
		"alice": {"alice-1", "alice-2"},
		"bob":   {"bob-1"},
		"carol": {}, // bound to nothing
	} {
		l, b := page(bound, user)
		if got := sessionsOf(l); !slices.Equal(got, want) || l.Total != len(want) || l.Totals.Calls != len(want) {
			t.Errorf("%s told %d rows (%d calls) of sessions %v, want %v", user, l.Total, l.Totals.Calls, got, want)
		}
		for _, r := range l.Rows {
			if r.User != user {
				t.Errorf("%s told user %q's row", user, r.User)
			}
		}
		for _, k := range l.CallerKeys {
			if k.CallerKeyID != keys[0].ID {
				t.Errorf("%s told key %+v", user, k)
			}
		}
		if user != "alice" {
			continue
		}
		// the lists beside the rows: nothing of anyone else's calls
		for _, other := range []string{"spare@example.com", "held@example.com", provider.KeyID("alpha-two"), keys[1].ID, keys[2].ID, "bob", "other-alice", "nobody-1", "claude-code", "opencode"} {
			if strings.Contains(b, other) {
				t.Errorf("alice's requests name %q: %s", other, b)
			}
		}
		if len(l.Accounts) != 1 || l.Accounts[0].ID != "me@example.com" {
			t.Errorf("alice told accounts %+v", l.Accounts)
		}
		if len(l.Agents) != 1 || l.Agents[0].ID != "codex" {
			t.Errorf("alice told agents %+v", l.Agents)
		}
		if got := len(l.By["callerKey"]); got != 1 {
			t.Errorf("alice told the split of %d keys: %+v", got, l.By["callerKey"])
		}
	}
	// no user named, and bound by session: everything
	for _, c := range []struct {
		h    http.Handler
		user string
	}{{bound, ""}, {bySession, "alice"}} {
		if l, _ := page(c.h, c.user); !slices.Equal(sessionsOf(l), everyone) {
			t.Errorf("%q told sessions %v, want %v", c.user, sessionsOf(l), everyone)
		}
	}
	// Other's alice is its own: her one call
	secret = secrets[1]
	if l, _ := page(bound, "alice"); !slices.Equal(sessionsOf(l), []string{"other-alice"}) {
		t.Errorf("Other's alice told sessions %v", sessionsOf(l))
	}
	// a held key: its own calls, and of them its user's alone
	secret = secrets[2]
	if l, _ := page(bound, ""); !slices.Equal(sessionsOf(l), []string{"held-alice", "held-bob"}) {
		t.Errorf("the held key told sessions %v", sessionsOf(l))
	}
	if l, b := page(bound, "alice"); !slices.Equal(sessionsOf(l), []string{"held-alice"}) || strings.Contains(b, "held-bob") {
		t.Errorf("the held key's alice told sessions %v: %s", sessionsOf(l), b)
	}
	secret = secrets[0]
	if c, b := do(bound, "al ice\x00"); c != http.StatusBadRequest {
		t.Errorf("a user that can't be taken: %d %s", c, b)
	}
	// user-accounts.json written over with what doesn't read: which
	// accounts alice is on is unknown, so she is told no row
	if err := os.WriteFile(usersPath(), []byte(`{"keys":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, b := do(bound, "alice"); c != http.StatusServiceUnavailable || strings.Contains(b, "example.com") || strings.Contains(b, "alice-1") {
		t.Errorf("unread users: %d %s", c, b)
	}
}
