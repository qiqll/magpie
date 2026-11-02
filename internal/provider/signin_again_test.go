package provider

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lapsedClaude is a saved Claude account Claude Code let go of.
func lapsedClaude(t *testing.T, user string) savedLogin {
	return savedLogin{Agent: "claude", User: user, Seen: time.Now().UTC().Add(-time.Hour),
		Profile: mustJSONRaw(t, map[string]any{"emailAddress": user}),
		Auth:    mustJSONRaw(t, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-gone", "refreshToken": "sk-ant-ort01-gone", "subscriptionType": "max"}}),
		Lapsed:  claudeLogoutLapse}
}

// logTo has the log written to a buffer for the test.
func logTo(t *testing.T) *syncBuffer {
	var b syncBuffer
	old := log.Writer()
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(old) })
	return &b
}

func lapseOf(ls []Login, user string) (string, bool) {
	for _, l := range ls {
		if l.User == user {
			return l.Lapsed, true
		}
	}
	return "", false
}

// "Sign in again" on an account's row whose sign-in lands on another
// account says so: the state names the one it was started for, the log
// says that one still has to be signed in again, and it still does.
func TestSignInAgainToAnotherAccountSaysSo(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "other@example.com", plan: "max", refresh: "sk-ant-ort01-other"}, "", true)
	saveLogins(t, lapsedClaude(t, "old@example.com"))
	logged := logTo(t)

	st, err := StartSignInWith("claude", SignInOptions{User: "old@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "the-code")
	st = waitDone(t, st.ID)
	if st.State != "done" || st.User != "other@example.com" || st.For != "old@example.com" || st.Again {
		t.Fatalf("state %+v", st)
	}
	if why, ok := lapseOf(Logins("claude"), "old@example.com"); !ok || why == "" {
		t.Fatalf("old@example.com listed %v, lapse %q", ok, why)
	}
	out := logged.String()
	if !strings.Contains(out, "done account other@example.com (another account than old@example.com, the one it was started for, which still has to be signed in again)") {
		t.Fatalf("log:\n%s", out)
	}
	if strings.Contains(out, "is signed in again") {
		t.Fatalf("a new account said signed in again:\n%s", out)
	}
}

// "Sign in again" that lands on the account it was started for says it is
// one listed already, renewed: the row is no longer marked, and the log
// says it is signed in again.
func TestSignInAgainRenewsTheAccount(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "old@example.com", plan: "max", refresh: "sk-ant-ort01-new"}, "", true)
	saveLogins(t, lapsedClaude(t, "old@example.com"))
	logged := logTo(t)

	st, err := StartSignInWith("claude", SignInOptions{User: "old@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "the-code")
	st = waitDone(t, st.ID)
	if st.State != "done" || st.User != "old@example.com" || !st.Again || st.For != "" {
		t.Fatalf("state %+v", st)
	}
	if why, ok := lapseOf(Logins("claude"), "old@example.com"); !ok || why != "" {
		t.Fatalf("old@example.com listed %v, lapse %q", ok, why)
	}
	if out := logged.String(); !strings.Contains(out, "sign-in: claude account old@example.com is signed in again: signed in again in magpie") {
		t.Fatalf("log:\n%s", out)
	}
}

// A sign-in to an account listed already that didn't have to be signed in
// again is Again, but the log says nothing of its being signed in again.
func TestSignInAgainToAHealthyAccountSaysNoLapse(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "old@example.com", plan: "max", refresh: "sk-ant-ort01-new"}, "", true)
	l := lapsedClaude(t, "old@example.com")
	l.Lapsed = ""
	saveLogins(t, l)
	logged := logTo(t)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "the-code")
	if st = waitDone(t, st.ID); st.State != "done" || !st.Again || st.For != "" {
		t.Fatalf("state %+v", st)
	}
	if out := logged.String(); strings.Contains(out, "is signed in again") {
		t.Fatalf("a healthy account said signed in again:\n%s", out)
	}
}

// The same for a ChatGPT account signed in to again from Codex's row: the
// path every built-in sign-in ends on (finish), not Claude Code's alone.
func TestCodexSignInAgainRenewsTheAccount(t *testing.T) {
	claudeHome(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": "cx@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-cx"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-cx",
		})
	}))
	defer fake.Close()
	oldTok, oldAddr := codexTokenURL, codexCallbackAddr
	t.Cleanup(func() { codexTokenURL, codexCallbackAddr = oldTok, oldAddr })
	codexTokenURL = fake.URL
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()
	saveLogins(t, savedLogin{Agent: "codex", User: "cx@example.com", Auth: json.RawMessage(`{}`), On: true,
		Lapsed: "cx@example.com's sign-in has expired — add the account again to use it"})
	logged := logTo(t)

	st, err := StartSignInWith("codex", SignInOptions{User: "cx@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "cx")
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "cx@example.com" || !st.Again || st.For != "" {
		t.Fatalf("state %+v", st)
	}
	if why, ok := lapseOf(Logins("codex"), "cx@example.com"); !ok || why != "" {
		t.Fatalf("cx@example.com listed %v, lapse %q", ok, why)
	}
	if out := logged.String(); !strings.Contains(out, "sign-in: codex account cx@example.com is signed in again: signed in again in magpie") {
		t.Fatalf("log:\n%s", out)
	}
}

// `claude auth login` run in a terminal, to an account whose row says
// Sign-in required, is seen by the next look, not up to 30 seconds (and
// the 10 Claude Code's credential is kept for) after the last one, and
// the log says the account is signed in again.
func TestTerminalSignInSeenAtOnce(t *testing.T) {
	home := claudeHome(t)
	logged := logTo(t)
	saveLogins(t, lapsedClaude(t, "old@example.com"))
	signInFilesDue()
	if why, _ := lapseOf(Logins("claude"), "old@example.com"); why == "" {
		t.Fatal("not marked to start with")
	}
	if strings.Contains(logged.String(), "is signed in again") {
		t.Fatalf("signed in again before the terminal's sign-in:\n%s", logged)
	}

	// the look just made, and the second it waits between looks at the
	// files gone by
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "old@example.com"}})
	signInFilesDue()

	ls := Logins("claude")
	if why, ok := lapseOf(ls, "old@example.com"); !ok || why != "" {
		t.Fatalf("old@example.com listed %v, lapse %q (%+v)", ok, why, ls)
	}
	if out := logged.String(); !strings.Contains(out, "sign-in: claude account old@example.com is signed in again: its own sign-in on this computer is a new one") {
		t.Fatalf("log:\n%s", out)
	}
	// nothing changed since: the next look isn't made before its time
	seen := loginsSeenAt.Load()
	signInFilesDue()
	Logins("claude")
	if loginsSeenAt.Load() != seen {
		t.Fatal("looked again with nothing changed")
	}
}

// signInFilesDue has the next look at the sign-in files made at once, not
// a second after the last one (another test's, in another HOME, too).
func signInFilesDue() {
	signInFiles.Lock()
	signInFiles.at = time.Time{}
	signInFiles.Unlock()
}
