package gateway

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// authRefused is Claude Code's answer for an account Anthropic answered
// authentication_failed, as the server's ledger had it on every banned
// account ("Login expired · Please run /login").
const authRefused = `    echo '{"type":"assistant","message":{"id":"x","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Login expired · Please run /login"}]},"error":"authentication_failed"}'
    echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":401,"result":"Login expired · Please run /login"}'
`

// holdClockAt holds the hold's clock at now, moved by the returned func.
func holdClockAt(t *testing.T, now time.Time) func(time.Duration) {
	t.Helper()
	was := authHoldClock
	authHoldClock = func() time.Time { return now }
	t.Cleanup(func() { authHoldClock = was })
	return func(d time.Duration) { now = now.Add(d) }
}

// holdHours saves the hold's length, nil for the default.
func holdHours(t *testing.T, h *int) {
	t.Helper()
	s := settings.Load()
	s.AuthHoldHours = h
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// claudeAsk is a Messages request of the claude subscription, its caller
// named by headers h.
func claudeAsk(s *Server, h map[string]string) *httptest.ResponseRecorder {
	return postAt(s, "/v1/messages", `{"model":"claude/claude-sonnet-5","max_tokens":50,"messages":[{"role":"user","content":"ping"}]}`, h)
}

// runsOf is the Claude Code runs log's accounts, comma-joined.
func runsOf(t *testing.T, log string) string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return strings.Join(strings.Fields(string(b)), ",")
}

// heldAnswer fails t unless rec is the hold's 429 in Anthropic's shape,
// resetting at until (on this computer's clock) with Retry-After the
// seconds to it from now.
func heldAnswer(t *testing.T, rec *httptest.ResponseRecorder, until, now time.Time) {
	t.Helper()
	var body struct {
		Error struct{ Type, Message string }
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	want := strconv.Itoa(int(until.Sub(now).Seconds()))
	if rec.Code != http.StatusTooManyRequests || body.Error.Type != "rate_limit_error" || rec.Header().Get("Retry-After") != want ||
		!strings.Contains(body.Error.Message, until.Local().Format("2006-01-02 15:04")) || strings.Contains(strings.ToLower(body.Error.Message), "login") {
		t.Fatalf("not the hold's 429 resetting at %v (Retry-After %s): %d %v %s", until, want, rec.Code, rec.Header(), rec.Body.String())
	}
}

// A user whose request Anthropic answers authentication_failed is held
// for the default length: that request itself is told the hold's 429 with
// its reset, no other account asked; every request of theirs after is
// told the same, nothing run, until the hold ends; another user isn't
// held. The row keeps what the vendor answered, and says the client was
// told 429 held. The account rests until the user acts.
func TestAuthFailedHoldsTheUser(t *testing.T) {
	log := claudeSubscriptionFirstSays(t, authRefused)
	now := time.Now().Truncate(time.Second)
	move := holdClockAt(t, now)
	until := now.Add(time.Duration(settings.DefaultAuthHoldHours) * time.Hour)
	s := New()
	bob := map[string]string{UserHeader: "bob"}

	heldAnswer(t, claudeAsk(s, bob), until, now)
	if runs := runsOf(t, log); runs != "own" {
		t.Fatalf("Claude runs %q: the spare was asked", runs)
	}
	row := lastUsage(t)
	if row.ErrType != claudeAuthFailed || row.Status == http.StatusTooManyRequests || row.ProviderAccount != "me@example.com" ||
		row.User != "bob" || !strings.HasPrefix(row.Told, "429 held until ") {
		t.Fatalf("the row: status %d, err_type %q, account %q, user %q, told %q", row.Status, row.ErrType, row.ProviderAccount, row.User, row.Told)
	}
	// the account rests: no request of anyone's goes to it
	for _, l := range provider.Logins("claude") {
		if l.User == "me@example.com" && !l.AuthFailed {
			t.Fatalf("the account doesn't rest: %+v", l)
		}
	}

	// held: told the same reset, nothing run
	move(time.Hour)
	heldAnswer(t, claudeAsk(s, bob), until, now.Add(time.Hour))
	if runs := runsOf(t, log); runs != "own" {
		t.Fatalf("a held request ran Claude Code: %q", runs)
	}
	if r := lastUsage(t); !r.Rejected || r.ErrType != heldErrType {
		t.Fatalf("the held request's row: %+v", r)
	}
	// another user isn't held, and is served by the spare
	if rec := claudeAsk(s, map[string]string{UserHeader: "alice"}); rec.Code != 200 {
		t.Fatalf("another user: %d %s", rec.Code, rec.Body.String())
	}
	if runs := runsOf(t, log); runs != "own,tok-spare" {
		t.Fatalf("runs %q", runs)
	}
	// the hold ends with its time
	move(time.Duration(settings.DefaultAuthHoldHours) * time.Hour)
	if rec := claudeAsk(s, bob); rec.Code != 200 {
		t.Fatalf("after the hold: %d %s", rec.Code, rec.Body.String())
	}
}

// The hold is kept on disk: a gateway started again holds the caller
// still. Release lets it go.
func TestAuthHoldSurvivesARestartAndReleases(t *testing.T) {
	claudeSubscriptionFirstSays(t, authRefused)
	now := time.Now().Truncate(time.Second)
	holdClockAt(t, now)
	bob := map[string]string{UserHeader: "bob"}
	claudeAsk(New(), bob)
	authHolds.Lock()
	authHolds.stamp, authHolds.holds = "", nil // as a new process
	authHolds.Unlock()
	until := now.Add(time.Duration(settings.DefaultAuthHoldHours) * time.Hour)
	heldAnswer(t, claudeAsk(New(), bob), until, now)
	hs, err := AuthHolds()
	if err != nil || len(hs) != 1 || hs[0].Kind != "user" || hs[0].Who != "bob" || hs[0].Account != "me@example.com" {
		t.Fatalf("holds %+v %v", hs, err)
	}
	if err := ReleaseAuthHold("user:bob"); err != nil {
		t.Fatal(err)
	}
	if rec := claudeAsk(New(), bob); rec.Code != 200 {
		t.Fatalf("released, still held: %d %s", rec.Code, rec.Body.String())
	}
	if err := ReleaseAuthHold("user:bob"); err == nil {
		t.Fatal("released twice")
	}
}

// Who is held: the user, else the calling key (no user), else the session
// (neither); a request of none is only walled off.
func TestAuthHoldHolders(t *testing.T) {
	for _, c := range []struct {
		name, kind string
		h          map[string]string
	}{
		{"session", "session", map[string]string{SessionHeader: "sess-1"}},
		{"none", "", map[string]string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			claudeSubscriptionFirstSays(t, authRefused)
			holdClockAt(t, time.Now())
			rec := claudeAsk(New(), c.h)
			hs, _ := AuthHolds()
			if c.kind == "" {
				if len(hs) != 0 || rec.Code == http.StatusTooManyRequests && rec.Header().Get(UserLimitHeader) == "held" {
					t.Fatalf("a request of nobody held: %+v %d", hs, rec.Code)
				}
				return
			}
			if len(hs) != 1 || hs[0].Kind != c.kind {
				t.Fatalf("holds %+v", hs)
			}
		})
	}
}

// A hold file that can't be read, with no last good generation, refuses
// every request 503 naming it: nobody held is let go unseen. With its last
// good generation beside it, that one is read.
func TestAuthHoldsUnreadable(t *testing.T) {
	claudeSubscriptionFirstSays(t, authRefused)
	now := time.Now().Truncate(time.Second)
	holdClockAt(t, now)
	os.MkdirAll(filepath.Dir(authHoldsPath()), 0o700)
	if err := os.WriteFile(authHoldsPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := claudeAsk(New(), map[string]string{UserHeader: "carol"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "auth-holds.json") {
		t.Fatalf("unreadable holds: %d %s", rec.Code, rec.Body.String())
	}
	good, _ := json.Marshal(map[string]any{"holds": []AuthHold{{Kind: "user", Who: "carol", At: now, Until: now.Add(time.Hour)}}})
	os.WriteFile(authHoldsPath()+".bak", good, 0o600)
	authHolds.Lock()
	authHolds.stamp = ""
	authHolds.Unlock()
	heldAnswer(t, claudeAsk(New(), map[string]string{UserHeader: "carol"}), now.Add(time.Hour), now)
}

// With the hold at 0, nobody is held: the request is told as it was
// before (the walled account the last, a limit to come back to), and the
// next goes on.
func TestAuthHoldOff(t *testing.T) {
	log := claudeSubscriptionFirstSays(t, authRefused)
	holdClockAt(t, time.Now())
	zero := 0
	holdHours(t, &zero)
	s := New()
	bob := map[string]string{UserHeader: "bob"}
	rec := claudeAsk(s, bob)
	if rec.Header().Get(UserLimitHeader) == "held" || runsOf(t, log) != "own" {
		t.Fatalf("held with the hold off: %d %v (runs %q)", rec.Code, rec.Header(), runsOf(t, log))
	}
	if hs, _ := AuthHolds(); len(hs) != 0 {
		t.Fatalf("holds %+v", hs)
	}
	if rec := claudeAsk(s, bob); rec.Code != 200 {
		t.Fatalf("the next request: %d %s", rec.Code, rec.Body.String())
	}
}

// A stream already begun (kept alive while Claude Code waited) that
// Anthropic then answers authentication_failed tells the client the
// hold's limit in its stream, its 200 sent: a rate_limit_error event with
// the reset. The kind stays the row's.
func TestAuthFailedMidStreamToldTheHold(t *testing.T) {
	fresh(t)
	now := time.Now().Truncate(time.Second)
	holdClockAt(t, now)
	until := now.Add(time.Duration(settings.DefaultAuthHoldHours) * time.Hour)
	events := make(chan Event, 3)
	events <- Event{Kind: KAlive}
	events <- Event{Kind: KError, Text: "Login expired · Please run /login", Status: 401, Code: claudeAuthFailed}
	close(events)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set(UserHeader, "bob")
	var told string
	ctx := context.WithValue(req.Context(), streamHoldKey{}, func() (AuthHold, bool) {
		return AuthHold{Kind: "user", Who: "bob", At: now, Until: until}, true
	})
	r := req.WithContext(context.WithValue(ctx, toldHeldKey{}, &told))
	w := httptest.NewRecorder()
	var u Usage
	relay(w, r, provider.Anthropic, "Claude Code", &Request{Stream: true}, events, &u, func() {}, nil, func(string, string, bool) {})
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"rate_limit_error"`) || !strings.Contains(body, until.Local().Format("2006-01-02 15:04")) || strings.Contains(body, "Please run /login") {
		t.Fatalf("the stream: %d %s", w.Code, body)
	}
	if u.ErrType != claudeAuthFailed || !strings.HasPrefix(told, "429 held until ") {
		t.Fatalf("the row's kind %q, told %q", u.ErrType, told)
	}
}

// A request with no user, sent with a gateway key, holds that key: its
// next request is held, another key's isn't.
func TestAuthHoldHoldsTheKey(t *testing.T) {
	log := claudeSubscriptionFirstSays(t, authRefused)
	now := time.Now().Truncate(time.Second)
	holdClockAt(t, now)
	held, err := access.Update("add-key", access.Change{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := access.Update("add-key", access.Change{Name: "Phone"})
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(secret string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude/claude-sonnet-5","max_tokens":50,"messages":[{"role":"user","content":"ping"}]}`))
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Authorization", "Bearer "+secret)
		lanGuard(s.Handler()).ServeHTTP(rec, req)
		return rec
	}
	until := now.Add(time.Duration(settings.DefaultAuthHoldHours) * time.Hour)
	heldAnswer(t, ask(held), until, now)
	hs, _ := AuthHolds()
	if len(hs) != 1 || hs[0].Kind != "key" || hs[0].Name != "Laptop" {
		t.Fatalf("holds %+v", hs)
	}
	heldAnswer(t, ask(held), until, now)
	if rec := ask(other); rec.Code != 200 {
		t.Fatalf("another key held: %d %s", rec.Code, rec.Body.String())
	}
	if runs := runsOf(t, log); runs != "own,tok-spare" {
		t.Fatalf("runs %q", runs)
	}
}

// Held, a routing group's request is told the hold at once: its other
// provider isn't asked either, since the caller is what is held.
func TestAuthHoldStopsTheGroup(t *testing.T) {
	log := claudeSubscriptionFirstSays(t, authRefused)
	now := time.Now().Truncate(time.Second)
	holdClockAt(t, now)
	if err := provider.SetRouting("claude", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	other := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "b", provider.Chat, other)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"claude/claude-sonnet-5", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	rec := postAt(New(), "/v1/messages", `{"model":"group/g","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`, map[string]string{UserHeader: "bob"})
	heldAnswer(t, rec, now.Add(time.Duration(settings.DefaultAuthHoldHours)*time.Hour), now)
	if other.n != 0 || runsOf(t, log) != "own" {
		t.Fatalf("asked on: the other provider %d times, Claude runs %q", other.n, runsOf(t, log))
	}
}

// Two requests of one caller meeting authentication_failed together start
// one hold: both read the same reset.
func TestAuthHoldOneResetForTwoAtOnce(t *testing.T) {
	claudeSubscriptionFirstSays(t, authRefused)
	now := time.Now().Truncate(time.Second)
	move := holdClockAt(t, now)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set(UserHeader, "bob")
	first, ok := startAuthHold(req, "claude", "me@example.com", "claude")
	if !ok {
		t.Fatal("not held")
	}
	move(time.Minute) // the second, a moment on, as two at once read the file in turn
	// it read the holds before the first was written, as a request at the
	// same time does: only the edit, under its lock, sees the first
	readAuthHolds(time.Now())
	authHolds.Lock()
	authHolds.holds = nil
	authHolds.Unlock()
	second, _ := startAuthHold(req, "claude", "me@example.com", "claude")
	if !second.Until.Equal(first.Until) {
		t.Fatalf("two resets: %v and %v", first.Until, second.Until)
	}
	hs, _ := AuthHolds()
	if len(hs) != 1 {
		t.Fatalf("holds %+v", hs)
	}
}

// A hold file that doesn't read is read, and logged, once until it
// changes, not at every request.
func TestAuthHoldsUnreadableLoggedOnce(t *testing.T) {
	fresh(t)
	holdClockAt(t, time.Now())
	os.MkdirAll(filepath.Dir(authHoldsPath()), 0o700)
	os.WriteFile(authHoldsPath(), []byte("{not json"), 0o600)
	authHolds.Lock()
	authHolds.stamp = ""
	authHolds.Unlock()
	var logged strings.Builder
	was := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(was) })
	for range 5 {
		if _, err := readAuthHolds(time.Now()); err == nil {
			t.Fatal("an unreadable file read")
		}
	}
	if n := strings.Count(strings.TrimSpace(logged.String()), "\n") + 1; logged.Len() == 0 || n != 1 {
		t.Fatalf("logged %d times:\n%s", n, logged.String())
	}
}
