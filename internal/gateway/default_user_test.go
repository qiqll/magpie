package gateway

import (
	"os"
	"testing"
)

// Bound by user, a request that names no user is DefaultUser's (T-157):
// answered, bound to an account of its own as any user is, held to its
// tier's (one account here), and the same user as one that names
// DefaultUser itself. A user's request is bound as before, on an account
// apart. Bound by session, a request without a user binds nobody.
func TestNoUserIsDefaultUser(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, heads := usersUpstream(t, nil)
	s := byUserUpTo(t, New(), 1)
	var on string
	for i, u := range []string{"", "", DefaultUser} {
		rec := userPost(s, "codex/gpt-5.5", u, "turn "+string(rune('a'+i)))
		got := asked(true)
		if rec.Code != 200 || len(got) != 1 {
			t.Fatalf("%q, turn %d: %d asked %v: %s", u, i, rec.Code, got, rec.Body.String())
		}
		if on == "" {
			on = got[0]
		}
		if got[0] != on {
			t.Fatalf("%q, turn %d: on %s, %s's account is %s", u, i, got[0], DefaultUser, on)
		}
		r := lastRoute(s)
		if r.User == nil || r.User.User != DefaultUser {
			t.Fatalf("%q, turn %d: route %+v", u, i, r.User)
		}
		if want := map[bool]string{true: "bound", false: "kept"}[i == 0]; r.User.Why != want {
			t.Fatalf("%q, turn %d: %s, want %s", u, i, r.User.Why, want)
		}
	}
	if rec := userPost(s, "codex/gpt-5.5", "alice", "alice's"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] == on {
		t.Fatalf("alice asked %v, %s is on %s", got, DefaultUser, on)
	}
	if b := boundIn(t, "codex"); len(b) != 2 || b[DefaultUser] == "" || b["alice"] == "" || b[DefaultUser] == b["alice"] {
		t.Fatalf("on disk: %v", b)
	}
	for _, h := range *heads {
		if h.Get(UserHeader) != "" {
			t.Fatalf("%s went upstream: %v", UserHeader, h)
		}
	}

	// bound by session: nothing is bound, DefaultUser or any
	if err := os.Remove(usersPath()); err != nil {
		t.Fatal(err)
	}
	s = New()
	if rec := userPost(s, "codex/gpt-5.5", "", "by session"); rec.Code != 200 {
		t.Fatalf("by session: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.User != nil {
		t.Fatalf("by session: bound %+v", r.User)
	}
	if _, err := os.Stat(usersPath()); !os.IsNotExist(err) {
		t.Fatalf("by session, user-accounts.json written: %v", err)
	}
}
