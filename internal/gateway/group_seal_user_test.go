package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// codexGroupTurnFor sends a Codex /responses turn to the group g for
// user carrying the seal enc, on Codex's ChatGPT token.
func codexGroupTurnFor(h http.Handler, user, enc string) *httptest.ResponseRecorder {
	input := `{"type":"compaction","encrypted_content":"` + enc + `"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}`
	r := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"group/g","stream":true,"input":[`+input+`]}`))
	r.Header.Set("Authorization", "Bearer chatgpt-token")
	r.Header.Set(UserHeader, user)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Bound by user, a routing group's turn carrying a seal another user's
// account wrote is refused as a provider's turn is (T-101, T-096's
// sealedRefusal): a 400 Codex takes as final, with no Retry-After or
// limit, naming no account and telling the user to start a new
// conversation, where the group's narrowing told it groupTurnRefused's
// 503 "none of the accounts ... can take this request now". Her own seal
// goes on her own account. user-accounts.json stale, bob's seal is
// unknown: a 503, never the final 400.
func TestUserGroupTurnOnAnothersSealIsFinal(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked := compactUpstream(t)
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	nativeOn(t, "codex")
	refusalGroup(t, "codex/gpt-5.5")
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "free"},
		"alice": {Who: "codex@third@example.com", Account: "third@example.com", At: now, Tier: "free"},
	}}})
	wroteCheckpoint("gAAAAB-sealed-by-bobs", sealedBy("spare@example.com"))
	wroteCheckpoint("gAAAAB-sealed-by-alices", sealedBy("third@example.com"))
	h := byUserUpTo(t, New(), 1).Handler()
	for i := range 2 {
		rec := codexGroupTurnFor(h, "alice", "gAAAAB-sealed-by-bobs")
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
	if got := asked("/responses"); len(got) != 0 {
		t.Fatalf("alice's group turn on bob's seal asked %q", got)
	}
	if rec := codexGroupTurnFor(h, "alice", "gAAAAB-sealed-by-alices"); rec.Code != 200 {
		t.Fatalf("alice on her own seal: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked("/responses"); len(got) != 1 || got[0] != "acct-3" {
		t.Fatalf("alice's own seal went to %q", got)
	}
	if err := os.WriteFile(usersPath(), []byte(`{"users": `), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := codexGroupTurnFor(h, "alice", "gAAAAB-sealed-by-bobs"); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "Start a new conversation") || strings.Contains(rec.Body.String(), "@example.com") {
		t.Fatalf("bob's seal, users stale: %d %s, want a 503", rec.Code, rec.Body.String())
	}
	if got := asked("/responses"); len(got) != 0 {
		t.Fatalf("users stale asked %q on bob's seal", got)
	}
}

// Bound by user, a group's turn from a user bound to nothing yet,
// carrying a seal another user's account wrote, is refused before the
// group's narrowing binds it (T-101): checkpointReaders left only the
// seal's writer, which usersOn would bind carol to and serve. Nothing is
// sent and no binding is written.
func TestUserGroupTurnUnboundOnAnothersSeal(t *testing.T) {
	for _, tiers := range []string{"", `{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":1,"share":1}}}`} {
		codexSignedIn(t, "spare@example.com", "third@example.com")
		asked := compactUpstream(t)
		if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
			t.Fatal(err)
		}
		nativeOn(t, "codex")
		refusalGroup(t, "codex/gpt-5.5")
		if tiers != "" {
			writeTiers(t, tiers)
		}
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "free"},
		}}})
		wroteCheckpoint("gAAAAB-sealed-by-bobs", sealedBy("spare@example.com"))
		h := byUserUpTo(t, New(), 1).Handler()
		rec := codexGroupTurnFor(h, "carol", "gAAAAB-sealed-by-bobs")
		body := rec.Body.String()
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Retry-After") != "" || strings.Contains(body, "@example.com") || !strings.Contains(body, "Start a new conversation") {
			t.Fatalf("tiers %q: carol on bob's seal: %d %s", tiers, rec.Code, body)
		}
		if got := asked("/responses"); len(got) != 0 {
			t.Fatalf("tiers %q: carol on bob's seal asked %q", tiers, got)
		}
		if b, err := os.ReadFile(usersPath()); err != nil || strings.Contains(string(b), "carol") {
			t.Fatalf("tiers %q: carol bound on bob's seal: %s %v", tiers, b, err)
		}
	}
}

// Bound by user, a seal written by a provider with no key and no account
// (one on this machine) is no user's, as usersOn binds nobody there: a
// turn carrying it goes on, to that provider directly or through a group,
// where sealedRefusal refused it with the 400 for every user, and each
// conversation there broke at its first native compaction (T-101's
// review).
func TestUserTurnOnASealOfNoAccount(t *testing.T) {
	fresh(t)
	var n int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"m"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","model":"m","usage":{"input_tokens":9,"output_tokens":2}}}`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "loc", Name: "Loc", Models: []string{"m"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	nativeOn(t, "loc")
	refusalGroup(t, "loc/m")
	wroteCheckpoint("gAAAAB-sealed-by-loc", candidate{p: provider.Provider{ID: "loc"}, model: "m", rest: "loc"})
	s := byUserUpTo(t, New(), 1)
	for _, model := range []string{"loc/m", "group/g"} {
		body := []byte(`{"model":"` + model + `","stream":true,"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-loc"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
		was := n
		if rec := userAt(s, "/v1/responses", "", body, "alice"); rec.Code != 200 || n != was+1 {
			t.Fatalf("%s: %d %s (asked %d)", model, rec.Code, rec.Body.String(), n-was)
		}
	}
}
