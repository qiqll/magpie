package gateway

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
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
// and a translation — goes on the keys its user is
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
// try again, told as a limit, no vendor asked; its count is estimated. A user not bound yet while
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
	} {
		rec := userAt(s, c.path, "", []byte(c.body), "carol")
		if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), userLimitSaid) || rec.Header().Get(UserLimitHeader) == "" {
			t.Fatalf("%s for carol: %d %v %s", c.path, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	estimated(t, userAt(s, "/v1/messages/count_tokens", "", []byte(`{"model":"twin/m","messages":[{"role":"user","content":"count me"}]}`), "carol"))
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

// A user's drawing that names no tier is of the tier its binding keeps,
// as its turn is: bob, bound at pro to two accounts to be signed in
// again, is given a third (pro's 4), not refused at free's 2.
func TestUserDrawingKeepsItsTierWithoutTheHeader(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	drawn := codexDrawn(t)
	lapseSaved(t, "spare@example.com")
	lapseSaved(t, "third@example.com")
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "pro",
			More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}},
	}}})
	draw := []byte(`{"model":"codex/gpt-image-2","prompt":"a magpie"}`)
	if rec := userAt(byUser(New()), "/v1/images/generations", "", draw, "bob"); rec.Code != 200 {
		t.Fatalf("bound at pro: %d %s", rec.Code, rec.Body.String())
	}
	if got := drawn(); len(got) != 1 {
		t.Fatalf("drew on %v", got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com", "third@example.com", "me@example.com"}) {
		t.Fatalf("no spare beside the two: %v", got)
	}
	if got := tierIn(t, "codex")["bob"]; got != "pro" {
		t.Fatalf("tier kept %q", got)
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

// Bound by user, a count of tokens is counted on the keys its user is on
// already, never another user's; a user bound to none is estimated, and
// the count binds nobody (any caller may send one for any user, at no
// cost, so it takes no seat).
func TestUserCountOnTheUsersOwnKey(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	u := newRoutesUp(t)
	twoKeyed(t, u, "m", "embed-1")
	s := byUserUpTo(t, New(), 1)
	count := []byte(`{"model":"twin/m","messages":[{"role":"user","content":"count me"}]}`)
	estimated(t, userAt(s, "/v1/messages/count_tokens", "", count, "carol"))
	if got := u.asked("/v1/messages/count_tokens"); len(got) != 0 {
		t.Fatalf("carol's count asked %v", got)
	}
	if _, err := os.Stat(usersPath()); err == nil {
		if b := boundIn(t, "twin"); len(b) != 0 {
			t.Fatalf("a count bound %v", b)
		}
	}
	for _, user := range []string{"alice", "bob"} {
		if rec := userAt(s, "/v1/embeddings", "", []byte(`{"model":"twin/embed-1","input":"x"}`), user); rec.Code != 200 {
			t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
		}
	}
	eachOnItsOwn(t, u, "/v1/messages/count_tokens", func(user string) *httptest.ResponseRecorder {
		return userAt(s, "/v1/messages/count_tokens", "", count, user)
	})
	if b := boundIn(t, "twin"); len(b) != 2 {
		t.Fatalf("bound %v, want alice and bob alone", b)
	}
}

// keepIn keeps the plan's order index for index with the candidates it
// leaves, which ownedBy reads by index.
func TestKeepInKeepsTheOrderInStep(t *testing.T) {
	cs := []candidate{{p: provider.Provider{ID: "a"}}, {p: provider.Provider{ID: "b"}}, {p: provider.Provider{ID: "c"}}}
	pl := planned{order: []Weighed{{Provider: "a"}, {Provider: "b"}, {Provider: "c"}}}
	out, opl := keepIn(cs, pl, func(c candidate) bool { return c.p.ID != "a" })
	if len(out) != 2 || len(opl.order) != 2 {
		t.Fatalf("%v %v", out, opl.order)
	}
	for i := range out {
		if out[i].p.ID != opl.order[i].Provider {
			t.Fatalf("out of step at %d: %s beside %s's weight", i, out[i].p.ID, opl.order[i].Provider)
		}
	}
}

// Bound by user with a gateway key held to some keys (#905), a user's
// drawing and embedding go on its own of those alone: the key's holds
// first, then the user's binding, never the key it may not use nor
// another user's.
func TestUserRoutesWithAKeyHeldToAccounts(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	u := newRoutesUp(t)
	if err := provider.Save(provider.Provider{ID: "twin", Name: "Twin", Chat: u.URL + "/v1", Key: "kA",
		Keys: []provider.KeyAccount{{Key: "kB"}, {Key: "kC"}}, Models: []string{"gpt-image-1", "embed-1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"twin/" + provider.KeyID("kB"), "twin/" + provider.KeyID("kC")}}); err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 1)
	for _, c := range []struct{ path, body string }{
		{"/v1/embeddings", `{"model":"twin/embed-1","input":"x"}`},
		{"/v1/images/generations", `{"model":"twin/gpt-image-1","prompt":"a magpie"}`},
	} {
		eachOnItsOwn(t, u, c.path, func(user string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", c.path, strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer "+secrets[0])
			req.Header.Set(UserHeader, user)
			s.Handler().ServeHTTP(rec, req)
			return rec
		})
		if slices.Contains(u.asked(c.path), "kA") {
			t.Fatalf("%s asked with kA, which the key may not use: %v", c.path, u.asked(c.path))
		}
	}
}

// Bound by user, a video is asked after on the key it was started on, the
// user's own, the second of the provider's included (it used to be asked
// on the first, another user's), and only by its user: another user of
// the gateway key, with its id, is told there is no such video and no
// vendor is asked, and a request with no user is refused.
func TestUserVideoFetchedByItsUserAlone(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path+" "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/content"):
			w.Header().Set("Content-Type", "video/mp4")
			io.WriteString(w, "fake-mp4")
		case r.Method == "GET":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"vid-1","object":"video","status":"completed","model":"grok-imagine-video"}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"vid-1","object":"video","status":"queued","model":"grok-imagine-video"}`)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset,
		Chat: up.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}, Models: []string{"grok-imagine-video"}}); err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 1)
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		return asked[len(asked)-1]
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(asked)
	}
	get := func(path, user string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		if user != "" {
			req.Header.Set(UserHeader, user)
		}
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	start := func(user string) string {
		rec := userAt(s, "/v1/videos", "", []byte(`{"model":"office/grok-imagine-video","prompt":"a magpie"}`), user)
		var v struct{ ID string }
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.ID == "" {
			t.Fatalf("%s's start: %d %s", user, rec.Code, rec.Body.String())
		}
		return v.ID
	}
	start("alice") // on kA, the first
	if got := last(); !strings.HasSuffix(got, " kA") {
		t.Fatalf("alice started on %q", got)
	}
	id := start("bob")
	if got := last(); !strings.HasSuffix(got, " kB") {
		t.Fatalf("bob started on %q", got)
	}
	for _, path := range []string{"/v1/videos/" + id, "/v1/videos/" + id + "/content"} {
		if rec := get(path, "bob"); rec.Code != 200 {
			t.Fatalf("bob's %s: %d %s", path, rec.Code, rec.Body.String())
		}
		if got := last(); !strings.HasPrefix(got, "GET ") || !strings.HasSuffix(got, " kB") {
			t.Fatalf("bob's %s asked as %q, not on kB", path, got)
		}
	}
	before := count()
	for _, path := range []string{"/v1/videos/" + id, "/v1/videos/" + id + "/content"} {
		if rec := get(path, "alice"); rec.Code != 404 || strings.Contains(rec.Body.String(), "bob") {
			t.Fatalf("alice asked after bob's video %s: %d %s", path, rec.Code, rec.Body.String())
		}
		if rec := get(path, ""); rec.Code != 400 || !strings.Contains(rec.Body.String(), "--bind=user") {
			t.Fatalf("no user asked after %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	// its owner's mark taken off the id, it is no one's: still refused
	bare, _, _ := strings.Cut(id, videoOwnerMark)
	if rec := get("/v1/videos/"+bare, "bob"); rec.Code != 404 {
		t.Fatalf("the id without its owner: %d %s", rec.Code, rec.Body.String())
	}
	if n := count(); n != before {
		t.Fatalf("refused asks reached the vendor: %d more", n-before)
	}
}

// revokedDrawn is a Codex image backend that answers acct-1's drawing as
// a sign-in its vendor revoked (a 401 token_revoked), and keeps the
// account each drawing was asked on.
func revokedDrawn(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var drew []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		drew = append(drew, acct)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if acct == "acct-1" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"Encountered invalidated oauth token for user, failing request","code":"token_revoked"},"status":401}`)
			return
		}
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

// A user's drawing on its one account, found revoked by the vendor as it
// is asked, is answered as its turn would be: with room in its tier, a
// spare beside it draws, the user kept on both; at its tier's most, a 429
// with when to try again — never the vendor's words of the sign-in.
func TestUserDrawingOnARevokedAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	drawn := revokedDrawn(t)
	alice := func() {
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "free"},
		}}})
	}
	draw := []byte(`{"model":"codex/gpt-image-2","prompt":"a magpie"}`)

	alice()
	rec := userAt(byUserUpTo(t, New(), 1), "/v1/images/generations", "", draw, "alice")
	limitedNotLapsed(t, rec)
	if got := drawn(); !slices.Equal(got, []string{"acct-1"}) {
		t.Fatalf("at its tier's most, drew on %v", got)
	}

	alice()
	rec = userAt(byUserUpTo(t, New(), 2), "/v1/images/generations", "", draw, "alice")
	if rec.Code != 200 {
		t.Fatalf("with room for a spare: %d %s", rec.Code, rec.Body.String())
	}
	saysNoLapse(t, rec)
	if got := drawn(); !slices.Equal(got, []string{"acct-1", "acct-2"}) {
		t.Fatalf("drew on %v", got)
	}
	if got := seatsIn(t, "codex")["alice"]; !slices.Equal(got, []string{"me@example.com", "spare@example.com"}) {
		t.Fatalf("not kept on the revoked one with a spare beside it: %v", got)
	}
}

// A user bound to none while every account of the provider rests is told
// so with when to try again (Retry-After), its drawing asked of no one:
// never routed among the provider's, another's among them.
func TestUserDrawingWithEveryAccountResting(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	drawn := codexDrawn(t)
	restLong(t, "me@example.com")
	restLong(t, "spare@example.com")
	rec := userAt(byUserUpTo(t, New(), 1), "/v1/images/generations", "", []byte(`{"model":"codex/gpt-image-2","prompt":"a magpie"}`), "dave")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	saysNoLapse(t, rec)
	if got := drawn(); len(got) != 0 {
		t.Fatalf("drew on %v", got)
	}
}

// A user's System One decision on its one account, found revoked as it
// is asked, is answered as its turn would be: a spare beside it decides
// with room in its tier, else a 429 with when to try again, never the
// vendor's words of the sign-in. No decision provider has a built-in
// account to revoke today (deciders are keys or plugins), so the tries
// are given Codex's accounts with a decision endpoint, as usersOn would
// hand them over.
func TestUserSystemOneOnARevokedAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	revoked := codexAccessToken(t)
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gone := r.Header.Get("Authorization") == "Bearer "+revoked
		mu.Lock()
		asked = append(asked, strconv.FormatBool(gone))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if gone {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"Encountered invalidated oauth token for user, failing request","code":"token_revoked"},"status":401}`)
			return
		}
		io.WriteString(w, `{"answers":{"intent":{"choice":"bug"}},"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	took := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := asked
		asked = nil
		return out
	}
	t.Cleanup(noRests)
	decide := func(max int) *httptest.ResponseRecorder {
		t.Helper()
		noRests() // each case afresh: the one before rested the revoked one
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "free"},
		}}})
		s := byUserUpTo(t, New(), max)
		p, err := provider.Find("codex")
		if err != nil {
			t.Fatal(err)
		}
		var cs []candidate
		var pl planned
		for _, c := range seatCandidates(*p) {
			c.p.Decide, c.model = up.URL+"/v1", "jev-latest"
			cs, pl.order = append(cs, c), append(pl.order, Weighed{Provider: "codex"})
		}
		body := []byte(`{"model":"codex/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`)
		r := httptest.NewRequest("POST", "/v1/systemone", bytes.NewReader(body))
		r.Header.Set(UserHeader, "alice")
		own, opl, u, err := s.usersOn(r, *p, cs, pl)
		if err != nil || u == nil {
			t.Fatalf("not bound: %v", err)
		}
		rec := httptest.NewRecorder()
		s.serveDecideTries(rec, r, body, "codex/jev-latest", time.Now(), own, opl, nil, u)
		return rec
	}

	limitedNotLapsed(t, decide(1))
	if got := took(); !slices.Equal(got, []string{"true"}) {
		t.Fatalf("at its tier's most, asked %v (true: the revoked one)", got)
	}

	rec := decide(2)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "bug") {
		t.Fatalf("with room for a spare: %d %s", rec.Code, rec.Body.String())
	}
	if got := took(); !slices.Equal(got, []string{"true", "false"}) {
		t.Fatalf("asked %v, want the revoked one, then the spare", got)
	}
	if got := seatsIn(t, "codex")["alice"]; !slices.Equal(got, []string{"me@example.com", "spare@example.com"}) {
		t.Fatalf("not kept on the revoked one with a spare beside it: %v", got)
	}
}
