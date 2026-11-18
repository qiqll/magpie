package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// connectProxy is an HTTP proxy on loopback that answers a CONNECT to
// each host with codes[host] (200 when it names none) and opens no tunnel
// anywhere; asked lists the hosts asked.
func connectProxy(t *testing.T, codes map[string]int) (url string, asked func() []string) {
	t.Helper()
	var mu sync.Mutex
	var hosts []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		code, ok := codes[r.Host]
		if !ok {
			code = http.StatusOK
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(s.Close)
	return s.URL, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), hosts...) }
}

// claudeRefusedThrough is the error of a Claude sign-in whose `claude auth
// login` fails with "proxy refused the connection", the global proxy set
// to proxy, its tunnel asked of that proxy by magpie as it is (T-107).
func claudeRefusedThrough(t *testing.T, proxy string) string {
	t.Helper()
	claudeHome(t)
	noAnthropic(t)
	fakeProxyRefusedClaudeLogin(t)
	claudeTunnel = netproxy.TunnelStatus
	if err := settings.Save(settings.Settings{Proxy: proxy}); err != nil {
		t.Fatal(err)
	}
	_, err := StartSignIn("claude")
	var used *SignInProxyError
	if !errors.As(err, &used) {
		t.Fatalf("err %v (%T)", err, err)
	}
	claudeLogins.Wait()
	return err.Error()
}

// A proxy that turns the tunnel away is said with the status it answers
// magpie's own CONNECT with, which Claude Code leaves out, and what to do
// (T-107).
func TestClaudeSignInProxyRefusedSaysTheStatus(t *testing.T) {
	proxy, asked := connectProxy(t, map[string]int{"platform.claude.com:443": http.StatusForbidden})
	got := claudeRefusedThrough(t, proxy)
	for _, want := range []string{"Login failed: proxy refused the connection (", "platform.claude.com:443", `"403 Forbidden"`, "turns the tunnel away", "pick another proxy"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the reason %q doesn't say %q", got, want)
		}
	}
	if a := asked(); len(a) != 1 || a[0] != "platform.claude.com:443" {
		t.Fatalf("asked %q", a)
	}
}

// One that wants a password says so.
func TestClaudeSignInProxyRefusedWantsAPassword(t *testing.T) {
	proxy, _ := connectProxy(t, map[string]int{"api.anthropic.com:443": http.StatusProxyAuthRequired})
	got := claudeRefusedThrough(t, proxy)
	if !strings.Contains(got, "api.anthropic.com:443") || !strings.Contains(got, "407") || !strings.Contains(got, "http://user:password@host:port") {
		t.Fatalf("the reason is %q", got)
	}
}

// One that lets magpie's tunnels through turned Claude Code away for a
// while only: the restart in the report, which a cap on one client's
// connections explains, is said with what to look at.
func TestClaudeSignInProxyRefusedForAWhile(t *testing.T) {
	proxy, asked := connectProxy(t, nil)
	got := claudeRefusedThrough(t, proxy)
	if !strings.Contains(got, "let tunnels to platform.claude.com and api.anthropic.com through") || !strings.Contains(got, "connection cap") {
		t.Fatalf("the reason is %q", got)
	}
	if a := asked(); len(a) != 2 {
		t.Fatalf("asked %q", a)
	}
}

// A proxy that can't be asked (nothing listens at the discard port, as
// the other sign-in tests' proxy) adds nothing; nor does a failure that
// isn't the proxy's.
func TestClaudeSignInProxyRefusedUnasked(t *testing.T) {
	addr := "http://127.0.0.1:9"
	if got := claudeRefusedThrough(t, addr); got != "Login failed: proxy refused the connection" {
		t.Fatalf("the reason is %q", got)
	}
	if got := claudeProxyRefused("Login failed: timed out", []string{"HTTPS_PROXY=" + addr}); got != "Login failed: timed out" {
		t.Fatalf("the reason is %q", got)
	}
}
