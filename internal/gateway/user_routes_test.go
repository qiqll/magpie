package gateway

import (
	"bytes"
	"cmp"
	"encoding/base64"
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

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// routesUp is a vendor with every API a request that isn't a turn asks
// of it, keeping the key each request came with, by path.
type routesUp struct {
	*httptest.Server
	mu   sync.Mutex
	keys map[string][]string
}

func newRoutesUp(t *testing.T) *routesUp {
	t.Helper()
	u := &routesUp{keys: map[string][]string{}}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		key := cmp.Or(r.Header.Get("x-api-key"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !strings.HasSuffix(r.URL.Path, "/models") {
			u.mu.Lock()
			u.keys[r.URL.Path] = append(u.keys[r.URL.Path], key)
			u.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/images/generations", "/v1/images/edits":
			io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+b64+`"}]}`)
		case "/v1/chat/completions":
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Here it is.","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+b64+`"}}]}}],"usage":{"prompt_tokens":3,"completion_tokens":50}}`)
		case "/v1/embeddings":
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case "/v1/rerank":
			io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.9}],"usage":{"total_tokens":3}}`)
		case "/v1/audio/transcriptions", "/v1/audio/translations":
			io.WriteString(w, `{"text":"Hello from the magpie."}`)
		case "/v1/systemone":
			io.WriteString(w, `{"answers":{"intent":{"choice":"bug"}},"usage":{"input_tokens":10,"output_tokens":2}}`)
		case "/v1/messages/count_tokens":
			io.WriteString(w, `{"input_tokens":5}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"no route `+r.URL.Path+`"}}`)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// asked is the keys path was asked with, the oldest first.
func (u *routesUp) asked(path string) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.keys[path])
}

// all is every key any path was asked with.
func (u *routesUp) all() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for _, ks := range u.keys {
		out = append(out, ks...)
	}
	return out
}

// twoKeyed is a provider of u's with two keys, kA first, its models ms.
func twoKeyed(t *testing.T, u *routesUp, ms ...string) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "twin", Name: "Twin", Chat: u.URL + "/v1", Anthropic: u.URL,
		Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}, Models: ms}); err != nil {
		t.Fatal(err)
	}
}

// eachOnItsOwn asks ask for alice, then bob, twice over, and says each
// was asked on one key of its own, never the other's: what path of the
// vendor saw.
func eachOnItsOwn(t *testing.T, u *routesUp, path string, ask func(user string) *httptest.ResponseRecorder) {
	t.Helper()
	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, user := range []string{"alice", "bob"} {
			before := len(u.asked(path))
			if rec := ask(user); rec.Code != 200 {
				t.Fatalf("%s for %s: %d %s", path, user, rec.Code, rec.Body.String())
			}
			got := u.asked(path)
			if len(got) != before+1 {
				t.Fatalf("%s for %s: asked %v", path, user, got[before:])
			}
			if user == "alice" {
				alice = append(alice, got[before])
			} else {
				bob = append(bob, got[before])
			}
		}
	}
	oneKeyEach(t, path, alice, bob)
}

// userAt posts body to path as user, with Content-Type ctype ("" for
// JSON).
func userAt(s *Server, path, ctype string, body []byte, user string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", cmp.Or(ctype, "application/json"))
	req.Header.Set(UserHeader, user)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Bound by user (--bind=user), each route that asks a model and isn't a
// turn — a drawing and an edit, an embedding, a rerank, a transcription
// and a translation, a count of tokens — goes on the keys its user is
// bound to, as the user's turns do (usersOn), never on another user's:
// alice and bob, each at most one account, are each asked on a key of
// their own. Before, each went on the provider's first key, whoever's.
func TestUserRoutesOnTheUsersOwnKeys(t *testing.T) {
	audio, audioType := audioFormBody(t, "model", "twin/whisper-1")
	edit := `{"model":"twin/gpt-image-1","prompt":"bluer","images":[{"image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngBytes) + `"}]}`
	for _, c := range []struct {
		route, path, ctype string
		body               []byte
	}{
		{"/v1/images/generations", "/v1/images/generations", "", []byte(`{"model":"twin/gpt-image-1","prompt":"a magpie"}`)},
		{"/v1/images/edits", "/v1/images/edits", "", []byte(edit)},
		{"/v1/embeddings", "/v1/embeddings", "", []byte(`{"model":"twin/embed-1","input":"a magpie"}`)},
		{"/v1/rerank", "/v1/rerank", "", []byte(`{"model":"twin/rerank-1","query":"birds","documents":["a magpie"]}`)},
		{"/v1/audio/transcriptions", "/v1/audio/transcriptions", audioType, audio},
		{"/v1/audio/translations", "/v1/audio/translations", audioType, audio},
		{"/v1/messages/count_tokens", "/v1/messages/count_tokens", "", []byte(`{"model":"twin/m","messages":[{"role":"user","content":"count me"}]}`)},
	} {
		t.Run(c.route, func(t *testing.T) {
			fresh(t)
			noRests()
			t.Cleanup(noRests)
			u := newRoutesUp(t)
			twoKeyed(t, u, "m", "gpt-image-1", "embed-1", "rerank-1", "whisper-1")
			s := byUserUpTo(t, New(), 1)
			eachOnItsOwn(t, u, c.path, func(user string) *httptest.ResponseRecorder {
				return userAt(s, c.route, c.ctype, c.body, user)
			})
			if b := boundIn(t, "twin"); len(b) != 2 {
				t.Fatalf("bound %v, want alice and bob", b)
			}
		})
	}
}

// Bound by user, System One asks its decision model on its user's keys
// alone, never another user's.
func TestUserSystemOneOnTheUsersOwnKey(t *testing.T) {
	fresh(t)
	u := newRoutesUp(t)
	if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}},
		Decide: u.URL + "/v1", Models: []string{"jev-latest"}}); err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 1)
	eachOnItsOwn(t, u, "/v1/systemone", func(user string) *httptest.ResponseRecorder {
		return userAt(s, "/v1/systemone", "", []byte(`{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`), user)
	})
}

// Bound by user, generate_image of magpie's image MCP server draws on
// the keys of the call's user, as /v1/images does.
func TestUserImageMCPDrawsOnTheUsersOwnKey(t *testing.T) {
	fresh(t)
	u := newRoutesUp(t)
	twoKeyed(t, u, "text", "gpt-image-1")
	if err := catalog.SaveLive("twin", u.URL+"/v1", []catalog.Model{{ID: "text"}}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(byUserUpTo(t, New(), 1).Handler())
	eachOnItsOwn(t, u, "/v1/images/generations", func(user string) *httptest.ResponseRecorder {
		rec := imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie"}), UserHeader, user)
		if m := readImageReply(t, rec); m.Result.IsError {
			t.Fatalf("%s: %+v", user, m)
		}
		return rec
	})
}

// videoKeys is the keys u was asked to start a video with.
func videoKeys(u *openAIVideos) []string {
	var out []string
	for _, a := range u.log() {
		if k, ok := strings.CutPrefix(a, "POST /v1/videos "); ok {
			out = append(out, k)
		}
	}
	return out
}

// eachVideoOnItsOwn is eachOnItsOwn for the videos u starts.
func eachVideoOnItsOwn(t *testing.T, u *openAIVideos, start func(user string)) {
	t.Helper()
	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, user := range []string{"alice", "bob"} {
			start(user)
			got := videoKeys(u)
			if user == "alice" {
				alice = append(alice, got[len(got)-1])
			} else {
				bob = append(bob, got[len(got)-1])
			}
		}
	}
	oneKeyEach(t, "videos", alice, bob)
}

// Bound by user, a video, from /v1/videos or from generate_video of the
// image MCP server, is started on its user's keys alone.
func TestUserVideosOnTheUsersOwnKey(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}})
	s := byUserUpTo(t, New(), 1)
	eachVideoOnItsOwn(t, up, func(user string) {
		if rec := userAt(s, "/v1/videos", "", []byte(`{"model":"studio/vid-1","prompt":"a kite"}`), user); rec.Code != 200 {
			t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
		}
	})

	fresh(t)
	up = newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}})
	s = byUserUpTo(t, New(), 1)
	was, wasPoll := imageMCPWithin, imageMCPPoll
	t.Cleanup(func() { imageMCPWithin, imageMCPPoll = was, wasPoll })
	imageMCPWithin, imageMCPPoll = time.Minute, time.Millisecond
	h := lanGuard(s.Handler())
	eachVideoOnItsOwn(t, up, func(user string) {
		if m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateVideoTool, map[string]any{"prompt": "a kite"}), UserHeader, user)); m.Result.IsError {
			t.Fatalf("%s: %+v", user, m)
		}
	})
}

// Bound by user, a video at another magpie, which is sent on one key and
// not gone over, is sent on its user's key, not the provider's first.
func TestUserVideoAtAnotherMagpieOnTheUsersOwnKey(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var made []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		made = append(made, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"vid-1"}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset,
		Chat: up.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}, Models: []string{"grok-imagine-video"}}); err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 1)
	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, user := range []string{"alice", "bob"} {
			if rec := userAt(s, "/v1/videos", "", []byte(`{"model":"office/grok-imagine-video","prompt":"a magpie"}`), user); rec.Code != 200 {
				t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
			}
			mu.Lock()
			last := made[len(made)-1]
			mu.Unlock()
			if user == "alice" {
				alice = append(alice, last)
			} else {
				bob = append(bob, last)
			}
		}
	}
	oneKeyEach(t, "another magpie's videos", alice, bob)
}

// A user none of whose keys is free — each another's alone (share 1) —
// is refused on these routes as its turn would be: a 429 with when to
// try again, told as a limit, no vendor asked. A user not bound yet while
// user-accounts.json can't be read is a 503, nothing asked, as for a turn.
func TestUserRoutesRefusedAsTheirTurns(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	u := newRoutesUp(t)
	twoKeyed(t, u, "m", "gpt-image-1", "embed-1")
	writeTiers(t, `{"default":"solo","tiers":{"solo":{"credits":1000,"max_accounts":1,"share":1}}}`)
	s := byUser(New())
	for _, user := range []string{"alice", "bob"} {
		if rec := userAt(s, "/v1/embeddings", "", []byte(`{"model":"twin/embed-1","input":"x"}`), user); rec.Code != 200 {
			t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
		}
	}
	before := len(u.all())
	for _, c := range []struct{ path, body string }{
		{"/v1/embeddings", `{"model":"twin/embed-1","input":"x"}`},
		{"/v1/images/generations", `{"model":"twin/gpt-image-1","prompt":"a magpie"}`},
		{"/v1/messages/count_tokens", `{"model":"twin/m","messages":[{"role":"user","content":"count me"}]}`},
	} {
		rec := userAt(s, c.path, "", []byte(c.body), "carol")
		if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), userLimitSaid) || rec.Header().Get(UserLimitHeader) == "" {
			t.Fatalf("%s for carol: %d %v %s", c.path, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	if got := u.all(); len(got) != before {
		t.Fatalf("carol's refused requests asked %v", got[before:])
	}

	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	if err := os.WriteFile(usersPath(), []byte(`{"users": {"twin": {"alice": `), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/embeddings", "/v1/images/generations"} {
		body := `{"model":"twin/embed-1","input":"x"}`
		if path == "/v1/images/generations" {
			body = `{"model":"twin/gpt-image-1","prompt":"a magpie"}`
		}
		if rec := userAt(s, path, "", []byte(body), "dave"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "user-accounts.json") {
			t.Fatalf("%s, the file unread: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if got := u.all(); len(got) != before {
		t.Fatalf("asked with the file unread: %v", got[before:])
	}
}

// codexDrawn is a Codex image backend that keeps the account each drawing
// was asked on.
func codexDrawn(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var drew []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		drew = append(drew, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"eA=="}]}`)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-image-2"}}); err != nil {
		t.Fatal(err)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := drew
		drew = nil
		return out
	}
}

// A user's drawing, its one account to be signed in again, keeps it
// bound: with room in its tier it is given a spare beside it and drawn
// on that (the lapsed one never asked); at its tier's most it is a 429
// with when to try again that says nothing of a sign-in, nobody asked —
// as its turn would be.
func TestUserDrawingWithItsAccountLapsed(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	drawn := codexDrawn(t)
	lapseSaved(t, "spare@example.com")
	bob := func() {
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "free"},
		}}})
	}
	draw := []byte(`{"model":"codex/gpt-image-2","prompt":"a magpie"}`)

	bob()
	rec := userAt(byUserUpTo(t, New(), 1), "/v1/images/generations", "", draw, "bob")
	limitedNotLapsed(t, rec)
	if got := drawn(); len(got) != 0 {
		t.Fatalf("at its tier's most, drew on %v", got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com"}) {
		t.Fatalf("binding changed: %v", got)
	}

	bob()
	if rec := userAt(byUserUpTo(t, New(), 2), "/v1/images/generations", "", draw, "bob"); rec.Code != 200 {
		t.Fatalf("with room for a spare: %d %s", rec.Code, rec.Body.String())
	}
	if got := drawn(); !slices.Equal(got, []string{"acct-1"}) {
		t.Fatalf("drew on %v, not the spare alone", got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com", "me@example.com"}) {
		t.Fatalf("not kept on the lapsed one with a spare beside it: %v", got)
	}
}

// Bound by session, the routes go on the provider's keys as they always
// did, whatever user a request names, and nothing is bound.
func TestSessionRoutesIgnoreTheUser(t *testing.T) {
	fresh(t)
	u := newRoutesUp(t)
	twoKeyed(t, u, "embed-1")
	s := New()
	for _, user := range []string{"alice", "bob"} {
		if rec := userAt(s, "/v1/embeddings", "", []byte(`{"model":"twin/embed-1","input":"x"}`), user); rec.Code != 200 {
			t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
		}
	}
	if got := u.asked("/v1/embeddings"); !slices.Equal(got, []string{"kA", "kA"}) {
		t.Fatalf("asked %v", got)
	}
	if _, err := os.Stat(usersPath()); err == nil {
		t.Fatal("bound by session, a user was bound")
	}
}
