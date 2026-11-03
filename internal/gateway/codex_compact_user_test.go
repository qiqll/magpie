package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// compactUpstream is a fake ChatGPT backend that answers every compact,
// turn and search; asked is the chatgpt-account-id of each request to
// path's end since the last reset ("" for one relayed on the sign-in as it
// came).
func compactUpstream(t *testing.T) (asked func(path string) []string) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string][]string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		for _, p := range []string{"/responses/compact", "/responses", "/alpha/search"} {
			if strings.HasSuffix(r.URL.Path, p) {
				seen[p] = append(seen[p], r.Header.Get("chatgpt-account-id"))
				break
			}
		}
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/alpha/search") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"output":"searched"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","model":"gpt-5.5","usage":{"input_tokens":9,"output_tokens":2}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return func(path string) []string {
		mu.Lock()
		defer mu.Unlock()
		got := seen[path]
		delete(seen, path)
		return got
	}
}

// codexCompactFor sends Codex's legacy /responses/compact for user,
// carrying the seal enc when it isn't empty, on Codex's ChatGPT token,
// with the headers kv (name, value, ...).
func codexCompactFor(h http.Handler, user, enc string, kv ...string) *httptest.ResponseRecorder {
	input := `{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}`
	if enc != "" {
		input = `{"type":"compaction","encrypted_content":"` + enc + `"},` + input
	}
	r := httptest.NewRequest("POST", CodexPath+"/responses/compact", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[`+input+`]}`))
	r.Header.Set("Authorization", "Bearer chatgpt-token")
	r.Header.Set(UserHeader, user)
	for i := 0; i+1 < len(kv); i += 2 {
		r.Header.Set(kv[i], kv[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Bound by user, Codex's legacy compact of one of its own models goes to
// one of its user's own accounts, never another user's (T-085). With
// Codex's sign-in paused, compactOn took the first other account on
// whoever it was bound to, and signed the compact as it past serve:
// alice's compact went to bob's. A seal is read only when one of the
// user's accounts wrote it: one bob's account sealed is refused for alice
// with a 400 (T-086), naming no account, and nothing is sent. With the sign-in
// on, a compact relayed on it went there whoever it was bound to. One
// signing with its own chatgpt-account-id spends its own sign-in, and
// goes as it came. Bound by session it goes where it went.
func TestUserCompactOnTheUsersOwnAccount(t *testing.T) {
	bindTwo := func(t *testing.T, bob, alice string) {
		t.Helper()
		now := time.Now().UTC()
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"bob":   {Who: "codex@" + bob, Account: bob, At: now, Tier: "free"},
			"alice": {Who: "codex@" + alice, Account: alice, At: now, Tier: "free"},
		}}})
	}
	pauseSignIn := func(t *testing.T) {
		t.Helper()
		if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the sign-in paused: each on its own", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com", "third@example.com") // acct-2, acct-3
		asked := compactUpstream(t)
		pauseSignIn(t)
		bindTwo(t, "spare@example.com", "third@example.com")
		h := byUserUpTo(t, New(), 1).Handler()
		for _, c := range []struct{ user, acct string }{{"alice", "acct-3"}, {"bob", "acct-2"}, {"alice", "acct-3"}} {
			if rec := codexCompactFor(h, c.user, ""); rec.Code != 200 {
				t.Fatalf("%s: %d %s", c.user, rec.Code, rec.Body.String())
			}
			if got := asked("/responses/compact"); len(got) != 1 || got[0] != c.acct {
				t.Fatalf("%s's compact went to %q, want %s", c.user, got, c.acct)
			}
		}
	})
	t.Run("a seal another user's account wrote is refused", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com", "third@example.com")
		asked := compactUpstream(t)
		pauseSignIn(t)
		nativeOn(t, "codex")
		bindTwo(t, "spare@example.com", "third@example.com")
		wroteCheckpoint("gAAAAB-sealed-by-bobs", sealedBy("spare@example.com"))
		wroteCheckpoint("gAAAAB-sealed-by-alices", sealedBy("third@example.com"))
		h := byUserUpTo(t, New(), 1).Handler()
		rec := codexCompactFor(h, "alice", "gAAAAB-sealed-by-bobs")
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Retry-After") != "" || strings.Contains(rec.Body.String(), "@example.com") {
			t.Fatalf("alice on bob's seal: %d %s", rec.Code, rec.Body.String())
		}
		if got := asked("/responses/compact"); len(got) != 0 {
			t.Fatalf("alice on bob's seal asked %q", got)
		}
		if rec := codexCompactFor(h, "alice", "gAAAAB-sealed-by-alices"); rec.Code != 200 {
			t.Fatalf("alice on her own seal: %d %s", rec.Code, rec.Body.String())
		}
		if got := asked("/responses/compact"); len(got) != 1 || got[0] != "acct-3" {
			t.Fatalf("alice's own seal went to %q", got)
		}
		if rec := codexCompactFor(h, "bob", "gAAAAB-sealed-by-bobs"); rec.Code != 200 {
			t.Fatalf("bob on his own seal: %d %s", rec.Code, rec.Body.String())
		}
		if got := asked("/responses/compact"); len(got) != 1 || got[0] != "acct-2" {
			t.Fatalf("bob's own seal went to %q", got)
		}
	})
	t.Run("the sign-in on: relayed on it only for its own user", func(t *testing.T) {
		codexSignedIn(t, "third@example.com") // the sign-in me@example.com is acct-1
		asked := compactUpstream(t)
		bindTwo(t, "me@example.com", "third@example.com")
		h := byUserUpTo(t, New(), 1).Handler()
		if rec := codexCompactFor(h, "alice", ""); rec.Code != 200 {
			t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
		}
		if got := asked("/responses/compact"); len(got) != 1 || got[0] != "acct-2" {
			t.Fatalf("alice's compact went to %q, want her acct-2", got)
		}
		if rec := codexCompactFor(h, "bob", ""); rec.Code != 200 {
			t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
		}
		if got := asked("/responses/compact"); len(got) != 1 || got[0] != "" {
			t.Fatalf("bob's compact went to %q, want the sign-in as it came", got)
		}
	})
	t.Run("a pin to one of the user's own, encoded or by trace id", func(t *testing.T) {
		// a pin is matched as every pin bound by user is (pinsSeat): its
		// user decoded, or its trace id; matched as written only, the
		// pin was missed and the compact went to the user's first
		codexSignedIn(t, "spare@example.com", "third@example.com")
		asked := compactUpstream(t)
		pauseSignIn(t)
		now := time.Now().UTC()
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"alice": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "free",
				More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}},
		}}})
		h := byUserUpTo(t, New(), 2).Handler()
		if rec := codexCompactFor(h, "alice", ""); rec.Code != 200 {
			t.Fatalf("unpinned: %d %s", rec.Code, rec.Body.String())
		}
		if got := asked("/responses/compact"); len(got) != 1 || got[0] != "acct-2" {
			t.Fatalf("unpinned compact went to %q, want her first, acct-2", got)
		}
		for _, pin := range []string{"third%40example.com", "codex@third@example.com"} {
			if rec := codexCompactFor(h, "alice", "", AccountHeader, pin); rec.Code != 200 {
				t.Fatalf("pin %s: %d %s", pin, rec.Code, rec.Body.String())
			}
			if got := asked("/responses/compact"); len(got) != 1 || got[0] != "acct-3" {
				t.Fatalf("pin %s: compact went to %q, want acct-3", pin, got)
			}
		}
	})
	t.Run("its own sign-in goes as it came", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com", "third@example.com")
		asked := compactUpstream(t)
		pauseSignIn(t)
		bindTwo(t, "spare@example.com", "third@example.com")
		h := byUserUpTo(t, New(), 1).Handler()
		if rec := codexCompactFor(h, "alice", "", "chatgpt-account-id", "acct-9"); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if got := asked("/responses/compact"); len(got) != 1 || got[0] != "acct-9" {
			t.Fatalf("compact went to %q, want acct-9 as it came", got)
		}
	})
	t.Run("bound by session as before", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com", "third@example.com")
		asked := compactUpstream(t)
		pauseSignIn(t)
		h := New().Handler()
		if rec := codexCompactFor(h, "alice", ""); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if got := asked("/responses/compact"); len(got) != 1 || got[0] != "acct-2" {
			t.Fatalf("compact went to %q, want compactOn's acct-2", got)
		}
	})
}

// A seal another user's account wrote is refused with a 400 Codex takes
// as final, not a 429 it asks again (T-086): openai/codex maps a 400 to
// InvalidRequest, which has no retry_delay, while a 429 is a RetryLimit
// retried on its Retry-After, and the seal will never be the user's.
// Asked again, it is refused the same way: no Retry-After, no limit, no
// account named, and the user is told to start a new conversation.
func TestUserCompactOnAnothersSealIsFinal(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked := compactUpstream(t)
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	nativeOn(t, "codex")
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "free"},
		"alice": {Who: "codex@third@example.com", Account: "third@example.com", At: now, Tier: "free"},
	}}})
	wroteCheckpoint("gAAAAB-sealed-by-bobs", sealedBy("spare@example.com"))
	h := byUserUpTo(t, New(), 1).Handler()
	for i := range 2 {
		rec := codexCompactFor(h, "alice", "gAAAAB-sealed-by-bobs")
		body := rec.Body.String()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("try %d: %d %s, want a 400", i, rec.Code, body)
		}
		if ra, lim := rec.Header().Get("Retry-After"), rec.Header().Get(UserLimitHeader); ra != "" || lim != "" {
			t.Fatalf("try %d: Retry-After %q, %s %q: told to ask again", i, ra, UserLimitHeader, lim)
		}
		if strings.Contains(body, "@example.com") || strings.Contains(body, userLimitSaid) || !strings.Contains(body, "Start a new conversation") {
			t.Fatalf("try %d: told %s", i, body)
		}
	}
	if got := asked("/responses/compact"); len(got) != 0 {
		t.Fatalf("alice on bob's seal asked %q", got)
	}
	// user-accounts.json broken after a read, whose seal bob's is is
	// unknown now: a 503, never the final 400; her own, as last read, goes
	wroteCheckpoint("gAAAAB-sealed-by-alices", sealedBy("third@example.com"))
	if err := os.WriteFile(usersPath(), []byte(`{"users": `), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := codexCompactFor(h, "alice", "gAAAAB-sealed-by-bobs"); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "Start a new conversation") {
		t.Fatalf("bob's seal, users stale: %d %s, want a 503", rec.Code, rec.Body.String())
	}
	if got := asked("/responses/compact"); len(got) != 0 {
		t.Fatalf("users stale asked %q on bob's seal", got)
	}
	if rec := codexCompactFor(h, "alice", "gAAAAB-sealed-by-alices"); rec.Code != 200 {
		t.Fatalf("her own seal, users stale: %d %s", rec.Code, rec.Body.String())
	}
	asked("/responses/compact")
}

// Bound by user, a compact carrying a seal while user-accounts.json has
// never read is told neither a final 400 nor sent (T-086): whose seal it
// is is unknown, and the user's own may be.
func TestUserCompactOnASealUsersUnread(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked := compactUpstream(t)
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	nativeOn(t, "codex")
	wroteCheckpoint("gAAAAB-sealed-by-alices", sealedBy("third@example.com"))
	if err := os.WriteFile(usersPath(), []byte(`{"users": `), 0o600); err != nil {
		t.Fatal(err)
	}
	h := byUserUpTo(t, New(), 1).Handler()
	if rec := codexCompactFor(h, "alice", "gAAAAB-sealed-by-alices"); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "Start a new conversation") || strings.Contains(rec.Body.String(), "@example.com") {
		t.Fatalf("users unread: %d %s, want a 503", rec.Code, rec.Body.String())
	}
	if got := asked("/responses/compact"); len(got) != 0 {
		t.Fatalf("users unread asked %q", got)
	}
}

// Bound by user, a turn or a web search for one of Codex's own models
// that codexAccounts would relay as it came (the sign-in the one account
// on) is served on the user's own instead (T-085): relayed, it reached the
// sign-in whoever it was bound to, past the user's tier. With the sign-in
// bob's, and each account shared by one user, alice's is refused and
// nothing reaches the backend; bob's is served.
func TestUserCodexRelayOnTheUsersOwnAccount(t *testing.T) {
	codexSignedIn(t)
	asked := compactUpstream(t)
	writeTiers(t, `{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":1,"share":1}}}`)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "free"},
	}}})
	h := byUser(New()).Handler()
	turn := func(user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
		r.Header.Set("Authorization", "Bearer chatgpt-token")
		r.Header.Set(UserHeader, user)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if rec := turn("alice"); rec.Code == 200 {
		t.Fatalf("alice's turn on bob's sign-in: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked("/responses"); len(got) != 0 {
		t.Fatalf("alice's turn asked %q", got)
	}
	if rec := turn("bob"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked("/responses"); len(got) != 1 {
		t.Fatalf("bob's turn asked %q", got)
	}
	if rec := codexSearchFor(h, CodexPath+"/alpha/search", codexSearchBody("gpt-5.5"), "alice"); rec.Code != 200 || strings.Contains(rec.Body.String(), "@example.com") {
		t.Fatalf("alice's search: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked("/alpha/search"); len(got) != 0 {
		t.Fatalf("alice's search asked %q", got)
	}
	if rec := codexSearchFor(h, CodexPath+"/alpha/search", codexSearchBody("gpt-5.5"), "bob"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "searched") {
		t.Fatalf("bob's search: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked("/alpha/search"); len(got) != 1 {
		t.Fatalf("bob's search asked %q", got)
	}
}
