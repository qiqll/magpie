package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// fakeProxyRefusedClaudeLogin points claudeExecutable at a `claude auth
// login` that fails as the reporter's did (T-103), before it opens a
// page, having written down the HTTPS_PROXY it was given.
func fakeProxyRefusedClaudeLogin(t *testing.T) (given string) {
	t.Helper()
	shellFakes(t)
	dir := t.TempDir()
	exe, given := filepath.Join(dir, "claude"), filepath.Join(dir, "given")
	testenv.Program(t, exe, `#!/bin/sh
[ "$1 $2" = "auth login" ] || exit 2
printf %s "$HTTPS_PROXY" > '`+given+`'
echo "Opening browser to sign in…"
echo "Login failed: proxy refused the connection"
exit 1
`)
	old := claudeExecutable
	claudeExecutable = func() string { return exe }
	t.Cleanup(func() { claudeExecutable = old })
	endClaudeSignIns(t)
	return given
}

// A Claude sign-in that fails says the proxy `claude auth login` was
// given, its user and password left out, and whose it was: to the page,
// in the start's error, and in the log's failure line (T-103).
func TestClaudeSignInFailureSaysItsProxy(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	given := fakeProxyRefusedClaudeLogin(t)
	buf := signInLog(t)
	if err := settings.Save(settings.Settings{Proxy: "http://user:pw-secret@127.0.0.1:9"}); err != nil {
		t.Fatal(err)
	}

	_, err := StartSignIn("claude")
	var used *SignInProxyError
	if !errors.As(err, &used) {
		t.Fatalf("err %v (%T) says no proxy", err, err)
	}
	if err.Error() != "Login failed: proxy refused the connection" {
		t.Fatalf("the reason is %q", err)
	}
	if used.Used != (SignInProxy{Proxy: "http://127.0.0.1:9", From: "settings"}) {
		t.Fatalf("said %+v", used.Used)
	}
	claudeLogins.Wait()
	if b, _ := os.ReadFile(given); string(b) != "http://user:pw-secret@127.0.0.1:9" {
		t.Fatalf("claude was given %q", b)
	}
	logSays(t, buf, "sign-in claude (", ") failed: ", "auth login` ended (exit status 1)", "given the proxy http://127.0.0.1:9 (settings)")
	if strings.Contains(buf.String(), "pw-secret") {
		t.Fatalf("the log has the proxy's password:\n%s", buf)
	}
}

// One picked in the sign-in is said as picked, "direct" as no proxy.
func TestClaudeSignInFailureSaysThePickedProxy(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	given := fakeProxyRefusedClaudeLogin(t)
	_ = settings.Save(settings.Settings{Proxy: "127.0.0.1:9"})
	for _, c := range []struct {
		pick string
		want SignInProxy
		env  string
	}{
		{"http://10.0.0.1:3128", SignInProxy{Proxy: "http://10.0.0.1:3128", From: "picked"}, "http://10.0.0.1:3128"},
		{"direct", SignInProxy{From: "picked"}, ""},
	} {
		pick := c.pick
		_, err := StartSignInWith("claude", SignInOptions{Proxy: &pick})
		var used *SignInProxyError
		if !errors.As(err, &used) || used.Used != c.want {
			t.Fatalf("%s: err %v, said %+v", pick, err, used)
		}
		claudeLogins.Wait()
		if b, _ := os.ReadFile(given); string(b) != c.env {
			t.Fatalf("%s: claude was given %q", pick, b)
		}
	}
}

// One that fails after its page opened says the proxy in its state, as
// the page reads it (T-103), and the log has it beside how Claude Code
// ended.
func TestClaudeSignInFailedStateSaysItsProxy(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeFailingClaudeLogin(t, droppingPort(t))
	buf := signInLog(t)
	_ = settings.Save(settings.Settings{Proxy: "direct"})
	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	_ = SubmitSignInCallback(st.ID, "the-code#st")
	st = waitDone(t, st.ID)
	if st.State != "failed" || st.Said().ProxyUsed == nil || *st.Said().ProxyUsed != (SignInProxy{From: "direct"}) {
		t.Fatalf("state %+v, proxy %+v", st, st.ProxyUsed)
	}
	logSays(t, buf, "auth login` ended (exit status 1)", "given no proxy (direct)")
}
