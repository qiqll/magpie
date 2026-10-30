package provider

import (
	"encoding/json"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// signInLog takes the log for the test: the window says why a sign-in
// failed only while it is open, so the log is where it is found later.
func signInLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(flags) })
	return buf
}

// logSays waits for the log to hold every one of want: the sign-in's
// goroutine writes it as the sign-in ends.
func logSays(t *testing.T, buf *syncBuffer, want ...string) {
	t.Helper()
	for end := time.Now().Add(signInDeadline); ; time.Sleep(20 * time.Millisecond) {
		got, all := buf.String(), true
		for _, w := range want {
			all = all && strings.Contains(got, w)
		}
		if all {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("the log doesn't say %q:\n%s", want, got)
		}
	}
}

// droppingPort is a port on this machine whose listener drops every
// connection unanswered, as a `claude auth login` that has stopped
// listening leaves it: a request to it fails, its address in the error.
// The listener is the test's until it ends, so no one else is handed it.
func droppingPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// fakeFailingClaudeLogin points claudeExecutable at a `claude auth login`
// that opens its page through $BROWSER with the browser sent back to port
// on this machine (nothing listens there), takes a pasted code, then says
// what failed, a form line holding a code among it, and exits 1. Its
// browser writes the page down, as magpie's does.
func fakeFailingClaudeLogin(t *testing.T, port string) {
	t.Helper()
	shellFakes(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude")
	testenv.Program(t, exe, `#!/bin/sh
[ "$1 $2" = "auth login" ] || exit 2
"$BROWSER" 'https://claude.com/cai/oauth/authorize?code=true&redirect_uri=http%3A%2F%2Flocalhost%3A`+port+`%2Fcallback&state=st'
printf 'Paste code here if prompted > '
read -r code
echo "token request: grant_type=authorization_code&code=sekrit-form&code_verifier=v"
echo "OAuth error: Request failed with status code 400"
echo "Login failed."
exit 1
`)
	browser := filepath.Join(dir, "browser")
	testenv.Program(t, browser, "#!/bin/sh\nprintf %s \"$1\" > \"$"+openedURLEnv+"\"\n")
	oldExe, oldOpener := claudeExecutable, claudeURLOpener
	claudeExecutable = func() string { return exe }
	claudeURLOpener = func() string { return browser }
	t.Cleanup(func() { claudeExecutable, claudeURLOpener = oldExe, oldOpener })
	endClaudeSignIns(t)
}

// A Claude sign-in whose `claude auth login` fails after the code is
// pasted: the log has Claude Code's last lines and how it ended, beyond
// the one line the window shows, with a line that may hold a code left
// out. What was pasted and wasn't taken is logged without itself: an
// address of another sign-in, and this sign-in's own, its code in it,
// that Claude Code's listener didn't answer.
func TestClaudeSignInFailureIsLogged(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	port := droppingPort(t)
	fakeFailingClaudeLogin(t, port)
	buf := signInLog(t)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, "http://localhost:1/callback?code=secret-pasted&state=st"); err == nil {
		t.Fatal("an address from another sign-in was taken")
	}
	logSays(t, buf, "sign-in claude ("+st.ID+"): what was pasted wasn't taken")
	if err := SubmitSignInCallback(st.ID, "http://localhost:"+port+"/callback?code=secret-own&state=st"); err == nil {
		t.Fatal("an address nothing answered was taken")
	}
	logSays(t, buf, "Claude Code didn't take that address")
	_ = SubmitSignInCallback(st.ID, "the-code#st")
	if st = waitDone(t, st.ID); st.State != "failed" || st.Error != "Login failed." {
		t.Fatalf("state %+v", st)
	}
	logSays(t, buf, "sign-in claude ("+st.ID+`) failed: "Login failed."`,
		"auth login` ended (exit status 1)", `"OAuth error: Request failed with status code 400"`, "(a line left out")
	for _, secret := range []string{"secret-pasted", "secret-own", "the-code#st", "sekrit-form", "oauth/authorize"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("the log has %q:\n%s", secret, buf)
		}
	}
}

// A Claude sign-in canceled (its window closed) ends its `claude auth
// login`; that is no failure, and the log says nothing of it.
func TestCanceledClaudeSignInLogsNoFailure(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeFailingClaudeLogin(t, droppingPort(t))
	buf := signInLog(t)
	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	CancelSignIn(st.ID)
	claudeLogins.Wait()
	if strings.Contains(buf.String(), "auth login` ended") || strings.Contains(buf.String(), "failed") {
		t.Fatalf("a canceled sign-in logged as failed:\n%s", buf)
	}
}

// Text from outside magpie is one line of the log, however it is made.
func TestLogTextIsOneLine(t *testing.T) {
	got := logText("x\n2026/10/28 12:00:00 forged" + strings.Repeat("y", 2*logTextMost))
	if strings.Contains(got, "\n") || len(got) > logTextMost+16 {
		t.Fatalf("%d bytes, %q", len(got), got)
	}
}

// An account Anthropic refused is logged once, with Claude Code's words,
// when it is first passed over; the same refusal again logs nothing.
func TestClaudeRefusalIsLoggedOnce(t *testing.T) {
	_, side := claudeAuthFixture(t, "")
	buf := signInLog(t)
	v := ClaudeLoginVersion(side.User)
	NoteClaudeSignInFailure(side.User, v, "Failed to authenticate: OAuth session expired and could not be refreshed")
	logSays(t, buf, "sign-in: claude account "+side.User+" has to be signed in again", "Anthropic refused its sign-in", `Claude Code said: "Failed to authenticate: OAuth session expired`)
	NoteClaudeSignInFailure(side.User, v, "Failed to authenticate: OAuth session expired and could not be refreshed")
	if n := strings.Count(buf.String(), "Anthropic refused its sign-in"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf)
	}
}

// The account Claude Code held, gone from it: logged as passed over.
func TestClaudeLoggedOutIsLogged(t *testing.T) {
	isolate(t)
	buf := signInLog(t)
	ls := []savedLogin{{Agent: "claude", User: "held@example.com", Held: true,
		Auth: json.RawMessage(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-x","refreshToken":"sk-ant-ort01-x"}}`)}}
	changed, lapsed := claudeLoggedOut(ls)
	if !changed || ls[0].Lapsed != claudeLogoutLapse || len(lapsed) != 1 {
		t.Fatalf("not marked: %+v, lapsed %q", ls[0], lapsed)
	}
	logClaudeLoggedOut(lapsed)
	logSays(t, buf, "sign-in: claude account held@example.com has to be signed in again", "Claude Code on this computer no longer holds its sign-in")
}

// What `claude auth login` printed is logged without a line that may hold
// a code or token, and with one that only names them.
func TestClaudeSaidSafe(t *testing.T) {
	left := "(a line left out: it may hold the sign-in's code)"
	for line, out := range map[string]bool{
		"Login failed.":                         false,
		"OAuth token revoked":                   false,
		"Failed to refresh OAuth token: 400":    false,
		"grant_type=authorization_code&code=x":  true,
		`{"access_token": "sk-ant-oat01-x"}`:    true,
		"refresh_token=sk-ant-ort01-x":          true,
		"see http://localhost:5555/callback":    true,
		"abcdefghijklmnopqrstu#statestatestate": true,
	} {
		if got := claudeSaidSafe([]string{line})[0]; (got == left) != out {
			t.Errorf("%q logged as %q", line, got)
		}
	}
	if n := len(claudeSaidSafe(make([]string, 3*claudeSaidKept))); n != claudeSaidKept {
		t.Fatalf("%d lines kept", n)
	}
}
