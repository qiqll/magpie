package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// chatFleet is vendors' chat completions keeping the key each request
// came with, each key answering as status says (200 when unset).
type chatFleet struct {
	mu     sync.Mutex
	asked  []string
	status map[string]int
}

func (f *chatFleet) set(key string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[key] = code
}

// took is the keys asked since it was last read, the oldest first.
func (f *chatFleet) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.asked
	f.asked = nil
	return out
}

func (f *chatFleet) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"no route `+r.URL.Path+`"}}`)
			return
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.asked = append(f.asked, key)
		code := f.status[key]
		f.mu.Unlock()
		if code != 0 && code != 200 {
			w.WriteHeader(code)
			io.WriteString(w, `{"error":{"message":"no capacity on this key","type":"server_error"}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// chatGroup saves providers ca (keys kc1, kc2) and cb (kd1, kd2) of f's,
// each serving m, and the group pair of ca/m then cb/m, in that order.
func chatGroup(t *testing.T, f *chatFleet) {
	t.Helper()
	a, b := f.serve(t), f.serve(t)
	for _, p := range []provider.Provider{
		{ID: "ca", Name: "CA", Chat: a.URL + "/v1", Key: "kc1", Keys: []provider.KeyAccount{{Key: "kc2"}}, Models: []string{"m"}},
		{ID: "cb", Name: "CB", Chat: b.URL + "/v1", Key: "kd1", Keys: []provider.KeyAccount{{Key: "kd2"}}, Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"ca/m", "cb/m"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
}

// groupChat asks the group pair for user, a conversation of its own.
func groupChat(s *Server, user string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"model": "group/pair", "messages": []map[string]string{{"role": "user", "content": "hi from " + user}}})
	return userAt(s, "/v1/chat/completions", "", body, user)
}

// Bound by user (--bind=user), a turn to a routing group goes on its
// user's own keys of each member's provider alone, as a turn to one
// provider does (bindUser), never another user's: alice and bob, each at
// most one account a provider, are each asked on a key of their own of
// the first member's, and failed over to the next member, on their own
// key of that one too. The next member's seats aren't taken while the
// first answers. Before, a group's turn went in the group's order
// whoever it was of: both on kc1.
func TestUserGroupTurnOnTheUsersOwnKeys(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	f := &chatFleet{status: map[string]int{}}
	chatGroup(t, f)
	s := byUserUpTo(t, New(), 1)
	ask := func(user string) []string {
		t.Helper()
		if rec := groupChat(s, user); rec.Code != 200 {
			t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
		}
		return f.took()
	}
	var alice, bob []string
	for i := 0; i < 2; i++ {
		alice, bob = append(alice, ask("alice")...), append(bob, ask("bob")...)
	}
	oneKeyEach(t, "the group's turn", alice, bob)
	if !strings.HasPrefix(alice[0], "kc") || !strings.HasPrefix(bob[0], "kc") {
		t.Fatalf("alice asked %q, bob %q: want the group's first member's keys", alice, bob)
	}
	if b := boundIn(t, "cb"); len(b) != 0 {
		t.Fatalf("bound on cb %v before any turn went to it", b)
	}
	// each one's key of the first member out of capacity: on to the next
	// member's, its user's own there too
	f.set("kc1", 503)
	f.set("kc2", 503)
	aliceNext, bobNext := ask("alice"), ask("bob")
	if len(aliceNext) != 2 || aliceNext[0] != alice[0] || len(bobNext) != 2 || bobNext[0] != bob[0] {
		t.Fatalf("failed over: alice asked %q, bob %q", aliceNext, bobNext)
	}
	if aliceNext[1] == bobNext[1] || !strings.HasPrefix(aliceNext[1], "kd") || !strings.HasPrefix(bobNext[1], "kd") {
		t.Fatalf("failed over: alice asked %q, bob %q: want each on a key of cb's of its own", aliceNext, bobNext)
	}
	if b := boundIn(t, "cb"); len(b) != 2 {
		t.Fatalf("bound on cb %v, want alice and bob", b)
	}
}

// Bound by user, a group's turn whose next member's provider refuses its
// user (each of its keys another's alone) is told so, as a turn to that
// provider would be, once the first member failed: never sent on the key
// that is another user's. Before, alice's turn went on to bob's kd.
func TestUserGroupTurnNeverOnAnothersKey(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	f := &chatFleet{status: map[string]int{}}
	a, b := f.serve(t), f.serve(t)
	for _, p := range []provider.Provider{
		{ID: "ca", Name: "CA", Chat: a.URL + "/v1", Key: "kc", Models: []string{"m"}},
		{ID: "cb", Name: "CB", Chat: b.URL + "/v1", Key: "kd", Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"ca/m", "cb/m"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	// one account each, an account one user's alone
	writeTiers(t, `{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":1,"share":1}}}`)
	s := byUser(New())
	// bob's turn to cb binds him its one key
	body, _ := json.Marshal(map[string]any{"model": "cb/m", "messages": []map[string]string{{"role": "user", "content": "bob's"}}})
	if rec := userAt(s, "/v1/chat/completions", "", body, "bob"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.took(); len(got) != 1 || got[0] != "kd" {
		t.Fatalf("bob asked %q", got)
	}
	// alice's first member answers: cb is never narrowed for her
	if rec := groupChat(s, "alice"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.took(); len(got) != 1 || got[0] != "kc" {
		t.Fatalf("alice asked %q", got)
	}
	// kc out of capacity: cb's one key is bob's alone, so alice is told
	// a limit, as a turn of hers to cb would be, and kd isn't asked
	f.set("kc", 503)
	rec := groupChat(s, "alice")
	if got := f.took(); len(got) != 1 || got[0] != "kc" {
		t.Fatalf("alice asked %q: want kc alone, never bob's kd", got)
	}
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), userLimitSaid) || strings.Contains(rec.Body.String(), "kd") {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if b := boundIn(t, "cb"); len(b) != 1 || b["bob"] == "" {
		t.Fatalf("bound on cb %v, want bob alone", b)
	}
}

// Bound by user, a drawing on a routing group goes on its user's own keys
// of the member's provider it reaches (drawOnAccounts), as a drawing on
// that provider does: alice and bob each on a key of their own, the next
// member's seats untaken while the first draws. Before, a group's member
// drew on its provider's first key for every user.
func TestUserDrawingGroupOnTheUsersOwnKeys(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	u := newRoutesUp(t)
	for _, p := range []provider.Provider{
		{ID: "da", Name: "DA", Chat: u.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}, Models: []string{"gpt-image-1"}},
		{ID: "db", Name: "DB", Chat: u.URL + "/v1", Key: "kC", Keys: []provider.KeyAccount{{Key: "kD"}}, Models: []string{"gpt-image-1"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "pics", Name: "pics", Members: []string{"da/gpt-image-1", "db/gpt-image-1"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 1)
	eachOnItsOwn(t, u, "/v1/images/generations", func(user string) *httptest.ResponseRecorder {
		return userAt(s, "/v1/images/generations", "", []byte(`{"model":"group/pics","prompt":"a magpie"}`), user)
	})
	if b := boundIn(t, "da"); len(b) != 2 {
		t.Fatalf("bound on da %v, want alice and bob", b)
	}
	if b := boundIn(t, "db"); len(b) != 0 {
		t.Fatalf("bound on db %v before any drawing went to it", b)
	}
}

// Bound by user, a video started on a routing group goes on its user's
// own key of the member's provider (onKeys), as one started on that
// provider does, and only its user may ask after it. Before, a group's
// member started every user's on its first key.
func TestUserVideoGroupOnTheUsersOwnKeys(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}})
	if err := provider.SaveGroup(provider.Group{ID: "reels", Name: "reels", Members: []string{"studio/vid-1"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 1)
	started := func() []string {
		up.mu.Lock()
		defer up.mu.Unlock()
		var out []string
		for _, a := range up.asked {
			if strings.HasPrefix(a, "POST /v1/videos ") {
				out = append(out, strings.TrimPrefix(a, "POST /v1/videos "))
			}
		}
		return out
	}
	ids := map[string]string{}
	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, user := range []string{"alice", "bob"} {
			before := len(started())
			rec := userAt(s, "/v1/videos", "", []byte(`{"model":"group/reels","prompt":"a cat on a fence","seconds":4}`), user)
			if rec.Code != 200 {
				t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
			}
			var obj map[string]any
			json.Unmarshal(rec.Body.Bytes(), &obj)
			ids[user], _ = obj["id"].(string)
			got := started()
			if len(got) != before+1 {
				t.Fatalf("%s: started %v", user, got[before:])
			}
			if user == "alice" {
				alice = append(alice, got[before])
			} else {
				bob = append(bob, got[before])
			}
		}
	}
	oneKeyEach(t, "the group's video", alice, bob)
	// bob can't ask after alice's video
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/videos/"+ids["alice"], bytes.NewReader(nil))
	req.Header.Set(UserHeader, "bob")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("bob asked after alice's video: %d %s", rec.Code, rec.Body.String())
	}
}

// Bound by user, an embedding or a rerank on a routing group (audio goes
// the same way, serveRetrieval) goes on its user's own keys of the
// member's provider it reaches, as one on that provider does (onKeys):
// alice and bob each on a key of their own, the next member's seats
// untaken while the first answers. Before, a group's went over every
// member's keys in order: both on kA.
func TestUserRetrievalGroupOnTheUsersOwnKeys(t *testing.T) {
	for _, c := range []struct{ path, body string }{
		{"/v1/embeddings", `{"model":"group/near","input":"a magpie"}`},
		{"/v1/rerank", `{"model":"group/near","query":"birds","documents":["a magpie"]}`},
	} {
		t.Run(c.path, func(t *testing.T) {
			fresh(t)
			noRests()
			t.Cleanup(noRests)
			u := newRoutesUp(t)
			for _, p := range []provider.Provider{
				{ID: "ea", Name: "EA", Chat: u.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}, Models: []string{"embed-1"}},
				{ID: "eb", Name: "EB", Chat: u.URL + "/v1", Key: "kC", Keys: []provider.KeyAccount{{Key: "kD"}}, Models: []string{"embed-1"}},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{ID: "near", Name: "near", Members: []string{"ea/embed-1", "eb/embed-1"}, Routing: "order"}); err != nil {
				t.Fatal(err)
			}
			s := byUserUpTo(t, New(), 1)
			eachOnItsOwn(t, u, c.path, func(user string) *httptest.ResponseRecorder {
				return userAt(s, c.path, "", []byte(c.body), user)
			})
			if b := boundIn(t, "ea"); len(b) != 2 {
				t.Fatalf("bound on ea %v, want alice and bob", b)
			}
			if b := boundIn(t, "eb"); len(b) != 0 {
				t.Fatalf("bound on eb %v before any request went to it", b)
			}
		})
	}
}
