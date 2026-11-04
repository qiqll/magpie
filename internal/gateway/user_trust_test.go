package gateway

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// shareUsers lets the gateway keys ids share their users
// (access.Key.SharesUsers), as the owner does from the Gateway page or
// magpie gateway-key users <id> shared.
func shareUsers(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := access.Update("users-key", access.Change{Key: id, SharesUsers: true}); err != nil {
			t.Fatal(err)
		}
	}
}

// sharing has the gateway keys ids share their users, beside those the
// key store says, for keys a test's user-accounts.json names alone.
func sharing(t *testing.T, ids ...string) {
	t.Helper()
	was := userSharersRead
	userSharersRead = func() (map[string]bool, bool) {
		ids0, known := was()
		out := maps.Clone(ids0)
		for _, id := range ids {
			out[id] = true
		}
		return out, known
	}
	t.Cleanup(func() { userSharersRead = was })
}

// The owner's option B (T-089): bob, bound under the key Tenant B, is
// another user when a caller of Tenant A, which doesn't share its users,
// names him. Its bob is bound to an account of its own (the one fewest
// are on), never Tenant B's bob's, which is left as it is; the two keys
// that share their users are still one bob, on one account (T-075).
func TestUserOfAKeyThatDoesntShareGetsNoSeatOfTheName(t *testing.T) {
	codexSignedIn(t, "spare@example.com") // me: acct-1, spare: acct-2
	asked, _ := usersUpstream(t, nil)
	keys, secrets := newCaller(t, "Tenant A", "Tenant B", "Front")
	shareUsers(t, keys[1].ID, keys[2].ID)
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[1].ID: {"codex": {"bob": bound("me@example.com", "", ago(time.Minute))}},
	}})
	s := byUserUpTo(t, New(), 2) // no share: as many users as come on an account
	if rec := userPostWith(s, secrets[0], "bob", "a's bob"); rec.Code != 200 {
		t.Fatalf("Tenant A's bob: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("Tenant A's bob asked %v, not an account of its own (acct-2)", got)
	}
	if r := lastRoute(s).User; r == nil || r.Why != "bound" || r.Account != "spare@example.com" || len(r.Spares) != 0 {
		t.Fatalf("Tenant A's bob: %+v", r)
	}
	f := usersFile(t)
	if b := f.Keys[keys[1].ID]["codex"]["bob"]; b.Account != "me@example.com" || len(b.More) != 0 {
		t.Fatalf("Tenant B's bob changed: %+v", b)
	}
	if b := f.Keys[keys[0].ID]["codex"]["bob"]; b.Account != "spare@example.com" || len(b.More) != 0 {
		t.Fatalf("Tenant A's bob: %+v", b)
	}
	// Front shares its users with Tenant B: its bob is Tenant B's
	if rec := userPostWith(s, secrets[2], "bob", "front's bob"); rec.Code != 200 {
		t.Fatalf("Front's bob: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("Front's bob asked %v, not Tenant B's bob's account (acct-1)", got)
	}
	if r := lastRoute(s).User; r == nil || r.Why != "kept" || r.Account != "me@example.com" || len(r.Spares) != 0 {
		t.Fatalf("Front's bob: %+v", r)
	}
}

// A key that doesn't share its users is a namespace of its own for seats
// too: qmcloud's caller naming "2", the name other's user goes by alone
// on kaAAw@ (share 1), is another user, and that account is taken.
// Before T-089 it was put on kaAAw@ as that same user
// (TestUserNameOfAnotherKeySharesItsSeats, which still holds for keys
// that share).
func TestUserNameOfAKeyThatDoesntShareIsAnother(t *testing.T) {
	_, ask, _ := keysOfUsers(t, false, `{"default":"solo","tiers":{"solo":{"credits":1000,"max_accounts":2,"share":1}}}`,
		map[string]map[string][]string{"other": {"2": {"kaAAw@post.com"}}})
	_, asked := limitUpstream(t)
	rec := ask("2")
	if got := asked(true); rec.Code != 429 || len(got) != 0 {
		t.Fatalf("qmcloud's \"2\", another user: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
}

// One name under two keys that don't share is two users of an account:
// with a tier that shares one with one other user (share 2), "2" of other
// and "2" of qmcloud fill kaAAw@, and "3" is refused it.
func TestUserNameUnderTwoKeysThatDontShareCountedTwice(t *testing.T) {
	_, ask, _ := keysOfUsers(t, false, `{"default":"duo","tiers":{"duo":{"credits":1000,"max_accounts":2,"share":2}}}`,
		map[string]map[string][]string{
			"other":   {"2": {"kaAAw@post.com"}},
			"qmcloud": {"2": {"kaAAw@post.com"}},
		})
	_, asked := limitUpstream(t)
	rec := ask("3")
	if got := asked(true); rec.Code != 429 || len(got) != 0 {
		t.Fatalf("user 3 beside two users \"2\": %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
}

// carol, idle under a key that doesn't share for four days and in use
// under another an hour ago, is two users: the idle one is let go of, the
// other kept. Before T-089 her use under either kept both.
func TestUserIdleUnderAKeyThatDoesntShare(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	usersUpstream(t, nil)
	keys, secrets := newCaller(t, "Tenant A", "Tenant B")
	s := byUser(New())
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"codex": {"carol": bound("me@example.com", "", ago(days(4))), "erin": bound("b@example.com", "", ago(time.Hour))}},
		keys[1].ID: {"codex": {"carol": bound("me@example.com", "", ago(time.Hour))}},
	}})
	forgetSeen()
	if rec := userPostWith(s, secrets[0], "dave", "dave's"); rec.Code != 200 {
		t.Fatalf("dave: %d %s", rec.Code, rec.Body.String())
	}
	f := usersFile(t)
	if _, kept := f.Keys[keys[0].ID]["codex"]["carol"]; kept {
		t.Fatalf("Tenant A's carol, idle four days, kept: %+v", f.Keys)
	}
	if _, kept := f.Keys[keys[1].ID]["codex"]["carol"]; !kept {
		t.Fatalf("Tenant B's carol, in use an hour ago, let go: %+v", f.Keys)
	}
}

// An account let go of by hand under a key that doesn't share is let go
// of for its user alone: the same name under another such key, and
// without a key, keeps it.
func TestUserUnboundUnderAKeyThatDoesntShare(t *testing.T) {
	codexSignedIn(t)
	// each bound on its own, at a time of its own: none a copy of
	// another's (T-091 lets go of a copy)
	one := func(at time.Duration) userBinding {
		return userBinding{Who: "codex@me@example.com", Account: "me@example.com", At: ago(at), Used: ago(time.Hour)}
	}
	writeUsers(t, userFile{
		Users: map[string]map[string]userBinding{"codex": {"alice": one(time.Hour)}},
		Keys:  map[string]map[string]map[string]userBinding{"k1": {"codex": {"alice": one(2 * time.Hour)}}, "k2": {"codex": {"alice": one(3 * time.Hour)}}},
	})
	if found, err := Unbind("k1", "codex", "alice", "codex@me@example.com"); !found || err != nil {
		t.Fatalf("%v %v", found, err)
	}
	f := usersFile(t)
	if _, kept := f.Keys["k1"]["codex"]["alice"]; kept {
		t.Fatalf("k1's alice kept: %+v", f)
	}
	if f.Keys["k2"]["codex"]["alice"].Account != "me@example.com" || f.Users["codex"]["alice"].Account != "me@example.com" {
		t.Fatalf("another alice let go: %+v", f)
	}
	// let go of already under k1
	if found, err := Unbind("k1", "codex", "alice", "codex@me@example.com"); found || err != nil {
		t.Fatalf("again: %v %v", found, err)
	}
}

// T-088's reset, bound by user, under a key that doesn't share its users:
// its alice isn't the alice bound without a key, so her account's reset
// is refused, nothing spent; one of its own alice is bound to is hers.
func TestUserOfAKeyThatDoesntShareSpendsNoResetOfTheName(t *testing.T) {
	codexSignedIn(t, "spare@example.com") // me: acct-1, spare: acct-2
	t.Setenv("MAGPIE_ADDR", "")
	b := newResetBackend(t, "acct-1", "acct-2")
	keys, secrets := newCaller(t, "Tenant")
	now := time.Now().UTC()
	writeUsers(t, userFile{
		Users: map[string]map[string]userBinding{"codex": {"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: now}}},
		Keys:  map[string]map[string]map[string]userBinding{keys[0].ID: {"codex": {"alice": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now}}}},
	})
	h := lanGuard(byUserUpTo(t, New(), 1).Handler())
	do := func(account string) (int, string) {
		r := httptest.NewRequest("POST", provider.RemoteCodexResetPath+"?user="+url.QueryEscape(account), nil)
		r.RemoteAddr, r.Host = "192.168.1.9:5000", "127.0.0.1:3425"
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		r.Header.Set(UserHeader, "alice")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, body := do("me@example.com"); code != http.StatusForbidden {
		t.Fatalf("the keyless alice's account: %d %s", code, body)
	}
	if _, spent := b.seen(); spent != "" {
		t.Fatalf("spent %q", spent)
	}
	if code, body := do("spare@example.com"); code != 200 {
		t.Fatalf("its own alice's account: %d %s", code, body)
	}
	if _, spent := b.seen(); spent != "acct-2" {
		t.Fatalf("spent %q, want acct-2", spent)
	}
}

// A key turned to share its users, or not to, is so on its next request,
// as the key store says then.
func TestUserGroupFollowsTheKeyStore(t *testing.T) {
	codexSignedIn(t)
	keys, _ := newCaller(t, "A", "B")
	group := func() (string, string) {
		userBinds.Lock()
		defer userBinds.Unlock()
		loadUsers()
		return userGroup(keys[0].ID), userGroup(keys[1].ID)
	}
	if a, b := group(); a != keys[0].ID || b != keys[1].ID {
		t.Fatalf("not shared yet: %q %q", a, b)
	}
	shareUsers(t, keys[0].ID)
	if a, b := group(); a != sharedUsers || b != keys[1].ID || a != groupOf(nil, "") {
		t.Fatalf("A shared: %q %q", a, b)
	}
	if _, err := access.Update("users-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if a, _ := group(); a != keys[0].ID {
		t.Fatalf("A its own again: %q", a)
	}
	if !slices.Equal([]string{groupOf(map[string]bool{"x": true}, "x"), groupOf(nil, "x")}, []string{sharedUsers, "x"}) {
		t.Fatal("groupOf")
	}
}

// A Claude Code run's web search, asked back through its MCP callback,
// which carries no key, is of the turn's key's user (userKey, askedAs):
// a turn under a key that doesn't share its users, naming the id of a
// user bound without a key, searches on an account bound to its own
// user, and the keyless one's binding is left as it is, its use too.
func TestClaudeRunSearchesAsItsKeysUser(t *testing.T) {
	keys, _ := dsKeyed(t, provider.Responses)
	callers, _ := newCaller(t, "Tenant")
	p, err := provider.Find("ds")
	if err != nil {
		t.Fatal(err)
	}
	seats := seatCandidates(*p)
	if len(seats) != 2 {
		t.Fatalf("%d seats", len(seats))
	}
	used := ago(3 * time.Hour)
	keyless := userBinding{Who: seats[0].who(), Account: shownAs(seats[0]), At: used, Used: used}
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"ds": {"bob": keyless}}})
	s := byUserUpTo(t, New(), 1)
	turn := httptest.NewRequest("POST", "/v1/messages", nil)
	turn.Header.Set(UserHeader, "bob")
	turn = turn.WithContext(access.WithIdentity(turn.Context(), access.Identity{KeyID: callers[0].ID, KeyName: "Tenant"}))
	if _, _, err := s.runSearch(turn)(context.Background(), "weather in Hangzhou"); err != nil {
		t.Fatal(err)
	}
	if got := keys("deepseek-flash"); len(got) != 1 {
		t.Fatalf("searched with %q", got)
	}
	f := usersFile(t)
	if b := f.Users["ds"]["bob"]; b.Who != keyless.Who || !b.Used.Equal(used) {
		t.Fatalf("the keyless bob's binding changed: %+v", b)
	}
	if b, ok := f.Keys[callers[0].ID]["ds"]["bob"]; !ok || strings.EqualFold(b.Who, keyless.Who) {
		t.Fatalf("Tenant's bob: %+v %v", b, ok)
	}
}

// A user of a key that doesn't share asking keeps only itself from being
// swept: the keyless user of its name, idle past userIdle, is let go of.
func TestUserIdleOfTheNameInAnotherGroupSwept(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	usersUpstream(t, nil)
	keys, secrets := newCaller(t, "Tenant")
	s := byUser(New())
	writeUsers(t, userFile{
		Users: map[string]map[string]userBinding{"codex": {"carol": bound("me@example.com", "", ago(days(4))), "erin": bound("b@example.com", "", ago(time.Hour))}},
		Keys:  map[string]map[string]map[string]userBinding{keys[0].ID: {"codex": {"carol": bound("b@example.com", "", ago(time.Hour))}}},
	})
	forgetSeen()
	if rec := userPostWith(s, secrets[0], "carol", "tenant's carol"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	f := usersFile(t)
	if _, kept := f.Users["codex"]["carol"]; kept {
		t.Fatalf("the keyless carol, idle four days, kept for Tenant's asking: %+v", f.Users)
	}
	if _, kept := f.Keys[keys[0].ID]["codex"]["carol"]; !kept {
		t.Fatalf("Tenant's carol let go: %+v", f.Keys)
	}
}

// A conversation's stick is its user's within its group of keys: the same
// id under a key that doesn't share is another owner, under keys that do
// and without a key one.
func TestStickOwnerByGroup(t *testing.T) {
	sharing(t, "front")
	if stickOwnerOf("", "bob") != stickOwnerOf("front", "bob") || stickOwnerOf("front", "bob") != stickOwner("bob") {
		t.Fatal("the shared group's bob isn't one owner")
	}
	if stickOwnerOf("tenant", "bob") == stickOwnerOf("", "bob") || stickOwnerOf("tenant", "bob") == stickOwnerOf("other", "bob") {
		t.Fatal("a key of its own shares its bob's sticks")
	}
	if stickOwnerOf("tenant", "") != "" {
		t.Fatal("no user, an owner")
	}
}
