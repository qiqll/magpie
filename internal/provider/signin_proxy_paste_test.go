package provider

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// pastedEnd is the address a sign-in's browser is sent back to with code,
// as the user copies it from a page that won't load.
func pastedEnd(t *testing.T, st SignInState, code string) string {
	t.Helper()
	u, err := url.Parse(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	return q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
}

// T-110: an account is added through a proxy picked as its callback
// address is pasted, without the global proxy changed for it. The address
// is traded for the account through the picked proxy, which is then the
// account's: the subscription's for its only account, that account's own
// beside others. The global proxy is left as it was.
func TestPastedCallbackGoesThroughThePickedProxy(t *testing.T) {
	claudeHome(t)
	dialed := dialsOut(t)
	first, firstSaw := codexVendorProxy(t)
	second, secondSaw := codexVendorProxy(t)
	oldTok, oldAddr := codexTokenURL, codexCallbackAddr
	t.Cleanup(func() { codexTokenURL, codexCallbackAddr = oldTok, oldAddr })
	codexTokenURL = "http://auth.openai.invalid/oauth/token"
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()
	const token = "http://auth.openai.invalid/oauth/token"
	global := settings.Load().Proxy

	paste := func(proxy, code string) SignInState {
		t.Helper()
		st, err := StartSignInWith("codex", SignInOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if now, _ := SignInStatus(st.ID); now.ProxyFixed {
			t.Fatal("a browser sign-in says it can't take a proxy as its address is pasted")
		}
		if err := SubmitSignInCallbackThrough(st.ID, pastedEnd(t, st, code), &proxy); err != nil {
			t.Fatal(err)
		}
		if st = waitDone(t, st.ID); st.State != "done" || st.User != code+"@example.com" || st.Error != "" {
			t.Fatalf("state %+v", st)
		}
		if st.Proxy != proxy {
			t.Fatalf("the sign-in says it went through %q", st.Proxy)
		}
		waitPortFree(t, codexCallbackAddr)
		return st
	}

	paste(first.URL, "a")
	if got := firstSaw(); !slices.Equal(got, []string{token}) {
		t.Fatalf("the picked proxy saw %q", got)
	}
	if p, _ := storedPicks("codex"); p.Proxy != first.URL || len(p.AccountProxies) != 0 {
		t.Fatalf("kept as proxy %q, accounts' %v", p.Proxy, p.AccountProxies)
	}

	// the next starts through the subscription's, the first's, and the
	// proxy picked as it is pasted is its own beside it
	paste(second.URL, "b")
	if got := secondSaw(); !slices.Equal(got, []string{token}) {
		t.Fatalf("the second picked proxy saw %q", got)
	}
	if got := firstSaw(); len(got) != 1 {
		t.Fatalf("the first account's proxy saw the second's sign-in too: %q", got)
	}
	if got := ProxyOfLogin("codex", "b@example.com"); got != second.URL {
		t.Fatalf("b's requests go through %q", got)
	}
	if got := ProxyOfLogin("codex", "a@example.com"); got != first.URL {
		t.Fatalf("a's requests go through %q", got)
	}
	if got := settings.Load().Proxy; got != global {
		t.Fatalf("the global proxy became %q", got)
	}
	if d := dialed(); len(d) != 0 {
		t.Fatalf("dialed %v past the proxies", d)
	}
}

// waitPortFree waits for the callback port a finished sign-in let go.
func waitPortFree(t *testing.T, addr string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ln, err := net.Listen("tcp", addr); err == nil {
			ln.Close()
			return
		}
	}
	t.Fatalf("%s was never let go", addr)
}

// A proxy picked as the address is pasted that isn't one is refused, and
// the sign-in goes on as it was: still waiting, through its own proxy.
func TestPastedCallbackRefusesABadProxy(t *testing.T) {
	claudeHome(t)
	oldAddr := codexCallbackAddr
	t.Cleanup(func() { codexCallbackAddr = oldAddr })
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()
	st, err := StartSignInWith("codex", SignInOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CancelSignIn(st.ID) })
	bad := "ftp://x"
	if err := SubmitSignInCallbackThrough(st.ID, pastedEnd(t, st, "a"), &bad); err == nil {
		t.Fatal("ftp://x was taken as a proxy")
	}
	if now, _ := SignInStatus(st.ID); now.State != "waiting" || now.Proxy != "" {
		t.Fatalf("after a bad proxy the sign-in is %+v", now)
	}
}

// Claude Code's `claude auth login` was given its proxy as it started, and
// trades the code itself: another proxy picked as the code is pasted
// can't reach it, so it is refused with what to do (start again through
// it), and the state says so for the window. The proxy it has is taken.
func TestClaudeSignInKeepsItsProxyAtPaste(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "k@example.com", name: "K", plan: "max", refresh: "sk-ant-ort01-k"}, "", true)
	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CancelSignIn(st.ID) })
	for end := time.Now().Add(signInDeadline); ; time.Sleep(20 * time.Millisecond) {
		if st, _ = SignInStatus(st.ID); st.State == "waiting" && st.URL != "" {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("never waiting: %+v", st)
		}
	}
	if !st.ProxyFixed {
		t.Fatal("Claude Code's sign-in says it takes a proxy as its code is pasted")
	}
	other := "http://127.0.0.1:9"
	if err := SubmitSignInCallbackThrough(st.ID, "code#state", &other); !errors.Is(err, errProxyFixed) {
		t.Fatalf("another proxy at paste: %v", err)
	}
	if now, _ := SignInStatus(st.ID); now.State != "waiting" || now.Proxy != "" {
		t.Fatalf("after the refusal the sign-in is %+v", now)
	}
}

// A plugin's sign-in finished by a pasted code trades it through the proxy
// picked as it is pasted, in place of the provider's it was authorized
// through: the plugin host is told it with the code.
func TestPluginCodeGoesThroughTheProxyPickedAtPaste(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_SIGNIN", "http://vendor.invalid/signin")
	t.Cleanup(plugin.Settle)
	var mu sync.Mutex
	var seen []string
	recorder := func(who string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, who+" "+r.URL.String())
			mu.Unlock()
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	sub, picked := recorder("sub"), recorder("picked")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	setProxies(t, "fakeco", sub.URL, nil)
	st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "home"})
	if err != nil || st.Proxy != sub.URL {
		t.Fatalf("StartPluginSignIn = %+v, %v", st, err)
	}
	if now, _ := SignInStatus(st.ID); now.ProxyFixed {
		t.Fatal("a plugin's pasted code says it can't take a proxy")
	}
	p := picked.URL
	if err := SubmitSignInCallbackThrough(st.ID, "good", &p); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, []string{"sub http://vendor.invalid/signin/device", "picked http://vendor.invalid/signin/token"}) {
		t.Fatalf("the proxies saw %q", seen)
	}
}
