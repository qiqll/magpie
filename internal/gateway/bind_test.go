package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// byUser is s bound by user, as `magpie serve --bind=user` starts it,
// each user on at most bindMaxDefault accounts of a provider.
func byUser(s *Server) *Server {
	s.bind = Binding{Mode: BindUser, Max: bindMaxDefault}
	return s
}

// byUserUpTo is s bound by user, each on at most max accounts.
func byUserUpTo(s *Server, max int) *Server {
	s.bind = Binding{Mode: BindUser, Max: max}
	return s
}

// BindEnv and BindMaxEnv take session or user and a whole number of 1 or
// more, the defaults when unset; anything else is an error that names
// the variable, beside the default binding.
func TestBindingOf(t *testing.T) {
	for _, c := range []struct {
		mode, max string
		want      Binding
	}{
		{"", "", Binding{BindSession, 2}},
		{"session", "", Binding{BindSession, 2}},
		{" user ", "", Binding{BindUser, 2}},
		{"USER", "3", Binding{BindUser, 3}},
		{"user", "1", Binding{BindUser, 1}},
	} {
		if got, err := BindingOf(c.mode, c.max); err != nil || got != c.want {
			t.Errorf("%q %q: %+v %v", c.mode, c.max, got, err)
		}
	}
	for _, c := range []struct{ mode, max, says string }{
		{"users", "", BindEnv},
		{"sessions", "", BindEnv},
		{"user", "0", BindMaxEnv},
		{"user", "-1", BindMaxEnv},
		{"user", "two", BindMaxEnv},
		{"user", "1.5", BindMaxEnv},
	} {
		got, err := BindingOf(c.mode, c.max)
		if err == nil || !strings.Contains(err.Error(), c.says) || got != (Binding{BindSession, 2}) {
			t.Errorf("%q %q: %+v %v", c.mode, c.max, got, err)
		}
	}
	t.Setenv(BindEnv, "user")
	t.Setenv(BindMaxEnv, "4")
	if s := New(); s.Binding() != (Binding{BindUser, 4}) {
		t.Fatalf("New read %+v", s.Binding())
	}
	t.Setenv(BindEnv, "nope")
	if s := New(); s.Binding() != (Binding{BindSession, 2}) {
		t.Fatalf("a bad %s: %+v", BindEnv, s.Binding())
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
// neither is bound. Listing models and counting tokens pick no account,
// and need none.
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
