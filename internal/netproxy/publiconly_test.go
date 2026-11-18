package netproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// publicIP stands for a proxy's public address; nothing is ever dialled
// at it (DialPublicAddr is pointed at a local listener).
const publicIP = "93.184.216.34"

// countingProxy is an HTTP proxy on loopback answering every request it
// is sent "ok", counting the connections made to it.
type countingProxy struct {
	ln    net.Listener
	conns atomic.Int32
	wg    sync.WaitGroup
}

func startFakeProxy(t *testing.T) *countingProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &countingProxy{ln: ln}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.conns.Add(1)
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer c.Close()
				if _, err := http.ReadRequest(bufio.NewReader(c)); err != nil {
					return
				}
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); p.wg.Wait() })
	return p
}

func (p *countingProxy) port() string {
	_, port, _ := net.SplitHostPort(p.ln.Addr().String())
	return port
}

// areaProxies has PublicOnly name the proxies given, LookupIP answer
// proxy.example from *answer, and a dial to publicIP reach the local
// proxy instead, for the test.
func areaProxies(t *testing.T, answer *string, proxies ...string) {
	t.Helper()
	was, lookup, dial := PublicOnly, LookupIP, DialPublicAddr
	t.Cleanup(func() { PublicOnly, LookupIP, DialPublicAddr = was, lookup, dial })
	PublicOnly = func(raw string) bool {
		for _, p := range proxies {
			if raw == p {
				return true
			}
		}
		return false
	}
	LookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "proxy.example" {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var out []netip.Addr
		for _, a := range strings.Split(*answer, ",") {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
	DialPublicAddr = func(ctx context.Context, d DialFunc, network, addr string) (net.Conn, error) {
		if host, port, _ := net.SplitHostPort(addr); host == publicIP {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return d(ctx, network, addr)
	}
}

// TestAreaProxyMustBePublic (P6): an area's proxy is refused at every
// address that isn't public, in each way one can be written, and kept at
// a public one.
func TestAreaProxyMustBePublic(t *testing.T) {
	answer := "127.0.0.1"
	areaProxies(t, &answer)
	for _, raw := range []string{
		"http://127.0.0.1:7890", "socks5://[::1]:1080", "http://localhost:8080", "http://localhost.:8080",
		"http://a.localhost:8080", "socks5://intranet:1080", "http://10.0.0.1:3128", "http://192.168.1.1:80",
		"http://172.16.0.1:80", "http://100.64.1.1:1080", "http://169.254.169.254:80", "http://[::ffff:127.0.0.1]:80",
		"http://[::ffff:7f00:1]:80", "http://0.0.0.0:80", "http://0.1.2.3:80", "http://198.18.0.1:80",
		"http://224.0.0.1:80", "http://[fe80::1]:80", "http://[fc00::1]:80", "http://[64:ff9b::7f00:1]:80",
		"http://[2002:7f00:1::]:80", "http://2130706433:80", "http://0x7f.1:80", "http://0177.0.0.1:80",
		"http://127.1:80", "http://0x7f000001:80", "socks5h://proxy.example:1080", "socks5h://proxy.example.:1080",
	} {
		if err := CheckPublic(context.Background(), raw); !errors.Is(err, ErrNotPublic) {
			t.Errorf("CheckPublic(%s) = %v, want ErrNotPublic", raw, err)
		}
	}
	// a name with one private address among public ones is refused
	answer = publicIP + ",10.0.0.7"
	if err := CheckPublic(context.Background(), "http://proxy.example:3128"); !errors.Is(err, ErrNotPublic) {
		t.Errorf("a name with a private address among its answers = %v", err)
	}
	if err := CheckPublic(context.Background(), "http://nowhere.example:3128"); !errors.Is(err, ErrUnresolved) {
		t.Errorf("a name with no address = %v", err)
	}
	answer = publicIP
	for _, raw := range []string{"http://" + publicIP + ":3128", "https://proxy.example:443", "socks5://u:p@proxy.example:1080", "socks5h://proxy.example:1080", "http://[2606:4700::1111]:80"} {
		if err := CheckPublic(context.Background(), raw); err != nil {
			t.Errorf("CheckPublic(%s) = %v, want nil", raw, err)
		}
	}
	for _, raw := range []string{"http://" + publicIP, "ftp://" + publicIP + ":21", publicIP + ":3128", "http://" + publicIP + ":3128/x", "http://" + publicIP + ":0"} {
		if err := CheckPublic(context.Background(), raw); err == nil || errors.Is(err, ErrNotPublic) {
			t.Errorf("CheckPublic(%s) = %v, want a malformed-proxy error", raw, err)
		}
	}
}

// TestAreaProxyDialRefusesRebind (P7): a public-only proxy whose name
// answered a public address when it was saved, and this computer's when
// it is dialled, is refused before any connection is made, for a request
// through Dispatch, a SOCKS dial and a child process's proxy. The
// owner's own proxy on loopback is dialled as ever.
func TestAreaProxyDialRefusesRebind(t *testing.T) {
	fp := startFakeProxy(t)
	area := "http://proxy.example:" + fp.port()
	socks := "socks5://proxy.example:" + fp.port()
	loop := "http://127.0.0.1:" + fp.port()
	answer := publicIP
	areaProxies(t, &answer, area, socks, loop)

	base := &http.Transport{Proxy: Func, DisableKeepAlives: true}
	client := &http.Client{Transport: Dispatch(base)}
	defer base.CloseIdleConnections()
	get := func(choice string) error {
		req, _ := http.NewRequestWithContext(With(context.Background(), choice), http.MethodGet, "http://vendor.example/v1/models", nil)
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
		}
		return err
	}

	if err := get(area); err != nil {
		t.Fatalf("through the area's proxy at a public address: %v", err)
	}
	if n := fp.conns.Load(); n != 1 {
		t.Fatalf("connections after a public dial = %d, want 1", n)
	}

	answer = "127.0.0.1" // the name points home since it was saved
	if err := get(area); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("rebound to loopback: %v, want ErrNotPublic", err)
	}
	if err := get(loop); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("an area's proxy written as loopback: %v, want ErrNotPublic", err)
	}
	if _, err := DialSOCKS(context.Background(), parsed(t, socks), "vendor.example:443"); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("SOCKS dial rebound to loopback: %v, want ErrNotPublic", err)
	}
	if n := fp.conns.Load(); n != 1 {
		t.Fatalf("connections after the refused dials = %d, want still 1", n)
	}

	// a child process is given no proxy that reaches this computer
	if env := EnvWith(area, []string{"PATH=/bin"}); !hasEnv(env, "HTTPS_PROXY="+refusedProxy) {
		t.Fatalf("a child's env for a rebound proxy: %q", env)
	}
	if _, err := EnvForBunWith(area, []string{"PATH=/bin"}, true); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("EnvForBunWith a rebound proxy: %v, want ErrNotPublic", err)
	}
	answer = publicIP
	if env := EnvWith(area, []string{"PATH=/bin"}); !hasEnv(env, "HTTPS_PROXY=http://"+publicIP+":"+fp.port()) {
		t.Fatalf("a child's env is given the address checked, not the name: %q", env)
	}

	// the owner's own proxy on loopback is no area's: dialled as ever
	PublicOnly = func(string) bool { return false }
	if err := get(loop); err != nil {
		t.Fatalf("the owner's loopback proxy: %v", err)
	}
	if n := fp.conns.Load(); n != 2 {
		t.Fatalf("connections after the owner's request = %d, want 2", n)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func parsed(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestAreaProxyWithPublicOnlyHoldsAnUnsavedProxy (T-214): a proxy no ips.json
// names (an area's probe or batch line) is held to public addresses when
// its request is WithPublicOnly's, over HTTP and SOCKS alike, and is
// dialled as ever without it.
func TestAreaProxyWithPublicOnlyHoldsAnUnsavedProxy(t *testing.T) {
	fp := startFakeProxy(t)
	unsaved := "http://proxy.example:" + fp.port()
	socks := "socks5://proxy.example:" + fp.port()
	loop := "http://127.0.0.1:" + fp.port()
	answer := publicIP
	areaProxies(t, &answer) // PublicOnly names none

	base := &http.Transport{Proxy: Func, DisableKeepAlives: true}
	client := &http.Client{Transport: Dispatch(base)}
	defer base.CloseIdleConnections()
	get := func(ctx context.Context, choice string) error {
		req, _ := http.NewRequestWithContext(With(ctx, choice), http.MethodGet, "http://vendor.example/v1/models", nil)
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
		}
		return err
	}
	held := WithPublicOnly(context.Background())
	if err := get(held, unsaved); err != nil {
		t.Fatalf("at a public address: %v", err)
	}
	answer = "127.0.0.1" // pointed home after it was judged
	if err := get(held, unsaved); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("rebound: %v, want ErrNotPublic", err)
	}
	if err := get(held, loop); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("written as loopback: %v, want ErrNotPublic", err)
	}
	if _, err := DialSOCKS(held, parsed(t, socks), "vendor.example:443"); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("SOCKS rebound: %v, want ErrNotPublic", err)
	}
	if n := fp.conns.Load(); n != 1 {
		t.Fatalf("connections = %d, want 1", n)
	}
	if err := get(context.Background(), loop); err != nil {
		t.Fatalf("the owner's own loopback proxy, not held: %v", err)
	}
	if n := fp.conns.Load(); n != 2 {
		t.Fatalf("connections = %d, want 2", n)
	}
}
