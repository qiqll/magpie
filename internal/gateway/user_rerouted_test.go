package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// ageStick has the conversation session's stick, the one of the test,
// answered by ago before it was.
func ageStick(t *testing.T, session string, ago time.Duration) {
	t.Helper()
	sticks.Lock()
	defer sticks.Unlock()
	n := 0
	for k, st := range sticks.m {
		if strings.HasSuffix(k, "|"+session) {
			st.at = st.at.Add(-ago)
			sticks.m[k] = st
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d sticks of %s", n, session)
	}
}

// A user's conversation past its cache's window (cold) is routed afresh
// as a new one is (T-042, the owner's choice A of 2026-11-02): to the one
// of the user's own the fewest are held on, not back on its first. s2
// went cold on acct-2, which holds nothing else, while acct-1 holds two.
// Before, it went to acct-1 for being first.
func TestUserColdSessionSpreadNotBackOnTheFirst(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
		"bob":   {Who: "codex@third@example.com", Account: "third@example.com"},
	}}})
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1", "s2", "s3"); got != "acct-1,acct-2,acct-1" {
		t.Fatalf("new sessions asked %s", got)
	}
	ageStick(t, "s2", chatGPTCold+time.Minute)
	if got := sessionsAsked(t, s, asked, "s2"); got != "acct-2" {
		t.Fatalf("cold session asked %s, want acct-2", got)
	}
	if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "cold" {
		t.Fatalf("route %+v", r.Affinity)
	}
	if got := sessionsAsked(t, s, asked, "s2"); got != "acct-2" {
		t.Fatalf("its next turn asked %s", got)
	}
	if got := seatsIn(t, "codex"); len(got["alice"]) != 2 || len(got["bob"]) != 1 {
		t.Fatalf("bound afresh: %v", got)
	}
}

// Pinned to its own, a cold conversation goes there alone, as a new one
// pinned does: the pin isn't spread.
func TestUserColdPinnedSessionNotSpread(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
	}}})
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1", "s2", "s3"); got != "acct-1,acct-2,acct-1" {
		t.Fatalf("new sessions asked %s", got)
	}
	ageStick(t, "s2", chatGPTCold+time.Minute)
	rec := postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi s2"}`,
		map[string]string{UserHeader: "alice", "session_id": "s2", AccountHeader: "me@example.com"})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(asked(true), ","); got != "acct-1" {
		t.Fatalf("pinned cold session asked %s, want acct-1", got)
	}
}

// A group's conversation gone cold is spread over its user's own as a new
// one is (T-042): its own stick isn't counted where it was, so with one
// other on each of kc1 and kc2 s2 goes back to kc2, its cache there, not
// to kc1 on a tie with itself. With s2 past heldFor, s1 cold moves off
// kc1, which s3 holds, to kc2, which holds none; pinned to kc1, it goes
// there.
func TestUserGroupColdSessionSpread(t *testing.T) {
	setup := func(t *testing.T) (*Server, func() string) {
		fresh(t)
		noRests()
		t.Cleanup(noRests)
		asked := cachedGroup(t, "order", "")
		aliceOnTwo(t)
		return byUserUpTo(t, New(), 3), asked
	}
	t.Run("back where it was", func(t *testing.T) {
		s, asked := setup(t)
		if got := groupSessionsAsked(t, s, asked, "alice", "s1", "s2"); got != "kc1,kc2" {
			t.Fatalf("new sessions asked %s", got)
		}
		ageStick(t, "s2", cacheCold+time.Minute)
		if got := groupSessionsAsked(t, s, asked, "alice", "s2"); got != "kc2" {
			t.Fatalf("cold session asked %s, want kc2", got)
		}
		if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "cold" {
			t.Fatalf("route %+v", r.Affinity)
		}
	})
	for _, pin := range []string{"", "kc1"} {
		name := "moved"
		if pin != "" {
			name = "pinned"
		}
		t.Run(name, func(t *testing.T) {
			s, asked := setup(t)
			if got := groupSessionsAsked(t, s, asked, "alice", "s1", "s2", "s3"); got != "kc1,kc2,kc1" {
				t.Fatalf("new sessions asked %s", got)
			}
			ageStick(t, "s1", cacheCold+time.Minute)
			ageStick(t, "s2", heldFor+time.Minute)
			h := map[string]string{UserHeader: "alice", "session_id": "s1"}
			want := "kc2"
			if pin != "" {
				h[AccountHeader], want = keyWho("ca", pin), pin
			}
			rec := postAt(s, "/v1/chat/completions", `{"model":"group/pair","messages":[{"role":"user","content":"hi s1"}]}`, h)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if got := asked(); got != want {
				t.Fatalf("cold session asked %s, want %s", got, want)
			}
		})
	}
}

// A group's conversation whose key rests is spread over its user's other
// own (T-042): s2's kc2 resting, it goes to kc3, holding one, not kc1,
// holding two, first in the group's order. Nobody is bound afresh.
func TestUserGroupRestingSessionSpread(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	asked := cachedGroup(t, "order", "")
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"ca": {
		"alice": {Who: keyWho("ca", "kc1"), Account: "kc1", At: now, More: []userSeat{{Who: keyWho("ca", "kc2"), Account: "kc2", At: now}, {Who: keyWho("ca", "kc3"), Account: "kc3", At: now}}},
	}}})
	s := byUserUpTo(t, New(), 3)
	if got := groupSessionsAsked(t, s, asked, "alice", "s1", "s2", "s3", "s4"); got != "kc1,kc2,kc3,kc1" {
		t.Fatalf("new sessions asked %s", got)
	}
	restingUntil.Lock()
	restingUntil.m[keyWho("ca", "kc2")] = time.Now().Add(time.Minute)
	restingUntil.Unlock()
	if got := groupSessionsAsked(t, s, asked, "alice", "s2"); got != "kc3" {
		t.Fatalf("resting session asked %s, want kc3", got)
	}
	if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "resting" {
		t.Fatalf("route %+v", r.Affinity)
	}
	if got := seatsIn(t, "ca"); len(got["alice"]) != 3 {
		t.Fatalf("bound afresh: %v", got)
	}
}

// A provider's conversation whose account rests goes the same way: s2's
// spare resting, it goes to third, holding one, not me, holding two. A
// provider's resting account isn't among its candidates, so the trace says
// gone.
func TestUserRestingSessionSpread(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{
			{Who: "codex@spare@example.com", Account: "spare@example.com"}, {Who: "codex@third@example.com", Account: "third@example.com"}}},
	}}})
	s := byUserUpTo(t, New(), 3)
	if got := sessionsAsked(t, s, asked, "s1", "s2", "s3", "s4"); got != "acct-1,acct-2,acct-3,acct-1" {
		t.Fatalf("new sessions asked %s", got)
	}
	restAs(t, failRate, "codex@spare@example.com")
	if got := sessionsAsked(t, s, asked, "s2"); got != "acct-3" {
		t.Fatalf("resting session asked %s, want acct-3", got)
	}
	if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "gone" {
		t.Fatalf("route %+v", r.Affinity)
	}
	if got := seatsIn(t, "codex"); len(got["alice"]) != 3 {
		t.Fatalf("bound afresh: %v", got)
	}
}

// A user with one account has a cold conversation on it, as every other,
// and is given no spare for it.
func TestUserOneAccountColdSessionStays(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com"},
	}}})
	s := byUser(New())
	if got := sessionsAsked(t, s, asked, "s1"); got != "acct-1" {
		t.Fatalf("asked %s", got)
	}
	ageStick(t, "s1", chatGPTCold+time.Minute)
	if got := sessionsAsked(t, s, asked, "s1"); got != "acct-1" {
		t.Fatalf("cold session asked %s", got)
	}
	if got := seatsIn(t, "codex")["alice"]; len(got) != 1 {
		t.Fatalf("a spare added: %v", got)
	}
}

// A provider's conversation gone cold isn't counted where it was: with one
// other on each of kc1 and kc2, s2 goes back to kc2, not to kc1 on a tie
// with itself (T-042). The provider's keys go cold in five minutes, well
// within heldFor, so its own stick would count.
func TestUserColdSessionNotCountedWhereItWas(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	asked := cachedGroup(t, "order", "")
	aliceOnTwo(t)
	s := byUserUpTo(t, New(), 3)
	send := func(session string) string {
		t.Helper()
		rec := postAt(s, "/v1/chat/completions", `{"model":"ca/m","messages":[{"role":"user","content":"hi `+session+`"}]}`,
			map[string]string{UserHeader: "alice", "session_id": session})
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", session, rec.Code, rec.Body.String())
		}
		return asked()
	}
	if got := send("s1") + "," + send("s2"); got != "kc1,kc2" {
		t.Fatalf("new sessions asked %s", got)
	}
	ageStick(t, "s2", cacheCold+time.Minute)
	if got := send("s2"); got != "kc2" {
		t.Fatalf("cold session asked %s, want kc2", got)
	}
	if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "cold" {
		t.Fatalf("route %+v", r.Affinity)
	}
}

// A conversation routed afresh whose request is out counts where it was
// sent alone, not where it was answered too: x, answered on a and sent
// to b, with z on a, leaves a and b one each, and a new one goes to a,
// first on the tie. Counted twice, a held two and it went to b.
func TestLeastHeldCountsARoutedAfreshOnce(t *testing.T) {
	c := func(key string) candidate {
		return candidate{p: provider.Provider{ID: "t042", Key: key}, model: "m", rest: "t042"}
	}
	a, b := c("ka"), c("kb")
	now := time.Now()
	sticks.Lock()
	sticks.m["t042|x"] = stick{rest: a.rest, who: a.who(), model: "m", at: now}
	sticks.opening["t042|x"] = stick{rest: b.rest, who: b.who(), model: "m", at: now}
	sticks.m["t042|z"] = stick{rest: a.rest, who: a.who(), model: "m", at: now}
	sticks.Unlock()
	t.Cleanup(func() {
		sticks.Lock()
		for _, k := range []string{"t042|x", "t042|z", "t042|n"} {
			delete(sticks.m, k)
			delete(sticks.opening, k)
		}
		sticks.Unlock()
	})
	got, _ := leastHeld("t042", "t042|n", []candidate{a, b}, planned{order: []Weighed{{}, {}}}, true, false, 0)
	if got[0].who() != a.who() {
		t.Fatalf("went to %s, want %s", got[0].who(), a.who())
	}
}
