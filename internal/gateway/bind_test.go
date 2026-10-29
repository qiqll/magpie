package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// byUser is s bound by user, as `magpie serve --bind=user` starts it,
// each user on as many accounts of a provider as its tier has
// (defaultTiers without a user-tiers.json: 2 for one without a tier).
func byUser(s *Server) *Server {
	s.bind = Binding{Mode: BindUser}
	return s
}

// byUserUpTo is s bound by user, each user without a tier on at most max
// accounts, as user-tiers.json says, and sharing each with any number of
// users (no share).
func byUserUpTo(t *testing.T, s *Server, max int) *Server {
	t.Helper()
	writeTiers(t, fmt.Sprintf(`{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":%d}}}`, max))
	return byUser(s)
}

// BindEnv takes session or user, session when unset; anything else is an
// error that names the variable, beside the default binding.
func TestBindingOf(t *testing.T) {
	for _, c := range []struct {
		mode string
		want Binding
	}{
		{"", Binding{BindSession}},
		{"session", Binding{BindSession}},
		{" user ", Binding{BindUser}},
		{"USER", Binding{BindUser}},
	} {
		if got, err := BindingOf(c.mode); err != nil || got != c.want {
			t.Errorf("%q: %+v %v", c.mode, got, err)
		}
	}
	for _, mode := range []string{"users", "sessions"} {
		got, err := BindingOf(mode)
		if err == nil || !strings.Contains(err.Error(), BindEnv) || got != (Binding{BindSession}) {
			t.Errorf("%q: %+v %v", mode, got, err)
		}
	}
	t.Setenv(BindEnv, "user")
	if s := New(); s.Binding() != (Binding{BindUser}) {
		t.Fatalf("New read %+v", s.Binding())
	}
	t.Setenv(BindEnv, "nope")
	if s := New(); s.Binding() != (Binding{BindSession}) {
		t.Fatalf("a bad %s: %+v", BindEnv, s.Binding())
	}
}

// A service installed with --bind-max still has MAGPIE_BIND_MAX: the
// gateway starts bound as MAGPIE_BIND says, ignores it, and logs that,
// once, naming user-tiers.json in its place.
func TestBindMaxLeftIsIgnored(t *testing.T) {
	codexSignedIn(t)
	var logs bytes.Buffer
	was := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(was) })
	t.Setenv(BindEnv, "user")
	t.Setenv(bindMaxEnv, "7")
	for range 2 {
		if s := New(); s.Binding() != (Binding{BindUser}) {
			t.Fatalf("with %s set: %+v", bindMaxEnv, s.Binding())
		}
	}
	if n := strings.Count(logs.String(), bindMaxEnv+"=7 is ignored"); n != 1 || !strings.Contains(logs.String(), "user-tiers.json") {
		t.Fatalf("said %d times: %s", n, logs.String())
	}
	if _, err := BindingFromEnv(); err != nil {
		t.Fatalf("refused on it: %v", err)
	}
}

// Bound by session, as magpie always has, a request's user changes
// nothing: it is routed as one without, no user is bound and
// user-accounts.json isn't written, nor read (one that doesn't read
// refuses nothing); the header goes no further than magpie still.
func TestSessionBindingIgnoresTheUser(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, heads := usersUpstream(t, nil)
	s := New()
	if s.Binding().byUser() {
		t.Fatal("bound by user unless told")
	}
	var with, without []string
	for i, u := range []string{"alice", "bob", "carol", "dave"} {
		in := "hello " + string(rune('a'+i))
		if rec := userPost(s, "codex/gpt-5.5", u, in+" with"); rec.Code != 200 {
			t.Fatalf("%s: %d %s", u, rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); r.User != nil {
			t.Fatalf("%s: bound %+v", u, r.User)
		}
		with = append(with, asked(true)...)
	}
	sticks.Lock()
	sticks.m, sticks.opening = map[string]stick{}, map[string]stick{}
	sticks.Unlock()
	for i := range 4 {
		in := "hello " + string(rune('a'+i))
		if rec := userPost(s, "codex/gpt-5.5", "", in+" with"); rec.Code != 200 {
			t.Fatalf("%d: %d %s", i, rec.Code, rec.Body.String())
		}
		without = append(without, asked(true)...)
	}
	if strings.Join(with, ",") != strings.Join(without, ",") {
		t.Fatalf("routed apart by the user: %v, without %v", with, without)
	}
	if _, err := os.Stat(usersPath()); err == nil {
		t.Fatal("user-accounts.json written bound by session")
	}
	for _, h := range *heads {
		if h.Get(UserHeader) != "" {
			t.Fatalf("%s went upstream: %v", UserHeader, h)
		}
	}
	// a file that doesn't read: bound by user it refuses (503), bound by
	// session it isn't read, and is left as it is
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	os.WriteFile(usersPath(), []byte(`{"users": `), 0o600)
	if rec := userPost(s, "codex/gpt-5.5", "alice", "unread"); rec.Code != 200 {
		t.Fatalf("an unreadable file read bound by session: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(usersPath()); string(b) != `{"users": ` {
		t.Fatalf("written over: %s", b)
	}
	// and an account turned away for good lets nobody go from it
	os.WriteFile(usersPath(), mustJSON(userFile{Users: map[string]map[string]userBinding{"codex": {"alice": {Who: "codex@me@example.com", Account: "me@example.com"}}}}), 0o600)
	was, _ := os.ReadFile(usersPath())
	s.unbindTurnedAway(candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}, rest: "codex"}, failRestricted)
	if b, _ := os.ReadFile(usersPath()); string(b) != string(was) {
		t.Fatalf("let go bound by session: %s", b)
	}
}

// postAt posts body to path on s with the headers h.
func postAt(s *Server, path, body string, h map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer magpie")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Bound by user, every request that asks a model names its user: one
// without, or with one the header can't take, is a 400 that says so and
// asks nobody — a group's and one pinned to an account too, though
// neither is bound. Listing models picks no account, and needs none; a
// count without a user is estimated, asking none
// (TestUserBindingCountsWithoutTheUserLocally).
func TestUserBindingNeedsTheUser(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	if err := provider.SaveGroup(provider.Group{Name: "Pair", Members: []string{"codex/gpt-5.5", "codex/gpt-5.4-mini"}}); err != nil {
		t.Fatal(err)
	}
	s := byUser(New())
	resp := `{"model":"%s","stream":true,"input":"hi"}`
	for _, c := range []struct {
		name, path, body string
		h                map[string]string
		says             string
	}{
		{"none", "/v1/responses", strings.Replace(resp, "%s", "codex/gpt-5.5", 1), nil, "--bind=user"},
		{"blank", "/v1/responses", strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{UserHeader: "   "}, "--bind=user"},
		{"too long", "/v1/responses", strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{UserHeader: strings.Repeat("u", userMax+1)}, "at most"},
		{"control", "/v1/responses", strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{UserHeader: "a\tb"}, "control"},
		{"format", "/v1/responses", strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{UserHeader: "a\u200bb"}, "format"},
		{"group", "/v1/responses", strings.Replace(resp, "%s", "group/pair", 1), nil, "--bind=user"},
		{"pinned", "/v1/responses", strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{AccountHeader: "me@example.com"}, "--bind=user"},
		{"chat", "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, nil, "--bind=user"},
		{"messages", "/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil, "--bind=user"},
		{"gemini", "/v1beta/models/codex/gpt-5.5:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`, nil, "--bind=user"},
		{"codex", CodexPath + "/responses", `{"model":"gpt-5.5","stream":true,"input":"hi"}`, nil, "--bind=user"},
		{"embeddings", "/v1/embeddings", `{"model":"codex/gpt-5.5","input":"hi"}`, nil, "--bind=user"},
	} {
		rec := postAt(s, c.path, c.body, c.h)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), c.says) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked without a user: %v", got)
	}
	// a group's and a pinned request with a user: answered, nobody bound
	for _, c := range []struct {
		model string
		h     map[string]string
	}{
		{"group/pair", map[string]string{UserHeader: "alice"}},
		{"codex/gpt-5.5", map[string]string{UserHeader: "alice", AccountHeader: "spare@example.com"}},
	} {
		if rec := postAt(s, "/v1/responses", strings.Replace(resp, "%s", c.model, 1), c.h); rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.model, rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); r.User != nil {
			t.Fatalf("%s: bound %+v", c.model, r.User)
		}
	}
	if _, err := os.Stat(usersPath()); err == nil {
		t.Fatal("a group's or a pinned request bound its user")
	}
	// no model asked, no user needed
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer magpie")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("models: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/v1/messages/count_tokens", "/v1beta/models/codex/gpt-5.5:countTokens"} {
		body := `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`
		if strings.Contains(path, "v1beta") {
			body = `{"contents":[{"parts":[{"text":"hi"}]}]}`
		}
		if rec := postAt(s, path, body, nil); rec.Code == 400 && strings.Contains(rec.Body.String(), "--bind=user") {
			t.Fatalf("%s needed a user: %s", path, rec.Body.String())
		}
	}
	// bound by session, none of them needs one
	s = New()
	if rec := postAt(s, "/v1/responses", strings.Replace(resp, "%s", "codex/gpt-5.5", 1), nil); rec.Code != 200 {
		t.Fatalf("bound by session, no user: %d %s", rec.Code, rec.Body.String())
	}
}

// Bound by user, a count without a user — Anthropic's count_tokens and
// Gemini's :countTokens — asks no account of the pool: it is estimated
// here, a 200, not refused. With a user, or bound by session, Anthropic's
// count still goes to the provider.
func TestUserBindingCountsWithoutTheUserLocally(t *testing.T) {
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := &countAsked{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Anthropic: srv.URL, Gemini: srv.URL, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	msg := `{"model":"fake/m1","messages":[{"role":"user","content":"hello there, count me"}]}`
	// users are read from X-User-ID too (MAGPIE_USER_HEADER), as from
	// UserHeader
	t.Setenv(UserHeaderEnv, "X-User-ID")
	s := byUser(New())
	// each a request noUser refuses generating: no user, one the header
	// can't take, or a tier that is none of user-tiers.json's
	for _, c := range []struct {
		name string
		h    map[string]string
	}{
		{"none", nil},
		{"blank", map[string]string{UserHeader: "   "}},
		{"control", map[string]string{UserHeader: "a\tb"}},
		{"too long", map[string]string{UserHeader: strings.Repeat("u", userMax+1)}},
		{"custom blank", map[string]string{"X-User-ID": "  "}},
		{"custom control", map[string]string{"X-User-ID": "a\tb"}},
		{"bad tier", map[string]string{UserHeader: "alice", UserTierHeader: "no-such-tier"}},
	} {
		if n := estimated(t, postAt(s, "/v1/messages/count_tokens", msg, c.h)); n == 999 {
			t.Errorf("count_tokens, %s: the provider's count", c.name)
		}
		if got := up.asked(); len(got) != 0 {
			t.Fatalf("count_tokens, %s: asked an account: %v", c.name, got)
		}
	}
	// Gemini's count is estimated whatever the binding (geminiCount asks
	// no provider): this half pins that, and no change to countOn turns it
	// red
	rec := postAt(s, "/v1beta/models/fake/m1:countTokens", `{"contents":[{"parts":[{"text":"hello there, count me"}]}]}`, nil)
	var gem struct {
		TotalTokens int `json:"totalTokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &gem); rec.Code != 200 || err != nil || gem.TotalTokens == 0 {
		t.Fatalf(":countTokens without a user: %d %s", rec.Code, rec.Body)
	}
	if got := up.asked(); len(got) != 0 {
		t.Fatalf(":countTokens without a user asked an account: %v", got)
	}
	// with a user bound to the provider's key, in either header, the
	// provider counts, as before (a count reads a binding, never makes
	// one: T-003, TestUserCountOnTheUsersOwnKey)
	seat := userBinding{Who: "fake#" + provider.KeyID("k"), Account: "k"}
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"fake": {"alice": seat, "bob": seat}}})
	for _, h := range []map[string]string{{UserHeader: "alice"}, {"X-User-ID": "bob"}} {
		if n := estimated(t, postAt(s, "/v1/messages/count_tokens", msg, h)); n != 999 {
			t.Fatalf("count_tokens with a user %v: %d, want the provider's 999", h, n)
		}
	}
	// and bound by session, without one
	if n := estimated(t, postAt(New(), "/v1/messages/count_tokens", msg, nil)); n != 999 {
		t.Fatalf("count_tokens bound by session: %d, want the provider's 999", n)
	}
	if got := up.asked(); len(got) != 3 {
		t.Fatalf("asked %v, want the counts with a user and by session", got)
	}
}
