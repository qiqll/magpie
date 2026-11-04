package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// T-099: bound by user, GET /v1/magpie/route?session= told a request
// naming its user (X-Magpie-User) any session's latest route: the member
// each try went to and why it failed, which names the account. A session
// id is not a secret a user can be trusted to lack. It is told the routes
// of its own requests alone (its name under a key of its group, T-089),
// and another user's session is the 404 a session with no route is, so
// the answer says nothing of whether it exists.
func TestBoundUserToldItsOwnSessionRoute(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	now := time.Now().UTC().Truncate(time.Second)
	keys, secrets := newCaller(t, "Full", "Other")
	one, two := "alpha#"+provider.KeyID("alpha-one"), "alpha#"+provider.KeyID("alpha-two")
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"alpha": {"alice": {Who: one, Account: "One", At: now}, "bob": {Who: two, Account: "Two", At: now}}},
		// another key's alice, apart from Full's: its users aren't shared
		keys[1].ID: {"alpha": {"alice": {Who: two, Account: "Two", At: now}}},
	}})
	// each route as a request of key's user begins it
	seed := func(s *Server) {
		begin := func(key, user, session, fail string) {
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			if user != "" {
				r.Header.Set(UserHeader, user)
			}
			r = r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: key}))
			rt := s.trace.beginFor(r, Route{Time: now, Session: session, Model: "m", Provider: "alpha"})
			s.trace.update(rt, func(rt *Route) {
				rt.Tries = append(rt.Tries, Try{Model: "m", Status: 429, Fail: fail})
			})
		}
		begin(keys[0].ID, "alice", "alice-s", "alice's try")
		begin(keys[0].ID, "bob", "bob-s", "bob's try on two@example.com")
		begin(keys[1].ID, "alice", "other-alice-s", "Other's alice's try")
		begin(keys[0].ID, "", "nobody-s", "a try of no user's")
		// magpie's own asks for alice: a classifier's (decideFor) and one
		// made for her (forUser), with no header and no key of their own,
		// the key the turn came with beside them (askedKeyKey)
		for _, c := range []struct {
			session string
			ctx     func(context.Context) context.Context
		}{
			{"alice-decide-s", func(ctx context.Context) context.Context { return context.WithValue(ctx, decideForKey{}, "alice") }},
			{"alice-asked-s", func(ctx context.Context) context.Context {
				return context.WithValue(ctx, askedForKey{}, askedFor{key: keys[0].ID, user: "alice"})
			}},
		} {
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			r = r.WithContext(context.WithValue(c.ctx(r.Context()), askedKeyKey{}, askedKey{id: keys[0].ID}))
			rt := s.trace.beginFor(r, Route{Time: now, Session: c.session, Model: "m", Provider: "alpha"})
			s.trace.update(rt, func(rt *Route) {
				rt.Tries = append(rt.Tries, Try{Model: "m", Status: 429, Fail: "magpie's ask for alice"})
			})
		}
		// one id both send: each is told its own latest
		begin(keys[0].ID, "alice", "shared-s", "alice's turn")
		begin(keys[0].ID, "bob", "shared-s", "bob's later turn")
	}
	bound := byUserUpTo(t, New(), 1)
	seed(bound)
	bySession := New()
	seed(bySession)
	secret := secrets[0]
	do := func(s *Server, target, user string, hdr ...string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", target, nil)
		r.RemoteAddr = "192.168.1.9:5000"
		r.Header.Set("Authorization", "Bearer "+secret)
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		lanGuard(s.Handler()).ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	fail := func(b string) string {
		t.Helper()
		var out struct {
			Route *struct{ Tries []struct{ Fail string } }
		}
		if err := json.Unmarshal([]byte(b), &out); err != nil {
			t.Fatal(err, b)
		}
		if out.Route == nil || len(out.Route.Tries) == 0 {
			return ""
		}
		return out.Route.Tries[0].Fail
	}
	// her own, as before
	if c, b := do(bound, "/v1/magpie/route?session=alice-s", "alice"); c != 200 || fail(b) != "alice's try" {
		t.Errorf("alice's own session: %d %s", c, b)
	}
	if c, b := do(bound, "/v1/magpie/route?session=bob-s", "bob"); c != 200 || fail(b) != "bob's try on two@example.com" {
		t.Errorf("bob's own session: %d %s", c, b)
	}
	if c, b := do(bound, "/v1/magpie/route?session=shared-s", "alice"); c != 200 || fail(b) != "alice's turn" {
		t.Errorf("alice's turn in a session bob sends too: %d %s", c, b)
	}
	if c, b := do(bound, "/v1/magpie/route", "alice", SessionHeader, "alice-s"); c != 200 || fail(b) != "alice's try" {
		t.Errorf("alice's own session by header: %d %s", c, b)
	}
	for _, session := range []string{"alice-decide-s", "alice-asked-s"} {
		if c, b := do(bound, "/v1/magpie/route?session="+session, "alice"); c != 200 || fail(b) != "magpie's ask for alice" {
			t.Errorf("magpie's own ask for alice, %s: %d %s", session, c, b)
		}
		if c, b := do(bound, "/v1/magpie/route?session="+session, "bob"); c != http.StatusNotFound {
			t.Errorf("bob asking %s: %d %s", session, c, b)
		}
	}
	// a session of no route at all, and every one that isn't hers: one
	// answer, word for word
	_, none := do(bound, "/v1/magpie/route?session=nope", "alice")
	if c, _ := do(bound, "/v1/magpie/route?session=nope", "alice"); c != http.StatusNotFound || strings.Contains(none, "nope") {
		t.Errorf("a session with no route: %d %s", c, none)
	}
	for _, session := range []string{"bob-s", "other-alice-s", "nobody-s"} {
		for _, target := range []string{"/v1/magpie/route?session=" + session, "/v1/magpie/route?session=" + session + "&after=0&wait=0.05"} {
			if c, b := do(bound, target, "alice"); c != http.StatusNotFound || b != strings.ReplaceAll(none, "nope", session) || strings.Contains(b, "try") {
				t.Errorf("alice asking %s: %d %s, want %s", target, c, b, none)
			}
		}
		if c, b := do(bound, "/v1/magpie/route", "alice", SessionHeader, session); c != http.StatusNotFound || b != none {
			t.Errorf("alice asking %s by header: %d %s", session, c, b)
		}
	}
	// Other's alice: her own, not Full's alice's
	secret = secrets[1]
	if c, b := do(bound, "/v1/magpie/route?session=other-alice-s", "alice"); c != 200 || fail(b) != "Other's alice's try" {
		t.Errorf("Other's alice's own session: %d %s", c, b)
	}
	if c, b := do(bound, "/v1/magpie/route?session=alice-s", "alice"); c != http.StatusNotFound {
		t.Errorf("Other's alice asking Full's alice's session: %d %s", c, b)
	}
	secret = secrets[0]
	// no user header, and bound by session: every session, as before
	for _, s := range []*Server{bound, bySession} {
		user := ""
		if s == bySession {
			user = "alice"
		}
		if c, b := do(s, "/v1/magpie/route?session=bob-s", user); c != 200 || fail(b) != "bob's try on two@example.com" {
			t.Errorf("bob's session asked with user %q (bound by user %v): %d %s", user, s == bound, c, b)
		}
		if c, b := do(s, "/v1/magpie/route?session=nope", user); c != 200 || !strings.Contains(b, `"route":null`) {
			t.Errorf("a session with no route, user %q (bound by user %v): %d %s", user, s == bound, c, b)
		}
	}
	if c, b := do(bound, "/v1/magpie/route?session=bob-s", "al ice\x00"); c != http.StatusBadRequest || strings.Contains(b, "try") {
		t.Errorf("a user that can't be taken: %d %s", c, b)
	}
	// user-accounts.json written over with what doesn't read: whose
	// sessions are alice's is still known, but not her seats: 503, as
	// every other read of hers
	if err := os.WriteFile(usersPath(), []byte(`{"keys":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, b := do(bound, "/v1/magpie/route?session=bob-s", "alice"); c != http.StatusServiceUnavailable || strings.Contains(b, "try") {
		t.Errorf("unread users: %d %s", c, b)
	}
}
