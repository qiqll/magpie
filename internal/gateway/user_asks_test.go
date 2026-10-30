package gateway

import (
	"bytes"
	"cmp"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// keysSeen is next behind a server that keeps the key each request to a
// model came with, and whether any carried a user header on.
func keysSeen(t *testing.T, next http.Handler) (up *httptest.Server, keys func(model string) []string, users func() int) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string][]string{}
	leaked := 0
	up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		mu.Lock()
		if !strings.HasSuffix(r.URL.Path, "/models") {
			key := cmp.Or(r.Header.Get("x-api-key"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			seen[modelOf(b)] = append(seen[modelOf(b)], key)
		}
		if r.Header.Get(UserHeader) != "" || r.Header.Get(UserTierHeader) != "" {
			leaked++
		}
		mu.Unlock()
		next.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	return up, func(model string) []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), seen[model]...)
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return leaked
		}
}

// oneKeyEach says alice's asks all went with one key, bob's with one
// other: each on the account it is bound to, never another user's.
func oneKeyEach(t *testing.T, what string, alice, bob []string) {
	t.Helper()
	if len(alice) == 0 || len(bob) == 0 {
		t.Fatalf("%s: alice asked with %q, bob with %q", what, alice, bob)
	}
	for _, k := range alice[1:] {
		if k != alice[0] {
			t.Fatalf("%s: alice asked with %q: more than its one account", what, alice)
		}
	}
	for _, k := range bob[1:] {
		if k != bob[0] {
			t.Fatalf("%s: bob asked with %q: more than its one account", what, bob)
		}
	}
	if alice[0] == bob[0] {
		t.Fatalf("%s: alice and bob both asked with %q, one's account was the other's", what, alice[0])
	}
}

// dsKeyed is a DeepSeek that searches on its API on, with two keys, each
// seen, the searcher magpie picks by itself.
func dsKeyed(t *testing.T, on provider.Protocol) (keys func(model string) []string, users func() int) {
	t.Helper()
	searchSandbox(t)
	ds := newFakeDeepSeek(t)
	up, keys, users := keysSeen(t, ds.Config.Handler)
	searchesOn(t, on, up.URL)
	p := provider.Provider{ID: "ds", Name: "DeepSeek", Key: "ka", Keys: []provider.KeyAccount{{Key: "kb"}}, Models: []string{"deepseek-flash"}}
	if on == provider.Anthropic {
		p.Anthropic = up.URL + "/anthropic"
	} else {
		p.Chat, p.Responses = up.URL+"/v1", up.URL+"/v1"
	}
	save(t, p)
	st := settings.Load()
	st.Searcher = ""
	settings.Save(st)
	return keys, users
}

// Bound by user (--bind=user), a web_search through the MCP server is
// asked of the searcher's accounts its caller's user is bound to, as a
// model request of that user would be, never drawn from every account of
// the provider; a call without its user is refused, and searches nothing.
func TestSearchMCPAskedOfTheUsersAccounts(t *testing.T) {
	keys, users := dsKeyed(t, provider.Responses)
	s := byUserUpTo(t, New(), 1)
	h := lanGuard(s.Handler())
	const here = "127.0.0.1:5000"

	m := readMCP(t, mcpPost(h, here, mcpSearchCall))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "--bind=user") {
		t.Fatalf("without a user: %+v", m)
	}
	if got := keys("deepseek-flash"); len(got) != 0 {
		t.Fatalf("searched without a user: %q", got)
	}

	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, u := range []string{"alice", "bob"} {
			m := readMCP(t, mcpPost(h, here, mcpSearchCall, UserHeader, u))
			if m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "22/17℃") {
				t.Fatalf("%s: %+v", u, m)
			}
			got := keys("deepseek-flash")
			if u == "alice" {
				alice = append(alice, got[len(got)-1])
			} else {
				bob = append(bob, got[len(got)-1])
			}
		}
	}
	oneKeyEach(t, "web_search", alice, bob)
	if b := boundIn(t, "ds"); len(b) != 2 {
		t.Fatalf("bound %v, want alice and bob", b)
	}
	if n := users(); n != 0 {
		t.Fatalf("%d searches took the user header to the vendor", n)
	}
}

// probesKeyed is eyed's upstream, its provider with two keys, each seen.
func probesKeyed(t *testing.T) (*Server, func(model string) []string, func() int) {
	t.Helper()
	fresh(t)
	up, keys, users := keysSeen(t, &eyeUp{})
	if err := provider.Save(provider.Provider{ID: "probe", Name: "Probe", Chat: up.URL + "/v1", Key: "ka", Keys: []provider.KeyAccount{{Key: "kb"}}, Models: []string{"text", "eye"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
		{ID: "eye", Images: true, ImageInput: imageInputBool(true)},
	}); err != nil {
		t.Fatal(err)
	}
	return byUserUpTo(t, New(), 1), keys, users
}

// Bound by user, analyze_image is asked of the describer's accounts its
// caller's user is bound to, the same image as another user's included
// (a look isn't one user's answer handed to the next); a call without its
// user is refused, and looks at nothing.
func TestVisionMCPAskedOfTheUsersAccounts(t *testing.T) {
	s, keys, users := probesKeyed(t)
	h := lanGuard(s.Handler())
	const here = "127.0.0.1:50000"
	call := analyzeCall("data:image/png;base64,aGVsbG8=", "")

	m := readMCP(t, visionPost(h, here, call))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "--bind=user") {
		t.Fatalf("without a user: %+v", m)
	}
	if got := keys("eye"); len(got) != 0 {
		t.Fatalf("looked without a user: %q", got)
	}

	var alice, bob []string
	for i, img := range []string{"aGVsbG8=", "d29ybGQ="} {
		for _, u := range []string{"alice", "bob"} {
			m := readMCP(t, visionPost(h, here, analyzeCall("data:image/png;base64,"+img, ""), UserHeader, u))
			if m.Result.IsError || m.Result.Content[0].Text != "A red square with the word HELLO in it." {
				t.Fatalf("%s, image %d: %+v", u, i, m)
			}
			got := keys("eye")
			if u == "alice" {
				alice = append(alice, got[len(got)-1])
			} else {
				bob = append(bob, got[len(got)-1])
			}
		}
	}
	if got := keys("eye"); len(got) != 4 {
		t.Fatalf("4 looks, one of each user at each image, asked %q", got)
	}
	oneKeyEach(t, "analyze_image", alice, bob)
	if b := boundIn(t, "probe"); len(b) != 2 {
		t.Fatalf("bound %v, want alice and bob", b)
	}
	if n := users(); n != 0 {
		t.Fatalf("%d looks took the user header to the vendor", n)
	}
}

// Bound by user, the image a text-only model's request carries is
// described on the account the request's user is bound to: the
// description beside the request, on the same user's account.
func TestImageDescribedOnTheUsersAccount(t *testing.T) {
	s, keys, users := probesKeyed(t)
	body := `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	for _, u := range []string{"alice", "bob", "alice", "bob"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set(UserHeader, u)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", u, rec.Code, rec.Body.String())
		}
	}
	text, eye := keys("text"), keys("eye")
	if len(text) != 4 || len(eye) != 2 {
		t.Fatalf("text asked with %q, eye with %q: want 4, and one look a user", text, eye)
	}
	oneKeyEach(t, "the text model", []string{text[0], text[2]}, []string{text[1], text[3]})
	if eye[0] != text[0] || eye[1] != text[1] {
		t.Fatalf("alice's and bob's images were described with %q, their requests went with %q", eye, text[:2])
	}
	if n := users(); n != 0 {
		t.Fatalf("%d requests took the user header to the vendor", n)
	}
}

// A searcher on its Anthropic API is sent the request as it came
// (passthrough): bound by user it is asked on the user's key too, and the
// user's header still goes no further than magpie.
func TestSearchMCPAnthropicSearcherKeepsTheUser(t *testing.T) {
	keys, users := dsKeyed(t, provider.Anthropic)
	h := lanGuard(byUserUpTo(t, New(), 1).Handler())
	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, u := range []string{"alice", "bob"} {
			m := readMCP(t, mcpPost(h, "127.0.0.1:5000", mcpSearchCall, UserHeader, u, UserTierHeader, "free"))
			if m.Result.IsError {
				t.Fatalf("%s: %+v", u, m)
			}
			got := keys("deepseek-flash")
			if u == "alice" {
				alice = append(alice, got[len(got)-1])
			} else {
				bob = append(bob, got[len(got)-1])
			}
		}
	}
	oneKeyEach(t, "an Anthropic searcher", alice, bob)
	if n := users(); n != 0 {
		t.Fatalf("%d searches took the user or tier header to the vendor", n)
	}
}

// userSearch asks model, which can't search, at path with a web_search
// tool for user: the search magpie makes for it (searchReply).
func userSearch(t *testing.T, s *Server, user string) {
	t.Helper()
	body := `{"model":"glm/glm-flash","max_tokens":3000,"stream":true,"messages":[{"role":"user","content":"用 web_search 工具搜索：今天上海天气"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set(UserHeader, user)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
	}
}

// Bound by user, the search magpie makes in a turn for a model that can't
// search is asked of the searcher's accounts the turn's user is on.
func TestTurnsSearchAskedOfTheUsersAccounts(t *testing.T) {
	keys, users := dsKeyed(t, provider.Responses)
	glm, result := newBlindModel(t)
	save(t, provider.Provider{ID: "glm", Name: "GLM", Key: "k", Chat: glm.URL, Models: []string{"glm-flash"}})
	s := byUserUpTo(t, New(), 1)
	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, u := range []string{"alice", "bob"} {
			userSearch(t, s, u)
			if !strings.Contains(result(), "22/17℃") {
				t.Fatalf("%s: the model was told %q", u, result())
			}
			got := keys("deepseek-flash")
			if u == "alice" {
				alice = append(alice, got[len(got)-1])
			} else {
				bob = append(bob, got[len(got)-1])
			}
		}
	}
	oneKeyEach(t, "a turn's web_search", alice, bob)
	if n := users(); n != 0 {
		t.Fatalf("%d searches took the user header to the vendor", n)
	}
}

// codexSearchFor is codexSearchPost for user.
func codexSearchFor(h http.Handler, path, body, user string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer magpie")
	r.Header.Set("x-openai-actor-authorization", "magpie")
	r.Header.Set("originator", "codex_exec")
	r.Header.Set("User-Agent", "codex_exec/0.162.1 (Linux 6.8; x86_64)")
	r.Header.Set(UserHeader, user)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Bound by user, Codex's web.run on a model no ChatGPT account serves is
// searched by magpie (alphaSearchHere) on the searcher's accounts the
// user is on.
func TestCodexWebRunHereAskedOfTheUsersAccounts(t *testing.T) {
	keys, _ := dsKeyed(t, provider.Responses)
	h := byUserUpTo(t, New(), 1).Handler()
	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, u := range []string{"alice", "bob"} {
			got := readSearchReply(t, codexSearchFor(h, "/v1/alpha/search", codexSearchBody("ds/deepseek-flash"), u))
			if !strings.Contains(got.Output, "22/17℃") {
				t.Fatalf("%s: answered %+v", u, got)
			}
			k := keys("deepseek-flash")
			if u == "alice" {
				alice = append(alice, k[len(k)-1])
			} else {
				bob = append(bob, k[len(k)-1])
			}
		}
	}
	oneKeyEach(t, "web.run searched by magpie", alice, bob)
}

// Bound by user, Codex's web.run on a ChatGPT account's model goes to the
// ChatGPT backend on the user's own account, never another user's: each
// on its one, apart, every time.
func TestCodexWebRunOnTheUsersChatGPTAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var mu sync.Mutex
	var tried []string
	leaked := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		if r.Header.Get(UserHeader) != "" {
			leaked++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"encrypted_output":"c","output":"OpenAI news from `+r.Header.Get("chatgpt-account-id")+`"}`)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	h := byUserUpTo(t, New(), 1).Handler()
	var alice, bob []string
	for i, path := range []string{"/v1/alpha/search", CodexPath + "/alpha/search"} {
		for _, u := range []string{"alice", "bob"} {
			got := readSearchReply(t, codexSearchFor(h, path, codexSearchBody("codex/gpt-5.5"), u))
			mu.Lock()
			last := tried[len(tried)-1]
			n := len(tried)
			mu.Unlock()
			if n != 2*i+map[string]int{"alice": 1, "bob": 2}[u] || got.Output != "OpenAI news from "+last {
				t.Fatalf("%s %s: asked %v, answered %+v", path, u, tried, got)
			}
			if u == "alice" {
				alice = append(alice, last)
			} else {
				bob = append(bob, last)
			}
		}
	}
	oneKeyEach(t, "web.run on ChatGPT", alice, bob)
	if b := boundIn(t, "codex"); len(b) != 2 {
		t.Fatalf("bound %v, want alice and bob", b)
	}
	if leaked != 0 {
		t.Fatalf("%d searches took the user header to ChatGPT", leaked)
	}
}

// Bound by user, a Kimi Code plan picked to search is asked on the key
// the user is on, never on the provider's first key for every user: its
// search service is sent the key straight, past serve.
func TestKimiSearchOnTheUsersKey(t *testing.T) {
	searchSandbox(t)
	var mu sync.Mutex
	var keys []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		if r.URL.Path != "/coding/v1/search" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		keys = append(keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		io.WriteString(w, `{"search_results":[{"site_name":"Go","title":"Go downloads","url":"https://go.dev/dl/","snippet":"go1.27.1 is the latest release.","date":"2026-09-30"}]}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi Code", Preset: "kimi-code-cn", Key: "sk-a", Keys: []provider.KeyAccount{{Key: "sk-b"}},
		Chat: up.URL + "/coding/v1", Models: []string{"kimi-for-coding"}}); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.Searcher = "kimi"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(byUserUpTo(t, New(), 1).Handler())
	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, u := range []string{"alice", "bob"} {
			m := readMCP(t, mcpPost(h, "127.0.0.1:5000", mcpSearchCall, UserHeader, u))
			if m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "go1.27.1") {
				t.Fatalf("%s: %+v", u, m)
			}
			mu.Lock()
			last := keys[len(keys)-1]
			mu.Unlock()
			if u == "alice" {
				alice = append(alice, last)
			} else {
				bob = append(bob, last)
			}
		}
	}
	oneKeyEach(t, "Kimi Code's search", alice, bob)
}

// A user is its gateway key's own: the same id sent with two keys is two
// users, each looked for on the account it is on, and one key's alice is
// never given the other's description.
func TestVisionMCPUsersOfEachKeyApart(t *testing.T) {
	s, keys, _ := probesKeyed(t)
	a, err := access.Update("add-key", access.Change{Name: "Tenant A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := access.Update("add-key", access.Change{Name: "Tenant B"})
	if err != nil {
		t.Fatal(err)
	}
	h := lanGuard(s.Handler())
	call := analyzeCall("data:image/png;base64,aGVsbG8=", "")
	for _, secret := range []string{a, b, a, b} {
		m := readMCP(t, visionPost(h, "127.0.0.1:50000", call, "Authorization", "Bearer "+secret, UserHeader, "alice"))
		if m.Result.IsError {
			t.Fatalf("%+v", m)
		}
	}
	got := keys("eye")
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("a's alice and b's alice looked with %q: want once each, on two accounts", got)
	}
}

// Bound by session, an image is described once whoever sends it, as it
// always was: a user's header changes nothing.
func TestBoundBySessionDescribesOnce(t *testing.T) {
	s, u := eyed(t)
	h := lanGuard(s.Handler())
	call := analyzeCall("data:image/png;base64,aGVsbG8=", "")
	for _, user := range []string{"alice", "bob", ""} {
		if m := readMCP(t, visionPost(h, "127.0.0.1:50000", call, UserHeader, user)); m.Result.IsError {
			t.Fatalf("%q: %+v", user, m)
		}
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("described %d times", n)
	}
}

// imagePosted sends a request with an image to model for user, with hdr
// as name, value pairs.
func imagePosted(t *testing.T, s *Server, model, user string, hdr ...string) {
	t.Helper()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(UserHeader, user)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
	}
}

// A routing group's request isn't bound to its user's accounts, nor one
// pinned to an account (X-Magpie-Account), but each needs its user, and
// the image magpie describes for it is: asked on the describer's account
// the user is on, bound there when on none.
func TestUnboundRequestsDescribeOnTheUsersAccount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		hdr   []string
	}{
		{"group", "group/g", nil},
		{"pinned", "probe/text", []string{AccountHeader, "probe#" + provider.KeyID("ka")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, keys, users := probesKeyed(t)
			if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"probe/text"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			for _, u := range []string{"alice", "bob", "alice", "bob"} {
				imagePosted(t, s, tc.model, u, tc.hdr...)
			}
			eye := keys("eye")
			if len(eye) != 2 || eye[0] == eye[1] {
				t.Fatalf("alice's and bob's images were described with %q: want once each, on two accounts", eye)
			}
			if tc.name == "pinned" {
				for _, k := range keys("text") {
					if k != "ka" {
						t.Fatalf("pinned to ka, the model was asked with %q", keys("text"))
					}
				}
			}
			if b := boundIn(t, "probe"); len(b) != 2 {
				t.Fatalf("bound %v, want alice and bob on the describer", b)
			}
			if n := users(); n != 0 {
				t.Fatalf("%d requests took the user header to the vendor", n)
			}
		})
	}
}

// Bound by user, a routing group's classifier is asked on the classifier's
// accounts the turn's user is on, as that user's own request to it would
// be (askFor), never on the provider's first key for every user; the same
// words from another user are asked on that user's, not answered from the
// first's (classifyAs). Before, every ask went with no user and no key, on
// the provider's first key, and the second user's was the first's answer.
func TestClassifierAskedOfTheUsersAccounts(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
	c.set("1", 0)
	up, keys, users := keysSeen(t, c)
	if err := provider.Save(provider.Provider{ID: "c", Name: "C", Key: "kc", Keys: []provider.KeyAccount{{Key: "kd"}}, Models: []string{"cls"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("c", up.URL+"/v1", []catalog.Model{{ID: "cls", Context: 32000}}); err != nil {
		t.Fatal(err)
	}
	s = byUserUpTo(t, s, 1)
	turn := func(u, words string) string {
		t.Helper()
		before := len(keys("cls"))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat(words, nil, 0, "")))
		req.Header.Set("x-session-id", u+": "+words)
		req.Header.Set(UserHeader, u)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s, %q: %d %s", u, words, rec.Code, rec.Body.String())
		}
		got := keys("cls")
		if len(got) != before+1 {
			t.Fatalf("%s, %q: the classifier was asked with %q, want once more", u, words, got[before:])
		}
		return got[before]
	}
	var alice, bob []string
	for _, round := range []string{"first", "second"} {
		alice = append(alice, turn("alice", "add the "+round+" unit test for alice's parser"))
		bob = append(bob, turn("bob", "add the "+round+" unit test for bob's parser"))
	}
	oneKeyEach(t, "the classifier", alice, bob)
	// alice's words again, from bob: asked on bob's account, not alice's answer
	if k := turn("bob", "add the first unit test for alice's parser"); k != bob[0] {
		t.Fatalf("bob was asked with %q, its account is %q", k, bob[0])
	}
	if b := boundIn(t, "c"); len(b) != 2 {
		t.Fatalf("bound %v, want alice and bob on the classifier's provider", b)
	}
	if n := users(); n != 0 {
		t.Fatalf("%d classifier asks took the user header to the vendor", n)
	}
}

// A gateway key held to some accounts asks the classifier on those alone,
// as its own request to the classifier's model would be: before, the ask
// went with no key at all, on the provider's first account, which the key
// may not use.
func TestClassifierAskedOnTheKeysAccounts(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
	c.set("1", 0)
	up, keys, _ := keysSeen(t, c)
	if err := provider.Save(provider.Provider{ID: "c", Name: "C", Key: "kc", Keys: []provider.KeyAccount{{Key: "kd"}}, Models: []string{"cls"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("c", up.URL+"/v1", []catalog.Model{{ID: "cls", Context: 32000}}); err != nil {
		t.Fatal(err)
	}
	// the group's members' keys, and only kd of c's
	held, secrets := newCaller(t, "Held")
	if _, err := access.Update("accounts-key", access.Change{Key: held[0].ID, Accounts: []string{"a/" + provider.KeyID("ka"), "b/" + provider.KeyID("kb"), "c/" + provider.KeyID("kd")}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat("add a unit test for the parser", nil, 0, "")))
	req.Header.Set("Authorization", "Bearer "+secrets[0])
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := keys("cls"); len(got) != 1 || got[0] != "kd" {
		t.Fatalf("the classifier was asked with %q, want kd, the one account of c the key may use", got)
	}
}

// Bound by user, a routing group's classifier that is a decision model
// (Jev, System One) is asked on the decision provider's keys the turn's
// user is on, as the user's own /v1/systemone would be (askDecider), never
// on its first key for every user. Before, askDecider asked every key in
// the provider's order whoever the turn was of.
func TestDecisionClassifierAskedOfTheUsersKeys(t *testing.T) {
	s, _, _, _ := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
	u := newRoutesUp(t)
	if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}},
		Decide: u.URL + "/v1", Models: []string{"jev-latest"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Classifier = "jev/jev-latest"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	s = byUserUpTo(t, s, 1)
	var alice, bob []string
	for _, round := range []string{"first", "second"} {
		for _, user := range []string{"alice", "bob"} {
			before := len(u.asked("/v1/systemone"))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat("add the "+round+" unit test for "+user+"'s parser", nil, 0, "")))
			req.Header.Set("x-session-id", user+"-"+round)
			req.Header.Set(UserHeader, user)
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("%s, %s turn: %d %s", user, round, rec.Code, rec.Body.String())
			}
			got := u.asked("/v1/systemone")
			if len(got) <= before {
				t.Fatalf("%s, %s turn: Jev wasn't asked", user, round)
			}
			if user == "alice" {
				alice = append(alice, got[before:]...)
			} else {
				bob = append(bob, got[before:]...)
			}
		}
	}
	oneKeyEach(t, "the decision classifier", alice, bob)
	if b := boundIn(t, "jev"); len(b) != 2 {
		t.Fatalf("bound %v, want alice and bob on the decision provider", b)
	}
}

// A gateway key held to some accounts asks a decision classifier on those
// alone, as its own /v1/systemone would be: before, askDecider asked the
// provider's first key, which the key may not use.
func TestDecisionClassifierAskedOnTheKeysAccounts(t *testing.T) {
	s, _, _, _ := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
	u := newRoutesUp(t)
	if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}},
		Decide: u.URL + "/v1", Models: []string{"jev-latest"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Classifier = "jev/jev-latest"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	held, secrets := newCaller(t, "Held")
	if _, err := access.Update("accounts-key", access.Change{Key: held[0].ID, Accounts: []string{"a/" + provider.KeyID("ka"), "b/" + provider.KeyID("kb"), "jev/" + provider.KeyID("kB")}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat("add a unit test for the parser", nil, 0, "")))
	req.Header.Set("Authorization", "Bearer "+secrets[0])
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := u.asked("/v1/systemone"); len(got) == 0 || slices.Contains(got, "kA") {
		t.Fatalf("Jev was asked with %q, want kB alone, the one key of jev the key may use", got)
	}
}
