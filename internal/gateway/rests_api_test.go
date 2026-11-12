package gateway

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// What rests in the process serving the gateway is told to a console on
// this computer that doesn't serve it (the Gateway page's User bindings):
// each rest by what it rests by, with why and until when, one ended not
// among them; a magpie that doesn't tell them is unknown, never nothing
// resting.
func TestOwnerRestsTold(t *testing.T) {
	fresh(t)
	until := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	restingUntil.Lock()
	restingUntil.m["relay#abc"], restingUntil.note["relay#abc"] = until, Rest{Why: failQuota, Status: 429}
	restingUntil.m["codex@a@x.com/gpt-5"], restingUntil.note["codex@a@x.com/gpt-5"] = until, Rest{Why: failRate, Model: "gpt-5"}
	restingUntil.m["ended"], restingUntil.note["ended"] = time.Now().Add(-time.Minute), Rest{Why: failQuota}
	restingUntil.Unlock()

	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))
	got, ok := OwnerRests()
	if !ok {
		t.Fatal("the serving magpie's rests weren't read")
	}
	if r := got["relay#abc"]; r.Why != failQuota || !r.Until.Equal(until) || r.Status != 429 {
		t.Errorf("relay#abc: %+v", r)
	}
	if r := got["codex@a@x.com/gpt-5"]; r.Why != failRate || r.Model != "gpt-5" || !r.Until.Equal(until) {
		t.Errorf("a model's rest: %+v", r)
	}
	if _, ok := got["ended"]; ok || len(got) != 2 {
		t.Errorf("an ended rest told, or more: %+v", got)
	}

	// another machine is told nothing, even where Handler is served
	// without lanGuard
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", RestsPath, nil)
	r.RemoteAddr = "192.168.1.9:5000"
	New().Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "relay#abc") {
		t.Errorf("another machine: %d %s", w.Code, w.Body)
	}

	// an older magpie, or one that refuses: unknown
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer old.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(old.URL, "http://"))
	if got, ok := OwnerRests(); ok || got != nil {
		t.Errorf("a magpie that doesn't tell its rests: %v %v", got, ok)
	}
}

// T-166: bound by user, /v1/magpie/rests told any full key every
// account's rest, provider@email and why. A request naming its user is
// told the rests of the accounts and keys it is on alone, one with a
// gateway key naming none DefaultUser's (T-163); this computer without a
// key (the Gateway page's OwnerRests) and bound by session, every rest.
func TestBoundUserToldItsOwnRests(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	now := time.Now().UTC().Truncate(time.Second)
	keys, secrets := newCaller(t, "Full")
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {
			"codex": {"alice": {Who: "codex@a@x.com", Account: "a@x.com", At: now}},
			"relay": {DefaultUser: {Who: "relay#abc", Account: "abc", At: now}},
		},
	}})
	until := time.Now().Add(20 * time.Minute)
	restingUntil.Lock()
	for _, id := range []string{"relay#abc", "codex@a@x.com/gpt-5", "codex@b@x.com", "codex"} {
		restingUntil.m[id], restingUntil.note[id] = until, Rest{Why: failQuota}
	}
	restingUntil.Unlock()
	every := "codex codex@a@x.com/gpt-5 codex@b@x.com relay#abc"

	do := func(s *Server, user string, keyed bool) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", RestsPath, nil)
		r.RemoteAddr = "127.0.0.1:5000"
		if keyed {
			r.RemoteAddr = "192.168.1.9:5000"
			r.Header.Set("Authorization", "Bearer "+secrets[0])
		}
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		w := httptest.NewRecorder()
		lanGuard(s.Handler()).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			return w.Code, w.Body.String()
		}
		var out struct{ Data map[string]Rest }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err, w.Body)
		}
		return w.Code, strings.Join(slices.Sorted(maps.Keys(out.Data)), " ")
	}
	bound := byUserUpTo(t, New(), 1)
	for _, c := range []struct {
		user  string
		keyed bool
		want  string
	}{
		{"alice", true, "codex@a@x.com/gpt-5"},
		{"", true, "relay#abc"},          // no user named: DefaultUser's
		{DefaultUser, true, "relay#abc"}, // as naming it
		{"bob", true, ""},                // on no account
		{"", false, every},               // this computer's owner
	} {
		if code, got := do(bound, c.user, c.keyed); code != 200 || got != c.want {
			t.Errorf("user %q (keyed %v): %d %q, want %q", c.user, c.keyed, code, got, c.want)
		}
	}
	if code, got := do(New(), "", true); code != 200 || got != every {
		t.Errorf("bound by session: %d %q, want %q", code, got, every)
	}
	if code, b := do(bound, "al ice\x00", true); code != http.StatusBadRequest || strings.Contains(b, "relay#abc") {
		t.Errorf("a user that can't be taken: %d %s", code, b)
	}
	if err := os.WriteFile(usersPath(), []byte(`{"keys":`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"alice", ""} {
		if code, b := do(bound, user, true); code != http.StatusServiceUnavailable || strings.Contains(b, "relay#abc") {
			t.Errorf("unread users, user %q: %d %s", user, code, b)
		}
	}
}
