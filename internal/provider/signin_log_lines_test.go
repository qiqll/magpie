package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	return len(regexp.MustCompile(`(?m)`+re).FindAllString(buf.String(), -1))
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

// A reason holding a token of any vendor's shape is left out of the log;
// a vendor's words that only name one are kept (security review of
// d1ac61ce).
func TestLogReasonLeavesTokensOut(t *testing.T) {
	for _, leak := range []string{
		"invalid token eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjMifQ.c2lnbmF0dXJlc2ln",
		"Authorization: Bearer ya29.a0AfH6SMBxyz",
		`{"error":"invalid_grant","code": "4/0AX4XfWh-abcdefghijklmnopqrstuv"}`,
		"callback state=Zk3x9q&code_challenge=abc",
		"invalid api key sk-ant-api03-AbCdEfGhIjKlMnOp",
		"bad credentials gho_16C7e42F292c6912E7710c838347Ae178B4a",
		`{"device_code":"3584d83530557fdd1f46af8289938c8ef79f9dc5"}`,
		"api_key: AIzaSyD-abcdefghijklmnop",
		`{"key":"sk-abc123def456ghi789"}`,
		"password=hunter2",
	} {
		if got := logReason(leak); !strings.HasPrefix(got, "(left out") {
			t.Errorf("%q logged as %q", leak, got)
		}
	}
	for _, kept := range []string{
		`{"error":{"code":"invalid_api_key","message":"Incorrect API key provided"}}`,
		"invalid_grant: Token has been expired or revoked.",
		"OAuth token revoked",
		"the code expired; start again",
		"exit status 1",
		`401 Unauthorized: {"error":"unauthorized","error_description":"Bad credentials"}`,
	} {
		if got := logReason(kept); strings.HasPrefix(got, "(left out") {
			t.Errorf("%q left out", kept)
		}
	}
}

// An account's name can't start a line of its own in the log.
func TestLogNameIsOneLine(t *testing.T) {
	buf := signInLog(t)
	logSignIn(SignInState{ID: "s1", Agent: "codex", User: "a\n2026/10/30 12:00:00 sign-in codex (x) done account victim"}, "started", 0, "")
	logLapsed("codex", "b\nforged", "why", 0, "by")
	if n := strings.Count(strings.TrimSpace(buf.String()), "\n"); n != 1 {
		t.Fatalf("%d line breaks:\n%s", n, buf)
	}
	if got := logName("me@example.com"); got != "me@example.com" {
		t.Fatalf("a plain name quoted: %s", got)
	}
}

// A plugin's key sign-in that failed with an error repeating the key, or
// an answer given with it, logs neither.
func TestKeyFailureLeavesWhatWasEnteredOut(t *testing.T) {
	inputs := map[string]string{"region": "us", "secretKey": "wJalrXUtnFEMI/K7MDENG"}
	for _, err := range []error{
		errors.New("bad key abc"),
		errors.New("bad secret_key wJalrXUtnFEMI/K7MDENG"),
	} {
		if got := keyFailure(err, "abc", inputs); strings.Contains(got, "abc") || strings.Contains(got, "wJalr") {
			t.Errorf("%v logged as %q", err, got)
		}
	}
	if got := keyFailure(errors.New("not available in region us"), "abc", inputs); got != "not available in region us" {
		t.Errorf("an answer of two letters left out: %q", got)
	}
}

// heldLog is a log whose writer blocks on a canceled sign-in's line until
// let go, as a journal's full pipe does.
type heldLog struct {
	syncBuffer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *heldLog) Write(b []byte) (int, error) {
	if strings.Contains(string(b), ") canceled") {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return h.syncBuffer.Write(b)
}

// A sign-in replaced by a newer one of the same agent is logged canceled
// once signIns is let go: while the log's writer is blocked on that line,
// a status poll, which takes signIns, still answers (review of d1ac61ce,
// M1).
func TestReplacedSignInIsLoggedCanceled(t *testing.T) {
	h := &heldLog{entered: make(chan struct{}), release: make(chan struct{})}
	let := sync.OnceFunc(func() { close(h.release) })
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(h)
	t.Cleanup(func() { let(); log.SetOutput(old); log.SetFlags(flags) })
	flow := func(id string) *signInFlow {
		return &signInFlow{st: SignInState{ID: id, Agent: "replaced-test", State: "waiting"}, done: make(chan struct{})}
	}
	first, second := flow("r1"), flow("r2")
	signIns.Lock()
	signIns.m[first.st.ID] = first
	signIns.Unlock()
	replaced := make(chan struct{})
	go func() {
		replaceSignIns(second)
		close(replaced)
	}()
	t.Cleanup(func() {
		let()
		<-replaced
		signIns.Lock()
		delete(signIns.m, second.st.ID)
		signIns.Unlock()
	})
	select {
	case <-h.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the replaced sign-in was never logged canceled")
	}
	polled := make(chan struct{})
	go func() {
		SignInStatus(second.st.ID)
		close(polled)
	}()
	select {
	case <-polled:
	case <-time.After(10 * time.Second):
		t.Fatal("a status poll waited on the log's writer: signIns was held while the canceled line was written")
	}
	let()
	logSays(t, &h.syncBuffer, "sign-in replaced-test (r1) canceled")
	if st := first.status(); st.State != "canceled" {
		t.Fatalf("the replaced sign-in %+v", st)
	}
}

// A plugin sign-in that can't start logs its start and its failure.
func TestPluginSignInThatCantStartIsLogged(t *testing.T) {
	signIn(t)
	buf := signInLog(t)
	bad := "ftp://nowhere"
	if _, err := StartPluginSignInWith("acme", 0, nil, SignInOptions{Proxy: &bad}); err == nil {
		t.Fatal("started")
	}
	logSays(t, buf, "sign-in "+PluginID("acme")+" (", ") started", ") failed: ")
	if n := linesOf(buf, `sign-in acme \(\S+\) failed`); n != 1 {
		t.Fatalf("%d failure lines:\n%s", n, buf)
	}
}

// What was pasted and refused is logged in the sign-in's layout, its
// reason left out when it holds a token (review of d1ac61ce, M3).
func TestPasteRefusedLeavesTokensOut(t *testing.T) {
	buf := signInLog(t)
	s := &signInFlow{st: SignInState{ID: "p1", Agent: "codex", State: "waiting"}}
	logPasteRefused(s, "the-paste", errors.New("GitHub said: bad credentials gho_16C7e42F292c6912E7710c838347Ae178B4a"))
	logSays(t, buf, "sign-in codex (p1) paste refused: what was pasted wasn't taken: (left out")
	logHasNoSecret(t, buf, "gho_")
}

// A token called expired that couldn't be renewed is logged once, by the
// renewal (logRefreshFailed), not again by NoteExpiredSignIn (review of
// d1ac61ce, M4).
func TestExpiredSignInFailureLoggedOnce(t *testing.T) {
	refreshFailedSaid.Clear()
	buf := signInLog(t)
	a := &Account{Agent: "codex", User: "x@example.com", renew: func(ctx context.Context, stale string) (string, string, error) {
		err := errors.New("renewal broke")
		logRefreshFailed("codex", "x@example.com", err)
		return "", "", err
	}}
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
	req.Header.Set("Authorization", "Bearer stale")
	NoteExpiredSignIn(context.Background(), a, req)
	if n := strings.Count(buf.String(), "renewal broke"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf)
	}
}

// A saved Claude account whose own folder's sign-in Claude Code emptied is
// logged as one to sign in again, and back once the folder holds a new
// one (review of d1ac61ce, M2).
func TestClaudeFolderLapseIsLogged(t *testing.T) {
	_, side := claudeAuthFixture(t, "")
	buf := signInLog(t)
	dir := claudeAccountDir(side.User)
	writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{"accessToken": "", "refreshToken": ""}})
	syncSavedClaude(side.User)
	logSays(t, buf, "sign-in: claude account "+side.User+" has to be signed in again", "Claude Code emptied its sign-in")
	writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "new-access", "refreshToken": "new-refresh", "expiresAt": time.Now().Add(time.Hour).UnixMilli()}})
	syncSavedClaude(side.User)
	logSays(t, buf, "sign-in: claude account "+side.User+" is signed in again: Claude Code holds a new sign-in")
	logHasNoSecret(t, buf, "new-access", "new-refresh")
}

// A plugin account signed in again has only the sign-in's "done" line,
// not a second "is signed in again" (review of d1ac61ce, L6).
func TestPluginSignedInAgainLoggedOnce(t *testing.T) {
	signIn(t)
	loginsMu.Lock()
	err := writeLogins([]savedLogin{{Agent: "plugin:acme", User: "p@example.com", Home: "k1", Lapsed: "expired"}})
	loginsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	buf := signInLog(t)
	notePluginLapseOf("plugin:acme", "k1", http.StatusOK, toldNow, nil)
	if l := sideLapsed(t, "plugin:acme", "p@example.com"); l != "" {
		t.Fatalf("still lapsed: %q", l)
	}
	if strings.Contains(buf.String(), "is signed in again") {
		t.Fatalf("logged:\n%s", buf)
	}
}

// A Copilot account GitHub refused is logged once, however many of its
// tokens are refused, and back when GitHub serves it (review of
// d1ac61ce, L7).
func TestCopilotLapseIsLogged(t *testing.T) {
	signIn(t)
	loginsMu.Lock()
	err := writeLogins([]savedLogin{{Agent: "copilot", User: "octo"}})
	loginsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	buf := signInLog(t)
	copilotLapse(copilotApp{User: "octo", Token: "gho_one", cli: true})
	copilotLapse(copilotApp{User: "octo", Token: "gho_two", cli: true})
	logSays(t, buf, "sign-in: copilot account octo has to be signed in again ("+copilotLapsedText+"): GitHub refused its token")
	if n := strings.Count(buf.String(), "has to be signed in again"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf)
	}
	copilotUnlapse(copilotApp{User: "octo", Token: "gho_three", cli: true})
	logSays(t, buf, "sign-in: copilot account octo is signed in again: GitHub served it")
	logHasNoSecret(t, buf, "gho_")
}

// A ChatGPT API account whose refresh token OpenAI won't take again is
// logged with OpenAI's code, quoted, and back once a refresh goes through
// (review of d1ac61ce, L8).
func TestChatGPTAPILapseIsLogged(t *testing.T) {
	claudeHome(t)
	refreshFailedSaid.Clear()
	f := newFakeOpenAI(t)
	if st := signInVia(t, f, "oaiapp_9", same); st.State != "done" {
		t.Fatalf("sign-in %+v", st)
	}
	user := "me@example.com"
	expire := func() {
		if err := editSideLogin(ChatGPTAPIID, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
			c, _ := siwcSaved(ls[i])
			c.Expires = time.Now().Add(time.Minute)
			ls[i].Auth, _ = json.Marshal(c)
			return ls, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	buf := signInLog(t)
	expire()
	f.mu.Lock()
	f.refuse = "refresh_token_reused"
	f.mu.Unlock()
	if _, err := siwcFresh(context.Background(), user, false); !errors.Is(err, ErrSIWCSignIn) {
		t.Fatalf("a dead refresh token: %v", err)
	}
	logSays(t, buf, "sign-in: "+ChatGPTAPIID+" account me@example.com has to be signed in again (me@example.com's ChatGPT sign-in has expired — sign in again) HTTP 400: OpenAI refused the refresh of its sign-in: \"refresh_token_reused\"",
		"sign-in: "+ChatGPTAPIID+" account me@example.com refresh failed HTTP 400")
	f.mu.Lock()
	f.refuse = ""
	f.mu.Unlock()
	if _, err := siwcFresh(context.Background(), user, true); err != nil {
		t.Fatal(err)
	}
	logSays(t, buf, "sign-in: "+ChatGPTAPIID+" account me@example.com is signed in again: its sign-in was refreshed")
	// OpenAI's code names a refresh token, it holds none
	for _, secret := range []string{"rt-1", "rt-2", "at-1", "at-2"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("the log has %q:\n%s", secret, buf)
		}
	}
}

// A refresh failure from a request that went away isn't logged; one that
// went through has the next failure logged at once (review of d1ac61ce,
// L10); a dial's port changing from try to try is the same failure.
func TestRefreshFailureLoggedAsTheSignInGoes(t *testing.T) {
	refreshFailedSaid.Clear()
	buf := signInLog(t)
	logRefreshFailed("codex", "c@example.com", fmt.Errorf("refresh: %w", context.Canceled))
	if buf.String() != "" {
		t.Fatalf("a canceled refresh logged:\n%s", buf)
	}
	logRefreshFailed("codex", "c@example.com", errors.New("dial tcp 127.0.0.1:5123: refused"))
	logRefreshFailed("codex", "c@example.com", errors.New("dial tcp 127.0.0.1:5124: refused"))
	if n := strings.Count(buf.String(), "refresh failed"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf)
	}
	logRefreshFailed("codex", "c@example.com", nil)
	logRefreshFailed("codex", "c@example.com", errors.New("dial tcp 127.0.0.1:5125: refused"))
	if n := strings.Count(buf.String(), "refresh failed"); n != 2 {
		t.Fatalf("not logged again after a refresh went through:\n%s", buf)
	}
}

// A failure naming an address is left out whole: not its host, its path
// or its query, nor the network's words with it (the owner's decision,
// 2026-10-30: nothing a vendor or the network said that may name where).
func TestAddressReasonIsLeftOut(t *testing.T) {
	refreshFailedSaid.Clear()
	buf := signInLog(t)
	err := fmt.Errorf("ChatGPT token refresh: %w", &url.Error{Op: "Post", URL: "https://auth.openai.com/oauth/token?code=sekrit&state=st", Err: errors.New("dial tcp: i/o timeout")})
	logRefreshFailed(ChatGPTAPIID, "me@example.com", err)
	logSignIn(SignInState{ID: "s1", Agent: "copilot"}, "failed", 0, "couldn't get the token from GitHub: "+err.Error())
	logSays(t, buf, "refresh failed: (left out", "sign-in copilot (s1) failed: (left out")
	logHasNoSecret(t, buf, "auth.openai.com", "/oauth/token", "sekrit", "i/o timeout")
}

// A `claude auth login` that gives no link in linkWait: its start fails
// with what it printed last, and the failure's one line has how it ended
// and what it printed before (review of d1ac61ce, M5: the start's failure
// came first and the rest was lost).
func TestClaudeSignInEndedBeforeItsLinkIsLogged(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	shellFakes(t)
	exe := filepath.Join(t.TempDir(), "claude")
	testenv.Program(t, exe, "#!/bin/sh\necho 'Something went wrong'\necho 'Port in use'\nexec sleep 30\n")
	oldExe, was := claudeExecutable, linkWait
	claudeExecutable, linkWait = func() string { return exe }, time.Second
	t.Cleanup(func() { claudeExecutable, linkWait = oldExe, was })
	endClaudeSignIns(t)
	buf := signInLog(t)
	if _, err := StartSignIn("claude"); err == nil {
		t.Fatal("started")
	}
	claudeLogins.Wait()
	logSays(t, buf, `failed: "Port in use" (`, "auth login` ended (", `"Something went wrong"`)
	if n := linesOf(buf, `^.*(?:failed|auth login. ended).*$`); n != 1 {
		t.Fatalf("%d lines say it failed:\n%s", n, buf)
	}
}
