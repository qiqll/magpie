package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// Bound by user, /v1/magpie/concurrency's "keys" (main's per-key lanes)
// tells a user naming itself the lanes of its own group's keys alone
// (groupOf): every key that shares its users when its own does, else its
// own key alone. Before, it was told every sharing key's lane whatever
// key it came by, and never its own key's when that key didn't share. A
// request naming no user is DefaultUser's (T-163): told its group's keys
// alone as any user, never every key.
func TestBoundUserToldItsOwnKeyLanes(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	now := time.Now().UTC().Truncate(time.Second)
	keys, secrets := newCaller(t, "Own", "SharedA", "SharedB")
	shareUsers(t, keys[1].ID, keys[2].ID)
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"alpha": {"alice": {Who: "alpha", Account: "One", At: now}}},
	}})
	srv := byUserUpTo(t, New(), 1)
	h := lanGuard(srv.Handler())
	for _, k := range keys {
		release, _, err := srv.keyLanes.take(t.Context(), keyLane(k.ID), 2, 0, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	told := func(secret, user string) []string {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/magpie/concurrency", nil)
		r.RemoteAddr = "192.168.1.9:5000"
		r.Header.Set("Authorization", "Bearer "+secret)
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out struct{ Keys map[string]Lane }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%q: %d %s", user, w.Code, w.Body)
		}
		got := []string{}
		for id := range out.Keys {
			got = append(got, id)
		}
		slices.Sort(got)
		return got
	}
	shared := slices.Sorted(slices.Values([]string{keys[1].ID, keys[2].ID}))
	for _, c := range []struct {
		name, secret, user string
		want               []string
	}{
		{"no user: DefaultUser's, its own key alone", secrets[0], "", []string{keys[0].ID}},
		{"no user, a key that shares: the sharing keys", secrets[1], "", shared},
		{"a key that doesn't share: its own alone", secrets[0], "alice", []string{keys[0].ID}},
		{"a key that shares: the sharing keys", secrets[1], "alice", shared},
	} {
		if got := told(c.secret, c.user); !slices.Equal(got, c.want) {
			t.Errorf("%s: told %v, want %v", c.name, got, c.want)
		}
	}
}
