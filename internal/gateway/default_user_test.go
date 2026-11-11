package gateway

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// Bound by user, a request that names no user is DefaultUser's (T-157):
// answered, bound to an account of its own as any user is, held to its
// tier's (one account here), and the same user as one that names
// DefaultUser itself. A user's request is bound as before, on an account
// apart. Bound by session, a request without a user binds nobody.
func TestNoUserIsDefaultUser(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, heads := usersUpstream(t, nil)
	s := byUserUpTo(t, New(), 1)
	var on string
	for i, u := range []string{"", "", DefaultUser} {
		rec := userPost(s, "codex/gpt-5.5", u, "turn "+string(rune('a'+i)))
		got := asked(true)
		if rec.Code != 200 || len(got) != 1 {
			t.Fatalf("%q, turn %d: %d asked %v: %s", u, i, rec.Code, got, rec.Body.String())
		}
		if on == "" {
			on = got[0]
		}
		if got[0] != on {
			t.Fatalf("%q, turn %d: on %s, %s's account is %s", u, i, got[0], DefaultUser, on)
		}
		r := lastRoute(s)
		if r.User == nil || r.User.User != DefaultUser {
			t.Fatalf("%q, turn %d: route %+v", u, i, r.User)
		}
		if want := map[bool]string{true: "bound", false: "kept"}[i == 0]; r.User.Why != want {
			t.Fatalf("%q, turn %d: %s, want %s", u, i, r.User.Why, want)
		}
	}
	if rec := userPost(s, "codex/gpt-5.5", "alice", "alice's"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] == on {
		t.Fatalf("alice asked %v, %s is on %s", got, DefaultUser, on)
	}
	if b := boundIn(t, "codex"); len(b) != 2 || b[DefaultUser] == "" || b["alice"] == "" || b[DefaultUser] == b["alice"] {
		t.Fatalf("on disk: %v", b)
	}
	for _, h := range *heads {
		if h.Get(UserHeader) != "" {
			t.Fatalf("%s went upstream: %v", UserHeader, h)
		}
	}

	// bound by session: nothing is bound, DefaultUser or any
	if err := os.Remove(usersPath()); err != nil {
		t.Fatal(err)
	}
	s = New()
	if rec := userPost(s, "codex/gpt-5.5", "", "by session"); rec.Code != 200 {
		t.Fatalf("by session: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.User != nil {
		t.Fatalf("by session: bound %+v", r.User)
	}
	if _, err := os.Stat(usersPath()); !os.IsNotExist(err) {
		t.Fatalf("by session, user-accounts.json written: %v", err)
	}
}

// Bound by user, a request without a user that Anthropic answers
// authentication_failed holds its caller (its session here, as it would
// be bound by session), and the next one without a user is told the
// hold's 429, nothing run: authHeld, which reads the request before
// noUser names DefaultUser, finds the hold startAuthHold wrote (T-157).
func TestNoUserAuthHoldHoldsItsCaller(t *testing.T) {
	log := claudeSubscriptionFirstSays(t, authRefused)
	now := time.Now().Truncate(time.Second)
	holdClockAt(t, now)
	until := now.Add(time.Duration(settings.DefaultAuthHoldHours) * time.Hour)
	s := byUser(New())
	sess := map[string]string{SessionHeader: "sess-1"}
	heldAnswer(t, claudeAsk(s, sess), until, now)
	if hs, _ := AuthHolds(); len(hs) != 1 || hs[0].Kind != "session" || hs[0].Who != "sess-1" {
		t.Fatalf("holds %+v", hs)
	}
	heldAnswer(t, claudeAsk(s, sess), until, now)
	if runs := runsOf(t, log); runs != "own" {
		t.Fatalf("a held request without a user ran Claude Code: %q", runs)
	}
}

// Bound by user, a video generate_video starts for a call without a user
// is DefaultUser's, and generation_result asked without a user finds it,
// its content downloaded the same way (T-157).
func TestImageMCPVideoWithoutAUserAskedAfter(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	s := byUser(New())
	h := lanGuard(s.Handler())
	was, wasPoll := imageMCPWithin, imageMCPPoll
	t.Cleanup(func() { imageMCPWithin, imageMCPPoll = was, wasPoll })
	imageMCPWithin, imageMCPPoll = 0, time.Millisecond
	m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateVideoTool, map[string]any{"prompt": "a kite sways", "seconds": 6})))
	text := m.Result.Content[0].Text
	i := strings.Index(text, `"video_`)
	if m.Result.IsError || i < 0 {
		t.Fatalf("still making: %+v", m)
	}
	id := strings.Trim(strings.Fields(text[i:])[0], `".,`)
	imageMCPWithin = time.Minute
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerationResultTool, map[string]any{"id": id})))
	urls := urlsIn(m.Result.Content[0].Text)
	if m.Result.IsError || len(urls) != 1 {
		t.Fatalf("asked after without a user: %+v", m)
	}
	if w := imageGet(h, "127.0.0.1:5000", urls[0]); w.Code != 200 || w.Body.String() != "fake-mp4-bytes" {
		t.Fatalf("content: %d %s", w.Code, w.Body.String())
	}
	// another user asking after it is told it isn't one being made
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerationResultTool, map[string]any{"id": id}), UserHeader, "alice"))
	if !m.Result.IsError {
		t.Fatalf("alice asked after %s's video: %+v", DefaultUser, m)
	}
}
