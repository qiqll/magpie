package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// Every kind of mark that has an account signed in again is read as its
// row reads it, and only for the sign-in it was made on: the gateway
// leaves such an account out while another can answer (a Claude account
// Anthropic refused was still asked on every request).
func TestSignInLapsedReadsEveryMark(t *testing.T) {
	signIn(t)
	resetRevoked()
	t.Cleanup(resetRevoked)
	write := func(t *testing.T, ls ...savedLogin) {
		t.Helper()
		resetRevoked()
		loginsMu.Lock()
		err := writeLogins(ls)
		loginsMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	tok := func(context.Context) (string, error) { return "tok", nil }
	refreshRefused := "old@example.com's sign-in has expired — add the account again to use it"

	// a saved ChatGPT account whose refresh was refused
	write(t, savedLogin{Agent: "codex", User: "old@example.com", Auth: json.RawMessage(`{}`), Lapsed: refreshRefused})
	side := &Account{Agent: "codex", User: "old@example.com", token: tok}
	if got := SignInLapsed(side); got != refreshRefused {
		t.Errorf("a refused refresh: %q", got)
	}
	// Codex's own sign-in is Codex's to refresh: an old lapse of its copy
	// says nothing of it, as its row doesn't
	if got := SignInLapsed(&Account{Agent: "codex", User: "old@example.com"}); got != "" {
		t.Errorf("Codex's own sign-in held for its copy's lapse: %q", got)
	}
	write(t, savedLogin{Agent: "codex", User: "old@example.com", Auth: json.RawMessage(`{}`)})
	if got := SignInLapsed(side); got != "" {
		t.Errorf("signed in again, still held: %q", got)
	}

	// a sign-in its vendor revoked, Codex's own too
	own := &Account{Agent: "codex", User: "me@example.com"}
	write(t, savedLogin{Agent: "codex", User: "me@example.com", Auth: json.RawMessage(`{}`)})
	NoteRevokedSignIn(context.Background(), own, revokedReq(t, "a"))
	if got := SignInLapsed(own); got != revokedText("me@example.com") {
		t.Errorf("a revoked sign-in: %q", got)
	}
	ServedSignIn(own)
	if got := SignInLapsed(own); got != "" {
		t.Errorf("served since, still held: %q", got)
	}

	// a ChatGPT API sign-in whose refresh OpenAI refused
	write(t, savedLogin{Agent: ChatGPTAPIID, User: "api@example.com", Auth: json.RawMessage(`{}`), Lapsed: "api@example.com's ChatGPT sign-in has expired — sign in again"})
	if got := SignInLapsed(&Account{Agent: ChatGPTAPIID, User: "api@example.com"}); got == "" {
		t.Error("a ChatGPT API sign-in refused isn't held")
	}

	// a plugin's account its vendor answered 401, by its key
	pp := plugin.Provider{ID: "zed"}
	write(t,
		savedLogin{Agent: pluginAgent(pp), User: "z", Home: "k1", Lapsed: "z's Zed sign-in has expired — sign in again"},
		savedLogin{Agent: pluginAgent(pp), User: "y", Home: "k2"})
	if got := SignInLapsed(&Account{Agent: "plugin", User: "z", plugin: &pp, pluginKey: "k1"}); got == "" {
		t.Error("a plugin's account refused isn't held")
	}
	if got := SignInLapsed(&Account{Agent: "plugin", User: "y", plugin: &pp, pluginKey: "k2"}); got != "" {
		t.Errorf("another account of the plugin held: %q", got)
	}

	// a Copilot account, for the token GitHub refused only
	write(t, savedLogin{Agent: "copilot", User: "gh", Auth: json.RawMessage(`{"oauth_token":"t1"}`), Lapsed: copilotLapsedText, Refused: copilotTokenMark("t1")})
	if got := SignInLapsed(copilotProvider(copilotApp{User: "gh", Token: "t1"}, "").Account); got != copilotLapsedText {
		t.Errorf("a refused Copilot token: %q", got)
	}
	if got := SignInLapsed(copilotProvider(copilotApp{User: "gh", Token: "t2"}, "").Account); got != "" {
		t.Errorf("signed in anew with another token, still held: %q", got)
	}

	for _, a := range []*Account{nil, {Agent: "codex"}, {Agent: "claude"}} {
		if got := SignInLapsed(a); got != "" {
			t.Errorf("%+v: %q", a, got)
		}
	}
}

// A Claude account Anthropic refused is held, the one Claude Code itself
// is signed in to and a saved one alike, for the credential refused only.
func TestSignInLapsedClaude(t *testing.T) {
	p, side := claudeAuthFixture(t, "")
	var sa *Account
	for _, q := range p.AlsoOn() {
		if q.Account.User == side.User {
			sa = q.Account
		}
	}
	if sa == nil {
		t.Fatal("the saved account isn't on")
	}
	if got := SignInLapsed(sa); got != "" {
		t.Fatalf("held before anything was refused: %q", got)
	}
	NoteClaudeSignInFailure(side.User, ClaudeLoginVersion(side.User), "Failed to authenticate: OAuth session expired and could not be refreshed")
	if got := SignInLapsed(sa); got != claudeAuthLapse {
		t.Fatalf("a refused saved account: %q", got)
	}
	if got := SignInLapsed(p.Account); got != "" {
		t.Fatalf("Claude Code's own held for another's refusal: %q", got)
	}
	// signed in again in magpie: another credential, not the one refused
	loginsMu.Lock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == "claude" && ls[i].User == side.User {
			ls[i].Auth = mustJSONRaw(t, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "new-access", "refreshToken": "new-refresh", "subscriptionType": "max"}})
		}
	}
	err := writeLogins(ls)
	loginsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := SignInLapsed(sa); got != "" {
		t.Fatalf("signed in again, still held: %q", got)
	}

	// Claude Code's own sign-in, refused for the credential it holds
	c, _, ok := claudeCredential()
	if !ok {
		t.Fatal("no own credential")
	}
	if who := refuseClaudeVersion(p.Account.User, c.version()); who == "" {
		t.Fatal("the own account isn't saved")
	}
	if got := SignInLapsed(p.Account); got != claudeAuthLapse {
		t.Fatalf("Claude Code's own refused: %q", got)
	}
}

// A revoked sign-in signed in again in magpie, or renewed by the agent
// itself, is back: routing no longer sends it a request for one it serves
// to clear the mark, so the mark kept in memory is read against the saved
// login's, which the sign-in anew cleared. A plugin account with no key of
// its own is none of the plugin's to match.
func TestSignInLapsedClearedBySigningInAnAgain(t *testing.T) {
	signIn(t)
	resetRevoked()
	t.Cleanup(resetRevoked)
	if err := addSideLogin(savedLogin{Agent: "zcode", User: "u@example.com", Auth: json.RawMessage(`{"token":"a"}`)}, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}
	a := &Account{Agent: "zcode", User: "u@example.com"}
	NoteRevokedSignIn(context.Background(), a, revokedReq(t, "a"))
	if got := SignInLapsed(a); got == "" {
		t.Fatal("a revoked sign-in isn't held")
	}
	// signed in again in magpie: another credential, and logins.json's
	// mark gone, though nothing was served since
	if err := addSideLogin(savedLogin{Agent: "zcode", User: "u@example.com", Auth: json.RawMessage(`{"token":"c"}`)}, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}
	if got := SignInLapsed(a); got != "" {
		t.Fatalf("signed in again, still held: %q", got)
	}

	pp := plugin.Provider{ID: "zed"}
	if got := SignInLapsed(&Account{Agent: "plugin", User: "z", plugin: &pp}); got != "" {
		t.Fatalf("a plugin account with no key held: %q", got)
	}
}

// A saved ChatGPT account whose refresh was refused is left out of
// routing, and tried again by the keepalive loop each time it runs: the
// refusal doesn't count as a renewal, so it stays due, and a refresh that
// goes through brings it back.
func TestSavedCodexLapseTriedAgainByKeepalive(t *testing.T) {
	home := signIn(t)
	resetRevoked()
	t.Cleanup(resetRevoked)
	codexSignIn(t, home, "old@example.com", "r-old")
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	refuse := true
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "refresh_token": "r-old-2"})
	}))
	defer fake.Close()
	old := codexTokenURL
	codexTokenURL = fake.URL
	t.Cleanup(func() { codexTokenURL = old })
	a := &Account{Agent: "codex", User: "old@example.com", token: func(context.Context) (string, error) { return "", nil }}
	if _, _, err := renewSavedLogin(context.Background(), "codex", "old@example.com", true); err == nil || SignInLapsed(a) == "" {
		t.Fatalf("a refused refresh isn't held: %v", err)
	}
	for range 2 {
		RenewLogins(context.Background(), keepAliveEvery)
		if SignInLapsed(a) == "" {
			t.Fatal("let back while still refused")
		}
	}
	refuse = false
	RenewLogins(context.Background(), keepAliveEvery)
	if got := SignInLapsed(a); got != "" {
		t.Fatalf("renewed by the keepalive loop, still held: %q", got)
	}
}

// A revoked saved ChatGPT account that a refresh mends (the keepalive
// loop's, now that routing doesn't ask it) is back, as its row says.
func TestRevokedCodexBackOnceRefreshed(t *testing.T) {
	home := signIn(t)
	resetRevoked()
	t.Cleanup(resetRevoked)
	codexSignIn(t, home, "old@example.com", "r-old")
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	refuse := true
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "refresh_token": "r-old-2"})
	}))
	defer fake.Close()
	old := codexTokenURL
	codexTokenURL = fake.URL
	t.Cleanup(func() { codexTokenURL = old })
	a := &Account{Agent: "codex", User: "old@example.com", token: func(context.Context) (string, error) { return "", nil }}
	NoteRevokedSignIn(context.Background(), a, revokedReq(t, "stale"))
	if SignInLapsed(a) == "" {
		t.Fatal("not held once revoked")
	}
	refuse = false
	RenewLogins(context.Background(), keepAliveEvery)
	if got := SignInLapsed(a); got != "" {
		t.Fatalf("refreshed, still held: %q", got)
	}
}

// A plugin's account its vendor refused is back once a reply on it goes
// through (a usage read's, routing no longer asking it): the mark goes.
// A Copilot account is read under its name with its host: a same-named
// one saved before #1220 on another host is another account.
func TestSignInLapsedPluginServedAndCopilotHost(t *testing.T) {
	signIn(t)
	resetRevoked()
	t.Cleanup(resetRevoked)
	pp := plugin.Provider{ID: "zed"}
	loginsMu.Lock()
	err := writeLogins([]savedLogin{
		{Agent: pluginAgent(pp), User: "z", Home: "k1", Lapsed: "z's Zed sign-in has expired — sign in again"},
		{Agent: "copilot", User: "gh", Auth: json.RawMessage(`{"oauth_token":"t1","host":"acme.ghe.com"}`), Lapsed: copilotLapsedText, Refused: copilotTokenMark("t1")},
	})
	loginsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a := &Account{Agent: "plugin", User: "z", plugin: &pp, pluginKey: "k1"}
	if SignInLapsed(a) == "" {
		t.Fatal("not held")
	}
	notePluginLapseOf(pluginAgent(pp), "k1", http.StatusOK, toldNow, nil)
	if got := SignInLapsed(a); got != "" {
		t.Fatalf("served, still held: %q", got)
	}
	if got := SignInLapsed(copilotProvider(copilotApp{User: "gh", Token: "t1"}, "").Account); got != "" {
		t.Fatalf("github.com's gh held for acme.ghe.com's: %q", got)
	}
	if got := SignInLapsed(copilotProvider(copilotApp{User: "gh@acme.ghe.com", Token: "t1"}, "").Account); got != copilotLapsedText {
		t.Fatalf("acme.ghe.com's gh: %q", got)
	}
}
