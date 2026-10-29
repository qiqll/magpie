package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
			seen[modelOf(b)] = append(seen[modelOf(b)], strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
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

// Bound by user (--bind=user), a web_search through the MCP server is
// asked of the searcher's accounts its caller's user is bound to, as a
// model request of that user would be, never drawn from every account of
// the provider; a call without its user is refused, and searches nothing.
func TestSearchMCPAskedOfTheUsersAccounts(t *testing.T) {
	searchSandbox(t)
	ds := newFakeDeepSeek(t)
	up, keys, users := keysSeen(t, ds.Config.Handler)
	searchesOn(t, provider.Responses, up.URL)
	save(t, provider.Provider{ID: "ds", Name: "DeepSeek", Key: "ka", Keys: []provider.KeyAccount{{Key: "kb"}},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", Models: []string{"deepseek-flash"}})
	st := settings.Load()
	st.Searcher = ""
	settings.Save(st)
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
