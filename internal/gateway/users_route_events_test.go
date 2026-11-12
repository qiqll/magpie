package gateway

import (
	"bufio"
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

// T-105: bound by user, the route-events feed and its snapshot told a
// request naming its user (X-Magpie-User) every user's requests as they
// were routed: their agent, provider, model, status, tries and times. It
// is told the requests of its own alone, as GET /v1/magpie/route is
// (Route.caller, its name under a key of its group, T-099), its active
// count of them too; a user header that can't be taken is 400, a users
// file that doesn't read 503, before and once its stream is going.
func TestBoundUserToldItsOwnRouteEvents(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	was := routeEventsRecheck
	routeEventsRecheck = 0
	t.Cleanup(func() { routeEventsRecheck = was })
	now := time.Now().UTC().Truncate(time.Second)
	keys, secrets := newCaller(t, "Full", "Other")
	one, two := "alpha#"+provider.KeyID("alpha-one"), "alpha#"+provider.KeyID("alpha-two")
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"alpha": {"alice": {Who: one, Account: "One", At: now}, "bob": {Who: two, Account: "Two", At: now}}},
		keys[1].ID: {"alpha": {"alice": {Who: two, Account: "Two", At: now}}},
	}})
	// a route as a request of key's user begins it, its agent saying whose
	begin := func(s *Server, key, user, agent string) *Route {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		r = r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: key}))
		rt := s.trace.beginFor(r, Route{Time: now, Agent: agent, Model: "m", Provider: "alpha"})
		s.trace.update(rt, func(rt *Route) { rt.Tries = append(rt.Tries, Try{Model: "m", Status: 429}) })
		return rt
	}
	seed := func(s *Server) {
		begin(s, keys[0].ID, "alice", "alice-a")
		begin(s, keys[0].ID, "bob", "bob-a")
		begin(s, keys[1].ID, "alice", "other-alice-a")
		begin(s, keys[0].ID, "", "nobody-a")
		begin(s, keys[0].ID, DefaultUser, "default-a") // as userDefaulted names it
		// a classifier's ask for alice, no header, the turn's key beside it
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), decideForKey{}, "alice"), askedKeyKey{}, askedKey{id: keys[0].ID}))
		s.trace.beginFor(r, Route{Time: now, Agent: "alice-decide-a", Model: "m"})
	}
	bound := byUserUpTo(t, New(), 1)
	srv := httptest.NewServer(lanGuard(bound.Handler()))
	t.Cleanup(srv.Close) // after each stream's body is closed
	bySession := New()
	sessionSrv := httptest.NewServer(lanGuard(bySession.Handler()))
	t.Cleanup(sessionSrv.Close)
	get := func(base, path, secret, user string, hdr ...string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		if user != "" {
			req.Header.Set(UserHeader, user)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	snap := func(base, secret, user string) (routeSnap, []string) {
		t.Helper()
		res := get(base, routeSnapshotPath, secret, user)
		var got routeSnap
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil || res.StatusCode != 200 || got.Active != len(got.Routes) {
			t.Fatalf("snapshot for %q: %d %+v %v", user, res.StatusCode, got, err)
		}
		var agents []string
		for _, a := range got.Routes {
			agents = append(agents, a.Agent)
		}
		return got, agents
	}
	cursor, _ := snap(srv.URL, secrets[0], "")
	seed(bound)
	seed(bySession)
	for _, c := range []struct {
		base, secret, user string
		want               string
	}{
		{srv.URL, secrets[0], "alice", "alice-a alice-decide-a"},
		{srv.URL, secrets[0], "bob", "bob-a"},
		{srv.URL, secrets[1], "alice", "other-alice-a"},
		// no user header with a gateway key: DefaultUser's alone (T-167)
		{srv.URL, secrets[0], "", "default-a"},
		{srv.URL, secrets[0], DefaultUser, "default-a"},
		// bound by session: everyone's, as before
		{sessionSrv.URL, secrets[0], "alice", "alice-a bob-a other-alice-a nobody-a default-a alice-decide-a"},
	} {
		if _, agents := snap(c.base, c.secret, c.user); strings.Join(agents, " ") != c.want {
			t.Errorf("snapshot for %q (bound by user %v): %v, want %s", c.user, c.base == srv.URL, agents, c.want)
		}
	}
	// replayed from before the seed: hers alone, the active count hers
	stream := get(srv.URL, routeEventsPath, secrets[0], "alice", "Last-Event-ID", cursor.Cursor)
	if stream.StatusCode != 200 {
		t.Fatal(stream.Status)
	}
	scan := bufio.NewScanner(stream.Body)
	mine := map[string]bool{"alice-a": true, "alice-decide-a": true}
	for n := 0; n < 3; n++ { // alice-a: started, its try; alice-decide-a: started
		ev := mustActivity(t, scan)
		if !mine[ev.Route.Agent] {
			t.Fatalf("alice told %s's change: %+v", ev.Route.Agent, ev)
		}
		if want := map[int]int{0: 1, 1: 1, 2: 2}[n]; ev.Active != want {
			t.Errorf("alice's change %d: active %d, want %d (hers going)", n, ev.Active, want)
		}
	}
	// live: bob's change, then hers; she is told hers first
	bobLive := begin(bound, keys[0].ID, "bob", "bob-live")
	begin(bound, keys[0].ID, "alice", "alice-live")
	if ev := mustActivity(t, scan); ev.Route.Agent != "alice-live" || ev.Kind != "request_started" || ev.Active != 3 {
		t.Fatalf("alice's next change: %+v", ev)
	}
	if ev := mustActivity(t, scan); ev.Route.Agent != "alice-live" || ev.Kind != "attempt_started" {
		t.Fatalf("alice's try: %+v", ev)
	}
	bound.trace.update(bobLive, func(r *Route) { r.Done, r.Status = true, 200 })
	// a users file that no longer reads: her stream ends at its next change
	if err := os.WriteFile(usersPath(), []byte(`{"keys":`), 0o600); err != nil {
		t.Fatal(err)
	}
	begin(bound, keys[0].ID, "alice", "alice-after")
	if ev, ok := nextActivity(t, scan); ok {
		t.Fatalf("told a change once the users file didn't read: %+v", ev)
	}
	if err := scan.Err(); err != nil {
		t.Fatal("the stream didn't end:", err)
	}
	for _, path := range []string{routeSnapshotPath, routeEventsPath} {
		if res := get(srv.URL, path, secrets[0], "alice", "Last-Event-ID", cursor.Cursor); res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s, unread users: %s", path, res.Status)
		}
		if res := get(srv.URL, path, secrets[0], strings.Repeat("a", userMax+1), "Last-Event-ID", cursor.Cursor); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s, a user that can't be taken: %s", path, res.Status)
		}
	}
}

// Bound by user, a stream reads its user's group again as it goes: Other's
// alice is told Full's alice's requests once Other shares its users, not
// before. And a quiet alice whose last change is far behind others' many
// is carried past them: she reconnects with the id she last read, not
// sent back to the snapshot.
func TestBoundUserRouteEventsFollowItsGroup(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	was := routeEventsRecheck
	routeEventsRecheck = 0
	t.Cleanup(func() { routeEventsRecheck = was })
	now := time.Now().UTC().Truncate(time.Second)
	keys, secrets := newCaller(t, "Full", "Other")
	one := "alpha#" + provider.KeyID("alpha-one")
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"alpha": {"alice": {Who: one, Account: "One", At: now}, "bob": {Who: one, Account: "One", At: now}}},
		keys[1].ID: {"alpha": {"alice": {Who: one, Account: "One", At: now}}},
	}})
	s := byUserUpTo(t, New(), 1)
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close) // after each stream's body is closed
	begin := func(key, user, agent string) *Route {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set(UserHeader, user)
		r = r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: key}))
		return s.trace.beginFor(r, Route{Time: now, Agent: agent, Model: "m"})
	}
	stream := func(secret, user, cursor string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+routeEventsPath, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set(UserHeader, user)
		req.Header.Set("Last-Event-ID", cursor)
		res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	cursor := activitySnapshot(t, srv.URL, secrets[1]).Cursor
	other := bufio.NewScanner(stream(secrets[1], "alice", cursor).Body)
	begin(keys[0].ID, "alice", "full-alice-before")
	begin(keys[1].ID, "alice", "other-alice")
	if ev := mustActivity(t, other); ev.Route.Agent != "other-alice" || ev.Active != 1 {
		t.Fatalf("Other's alice told before Other shared its users: %+v", ev)
	}
	sharing(t, keys[0].ID, keys[1].ID)
	begin(keys[0].ID, "alice", "full-alice-after")
	if ev := mustActivity(t, other); ev.Route.Agent != "full-alice-after" || ev.Active != 3 {
		t.Fatalf("Other's alice not told Full's alice's once Other shares its users: %+v", ev)
	}
	// a quiet alice: her last change, then bob's more than the ring keeps
	res := stream(secrets[0], "alice", activitySnapshot(t, srv.URL, secrets[0]).Cursor)
	scan := bufio.NewScanner(res.Body)
	begin(keys[0].ID, "alice", "alice-last")
	if ev := mustActivity(t, scan); ev.Route.Agent != "alice-last" {
		t.Fatal(ev)
	}
	for i := 0; i < routeEventsKeep+8; i++ {
		begin(keys[0].ID, "bob", "bob-busy")
	}
	begin(keys[0].ID, "bob", "bob-busy") // wakes it past the last bob's once more
	// the id she was last sent: the last she read
	id := ""
	for id != activityCursorNow(s) {
		if !scan.Scan() {
			t.Fatal("the stream ended:", scan.Err())
		}
		if v, ok := strings.CutPrefix(scan.Text(), "id: "); ok {
			id = v
		}
		if strings.Contains(scan.Text(), "bob-busy") {
			t.Fatal("alice told bob's:", scan.Text())
		}
	}
	res.Body.Close()
	if res := stream(secrets[0], "alice", id); res.StatusCode != 200 {
		t.Fatalf("a quiet alice reconnecting with her last id: %s", res.Status)
	}
}

// activityCursorNow is the feed's cursor as of its last change.
func activityCursorNow(s *Server) string {
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	return s.trace.activity.cursor()
}
