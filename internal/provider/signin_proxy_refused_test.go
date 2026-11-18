package provider

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// One that is reached but doesn't reach the host (502) is said as that,
// not as a rule to change (review of T-107, M1).
func TestClaudeSignInProxyRefusedUpstream(t *testing.T) {
	proxy, _ := connectProxy(t, map[string]int{"platform.claude.com:443": http.StatusBadGateway})
	got := claudeRefusedThrough(t, proxy)
	if !strings.Contains(got, `"502 Bad Gateway"`) || !strings.Contains(got, "doesn't reach the host") || strings.Contains(got, "let it pass CONNECT") {
		t.Fatalf("the reason is %q", got)
	}
}

// magpie's own bridge to a SOCKS proxy is not asked: its answer is
// magpie's word on the SOCKS proxy, not a rule of a proxy's (review of
// T-107, M1).
func TestClaudeSignInProxyRefusedSkipsTheBridge(t *testing.T) {
	// a SOCKS proxy that drops every connection: the bridge answers each
	// CONNECT with its own 502 at once
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
	socks, _ := netproxy.Parse("socks5://" + ln.Addr().String())
	bridge, err := netproxy.Bridge(socks)
	if err != nil {
		t.Fatal(err)
	}
	if got := claudeProxyRefused("Login failed: proxy refused the connection", []string{"HTTPS_PROXY=" + bridge}); got != "Login failed: proxy refused the connection" {
		t.Fatalf("the reason is %q", got)
	}
}

// A proxy that takes the connection and never answers holds the failure
// back for claudeTunnelWait in all, then adds nothing (review of T-107,
// M2).
func TestClaudeSignInProxyRefusedWaitsBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	was := claudeTunnelWait
	claudeTunnelWait = 200 * time.Millisecond
	t.Cleanup(func() { claudeTunnelWait = was })
	done := make(chan string, 1)
	go func() {
		done <- claudeProxyRefused("Login failed: proxy refused the connection", []string{"HTTPS_PROXY=http://" + ln.Addr().String()})
	}()
	select {
	case got := <-done:
		if got != "Login failed: proxy refused the connection" {
			t.Fatalf("the reason is %q", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the failure waited past claudeTunnelWait")
	}
}
