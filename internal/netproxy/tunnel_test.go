package netproxy

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeConnectProxy is an HTTP proxy on loopback that answers every
// CONNECT with code and opens no tunnel anywhere; asked notes each
// CONNECT's target and Proxy-Authorization.
func fakeConnectProxy(t *testing.T, code int) (addr string, asked chan [2]string) {
	t.Helper()
	asked = make(chan [2]string, 8)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		asked <- [2]string{r.Host, r.Header.Get("Proxy-Authorization")}
		w.WriteHeader(code)
	}))
	t.Cleanup(s.Close)
	return s.Listener.Addr().String(), asked
}

// A proxy that turns a tunnel away is said with its status, and one asked
// carries the user and password its address holds, as the CLI's would
// (T-107).
func TestTunnelStatusSaysTheProxysAnswer(t *testing.T) {
	addr, asked := fakeConnectProxy(t, http.StatusForbidden)
	status, code, err := TunnelStatus(context.Background(), "http://bob:s3cret@"+addr, "platform.claude.com:443")
	if err != nil || code != 403 || status != "403 Forbidden" {
		t.Fatalf("status %q code %d err %v", status, code, err)
	}
	got := <-asked
	if got[0] != "platform.claude.com:443" || got[1] != "Basic "+base64.StdEncoding.EncodeToString([]byte("bob:s3cret")) {
		t.Fatalf("asked %q", got)
	}

	open, _ := fakeConnectProxy(t, http.StatusOK)
	// a bare host:port is an http:// proxy, as Parse reads it
	if status, code, err := TunnelStatus(context.Background(), open, "api.anthropic.com:443"); err != nil || code != 200 {
		t.Fatalf("status %q code %d err %v", status, code, err)
	}
}

// Only an http:// proxy is asked; one that answers nothing is an error,
// not a status.
func TestTunnelStatusAsksOnlyHTTPProxies(t *testing.T) {
	for _, raw := range []string{"socks5://127.0.0.1:1", "https://127.0.0.1:1", "direct://", ""} {
		if _, _, err := TunnelStatus(context.Background(), raw, "platform.claude.com:443"); err == nil {
			t.Fatalf("%q was asked", raw)
		}
	}
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
			c.Write([]byte("not http\r\n\r\n"))
			c.Close()
		}
	}()
	if _, _, err := TunnelStatus(context.Background(), "http://"+ln.Addr().String(), "platform.claude.com:443"); err == nil || !strings.Contains(err.Error(), "didn't read") {
		t.Fatalf("err %v", err)
	}
}
