package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// logHasNoSecret fails the test when the log holds a code, a token or a
// sign-in's address.
func logHasNoSecret(t *testing.T, buf *syncBuffer, secrets ...string) {
	t.Helper()
	got := buf.String()
	for _, s := range append([]string{"code=", "access_token", "refresh_token", "id_token", "oauth/authorize", "http://localhost", "https://"}, secrets...) {
		if strings.Contains(got, s) {
			t.Fatalf("the log has %q:\n%s", s, got)
		}
	}
}

// linesOf counts the log's lines matching re.
func linesOf(buf *syncBuffer, re string) int {
	return len(regexp.MustCompile(`(?m)` + re).FindAllString(buf.String(), -1))
}

// A built-in sign-in that can't start logs its start and its failure, one
// line each, under the session's id.
func TestSignInThatCantStartIsLogged(t *testing.T) {
	signIn(t)
	buf := signInLog(t)
	if _, err := StartSignInWith("no-such", SignInOptions{}); err == nil {
		t.Fatal("started")
	}
	logSays(t, buf, "sign-in no-such (", ") started", `failed: "magpie can't sign in to no-such accounts"`)
	if n := linesOf(buf, `sign-in no-such \(\S+\) failed`); n != 1 {
		t.Fatalf("%d failure lines:\n%s", n, buf)
	}
}

// A Codex sign-in logs its start and its end: done with the account, or
// failed with the vendor's status and words, never the code traded.
func TestCodexSignInStartAndEndAreLogged(t *testing.T) {
	claudeHome(t)
	fakeCodexTokens(t, nil)
	buf := signInLog(t)

	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, callbackAddress(t, st, "secret-bad-code", "")); err == nil {
		t.Fatal("a code the vendor refused was taken")
	}
	logSays(t, buf, "sign-in codex ("+st.ID+") started", "sign-in codex ("+st.ID+`) failed HTTP 400: "the sign-in was refused (400): bad code"`)

	st, err = StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, callbackAddress(t, st, "cx", "")); err != nil {
		t.Fatal(err)
	}
	logSays(t, buf, "sign-in codex ("+st.ID+") done account cx@example.com")
	logHasNoSecret(t, buf, "secret-bad-code", "r-cx")
}

// The last line `claude auth login` printed is the window's error, but
// the log never has it as it was when it may hold the sign-in's code
// (signin.go logged it raw); and its failure is one line, not two.
func TestClaudeSignInFailureLoggedOnceWithoutItsCode(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	port := droppingPort(t)
	shellFakes(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude")
	testenv.Program(t, exe, `#!/bin/sh
[ "$1 $2" = "auth login" ] || exit 2
"$BROWSER" 'https://claude.com/cai/oauth/authorize?code=true&redirect_uri=http%3A%2F%2Flocalhost%3A`+port+`%2Fcallback&state=st'
printf 'Paste code here if prompted > '
read -r code
echo "Login failed: grant_type=authorization_code&code=sekrit-last"
exit 1
`)
	browser := filepath.Join(dir, "browser")
	testenv.Program(t, browser, "#!/bin/sh\nprintf %s \"$1\" > \"$"+openedURLEnv+"\"\n")
	oldExe, oldOpener := claudeExecutable, claudeURLOpener
	claudeExecutable = func() string { return exe }
	claudeURLOpener = func() string { return browser }
	t.Cleanup(func() { claudeExecutable, claudeURLOpener = oldExe, oldOpener })
	endClaudeSignIns(t)
	buf := signInLog(t)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	_ = SubmitSignInCallback(st.ID, "the-code#st")
	if st = waitDone(t, st.ID); st.State != "failed" || !strings.Contains(st.Error, "sekrit-last") {
		t.Fatalf("state %+v", st)
	}
	logSays(t, buf, "sign-in claude ("+st.ID+") failed: (left out", "auth login` ended (exit status 1)")
	claudeLogins.Wait()
	if n := linesOf(buf, `^.*(?:failed|auth login. ended).*$`); n != 1 {
		t.Fatalf("%d lines say it failed:\n%s", n, buf)
	}
	logHasNoSecret(t, buf, "sekrit-last", "the-code#st")
}

// A Copilot sign-in declined on GitHub is logged once, by finish, not
// a second time by the device flow itself.
func TestCopilotSignInFailureLoggedOnce(t *testing.T) {
	signIn(t)
	copilotFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":"access_denied"}`)
	}, func(w http.ResponseWriter, r *http.Request) {})
	buf := signInLog(t)
	st, err := StartSignInAt("copilot", "")
	if err != nil {
		t.Fatal(err)
	}
	if st = waitDone(t, st.ID); st.State != "failed" {
		t.Fatalf("state %+v", st)
	}
	logSays(t, buf, "sign-in copilot ("+st.ID+") started", "sign-in copilot ("+st.ID+`) failed: "the sign-in was declined on GitHub"`)
	if n := strings.Count(buf.String(), "declined"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf)
	}
	logHasNoSecret(t, buf, "ABCD-1234")
}

// A revoked sign-in is logged as one to sign in again once, when it is
// marked, and as back when a request is served on it.
func TestRevokedSignInLapseIsLogged(t *testing.T) {
	signIn(t)
	resetRevoked()
	t.Cleanup(resetRevoked)
	if err := addSideLogin(savedLogin{Agent: "zcode", User: "u@example.com", Auth: json.RawMessage(`{"token":"a"}`)}, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}
	buf := signInLog(t)
	a := &Account{Agent: "zcode", User: "u@example.com"}
	NoteRevokedSignIn(context.Background(), a, revokedReq(t, "a"))
	NoteRevokedSignIn(context.Background(), a, revokedReq(t, "a"))
	logSays(t, buf, "sign-in: zcode account u@example.com has to be signed in again (u@example.com's sign-in was revoked; sign in again) HTTP 401: its vendor refused a request")
	if n := strings.Count(buf.String(), "has to be signed in again"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf)
	}
	ServedSignIn(a)
	logSays(t, buf, "sign-in: zcode account u@example.com is signed in again: its vendor served it")
	logHasNoSecret(t, buf)
}

// A plugin account the vendor refused is logged when it is marked and
// when it is served again, once each.
func TestPluginLapseIsLogged(t *testing.T) {
	signIn(t)
	loginsMu.Lock()
	err := writeLogins([]savedLogin{{Agent: "plugin:acme", User: "p@example.com", Home: "k1"}})
	loginsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	buf := signInLog(t)
	text := func(user string) string { return user + "'s Acme sign-in has expired — sign in again" }
	notePluginLapseOf("plugin:acme", "k1", http.StatusUnauthorized, toldNow, text)
	notePluginLapseOf("plugin:acme", "k1", http.StatusUnauthorized, toldNow, text)
	logSays(t, buf, "sign-in: plugin:acme account p@example.com has to be signed in again (p@example.com's Acme sign-in has expired — sign in again) HTTP 401")
	notePluginLapseOf("plugin:acme", "k1", http.StatusOK, toldNow, text)
	notePluginLapseOf("plugin:acme", "k1", http.StatusOK, toldNow, text)
	logSays(t, buf, "sign-in: plugin:acme account p@example.com is signed in again: its vendor served it")
	if n, m := strings.Count(buf.String(), "has to be signed in again"), strings.Count(buf.String(), "is signed in again"); n != 1 || m != 1 {
		t.Fatalf("logged %d and %d times:\n%s", n, m, buf)
	}
}

// A refused refresh of a saved ChatGPT account is logged as a refresh
// failed, with its status, and the account as one to sign in again, once.
func TestRefusedRefreshIsLogged(t *testing.T) {
	signIn(t)
	refreshFailedSaid.Clear()
	loginsMu.Lock()
	err := writeLogins([]savedLogin{{Agent: "codex", User: "r@example.com", Auth: json.RawMessage(`{}`)}})
	loginsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	buf := signInLog(t)
	refused := &tokenRefused{status: 400, msg: "refresh_token=secret-refresh is invalid"}
	for range 2 {
		_ = savedRefreshFailed("codex", "r@example.com", errors.Join(refreshRefused("refused"), refused))
	}
	logSays(t, buf, "sign-in: codex account r@example.com refresh failed HTTP 400: (left out",
		"sign-in: codex account r@example.com has to be signed in again (r@example.com's sign-in has expired — add the account again to use it): its vendor refused the refresh")
	if n, m := strings.Count(buf.String(), "refresh failed"), strings.Count(buf.String(), "has to be signed in again"); n != 1 || m != 1 {
		t.Fatalf("logged %d and %d times:\n%s", n, m, buf)
	}
	logHasNoSecret(t, buf, "secret-refresh")
}

// A sign-in's reason that may hold a code, a token or an address is left
// out of the log, whoever gave it; one that names none is kept, quoted.
func TestSignInLogLeavesSecretsOut(t *testing.T) {
	buf := signInLog(t)
	for _, why := range []string{
		"token request: grant_type=authorization_code&code=sekrit1",
		`{"access_token": "sekrit2"}`,
		"open https://auth.example.com/authorize?state=sekrit3",
		"sekrit4sekrit4sekrit4#statestate",
	} {
		logSignIn(SignInState{ID: "s1", Agent: "codex"}, "failed", 0, why)
	}
	logSignIn(SignInState{ID: "s1", Agent: "codex"}, "failed", 403, "unsupported_country_region_territory")
	logSays(t, buf, `sign-in codex (s1) failed HTTP 403: "unsupported_country_region_territory"`)
	if n := strings.Count(buf.String(), "(left out"); n != 4 {
		t.Fatalf("%d left out:\n%s", n, buf)
	}
	logHasNoSecret(t, buf, "sekrit")
}

// A refresh failure that keeps failing the same way is logged once in
// refreshFailedQuiet, not once a request; another reason is logged.
func TestRefreshFailureLoggedOnceAWhile(t *testing.T) {
	refreshFailedSaid.Clear()
	buf := signInLog(t)
	for range 3 {
		logRefreshFailed("gemini", "g@example.com", errors.New("dial tcp: i/o timeout"))
	}
	logRefreshFailed("gemini", "g@example.com", errors.New("no token"))
	if n := strings.Count(buf.String(), "refresh failed"); n != 2 {
		t.Fatalf("logged %d times:\n%s", n, buf)
	}
	refreshFailedSaid.Store(strings.ToLower("gemini\x00g@example.com"), refreshFailure{"no token", time.Now().Add(-refreshFailedQuiet - time.Second)})
	logRefreshFailed("gemini", "g@example.com", errors.New("no token"))
	if n := strings.Count(buf.String(), "refresh failed"); n != 3 {
		t.Fatalf("not logged again after a while:\n%s", buf)
	}
}
