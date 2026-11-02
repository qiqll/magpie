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

// usagelessGroup is cachedGroup with a vendor that reports no usage at
// all, so every turn of a conversation is routed afresh as no-cache.
func usagelessGroup(t *testing.T) (asked func() string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
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
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"ca/m", "cb/m"}, Routing: "order"}); err != nil {
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

// usagelessSetup is a gateway bound by user, alice on kc1 and kc2, its
// vendor reporting no usage, and a send of one turn of session to model,
// which fails on a route with no affinity or one that isn't "first" at the
// session's first turn and "no-cache" at every later one.
func usagelessSetup(t *testing.T, model string) func(session string) string {
	t.Helper()
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	asked := usagelessGroup(t)
	aliceOnTwo(t)
	s := byUserUpTo(t, New(), 3)
	seen := map[string]bool{}
	return func(session string) string {
		t.Helper()
		rec := postAt(s, "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi `+session+`"}]}`,
			map[string]string{UserHeader: "alice", "session_id": session})
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", session, rec.Code, rec.Body.String())
		}
		want := "first"
		if seen[session] {
			want = "no-cache"
		}
		seen[session] = true
		if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != want {
			t.Fatalf("%s routed as %+v, want %s", session, r.Affinity, want)
		}
		return asked()
	}
}

// A conversation whose vendor reports no usage is routed afresh at every
// turn (no-cache), and on a tie with the fewest held it stays on the
// user's account it was answered on (T-049, the owner's choice A of
// 2026-11-02), on a provider and on a group's member alike: s2, on kc2,
// with s1 on kc1 past heldFor, holds none on either. Before, each such
// turn went to kc1, first on the tie.
func TestUserUsagelessSessionStaysOnATie(t *testing.T) {
	for _, model := range []string{"ca/m", "group/pair"} {
		t.Run(model, func(t *testing.T) {
			send := usagelessSetup(t, model)
			if got := send("s1") + "," + send("s2"); got != "kc1,kc2" {
				t.Fatalf("new sessions asked %s", got)
			}
			ageStick(t, "s1", heldFor+time.Minute)
			for turn := 2; turn <= 4; turn++ {
				if got := send("s2"); got != "kc2" {
					t.Fatalf("turn %d asked %s, want kc2", turn, got)
				}
			}
		})
	}
}

// Where it was holds more than another of the user's own, a conversation
// routed afresh still moves (T-049 keeps it on a tie only): s2 on kc2 with
// s4, kc1's s1 and s3 past heldFor, goes to kc1, holding none.
func TestUserUsagelessSessionMovesOffTheBusier(t *testing.T) {
	for _, model := range []string{"ca/m", "group/pair"} {
		t.Run(model, func(t *testing.T) {
			send := usagelessSetup(t, model)
			if got := send("s1") + "," + send("s2") + "," + send("s3") + "," + send("s4"); got != "kc1,kc2,kc1,kc2" {
				t.Fatalf("new sessions asked %s", got)
			}
			ageStick(t, "s1", heldFor+time.Minute)
			ageStick(t, "s3", heldFor+time.Minute)
			if got := send("s2"); got != "kc1" {
				t.Fatalf("s2 asked %s, want kc1", got)
			}
		})
	}
}

// On a tie, the account a conversation was answered on is kept only while
// leastHeld would pick it at all: all but used up, resting or aside, it is
// passed over as before, and unbound (no spentLast) a tie, y on a and z
// on b, keeps routing's order (T-049).
func TestLeastHeldKeepsWhereItWasOnlyWhenReady(t *testing.T) {
	c := func(key string) candidate {
		return candidate{p: provider.Provider{ID: "t049", Key: key}, model: "m", rest: "t049"}
	}
	a, b := c("ka"), c("kb")
	sticks.Lock()
	now := time.Now()
	sticks.m["t049|x"] = stick{rest: b.rest, who: b.who(), model: "m", at: now}
	sticks.m["t049|y"] = stick{rest: a.rest, who: a.who(), model: "m", at: now}
	sticks.m["t049|z"] = stick{rest: b.rest, who: b.who(), model: "m", at: now}
	sticks.Unlock()
	t.Cleanup(func() {
		sticks.Lock()
		for _, k := range []string{"t049|x", "t049|y", "t049|z"} {
			delete(sticks.m, k)
			delete(sticks.opening, k)
		}
		sticks.Unlock()
	})
	for _, tc := range []struct {
		name      string
		second    Weighed
		spentLast bool
		want      candidate
	}{
		{"ready", Weighed{}, true, b},
		{"spent", Weighed{Known: true, Used: provider.SpentShare}, true, a},
		{"past lowShare", Weighed{Known: true, Used: lowShare}, true, a},
		{"resting", Weighed{Rest: &Rest{}}, true, a},
		{"aside", Weighed{Aside: true}, true, a},
		{"unbound", Weighed{}, false, a},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := leastHeld("t049", "t049|x", []candidate{a, b}, planned{order: []Weighed{{}, tc.second}}, tc.spentLast, false)
			if got[0].who() != tc.want.who() {
				t.Fatalf("went to %s, want %s", got[0].who(), tc.want.who())
			}
		})
	}
}
