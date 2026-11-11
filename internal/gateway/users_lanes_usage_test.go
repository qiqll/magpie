package gateway

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// T-093: bound by user, a request naming its user (X-Magpie-User) was
// told every account's lane from /v1/magpie/concurrency (codex@<email>)
// and everyone's usage summary from /v1/magpie/usage, every account,
// key, user and session in it. It is told the lanes of the accounts and
// keys it is on alone, as its quotas (T-090), and of the usage its own
// calls on them: another user's, another key's alice's (T-089) and its
// own on an account it isn't on are left out. No user named is
// DefaultUser's (T-163), told its own alone as any user; bound by
// session: everything, as before.
func TestBoundUserToldItsOwnLanesAndUsage(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	now := time.Now().UTC().Truncate(time.Second)
	keys, secrets := newCaller(t, "Full", "Other")
	one, two, beta := "alpha#"+provider.KeyID("alpha-one"), "alpha#"+provider.KeyID("alpha-two"), "beta#"+provider.KeyID("beta-key")
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {
			"alpha": {"alice": {Who: one, Account: "One", At: now}, "bob": {Who: two, Account: "Two", At: now}},
			"beta":  {"alice": {Who: beta, Account: provider.Mask("beta-key"), At: now}},
			"codex": {"alice": {Who: "codex@gone@example.com", Account: "gone@example.com", At: now,
				More: []userSeat{{Who: "codex@me@example.com", Account: "me@example.com", At: now}}},
				DefaultUser: {Who: "codex@spare@example.com", Account: "spare@example.com", At: now}},
		},
		// another key's alice, apart from Full's: its users aren't shared
		keys[1].ID: {"alpha": {"alice": {Who: two, Account: "Two", At: now}}},
	}})
	srv := byUserUpTo(t, New(), 1)
	bound := lanGuard(srv.Handler())
	sessions := New()
	bySession := lanGuard(sessions.Handler())
	// a request out on each lane, in both servers
	every := []string{one, two, beta, "codex@me@example.com", "codex@spare@example.com", "plain"}
	for _, s := range []*Server{srv, sessions} {
		for _, who := range every {
			release, _ := s.lanes.tryTake(who, 0)
			t.Cleanup(release)
		}
	}
	// the calls: alice's own on her seats (two), bob's, alice's on an
	// account she isn't on, Other's alice's, and one naming no user
	at := time.Now().Add(-time.Hour)
	call := func(key, user, prov, keyID, account, session string) {
		usage.Append(usage.Record{Time: at, Agent: "codex", Provider: prov, Model: "m1", Status: 200, Input: 10, Output: 1,
			CallerKeyID: key, User: user, ProviderKeyID: keyID, ProviderAccount: account, Session: session})
	}
	call(keys[0].ID, "alice", "alpha", provider.KeyID("alpha-one"), "", "alice-1")
	call(keys[0].ID, "alice", "codex", "", "me@example.com", "alice-2")
	call(keys[0].ID, "bob", "alpha", provider.KeyID("alpha-two"), "", "bob-1")
	call(keys[0].ID, "alice", "codex", "", "spare@example.com", "alice-off-seat")
	call(keys[1].ID, "alice", "alpha", provider.KeyID("alpha-two"), "", "other-alice")
	call(keys[0].ID, "", "codex", "", "me@example.com", "nobody-1")
	// one of DefaultUser's on its own seat, as a request naming no user is
	// logged since T-157
	call(keys[0].ID, DefaultUser, "codex", "", "spare@example.com", "default-1")
	// a request of bob's in flight, its session named
	in := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	in.Header.Set(UserHeader, "bob")
	in.Header.Set(SessionHeader, "bob-live")
	for _, s := range []*Server{srv, sessions} {
		_, out := s.active.add(in)
		t.Cleanup(out)
	}

	secret := secrets[0]
	do := func(h http.Handler, path, user string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "192.168.1.9:5000"
		r.Header.Set("Authorization", "Bearer "+secret)
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	lanes := func(h http.Handler, user string, whole bool) []string {
		t.Helper()
		c, b := do(h, "/v1/magpie/concurrency", user)
		var out struct {
			Data     map[string]Lane
			Requests *int
			Waiting  *int
			Active   []Active
		}
		if c != 200 || json.Unmarshal([]byte(b), &out) != nil || out.Requests == nil || out.Waiting == nil {
			t.Fatalf("%q concurrency: %d %s", user, c, b)
		}
		// bob's request in flight: told the key's owner, and no user,
		// bob included, as a request counted in knows no key yet
		if live := slices.ContainsFunc(out.Active, func(a Active) bool { return a.Session == "bob-live" }); live != whole {
			t.Errorf("%q told bob's request in flight: %v", user, live)
		}
		got := []string{}
		for who := range out.Data {
			got = append(got, who)
		}
		slices.Sort(got)
		return got
	}
	summary := func(h http.Handler, user string) (usageapi.Usage, string) {
		t.Helper()
		c, b := do(h, "/v1/magpie/usage?period=all", user)
		var out usageapi.Usage
		if c != 200 || json.Unmarshal([]byte(b), &out) != nil {
			t.Fatalf("%q usage: %d %s", user, c, b)
		}
		return out, b
	}
	sessionsOf := func(u usageapi.Usage) []string {
		got := []string{}
		for _, g := range u.Sessions {
			got = append(got, g.ID)
		}
		slices.Sort(got)
		return got
	}
	all := slices.Sorted(slices.Values(every))
	allSessions := []string{"alice-1", "alice-2", "alice-off-seat", "bob-1", "default-1", "nobody-1", "other-alice"}

	want := map[string]struct{ lanes, sessions []string }{
		"alice": {[]string{one, beta, "codex@me@example.com"}, []string{"alice-1", "alice-2"}},
		"bob":   {[]string{two}, []string{"bob-1"}},
		"carol": {[]string{}, []string{}}, // bound to nothing
		// no user named: DefaultUser's, as naming it
		"":          {[]string{"codex@spare@example.com"}, []string{"default-1"}},
		DefaultUser: {[]string{"codex@spare@example.com"}, []string{"default-1"}},
	}
	for user, w := range want {
		slices.Sort(w.lanes)
		if got := lanes(bound, user, false); !slices.Equal(got, w.lanes) {
			t.Errorf("%s told lanes %v, want %v", user, got, w.lanes)
		}
		u, b := summary(bound, user)
		if got := sessionsOf(u); !slices.Equal(got, w.sessions) || u.Calls != len(w.sessions) {
			t.Errorf("%s told calls %d of sessions %v, want %v", user, u.Calls, got, w.sessions)
		}
		for _, g := range u.Users {
			if g.User != cmp.Or(user, DefaultUser) {
				t.Errorf("%s told user %q's calls", user, g.User)
			}
		}
		if user == "alice" {
			for _, other := range []string{"spare@example.com", provider.KeyID("alpha-two"), "bob", "other-alice", "nobody-1"} {
				if strings.Contains(b, other) {
					t.Errorf("alice's summary names %q: %s", other, b)
				}
			}
			if len(u.Accounts) != 1 || u.Accounts[0].Account != "me@example.com" {
				t.Errorf("alice told accounts %+v", u.Accounts)
			}
		}
	}
	// a call of alice's on her spare, its account named only in Host (a
	// record from before ProviderAccount), after her summary was read:
	// she is told it
	usage.Append(usage.Record{Time: at, Agent: "codex", Provider: "codex", Model: "m1", Status: 200, Input: 10, Output: 1,
		CallerKeyID: keys[0].ID, User: "alice", Host: "chatgpt.com as Me@Example.com", Session: "alice-3"})
	allSessions = append(allSessions, "alice-3")
	slices.Sort(allSessions)
	if u, _ := summary(bound, "alice"); !slices.Equal(sessionsOf(u), []string{"alice-1", "alice-2", "alice-3"}) {
		t.Errorf("alice after her third call told sessions %v", sessionsOf(u))
	}
	// bound by session: everything
	for _, c := range []struct {
		h    http.Handler
		user string
	}{{bySession, ""}, {bySession, "alice"}} {
		if got := lanes(c.h, c.user, true); !slices.Equal(got, all) {
			t.Errorf("%q told lanes %v, want %v", c.user, got, all)
		}
		if u, _ := summary(c.h, c.user); !slices.Equal(sessionsOf(u), allSessions) {
			t.Errorf("%q told sessions %v, want %v", c.user, sessionsOf(u), allSessions)
		}
	}
	// this machine without a key, as magpie's own console asks
	// (OwnerLanes): the owner's, every lane and call, as before (T-163)
	for _, path := range []string{"/v1/magpie/concurrency", "/v1/magpie/usage?period=all"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		bound.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("this machine, no key, %s: %d %s", path, w.Code, w.Body)
		}
		var out struct {
			Data     map[string]Lane
			Sessions []struct{ ID string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if path == "/v1/magpie/concurrency" && len(out.Data) != len(all) {
			t.Errorf("this machine, no key: told lanes %v, want %v", out.Data, all)
		}
		if path != "/v1/magpie/concurrency" && len(out.Sessions) != len(allSessions) {
			t.Errorf("this machine, no key: told sessions %v, want %v", out.Sessions, allSessions)
		}
	}
	// Other's alice is its own: Two's lane, and her one call on it
	secret = secrets[1]
	if got := lanes(bound, "alice", false); !slices.Equal(got, []string{two}) {
		t.Errorf("Other's alice told lanes %v", got)
	}
	if u, _ := summary(bound, "alice"); !slices.Equal(sessionsOf(u), []string{"other-alice"}) {
		t.Errorf("Other's alice told sessions %v", sessionsOf(u))
	}
	secret = secrets[0]
	for _, path := range []string{"/v1/magpie/concurrency", "/v1/magpie/usage?period=all"} {
		if c, b := do(bound, path, "al ice\x00"); c != http.StatusBadRequest {
			t.Errorf("%s, a user that can't be taken: %d %s", path, c, b)
		}
	}
	// user-accounts.json written over with what doesn't read: which
	// accounts alice is on is unknown, so she is told nothing of them
	if err := os.WriteFile(usersPath(), []byte(`{"keys":`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/magpie/concurrency", "/v1/magpie/usage?period=all"} {
		for _, user := range []string{"alice", ""} {
			if c, b := do(bound, path, user); c != http.StatusServiceUnavailable || strings.Contains(b, "example.com") || strings.Contains(b, "alpha#") {
				t.Errorf("unread %s, %q: %d %s", path, user, c, b)
			}
		}
	}
}
