package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cachedGroup saves providers ca (keys kc1, kc2, kc3) and cb (kd1) of one
// vendor whose every reply was sent a prompt worth keeping (5000 tokens,
// none said cached), each serving m, and the group pair of ca/m then
// cb/m, routed as routing says, kept as affinity says. asked is the keys asked since it was last
// read, the oldest first.
func cachedGroup(t *testing.T, routing, affinity string) (asked func() string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5000,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	for _, p := range []provider.Provider{
		{ID: "ca", Name: "CA", Chat: up.URL + "/v1", Key: "kc1", Keys: []provider.KeyAccount{{Key: "kc2"}, {Key: "kc3"}}, Models: []string{"m"}},
		{ID: "cb", Name: "CB", Chat: up.URL + "/v1", Key: "kd1", Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"ca/m", "cb/m"}, Routing: routing, Affinity: affinity}); err != nil {
		t.Fatal(err)
	}
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		out := strings.Join(got, ",")
		got = nil
		return out
	}
}

// groupSessionsAsked sends each session's request to the group pair for
// user in turn, and returns the keys asked, failing on anything but a 200.
func groupSessionsAsked(t *testing.T, s *Server, asked func() string, user string, sessions ...string) string {
	t.Helper()
	for _, session := range sessions {
		rec := postAt(s, "/v1/chat/completions", `{"model":"group/pair","messages":[{"role":"user","content":"hi `+session+`"}]}`,
			map[string]string{UserHeader: user, "session_id": session})
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", session, rec.Code, rec.Body.String())
		}
	}
	return asked()
}

// aliceOnTwo binds alice to kc1 and kc2 of ca, bob to kc3.
func aliceOnTwo(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"ca": {
		"alice": {Who: keyWho("ca", "kc1"), Account: "kc1", At: now, More: []userSeat{{Who: keyWho("ca", "kc2"), Account: "kc2", At: now}}},
		"bob":   {Who: keyWho("ca", "kc3"), Account: "kc3", At: now},
	}}})
}

// Bound by user, a routing group's new sessions spread over its user's
// own accounts of the member's provider as a provider's do (T-041, T-013's
// choice A): each goes to the one of them the fewest conversations of the
// group are held on, the first on a tie, and each session's later turns
// stay where it went. Another user's key, holding none, is never asked,
// the user is bound to nothing more, and the next member is untouched.
// Before, every new session of the group went to the user's first, kc1.
// The member's provider routed its own way (ownRouting) is spread too.
func TestUserGroupSessionsSpreadOverItsAccounts(t *testing.T) {
	for _, c := range []struct{ name, group, own string }{{"order", "order", ""}, {"smart", "", ""}, {"own-routing", "", "order"}} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			noRests()
			t.Cleanup(noRests)
			asked := cachedGroup(t, c.group, "")
			if c.own != "" {
				ca, _ := provider.Find("ca")
				ca.Routing = c.own
				if err := provider.Save(*ca); err != nil {
					t.Fatal(err)
				}
			}
			aliceOnTwo(t)
			s := byUserUpTo(t, New(), 3)
			if got := groupSessionsAsked(t, s, asked, "alice", "s1", "s2", "s3"); got != "kc1,kc2,kc1" {
				t.Fatalf("new sessions asked %s", got)
			}
			if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "first" {
				t.Fatalf("route %+v", r.Affinity)
			}
			if got := groupSessionsAsked(t, s, asked, "alice", "s2", "s1", "s2", "s3"); got != "kc2,kc1,kc2,kc1" {
				t.Fatalf("later turns asked %s", got)
			}
			if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "cache" || !r.Affinity.Kept {
				t.Fatalf("a later turn not kept: %+v", r.Affinity)
			}
			if b := boundIn(t, "cb"); len(b) != 0 {
				t.Fatalf("bound on cb %v", b)
			}
			if got := seatsIn(t, "ca"); len(got["alice"]) != 2 || len(got["bob"]) != 1 {
				t.Fatalf("bound afresh: %v", got)
			}
		})
	}
}

// A group's sessions of a user with one account of the member's provider
// all go there, as before; a spread session's turn whose own fails goes on
// to its other own and the next member as before.
func TestUserGroupOneAccountKeepsEverySession(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	asked := cachedGroup(t, "order", "")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"ca": {
		"alice": {Who: keyWho("ca", "kc1"), Account: "kc1", At: time.Now().UTC()},
	}}})
	s := byUserUpTo(t, New(), 1)
	if got := groupSessionsAsked(t, s, asked, "alice", "s1", "s2", "s3"); got != "kc1,kc1,kc1" {
		t.Fatalf("one account's sessions asked %s", got)
	}
}

// Kept within a turn only, or never, a group's sessions aren't spread, as
// a provider's aren't: each next turn routes afresh, to the first.
func TestUserGroupSessionsWithinATurnStayOnTheFirst(t *testing.T) {
	for _, mode := range []string{provider.AffinityTurn, provider.AffinityOff} {
		t.Run(mode, func(t *testing.T) {
			fresh(t)
			noRests()
			t.Cleanup(noRests)
			asked := cachedGroup(t, "order", mode)
			aliceOnTwo(t)
			s := byUserUpTo(t, New(), 3)
			if got := groupSessionsAsked(t, s, asked, "alice", "s1", "s2", "s1", "s2"); got != "kc1,kc1,kc1,kc1" {
				t.Fatalf("sessions asked %s", got)
			}
		})
	}
}

// A group's turn pinned to its user's own goes there alone, however many
// conversations it holds (T-036): the pin isn't spread.
func TestUserGroupPinnedSessionNotSpread(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	asked := cachedGroup(t, "order", "")
	aliceOnTwo(t)
	s := byUserUpTo(t, New(), 3)
	if got := groupSessionsAsked(t, s, asked, "alice", "s1"); got != "kc1" {
		t.Fatalf("first session asked %s", got)
	}
	rec := postAt(s, "/v1/chat/completions", `{"model":"group/pair","messages":[{"role":"user","content":"hi s2"}]}`,
		map[string]string{UserHeader: "alice", "session_id": "s2", AccountHeader: keyWho("ca", "kc1")})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := asked(); got != "kc1" {
		t.Fatalf("pinned session asked %s, want kc1", got)
	}
}
