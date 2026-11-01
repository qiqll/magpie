package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// headlessPost is sessionPost without a session header: the conversation
// is known by its first user message alone, input.
func headlessPost(s *Server, user, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`))
	req.Header.Set("Authorization", "Bearer magpie")
	if user != "" {
		req.Header.Set(UserHeader, user)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// stickKeys is every conversation a stick is kept for.
func stickKeys() []string {
	sticks.Lock()
	defer sticks.Unlock()
	var ks []string
	for k := range sticks.m {
		ks = append(ks, k)
	}
	return ks
}

// Two users bound to the same two accounts open with the same words and
// no session header (T-043): each one's conversation is its own, so bob's
// first turn is new, not alice's kept, and goes to the account holding
// fewer (spare); his answer doesn't take alice's stick over, and her next
// turn stays on hers, its cache read there.
func TestUserSameFirstWordsKeptApart(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked := cachedUpstream(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
		"bob":   {Who: "codex@me@example.com", Account: "me@example.com", More: []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com"}}},
	}}})
	s := byUser(New())
	for _, user := range []string{"alice", "bob"} {
		if rec := headlessPost(s, user, "hi"); rec.Code != 200 {
			t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "first" {
			t.Fatalf("%s's first turn: %+v", user, r.Affinity)
		}
	}
	if rec := headlessPost(s, "alice", "hi"); rec.Code != 200 {
		t.Fatalf("alice again: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "cache" || !r.Affinity.Kept {
		t.Fatalf("alice's next turn not kept: %+v", r.Affinity)
	}
	if got := strings.Join(asked(true), ","); got != "acct-1,acct-2,acct-1" {
		t.Fatalf("asked %s", got)
	}
	ks := stickKeys()
	if len(ks) != 2 {
		t.Fatalf("sticks %q", ks)
	}
	for _, k := range ks {
		if !strings.Contains(k, "|user:") {
			t.Fatalf("a bound user's stick without its user: %q", k)
		}
	}
	// fresh leaves the opening count as other tests left it: only this
	// test's users' are looked at
	sticks.Lock()
	n := 0
	for k := range sticks.opening {
		if strings.Contains(k, "|user:") {
			n++
		}
	}
	sticks.Unlock()
	if n != 0 {
		t.Fatalf("%d conversations still counted as opening", n)
	}
}

// Bound by session, the same user header names no one: a conversation is
// kept under its key as before T-043.
func TestUnboundStickKeyAsBefore(t *testing.T) {
	codexSignedIn(t)
	cachedUpstream(t)
	s := New()
	if rec := headlessPost(s, "alice", "hi"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	ks := stickKeys()
	if len(ks) != 1 || strings.Contains(ks[0], "user:") || !strings.HasSuffix(ks[0], "|"+conversationID(http.Header{}, []byte(`{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`))) {
		t.Fatalf("sticks %q", ks)
	}
}

// stickKey with no owner is the key as it was; an owner's goes between
// scope and the conversation, so a count by scope's prefix and a lead
// found by its id's suffix still find it, and owners whose ids would
// spell the separator stay apart.
func TestStickKeyOwner(t *testing.T) {
	h := http.Header{}
	h.Set("session_id", "thread-1")
	if got := stickKey("p/m", "", h, nil); got != "p/m|thread-1" {
		t.Fatalf("unbound %q", got)
	}
	a := stickKey("p/m", stickOwner("k", "a|b"), h, nil)
	b := stickKey("p/m", stickOwner("k|a", "b"), h, nil)
	if a == b || !strings.HasPrefix(a, "p/m|") || !strings.HasSuffix(a, "|thread-1") {
		t.Fatalf("owned %q %q", a, b)
	}
	if stickOwner("k", "") != "" {
		t.Fatal("no user, an owner")
	}
}
