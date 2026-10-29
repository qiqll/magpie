package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// usersUpstream stands in for the ChatGPT backend, each account answering
// with its own id, or with the status refuse names for it; asked is the
// accounts asked, and with reset starts the list again. Every request's
// headers are kept in heads.
func usersUpstream(t *testing.T, refuse map[string]int) (asked func(reset bool) []string, heads *[]http.Header) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	var hs []http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		acct := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		got = append(got, acct)
		hs = append(hs, r.Header.Clone())
		status := refuse[acct]
		mu.Unlock()
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"from `+acct+`"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return func(reset bool) []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(got)
		if reset {
			got = nil
		}
		return out
	}, &hs
}

// userPost asks for model on magpie's /v1/responses for user, each with
// a conversation of its own.
// refusedWhy is why the last request to s was refused its user, as the
// usage log keeps it: a 429's caller is told only userLimitSaid.
func refusedWhy(s *Server) string {
	if calls := s.Recent(); len(calls) > 0 {
		return calls[0].Error
	}
	return ""
}

func userPost(s *Server, model, user, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"input":"`+input+`"}`))
	req.Header.Set("Authorization", "Bearer magpie")
	if user != "" {
		req.Header.Set(UserHeader, user)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// boundIn is the accounts user-accounts.json binds each user of the
// provider pid to.
func boundIn(t *testing.T, pid string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(usersPath())
	if err != nil {
		t.Fatalf("user-accounts.json: %v", err)
	}
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("user-accounts.json: %v: %s", err, b)
	}
	out := map[string]string{}
	for u, b := range f.Users[pid] {
		out[u] = b.Account
	}
	return out
}

// Each user is bound on its first request to the account fewest users are
// bound to, and stays on it for every model of the provider, whatever
// affinity or routing would pick; the binding is kept on disk by the
// user's own id, and the header goes no further than magpie.
func TestUserBoundToOneAccountOfTheProvider(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, heads := usersUpstream(t, nil)
	s := byUserUpTo(t, New(), 2) // no share: as many users as come on an account
	first := map[string]string{}
	for _, u := range []string{"alice", "bob", "carol"} {
		rec := userPost(s, "codex/gpt-5.5", u, "hello "+u)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", u, rec.Code, rec.Body.String())
		}
		got := asked(true)
		if len(got) != 1 {
			t.Fatalf("%s: asked %v", u, got)
		}
		first[u] = got[0]
		if r := lastRoute(s); r.User == nil || r.User.User != u || r.User.Why != "bound" || len(r.Order) != 1 {
			t.Fatalf("%s: route %+v order %+v", u, r.User, r.Order)
		}
	}
	if first["alice"] == first["bob"] || first["bob"] == first["carol"] || first["alice"] == first["carol"] {
		t.Fatalf("three users on three accounts weren't spread: %v", first)
	}
	// again, on another model and in another conversation: the same account
	for _, model := range []string{"codex/gpt-5.5", "codex/gpt-5.4-mini"} {
		for _, u := range []string{"alice", "bob", "carol"} {
			rec := userPost(s, model, u, "later, "+u+", on "+model)
			if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != first[u] {
				t.Fatalf("%s on %s: %d asked %v, bound to %s: %s", u, model, rec.Code, got, first[u], rec.Body.String())
			}
			if r := lastRoute(s); r.User == nil || r.User.Why != "kept" {
				t.Fatalf("%s on %s: route %+v", u, model, r.User)
			}
		}
	}
	for _, h := range *heads {
		if h.Get(UserHeader) != "" {
			t.Fatalf("%s went upstream: %v", UserHeader, h)
		}
	}
	if b := boundIn(t, "codex"); len(b) != 3 || b["alice"] == "" {
		t.Fatalf("on disk: %v", b)
	}
	// read from the file again, as after a restart
	userBinds.Lock()
	userBinds.read = false
	userBinds.m = nil
	userBinds.Unlock()
	rec := userPost(s, "codex/gpt-5.5", "carol", "after a restart")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != first["carol"] {
		t.Fatalf("after a restart: asked %v, bound to %s", got, first["carol"])
	}
	// a request without a user is refused, bound by user, and binds
	// nobody
	if rec := userPost(s, "codex/gpt-5.5", "", "nobody's"); rec.Code != 400 {
		t.Fatalf("no user: %d %s", rec.Code, rec.Body.String())
	}
	if b := boundIn(t, "codex"); len(b) != 3 {
		t.Fatalf("a request without a user bound one: %v", b)
	}
}

// Users arriving at once are spread too: each binding is made, and
// counted, before the next user is bound.
func TestUsersAtOnceSpread(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	usersUpstream(t, nil)
	s := byUserUpTo(t, New(), 2) // no share: as many users as come on an account
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := "user-" + string(rune('a'+i))
			if rec := userPost(s, "codex/gpt-5.5", u, "hi "+u); rec.Code != 200 {
				t.Errorf("%s: %d %s", u, rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	per := map[string]int{}
	for _, a := range boundIn(t, "codex") {
		per[a]++
	}
	if len(per) != 3 || per["me@example.com"] != 8 || per["spare@example.com"] != 8 || per["third@example.com"] != 8 {
		t.Fatalf("24 users over 3 accounts: %v", per)
	}
}

// The account a user is bound to failing is the caller's error: no other
// account answers in its place, and the user stays bound to it.
func TestUserBoundAccountsFailureIsTheCallers(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	refuse := map[string]int{}
	asked, _ := usersUpstream(t, refuse)
	s := byUser(New())
	if rec := userPost(s, "codex/gpt-5.5", "alice", "hi"); rec.Code != 200 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	bound := asked(true)[0]
	refuse[bound] = 429
	rec := userPost(s, "codex/gpt-5.5", "alice", "again")
	if rec.Code < 400 {
		t.Fatalf("answered past its account's 429: %d %s", rec.Code, rec.Body.String())
	}
	// the vendor's own 429, asked again in place until it gives up: said
	// as a limit on the reply it fails with, not only on the next
	if limit, until := limitOf(t, rec); limit != "rate" || !until.After(time.Now()) {
		t.Fatalf("the vendor's 429: %q until %v: %v", limit, until, rec.Header())
	}
	if got := asked(true); slices.ContainsFunc(got, func(a string) bool { return a != bound }) {
		t.Fatalf("another account was asked: %v", got)
	}
	// resting now: refused without asking anyone
	rec = userPost(s, "codex/gpt-5.5", "alice", "and again")
	if rec.Code != 429 || !strings.Contains(refusedWhy(s), "rests until") {
		t.Fatalf("resting: %d %s", rec.Code, rec.Body.String())
	}
	if limit, until := limitOf(t, rec); limit != "rate" || !until.After(time.Now()) {
		t.Fatalf("resting, rate limited: %q until %v", limit, until)
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked while resting: %v", got)
	}
	if b := boundIn(t, "codex"); b["alice"] == "" {
		t.Fatalf("let go: %v", b)
	}
}

// A user bound to an account no longer on the provider is bound afresh on
// its next request, to one that is.
func TestUserBindingGoesWithItsAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUserUpTo(t, New(), 2) // no share: as many users as come on an account
	userPost(s, "codex/gpt-5.5", "alice", "hi")
	userPost(s, "codex/gpt-5.5", "bob", "hi")
	asked(true)
	b := boundIn(t, "codex")
	if b["bob"] != "spare@example.com" {
		t.Fatalf("bob isn't on the spare account: %v", b)
	}
	// the spare account switched off
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var saved []map[string]any
	raw, _ := os.ReadFile(logins)
	json.Unmarshal(raw, &saved)
	for _, l := range saved {
		l["on"] = false
	}
	os.WriteFile(logins, mustJSON(saved), 0o600)
	provider.ForgetAccounts()
	rec := userPost(s, "codex/gpt-5.5", "bob", "after")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("bob: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	if r := lastRoute(s); r.User == nil || r.User.Why != "gone" {
		t.Fatalf("route %+v", r.User)
	}
	if b := boundIn(t, "codex"); b["bob"] != "me@example.com" {
		t.Fatalf("bob not bound afresh: %v", b)
	}
}

// A user bound to an account whose sign-in its vendor revoked stays bound
// to it (the owner's decision, 2026-10-29; it used to be let go): the
// request that found it revoked is given a spare beside it, its tier
// having room, and answered there, never told of the sign-in; later ones
// go to the spare without asking the revoked one; signed in again, the
// user's requests go back to it, the binding as it was.
func TestUserKeptBoundWhenItsAccountIsRevoked(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	tried := revokedUpstream(t, codexAccessToken(t))
	s := byUser(New())
	// alice on the first account, the one Codex signed in to, revoked
	rec := userPost(s, "codex/gpt-5.5", "alice", "ping")
	if rec.Code != 200 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	saysNoLapse(t, rec)
	if got := strings.Join(tried(true), ","); got != "acct-1,acct-2" {
		t.Fatalf("first: asked %s", got)
	}
	if got := seatsIn(t, "codex")["alice"]; !slices.Equal(got, []string{"me@example.com", "spare@example.com"}) {
		t.Fatalf("not kept on the revoked account with a spare beside it: %v", got)
	}
	if rec := userPost(s, "codex/gpt-5.5", "alice", "later"); rec.Code != 200 {
		t.Fatalf("later: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(tried(true), ","); got != "acct-2" {
		t.Fatalf("later: asked %s", got)
	}

	// Codex signed in again: a new token, the revoked mark no longer its
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": "acct-1", "n": 2})
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      "h." + base64.RawURLEncoding.EncodeToString(mustJSON(map[string]any{"email": "me@example.com"})) + ".s",
		"access_token":  "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s",
		"refresh_token": "r-acct-1b", "account_id": "acct-1"}}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"), mustJSON(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	if rec := userPost(s, "codex/gpt-5.5", "alice", "back"); rec.Code != 200 {
		t.Fatalf("back: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(tried(true), ","); got != "acct-1" {
		t.Fatalf("signed in again, asked %s, not its own account", got)
	}
	if got := seatsIn(t, "codex")["alice"]; !slices.Equal(got, []string{"me@example.com", "spare@example.com"}) {
		t.Fatalf("binding changed: %v", got)
	}
}

// An account found revoked by any request, a bound user's or not (a
// group's), keeps the users bound to it (the owner's decision,
// 2026-10-29; it used to let them go).
func TestUsersKeptBoundToAnAccountRevokedForAnother(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	tried := revokedUpstream(t, codexAccessToken(t))
	if err := provider.SaveGroup(provider.Group{Name: "Solo", Members: []string{"codex/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	os.WriteFile(usersPath(), mustJSON(userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com"},
		"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com"},
	}}}), 0o600)
	s := byUser(New())
	if rec := userPost(s, "group/solo", "ops", "ping"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(tried(true), ","); got != "acct-1,acct-2" {
		t.Fatalf("asked %s", got)
	}
	if b := boundIn(t, "codex"); b["alice"] != "me@example.com" || b["bob"] != "spare@example.com" {
		t.Fatalf("%v", b)
	}
	// alice's next request goes to a spare added beside the revoked one,
	// which she stays bound to
	rec := userPost(s, "codex/gpt-5.5", "alice", "next")
	if rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	saysNoLapse(t, rec)
	if got := strings.Join(tried(true), ","); got != "acct-2" {
		t.Fatalf("alice asked %s", got)
	}
	if got := seatsIn(t, "codex")["alice"]; !slices.Equal(got, []string{"me@example.com", "spare@example.com"}) {
		t.Fatalf("alice's accounts %v", got)
	}
}

// A user-accounts.json that can't be read is unknown, not "nobody bound":
// a user's request is refused, nothing is sent, and the file is left as it
// was; a request without a user goes on.
func TestUnreadableUserBindingsRefuse(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	if err := os.WriteFile(usersPath(), []byte(`{"users": {"codex": {"alice": `), 0o600); err != nil {
		t.Fatal(err)
	}
	s := byUser(New())
	rec := userPost(s, "codex/gpt-5.5", "alice", "hi")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "user-accounts.json") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
	if b, _ := os.ReadFile(usersPath()); string(b) != `{"users": {"codex": {"alice": ` {
		t.Fatalf("written over: %s", b)
	}
	// a group's request isn't bound: it reads no file, and goes on
	if err := provider.SaveGroup(provider.Group{Name: "Solo", Members: []string{"codex/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if rec := userPost(s, "group/solo", "alice", "hi"); rec.Code != 200 {
		t.Fatalf("a group's: %d %s", rec.Code, rec.Body.String())
	}
}

// A user's id too long, or with control characters, is a 400.
func TestUserHeaderRefusedBad(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	for _, u := range []string{strings.Repeat("u", userMax+1), "a\tb"} {
		if rec := userPost(s, "codex/gpt-5.5", u, "hi"); rec.Code != 400 || !strings.Contains(rec.Body.String(), UserHeader) {
			t.Fatalf("%q: %d %s", u, rec.Code, rec.Body.String())
		}
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
	if _, err := os.Stat(usersPath()); err == nil {
		t.Fatal("a bad id was bound")
	}
}

// Codex's relay copies the client's headers on: never the user's id.
func TestCodexRelayDropsTheUser(t *testing.T) {
	dst := http.Header{}
	copyHeaders(dst, http.Header{"X-Magpie-User": {"alice"}, "Accept": {"text/event-stream"}})
	if dst.Get(UserHeader) != "" || dst.Get("Accept") == "" {
		t.Fatalf("%v", dst)
	}
}

// userPostIn is userPost with the user in header name.
func userPostIn(s *Server, name, user, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`))
	req.Header.Set("Authorization", "Bearer magpie")
	req.Header.Set(name, user)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// A caller that sends its users' ids under a name of its own has them
// read from it once MAGPIE_USER_HEADER names it, and only then: a client
// sending X-User-ID for its own reasons isn't held to one account by it.
// The header goes no further than magpie, and X-Magpie-User is read
// first.
func TestUserHeaderOfTheCallersOwn(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, heads := usersUpstream(t, nil)
	s := byUser(New())
	// not the user's header: bound by user, a request without a user;
	// bound by session, routed as any
	if rec := userPostIn(s, "X-User-ID", "alice", "unnamed"); rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := userPostIn(New(), "X-User-ID", "alice", "unnamed"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(usersPath()); err == nil {
		t.Fatalf("X-User-ID bound a user without %s", UserHeaderEnv)
	}
	t.Setenv(UserHeaderEnv, "x-user-id")
	for i, in := range []string{"one", "two", "three"} {
		if rec := userPostIn(s, "X-User-ID", "alice", in); rec.Code != 200 {
			t.Fatalf("%s: %d %s", in, rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); r.User == nil || r.User.User != "alice" || (i > 0 && r.User.Why != "kept") {
			t.Fatalf("%s: route %+v", in, r.User)
		}
	}
	if got := asked(true); len(got) != 4 || got[1] != got[2] || got[2] != got[3] {
		t.Fatalf("asked %v", got)
	}
	for _, h := range *heads {
		if h.Get("X-User-ID") != "" {
			t.Fatalf("X-User-ID went upstream: %v", h)
		}
	}
	// both: X-Magpie-User's is the user
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"both"}`))
	req.Header.Set("Authorization", "Bearer magpie")
	req.Header.Set("X-User-ID", "alice")
	req.Header.Set(UserHeader, "bob")
	s.Handler().ServeHTTP(rec, req)
	if r := lastRoute(s); rec.Code != 200 || r.User == nil || r.User.User != "bob" {
		t.Fatalf("both: %d %+v", rec.Code, r.User)
	}
	dst := http.Header{}
	copyHeaders(dst, http.Header{"X-User-Id": {"alice"}})
	if len(dst) != 0 {
		t.Fatalf("Codex's relay copied it: %v", dst)
	}
	// a name that carries the key, or an agent's session, is never read
	for _, name := range []string{"Authorization", "x-session-id", "X-Magpie-Session", "bad name"} {
		t.Setenv(UserHeaderEnv, name)
		if got := userHeaders(); len(got) != 1 || got[0] != UserHeader {
			t.Fatalf("%q: %v", name, got)
		}
	}
}

// userPostWith is userPost with the gateway key secret.
func userPostWith(s *Server, secret, user, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set(UserHeader, user)
	lanGuard(s.Handler()).ServeHTTP(rec, req)
	return rec
}

// Each gateway key's users are its own: the same id sent with another key
// is another user, bound by itself, and a key's users are counted with
// every other's when the fewest bound is picked.
func TestUsersOfEachKeyApart(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	a, err := access.Update("add-key", access.Change{Name: "Tenant A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := access.Update("add-key", access.Change{Name: "Tenant B"})
	if err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 2) // no share: as many users as come on an account
	if rec := userPostWith(s, a, "alice", "a's alice"); rec.Code != 200 {
		t.Fatalf("a: %d %s", rec.Code, rec.Body.String())
	}
	if rec := userPostWith(s, b, "alice", "b's alice"); rec.Code != 200 {
		t.Fatalf("b: %d %s", rec.Code, rec.Body.String())
	}
	got := asked(true)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("one key's alice took the other's place, or wasn't counted: %v", got)
	}
	raw, _ := os.ReadFile(usersPath())
	var f userFile
	json.Unmarshal(raw, &f)
	if len(f.Users) != 0 || len(f.Keys) != 2 {
		t.Fatalf("not kept by key: %s", raw)
	}
}

// A failure the request itself may have caused (a 400) rests nobody: a
// caller's bad request on one user's account must not take it away from
// every other user.
func TestUserBadRequestRestsNobody(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	refuse := map[string]int{}
	asked, _ := usersUpstream(t, refuse)
	s := byUser(New())
	userPost(s, "codex/gpt-5.5", "mallory", "hi")
	bound := asked(true)[0]
	refuse[bound] = 400
	if rec := userPost(s, "codex/gpt-5.5", "mallory", "bad"); rec.Code != 400 || rec.Header().Get(UserLimitHeader) != "" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if rests := restingNow(t); len(rests) != 0 {
		t.Fatalf("a 400 rested %v", rests)
	}
}

// An account paused is the user's to undo, not gone: a user bound to the
// one Codex is signed in to, paused for the others, stays bound to it, its
// requests refused, not answered by another account.
func TestUserKeptOnItsPausedAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	userPost(s, "codex/gpt-5.5", "alice", "hi")
	if got := asked(true); len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("alice on %v", got)
	}
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var saved []map[string]any
	raw, _ := os.ReadFile(logins)
	json.Unmarshal(raw, &saved)
	saved = append(saved, map[string]any{"agent": "codex", "user": "me@example.com", "paused": true, "seen": time.Now()})
	os.WriteFile(logins, mustJSON(saved), 0o600)
	provider.ForgetAccounts()
	rec := userPost(s, "codex/gpt-5.5", "alice", "paused")
	if rec.Code < 400 {
		t.Fatalf("answered by another account: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
	if b := boundIn(t, "codex"); b["alice"] != "me@example.com" {
		t.Fatalf("let go of a paused account: %v", b)
	}
}

// A user-accounts.json that stops reading keeps the users read before
// bound, binds nobody new, and is left as it is.
func TestUserBindingsKeptWhenTheFileBreaks(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	userPost(s, "codex/gpt-5.5", "alice", "hi")
	bound := asked(true)[0]
	os.WriteFile(usersPath(), []byte(`{"users": `), 0o600)
	if rec := userPost(s, "codex/gpt-5.5", "alice", "again"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != bound {
		t.Fatalf("alice asked %v, bound to %s", got, bound)
	}
	if rec := userPost(s, "codex/gpt-5.5", "bob", "new"); rec.Code != 503 || strings.Contains(rec.Body.String(), string(filepath.Separator)+"user-accounts.json") {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(usersPath()); string(b) != `{"users": ` {
		t.Fatalf("written over: %s", b)
	}
}

// MAGPIE_USER_HEADER never names a header magpie passes on to a vendor as
// an agent sent it, nor one that carries a key: read as a user, it would
// go upstream, or be taken off a request that needs it.
func TestUserHeaderNeverAnAgentsOwn(t *testing.T) {
	for _, name := range []string{"X-Codex-User-Id", "X-Openai-User", "Anthropic-User", "X-Stainless-User", "User-Agent", "Originator", "Chatgpt-Account-Id", "X-Goog-Api-Key", "X-Api-Key", "X-Magpie-Session", "X-Auth-User"} {
		t.Setenv(UserHeaderEnv, name)
		if got := userHeaders(); len(got) != 1 {
			t.Errorf("%s: %v", name, got)
		}
	}
	t.Setenv(UserHeaderEnv, "X-User-ID")
	if got := userHeaders(); len(got) != 2 || got[1] != "X-User-Id" {
		t.Fatalf("X-User-ID: %v", got)
	}
}

// A 401 that a refresh mends (the saved account's token renewed, as Codex
// does on a 401) isn't the account turned away: its users stay bound.
func TestUserKeptWhenARefreshMendsTheSignIn(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var spareToken string
	var saved []map[string]any
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"))
	json.Unmarshal(raw, &saved)
	spareToken = saved[0]["auth"].(map[string]any)["tokens"].(map[string]any)["access_token"].(string)
	tried := revokedUpstream(t, spareToken)
	refreshed := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshed++
		claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": "acct-2", "n": 2})
		json.NewEncoder(w).Encode(map[string]any{"access_token": "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s", "refresh_token": "r-acct-2b"})
	}))
	t.Cleanup(fake.Close)
	t.Cleanup(provider.CodexTokenURLForTest(fake.URL))
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	os.WriteFile(usersPath(), mustJSON(userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com"},
	}}}), 0o600)
	s := byUser(New())
	userPost(s, "codex/gpt-5.5", "bob", "ping")
	if refreshed == 0 || provider.SignInMarked(&provider.Account{Agent: "codex", User: "spare@example.com"}) {
		t.Fatalf("not refreshed (%d), or marked; asked %v", refreshed, tried(false))
	}
	if b := boundIn(t, "codex"); b["bob"] != "spare@example.com" {
		t.Fatalf("let go on a 401 a refresh mended: %v", b)
	}
}

// A 429 to a caller that sent a user says only userLimitSaid; another
// status, or no user, is said as it is.
func TestSaidToUser(t *testing.T) {
	const msg = "Codex (me@example.com): its queue is full"
	if got := saidToUser("alice", 429, msg); got != userLimitSaid {
		t.Fatalf("a user's 429: %q", got)
	}
	if got := saidToUser("", 429, msg); got != msg {
		t.Fatalf("no user: %q", got)
	}
	if got := saidToUser("alice", 400, msg); got != msg {
		t.Fatalf("a user's 400: %q", got)
	}
}
