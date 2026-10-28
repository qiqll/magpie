package provider

import (
	"encoding/json"
	"log"
	"path/filepath"
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

// A Claude sign-in whose `claude auth login` fails after the code is
// pasted: the log has Claude Code's last lines and how it ended, beyond
// the one line the window shows; an address that isn't this sign-in's is
// logged as not taken, without what was pasted.
func TestClaudeSignInFailureIsLogged(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	shellFakes(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude")
	testenv.Program(t, exe, `#!/bin/sh
[ "$1 $2" = "auth login" ] || exit 2
echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&state=st"
printf 'Paste code here if prompted > '
read -r code
echo "OAuth error: Request failed with status code 400"
echo "Login failed."
exit 1
`)
	browser := filepath.Join(dir, "browser")
	testenv.Program(t, browser, "#!/bin/sh\nexit 1\n")
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
	if err := SubmitSignInCallback(st.ID, "http://localhost:1/callback?code=secret-pasted"); err == nil {
		t.Fatal("an address from another sign-in was taken")
	}
	logSays(t, buf, "sign-in claude ("+st.ID+"): the pasted address wasn't taken")
	if strings.Contains(buf.String(), "secret-pasted") {
		t.Fatalf("the log has what was pasted:\n%s", buf)
	}
	_ = SubmitSignInCallback(st.ID, "the-code#st")
	if st = waitDone(t, st.ID); st.State != "failed" || st.Error != "Login failed." {
		t.Fatalf("state %+v", st)
	}
	logSays(t, buf, "sign-in claude ("+st.ID+") failed: Login failed.",
		"auth login` ended (exit status 1)", `"OAuth error: Request failed with status code 400"`)
	if strings.Contains(buf.String(), "the-code#st") || strings.Contains(buf.String(), "oauth/authorize") {
		t.Fatalf("the log has the sign-in's code or link:\n%s", buf)
	}
}

// An account Anthropic refused is logged once, with Claude Code's words,
// when it is first passed over; the same refusal again logs nothing.
func TestClaudeRefusalIsLoggedOnce(t *testing.T) {
	_, side := claudeAuthFixture(t, "")
	buf := signInLog(t)
	v := ClaudeLoginVersion(side.User)
	NoteClaudeSignInFailure(side.User, v, "Failed to authenticate: OAuth session expired and could not be refreshed")
	logSays(t, buf, "claude "+side.User+": Anthropic refused its sign-in", "Claude Code said: Failed to authenticate: OAuth session expired")
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
	if !claudeLoggedOut(ls) || ls[0].Lapsed != claudeLogoutLapse {
		t.Fatalf("not marked: %+v", ls[0])
	}
	logSays(t, buf, "claude held@example.com: Claude Code on this computer no longer holds its sign-in")
}
