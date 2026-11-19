package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/renewal"
)

// The public address an area's proxy name answers in these tests; no
// connection is ever made to it (dialPublicHere).
const ipsPublic = "93.184.216.34"

// stubProxyDNS has proxy.example (and every name under .example) answer
// *answer, and a connection to ipsPublic reach this computer at the same
// port, where the test's fake proxy listens.
func stubProxyDNS(t *testing.T, answer *atomic.Value) {
	t.Helper()
	lookup, dial := netproxy.LookupIP, netproxy.DialPublicAddr
	t.Cleanup(func() { netproxy.LookupIP, netproxy.DialPublicAddr = lookup, dial })
	netproxy.LookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		if !strings.HasSuffix(host, ".example") {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return []netip.Addr{netip.MustParseAddr(answer.Load().(string))}, nil
	}
	netproxy.DialPublicAddr = func(ctx context.Context, d netproxy.DialFunc, network, addr string) (net.Conn, error) {
		if host, port, _ := net.SplitHostPort(addr); host == ipsPublic {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return d(ctx, network, addr)
	}
}

// traceProxy is an HTTP proxy on loopback that answers every request
// with a trace page saying exit, as Cloudflare's would through it, or
// with status when it is set; it counts the connections made to it.
type traceProxy struct {
	ln     net.Listener
	conns  atomic.Int32
	status atomic.Int32
	exit   string
	wg     sync.WaitGroup
	mu     sync.Mutex
	open   []net.Conn // closed as the test ends: a client keeps them idle
}

func (p *traceProxy) stop() {
	p.ln.Close()
	p.drop()
	p.wg.Wait()
}

// drop closes the connections made to it so far, which a client would
// have used again.
func (p *traceProxy) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.open {
		c.Close()
	}
	p.open = nil
}

func startTraceProxy(t *testing.T, exit string) *traceProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &traceProxy{ln: ln, exit: exit}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.conns.Add(1)
			p.mu.Lock()
			p.open = append(p.open, c)
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					req, err := http.ReadRequest(br)
					if err != nil {
						return
					}
					io.Copy(io.Discard, req.Body)
					if s := p.status.Load(); s != 0 {
						fmt.Fprintf(c, "HTTP/1.1 %d Nope\r\nContent-Length: 34\r\n\r\ninternal router page 192.168.1.1!!", s)
						continue
					}
					body := "fl=1\nip=" + p.exit + "\nloc=JP\n"
					if strings.HasPrefix(req.URL.Path, "/v1/") {
						body = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
					}
					fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
				}
			}()
		}
	}()
	t.Cleanup(p.stop)
	return p
}

func (p *traceProxy) port() string {
	_, port, _ := net.SplitHostPort(p.ln.Addr().String())
	return port
}

// ipsWorld is adminWorld with two areas, A (study) and B (work), each
// with a key of plan, an owner's IP, and the trace page at a loopback
// address through the proxy.
type ipsWorld struct {
	do                adminDo
	console           string
	aID, a, bID, b    string
	aKey, bKey, owner string
	answer            *atomic.Value
	proxy             *traceProxy
	reached           *atomic.Int32
}

func newIPsWorld(t *testing.T) *ipsWorld {
	t.Helper()
	w := &ipsWorld{answer: &atomic.Value{}}
	w.do, w.console, w.reached = adminWorld(t)
	w.aID, w.a = makeArea(t, w.do, w.console, "学习", "plan")
	w.bID, w.b = makeArea(t, w.do, w.console, "工作", "plan")
	for i, secret := range []string{w.a, w.b} {
		b, _ := json.Marshal(map[string]string{"provider": "plan", "name": "k", "key": fmt.Sprintf("sk-area-%010d", i)})
		if c, body := w.do("POST", AdminPath+"/accounts/key", lanPeer, secret, string(b)); c != http.StatusCreated {
			t.Fatal(c, body)
		}
	}
	w.aKey = "plan/" + provider.KeyID("sk-area-0000000000")
	w.bKey = "plan/" + provider.KeyID("sk-area-0000000001")
	w.owner = "plan/" + provider.KeyID("upstream-secret-plan")
	w.answer.Store(ipsPublic)
	stubProxyDNS(t, w.answer)
	w.proxy = startTraceProxy(t, "203.0.113.7")
	was := areaTraceURL
	areaTraceURL = "http://trace.example/cdn-cgi/trace"
	t.Cleanup(func() { areaTraceURL = was })
	if err := renewal.SetIPs([]renewal.IP{{IP: "198.51.100.1", Proxies: []string{"http://owner:secret@192.168.1.2:7890"}}, {IP: "198.51.100.2"}}, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *ipsWorld) post(t *testing.T, secret, path string, in any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(in)
	return w.do("POST", AdminPath+path, lanPeer, secret, string(b))
}

// addIP is an IP the area's admin adds with one proxy at proxy.example,
// and that proxy's id.
func (w *ipsWorld) addIP(t *testing.T, secret, ip string) string {
	t.Helper()
	proxy := "http://user:pw@" + proxyHost(ip) + ":" + w.proxy.port()
	c, body := w.post(t, secret, "/ips", map[string]any{"ip": ip, "name": "东京", "proxies": []string{proxy}})
	if c != http.StatusCreated {
		t.Fatalf("adding %s: %d %s", ip, c, body)
	}
	if strings.Contains(body, "pw@") || strings.Contains(body, "user:") {
		t.Fatalf("the login is told: %s", body)
	}
	return renewal.ProxyID(proxy)
}

// proxyHost is the name of the proxy addIP gives ip, one of its own.
func proxyHost(ip string) string { return "p-" + strings.ReplaceAll(ip, ".", "-") + ".example" }

func ipsOf(t *testing.T, body string) []adminIP {
	t.Helper()
	var out []adminIP
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(body, err)
	}
	return out
}

// TestAdminIPsOwnAreaOnly (P1): an area lists, changes, removes, checks
// and puts on its accounts its own IPs alone; the owner's and another
// area's are no such IP. The console reads every area's, never the
// owner's, and changes none.
func TestAdminIPsOwnAreaOnly(t *testing.T) {
	w := newIPsWorld(t)
	pxA := w.addIP(t, w.a, "203.0.113.7")
	pxB := w.addIP(t, w.b, "203.0.113.8")

	c, body := w.do("GET", AdminPath+"/ips", lanPeer, w.a, "")
	got := ipsOf(t, body)
	if c != 200 || len(got) != 1 || got[0].IP != "203.0.113.7" || got[0].Area != "" || got[0].GivenByOwner || len(got[0].Proxies) != 1 || got[0].Proxies[0].ID != pxA {
		t.Fatalf("A's list: %d %s", c, body)
	}
	if strings.Contains(body, "198.51.100") || strings.Contains(body, "203.0.113.8") {
		t.Fatalf("A is told another's IP: %s", body)
	}
	// the handler's own check: the store is stood in for by writes that
	// would take anything, and none of them is reached
	var reachedStore atomic.Int32
	change, remove, attach := areaIPChange, areaIPRemove, areaIPAttach
	t.Cleanup(func() { areaIPChange, areaIPRemove, areaIPAttach = change, remove, attach })
	areaIPChange = func(string, string, time.Time, func(*renewal.IP) error) (renewal.IP, error) {
		reachedStore.Add(1)
		return renewal.IP{}, nil
	}
	areaIPRemove = func(string, string, func(renewal.IP) error) error { reachedStore.Add(1); return nil }
	areaIPAttach = func(string, string, string, string) error { reachedStore.Add(1); return nil }
	for _, ip := range []string{"203.0.113.8", "198.51.100.1"} {
		for _, call := range []struct {
			path string
			in   map[string]any
		}{
			{"/ips/update", map[string]any{"ip": ip, "name": "mine"}},
			{"/ips/delete", map[string]any{"ip": ip}},
			{"/ips/check", map[string]any{"ip": ip, "proxy": pxB}},
			{"/accounts/proxy", map[string]any{"id": w.aKey, "ip": ip, "proxy": pxB}},
		} {
			if c, body := w.post(t, w.a, call.path, call.in); c != http.StatusNotFound {
				t.Errorf("A %s %s: %d %s", call.path, ip, c, body)
			}
		}
	}
	// B's IP under A's address, A's proxy on B's account or the owner's
	for _, in := range []map[string]any{
		{"id": w.aKey, "ip": "203.0.113.7", "proxy": pxB},
		{"id": w.bKey, "ip": "203.0.113.7", "proxy": pxA},
		{"id": w.owner, "ip": "203.0.113.7", "proxy": pxA},
	} {
		if c, body := w.post(t, w.a, "/accounts/proxy", in); c != http.StatusNotFound {
			t.Errorf("A attached %v: %d %s", in, c, body)
		}
	}
	if n := reachedStore.Load(); n != 0 {
		t.Fatalf("the handler handed %d writes of another's IP to the store", n)
	}
	areaIPChange, areaIPRemove, areaIPAttach = change, remove, attach
	ips, _, _ := renewal.IPs()
	for _, x := range ips {
		if x.IP == "203.0.113.8" && x.Name != "东京" || x.IP == "198.51.100.1" && x.Name != "" {
			t.Fatalf("changed: %+v", x)
		}
	}

	// the console: every area's, each with its area, never the owner's
	c, body = w.do("GET", AdminPath+"/ips", lanPeer, w.console, "")
	all := ipsOf(t, body)
	if c != 200 || len(all) != 2 || strings.Contains(body, "198.51.100") {
		t.Fatalf("the console's list: %d %s", c, body)
	}
	for _, x := range all {
		if x.Area != map[string]string{"203.0.113.7": w.aID, "203.0.113.8": w.bID}[x.IP] {
			t.Fatalf("the console's %s names %q", x.IP, x.Area)
		}
	}
	if c, body = w.do("GET", AdminPath+"/ips?area="+w.bID, lanPeer, w.console, ""); c != 200 || len(ipsOf(t, body)) != 1 || ipsOf(t, body)[0].IP != "203.0.113.8" {
		t.Fatalf("?area=: %d %s", c, body)
	}
	if c, body = w.do("GET", AdminPath+"/areas/"+w.aID+"/ips", lanPeer, w.console, ""); c != 200 || len(ipsOf(t, body)) != 1 || ipsOf(t, body)[0].IP != "203.0.113.7" {
		t.Fatalf("/areas/{id}/ips: %d %s", c, body)
	}
	if c, _ = w.do("GET", AdminPath+"/ips?area="+w.bID, lanPeer, w.a, ""); c != http.StatusBadRequest {
		t.Fatalf("A's ?area=: %d", c)
	}
	for _, path := range []string{"/ips", "/ips/update", "/ips/delete", "/ips/check", "/accounts/proxy"} {
		if c, body := w.post(t, w.console, path, map[string]any{"ip": "203.0.113.7", "proxies": []string{}}); c != http.StatusForbidden {
			t.Errorf("the console POST %s: %d %s", path, c, body)
		}
	}
	if c, _ = w.do("GET", AdminPath+"/areas/"+w.aID+"/ips", lanPeer, w.a, ""); c != http.StatusForbidden {
		t.Fatalf("A read /areas/{id}/ips: %d", c)
	}
}

// The store refuses an IP not the area's when the handler lets it
// through (the handler's half of P1 taken out by a stub): each write
// reaches renewal, which says no such IP.
func TestAdminIPsStoreChecksTheArea(t *testing.T) {
	w := newIPsWorld(t)
	w.addIP(t, w.b, "203.0.113.8")
	// stand-in for a handler that forgot to check: the areaIP lookup is
	// skipped by asking the store directly as the handler would
	if _, err := areaIPChange(w.aID, "203.0.113.8", time.Now(), func(x *renewal.IP) error { x.Name = "A's"; return nil }); !errors.Is(err, renewal.ErrIPNotFound) {
		t.Fatalf("the store let A change B's IP: %v", err)
	}
	if err := areaIPRemove(w.aID, "198.51.100.1", nil); !errors.Is(err, renewal.ErrIPNotFound) {
		t.Fatalf("the store let A remove the owner's IP: %v", err)
	}
	if err := areaIPAttach(w.aID, w.aKey, "203.0.113.8", renewal.ProxyID("http://user:pw@"+proxyHost("203.0.113.8")+":"+w.proxy.port())); !errors.Is(err, renewal.ErrIPNotFound) {
		t.Fatalf("the store let A put B's proxy on: %v", err)
	}
	if err := areaIPAttach(w.aID, w.owner, "198.51.100.1", renewal.ProxyID("http://owner:secret@192.168.1.2:7890")); err == nil {
		t.Fatal("the store let A put the owner's proxy on the owner's key")
	}
}

// TestAttachOnlyOwnIPToOwnAccount through the routes (P4): A's proxy on
// A's key is what the key's requests go through, the account says so,
// and the owner's change wins (overridden, 409).
func TestAdminAccountProxyRoute(t *testing.T) {
	w := newIPsWorld(t)
	px := w.addIP(t, w.a, "203.0.113.7")
	c, body := w.post(t, w.a, "/accounts/proxy", map[string]any{"id": w.aKey, "ip": "203.0.113.7", "proxy": px})
	var acc adminAccount
	json.Unmarshal([]byte(body), &acc)
	if c != 200 || acc.Proxy == nil || acc.Proxy.ID != px || acc.Proxy.IP != "203.0.113.7" || acc.Proxy.Overridden {
		t.Fatalf("attached: %d %s", c, body)
	}
	if c, body = w.do("GET", AdminPath+"/ips", lanPeer, w.a, ""); !strings.Contains(body, `"accounts":["`+w.aKey+`"]`) {
		t.Fatalf("the IP's accounts: %s", body)
	}
	if c, body := w.post(t, w.a, "/ips/delete", map[string]any{"ip": "203.0.113.7"}); c != http.StatusConflict || !strings.Contains(body, adminInUse) || !strings.Contains(body, w.aKey) {
		t.Fatalf("deleting an IP in use: %d %s", c, body)
	}
	if c, body := w.post(t, w.a, "/ips/update", map[string]any{"ip": "203.0.113.7", "removeProxies": []string{px}}); c != http.StatusConflict || !strings.Contains(body, adminInUse) {
		t.Fatalf("removing a proxy in use: %d %s", c, body)
	}
	// the owner sets the key's proxy in Providers
	p, _ := provider.Find("plan")
	_, fp, _ := strings.Cut(w.aKey, "/")
	p.AccountProxies = map[string]string{fp: "http://127.0.0.1:7890"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	_, body = w.do("GET", AdminPath+"/accounts", lanPeer, w.a, "")
	if !strings.Contains(body, `"overridden":true`) {
		t.Fatalf("the owner's change isn't overridden: %s", body)
	}
	if c, body := w.post(t, w.a, "/accounts/proxy", map[string]any{"id": w.aKey, "ip": "203.0.113.7", "proxy": px}); c != http.StatusConflict {
		t.Fatalf("put back over the owner's: %d %s", c, body)
	}
	if c, _ := w.post(t, w.a, "/accounts/proxy", map[string]any{"id": w.aKey, "proxy": ""}); c != 200 {
		t.Fatal("detach:", c)
	}
	if p, _ := provider.Find("plan"); p.AccountProxies[fp] != "http://127.0.0.1:7890" {
		t.Fatalf("the area took the owner's proxy off: %v", p.AccountProxies)
	}
}

// TestAreaProxyMustBePublic at the routes (P6): an area adds no proxy at
// an address that isn't public, in any spelling, nor a name that
// resolves to one; nothing is written.
func TestAdminAreaProxyMustBePublic(t *testing.T) {
	w := newIPsWorld(t)
	w.answer.Store("127.0.0.1")
	for _, p := range []string{
		"http://127.0.0.1:7890", "socks5://[::1]:1080", "http://localhost:8080", "http://10.0.0.1:3128",
		"http://100.64.1.1:1080", "http://169.254.169.254:80", "http://[::ffff:127.0.0.1]:80", "http://0.0.0.0:80",
		"http://proxy.example:3128", "http://2130706433:80", "http://intranet:80",
	} {
		c, body := w.post(t, w.a, "/ips", map[string]any{"ip": "203.0.113.7", "proxies": []string{p}})
		if c != http.StatusBadRequest || !strings.Contains(body, adminProxyNotPublic) {
			t.Errorf("%s: %d %s", p, c, body)
		}
	}
	c, body := w.post(t, w.a, "/ips", map[string]any{"ip": "203.0.113.7", "proxies": []string{}})
	if c != http.StatusCreated {
		t.Fatal(c, body)
	}
	if c, body := w.post(t, w.a, "/ips/update", map[string]any{"ip": "203.0.113.7", "addProxies": []string{"http://192.168.1.1:80"}}); c != http.StatusBadRequest || !strings.Contains(body, adminProxyNotPublic) {
		t.Fatalf("an added proxy: %d %s", c, body)
	}
	ips, _, _ := renewal.IPs()
	for _, x := range ips {
		if x.Area != "" && len(x.Proxies) > 0 {
			t.Fatalf("kept: %+v", x)
		}
	}
	// 409 that names no owner, counted as the add routes' are
	for _, ip := range []string{"198.51.100.1", "203.0.113.7"} {
		if c, body := w.post(t, w.b, "/ips", map[string]any{"ip": ip, "proxies": []string{}}); c != http.StatusConflict || strings.Contains(body, w.aID) || strings.Contains(body, "owner") {
			t.Fatalf("B adding %s: %d %s", ip, c, body)
		}
	}
}

// TestAreaProxyDialRefusesRebind at the gateway (P7): a proxy saved at a
// public address whose name now answers this computer's is refused before
// any connection, for a check and for a real request of the account it
// is on.
func TestAdminAreaProxyRebindRefused(t *testing.T) {
	w := newIPsWorld(t)
	// a vendor by a name, which a request goes to through a proxy (a
	// loopback one is gone to direct); only the area's key on
	if err := provider.Delete("other"); err != nil { // no other way for m1
		t.Fatal(err)
	}
	p, _ := provider.Find("plan")
	p.Chat = "http://vendor.example/v1"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{w.owner, w.bKey} {
		_, fp, _ := strings.Cut(ref, "/")
		if err := provider.SetKeyOn("plan", fp, false); err != nil {
			t.Fatal(err)
		}
	}
	px := w.addIP(t, w.a, "203.0.113.7")
	if c, body := w.post(t, w.a, "/accounts/proxy", map[string]any{"id": w.aKey, "ip": "203.0.113.7", "proxy": px}); c != 200 {
		t.Fatal(c, body)
	}
	ask := func() int {
		c, _ := w.do("POST", "/v1/chat/completions", loopback, "", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
		return c
	}
	if c := ask(); c != 200 {
		t.Fatalf("through the area's proxy at a public address: %d", c)
	}
	if w.proxy.conns.Load() == 0 {
		t.Fatal("the turn didn't go through the area's proxy")
	}
	w.proxy.drop() // the next request dials again
	before := w.proxy.conns.Load()
	w.answer.Store("127.0.0.1")
	if c := ask(); c == 200 {
		t.Fatalf("a turn went through the rebound proxy (%d connections, %d before; vendor reached %d)", w.proxy.conns.Load(), before, w.reached.Load())
	}
	c, body := w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": px})
	if c != 200 || !strings.Contains(body, `"error":"not_public"`) || strings.Contains(body, `"ok":true`) {
		t.Fatalf("the check of a rebound proxy: %d %s", c, body)
	}
	if n := w.proxy.conns.Load(); n != before {
		t.Fatalf("connections to the rebound proxy: %d, want %d", n, before)
	}
}

// TestAreaProxyCheckRedacted (P8): a check says a category alone: no
// address, login, text the proxy sent, nor magpie's own exit.
func TestAreaProxyCheckRedacted(t *testing.T) {
	w := newIPsWorld(t)
	px := w.addIP(t, w.a, "203.0.113.7")
	c, body := w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": px})
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	if c != 200 || out["ok"] != true || out["matches"] != true || out["ip"] != "203.0.113.7" || out["loc"] != "JP" || out["proxy"] != px {
		t.Fatalf("a check: %d %s", c, body)
	}
	for k := range out {
		if !strings.Contains(" proxy ok ip loc ms matches error ", " "+k+" ") {
			t.Errorf("a check tells %q", k)
		}
	}
	w.proxy.status.Store(407)
	if _, body = w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": px}); !strings.Contains(body, `"error":"proxy_auth"`) {
		t.Fatalf("407: %s", body)
	}
	w.proxy.status.Store(502)
	_, body = w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": px})
	if !strings.Contains(body, `"error":"http_status"`) {
		t.Fatalf("502: %s", body)
	}
	for _, leak := range []string{"192.168", "router", ".example", "user", "pw", w.proxy.port(), "direct"} {
		if strings.Contains(body, leak) {
			t.Errorf("a failed check tells %q: %s", leak, body)
		}
	}
	w.proxy.stop()
	if _, body = w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": px}); !strings.Contains(body, `"error":"refused"`) || strings.Contains(body, "127.0.0.1") {
		t.Fatalf("refused: %s", body)
	}
}

// TestAreaProxyCheckHeld (P9): an area's 11th check in the window is
// 429 with Retry-After and makes no connection; another area isn't
// held, and a check running holds the area's next.
func TestAreaProxyCheckHeld(t *testing.T) {
	w := newIPsWorld(t)
	pxA := w.addIP(t, w.a, "203.0.113.7")
	pxB := w.addIP(t, w.b, "203.0.113.8")
	check := func(secret, ip, px string) (int, string) {
		return w.post(t, secret, "/ips/check", map[string]any{"ip": ip, "proxy": px})
	}
	for i := range checksMost {
		if c, body := check(w.a, "203.0.113.7", pxA); c != 200 {
			t.Fatal(i, c, body)
		}
	}
	before := w.proxy.conns.Load()
	c, body := check(w.a, "203.0.113.7", pxA)
	if c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyChecks) {
		t.Fatalf("the 11th: %d %s", c, body)
	}
	if n := w.proxy.conns.Load(); n != before {
		t.Fatalf("a held check connected: %d, want %d", n, before)
	}
	if c, body := check(w.b, "203.0.113.8", pxB); c != 200 {
		t.Fatalf("B was held: %d %s", c, body)
	}
	// one at a time
	var p proxyChecks
	if _, _, ok := p.begin([]string{"x"}, time.Now()); !ok {
		t.Fatal("first")
	}
	if wait, _, ok := p.begin([]string{"x"}, time.Now()); ok || wait <= 0 {
		t.Fatal("a second at once ran")
	}
	if _, _, ok := p.begin([]string{"y"}, time.Now()); !ok {
		t.Fatal("another area held")
	}
	p.end("x")
	if _, _, ok := p.begin([]string{"x"}, time.Now()); !ok {
		t.Fatal("after the first ended")
	}
}

// TestOwnerGivenProxyNotCheckable (P10): an IP the owner gave an area is
// the area's to list and put on its accounts, never to check, change
// the proxies of or delete; its proxy may be on the owner's network.
func TestOwnerGivenProxyNotCheckable(t *testing.T) {
	w := newIPsWorld(t)
	if err := renewal.GiveIP("198.51.100.1", w.aID); err != nil {
		t.Fatal(err)
	}
	c, body := w.do("GET", AdminPath+"/ips", lanPeer, w.a, "")
	got := ipsOf(t, body)
	if c != 200 || len(got) != 1 || !got[0].GivenByOwner || got[0].Proxies[0].URL != "http://192.168.1.2:7890" || strings.Contains(body, "secret") {
		t.Fatalf("the given IP: %d %s", c, body)
	}
	px := got[0].Proxies[0].ID
	before := w.proxy.conns.Load()
	if c, body := w.post(t, w.a, "/ips/check", map[string]any{"ip": "198.51.100.1", "proxy": px}); c != http.StatusBadRequest {
		t.Fatalf("checked: %d %s", c, body)
	}
	if c, _ := w.post(t, w.a, "/ips/update", map[string]any{"ip": "198.51.100.1", "removeProxies": []string{px}}); c != http.StatusBadRequest {
		t.Fatalf("its proxies changed: %d", c)
	}
	if c, _ := w.post(t, w.a, "/ips/delete", map[string]any{"ip": "198.51.100.1"}); c != http.StatusBadRequest {
		t.Fatalf("deleted: %d", c)
	}
	if c, body := w.post(t, w.a, "/accounts/proxy", map[string]any{"id": w.aKey, "ip": "198.51.100.1", "proxy": px}); c != 200 {
		t.Fatalf("attached: %d %s", c, body)
	}
	if w.proxy.conns.Load() != before {
		t.Fatal("connected")
	}
	// taken back, it leaves the account
	if err := renewal.TakeBackIP("198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SweepAreaProxies(); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("plan"); len(p.AccountProxies) != 0 {
		t.Fatalf("taken back, still on: %v", p.AccountProxies)
	}
}

// An ips.json that can't be read is 503 for every IP route and a write
// goes nowhere: never "no IPs".
func TestAdminIPsUnreadable(t *testing.T) {
	w := newIPsWorld(t)
	path := filepath.Join(appdir.Config(), "ips.json")
	os.WriteFile(path, []byte(`{"ips":[`), 0o600)
	for _, call := range []struct{ method, path, secret, body string }{
		{"GET", "/ips", w.a, ""}, {"GET", "/ips", w.console, ""},
		{"POST", "/ips", w.a, `{"ip":"203.0.113.9","proxies":[]}`},
		{"POST", "/ips/update", w.a, `{"ip":"203.0.113.9"}`},
		{"POST", "/accounts/proxy", w.a, `{"id":"` + w.aKey + `","ip":"203.0.113.9","proxy":"px_0000000000"}`},
	} {
		c, body := w.do(call.method, AdminPath+call.path, lanPeer, call.secret, call.body)
		if c != http.StatusServiceUnavailable || !strings.Contains(body, adminIPsUnread) {
			t.Errorf("%s %s: %d %s", call.method, call.path, c, body)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != `{"ips":[` {
		t.Fatalf("written over: %s", b)
	}
	_ = area.ErrNotFound
}
