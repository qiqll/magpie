package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A user's 400 (its accounts don't serve the model) and 503 (paused, or
// the conversation sealed by another) name none of the accounts it is
// bound to, nor how many there are, the one the gateway key may not use
// included: which they are is the operator's, in the call's error. The
// names are a key's label and a plugin account's id, which hideAccounts
// leaves alone where magpie writes no name (#T-016).
func TestUserRefusalNamesNoAccount(t *testing.T) {
	namesAre(t, []string{"codex"}, provider.AccountRef{User: "work-seat"}, provider.AccountRef{User: "held-seat"})
	both := userBinding{Who: "codex@work-seat", Account: "work-seat", More: []userSeat{{Who: "codex@held-seat", Account: "held-seat"}}}
	held := Weighed{Provider: "codex", Kind: "account", Who: "held-seat", Model: "gpt-5.5", Held: true}
	barred := Weighed{Provider: "codex", Kind: "account", Who: "work-seat", Model: "gpt-5.5", Barred: true}
	for _, c := range []struct {
		name   string
		pl     planned
		status int
		says   string
	}{
		{"unserved", planned{left: []Weighed{held, barred}}, http.StatusBadRequest, "gpt-5.5"},
		{"paused", planned{}, http.StatusServiceUnavailable, "paused"},
	} {
		for _, from := range []provider.Protocol{provider.Chat, provider.Anthropic} {
			refused := boundRefusal("bob", both, "codex", nil, c.pl)
			rec := httptest.NewRecorder()
			call := Call{}
			New().userRefused(rec, from, &call, &UserBound{User: "bob", seats: both.seats()}, refused)
			body := rec.Body.String()
			if rec.Code != c.status || !strings.Contains(body, c.says) || !strings.Contains(body, "bob") {
				t.Fatalf("%s %s: %d %s", c.name, from, rec.Code, body)
			}
			if strings.Contains(body, "seat") || strings.Contains(body, "an account, an account") {
				t.Fatalf("%s %s: the caller told the user's accounts: %s", c.name, from, body)
			}
			if !strings.Contains(call.Error, "work-seat, held-seat") {
				t.Fatalf("%s %s: the call's error lost them: %q", c.name, from, call.Error)
			}
			// a route that isn't a turn says it as an error
			if err := (&userRefusedError{refused: refused}).Error(); strings.Contains(err, "seat") {
				t.Fatalf("%s: usersOn's error: %s", c.name, err)
			}
		}
	}
}

// Bound by user to the one Codex account, paused since: serve's own
// answer names it to no caller, the usage log does.
func TestUserPausedAccountUnnamed(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	s := byUser(New())
	userPost(s, "codex/gpt-5.5", "alice", "hi")
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var saved []map[string]any
	raw, _ := os.ReadFile(logins)
	json.Unmarshal(raw, &saved)
	saved = append(saved, map[string]any{"agent": "codex", "user": "me@example.com", "paused": true, "seen": time.Now()})
	os.WriteFile(logins, mustJSON(saved), 0o600)
	provider.ForgetAccounts()
	rec := userPost(s, "codex/gpt-5.5", "alice", "paused")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `\"alice\" is bound to no account that can take this request`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if why := refusedWhy(s); !strings.Contains(why, "me@example.com") {
		t.Fatalf("the call's error: %q", why)
	}
}
