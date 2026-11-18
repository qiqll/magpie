package provider

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/netproxy"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/renewal"
)

const (
	areaProxyA = "http://u:p@93.184.216.34:3128"
	areaProxyB = "socks5://93.184.216.35:1080"
	ownerProxy = "http://127.0.0.1:7890"
)

// areaProxyHome is a relay of keys, two of them A's, one Codex account of
// A's, and an IP of A's and of B's with a proxy each.
func areaProxyHome(t *testing.T) (a, b area.Area, keyRefs []string, login string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-owner-00000001", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	a, _ = area.Create("学习", nil, nil)
	b, _ = area.Create("工作", nil, nil)
	for _, k := range []string{"sk-area-0000000001", "sk-area-0000000002"} {
		if err := AddKey("relay", "", k, ""); err != nil {
			t.Fatal(err)
		}
		ref := "relay/" + KeyID(k)
		area.Claim(a.ID, ref)
		keyRefs = append(keyRefs, ref)
	}
	if _, _, err := addLoginOf(codexTestLogin(t, "Area@Example.com"), true); err != nil {
		t.Fatal(err)
	}
	login = "codex/" + LoginID("codex", "Area@Example.com")
	if err := area.Claim(a.ID, login); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := renewal.AddAreaIP(a.ID, renewal.IP{IP: "203.0.113.9", Proxies: []string{areaProxyA}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := renewal.AddAreaIP(b.ID, renewal.IP{IP: "203.0.113.10", Proxies: []string{areaProxyB}}, now); err != nil {
		t.Fatal(err)
	}
	return a, b, keyRefs, login
}

// keyProxy is the proxy relay's key ref goes through.
func keyProxy(t *testing.T, ref string) string {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range p.Keys {
		if "relay/"+keyID(k.Key) == ref {
			one := *p
			one.Key = k.Key
			return one.ProxyChoice()
		}
	}
	return "missing"
}

// TestAttachOnlyOwnIPToOwnAccount (P4): an area puts a proxy of its own
// IP on its own account alone, written where ProxyChoice reads it: a key
// by its fingerprint, a subscription account by its login in lower case.
func TestAttachOnlyOwnIPToOwnAccount(t *testing.T) {
	a, _, keys, login := areaProxyHome(t)
	idA, idB := renewal.ProxyID(areaProxyA), renewal.ProxyID(areaProxyB)
	if err := AttachAreaProxy(a.ID, keys[0], "203.0.113.10", idB); !errors.Is(err, renewal.ErrIPNotFound) {
		t.Fatalf("B's proxy on A's key: %v", err)
	}
	if err := AttachAreaProxy(a.ID, keys[0], "203.0.113.9", idB); !errors.Is(err, renewal.ErrIPNotFound) {
		t.Fatalf("B's proxy id under A's IP: %v", err)
	}
	owners := "relay/" + KeyID("sk-owner-00000001")
	if err := AttachAreaProxy(a.ID, owners, "203.0.113.9", idA); !errors.Is(err, area.ErrNotOwned) {
		t.Fatalf("A's proxy on the owner's key: %v", err)
	}
	if got := keyProxy(t, keys[0]); got != "" {
		t.Fatalf("a refused attach wrote %q", got)
	}
	for _, ref := range []string{keys[0], login} {
		if err := AttachAreaProxy(a.ID, ref, "203.0.113.9", idA); err != nil {
			t.Fatal(ref, err)
		}
	}
	if got := keyProxy(t, keys[0]); got != areaProxyA {
		t.Fatalf("the key goes through %q", got)
	}
	if got := keyProxy(t, keys[1]); got != "" {
		t.Fatalf("the other key goes through %q", got)
	}
	p, _ := storedPicks("codex")
	if p.AccountProxies["area@example.com"] != areaProxyA || ProxyOfLogin("codex", "Area@Example.com") != areaProxyA {
		t.Fatalf("the account's proxy: %v", p.AccountProxies)
	}
	got, _ := area.Get(a.ID)
	for _, c := range got.Accounts {
		if c.Ref == keys[0] && (c.Proxy == nil || c.Proxy.ID != idA || c.Proxy.Key != KeyID("sk-area-0000000001")) {
			t.Fatalf("the record: %+v", c.Proxy)
		}
	}
	if err := DetachAreaProxy(a.ID, keys[0]); err != nil || keyProxy(t, keys[0]) != "" {
		t.Fatalf("detached: %q %v", keyProxy(t, keys[0]), err)
	}
}

// TestReleaseClearsAreaProxy (P5): an account its area lets go of
// (taken back by the owner, forgotten) goes through the area's proxy no
// longer; a key's new value keeps it; the owner's own value is never
// taken off, and the area is told it is overridden and can't put its
// own back.
func TestReleaseClearsAreaProxy(t *testing.T) {
	a, _, keys, login := areaProxyHome(t)
	id := renewal.ProxyID(areaProxyA)
	for _, ref := range append(keys, login) {
		if err := AttachAreaProxy(a.ID, ref, "203.0.113.9", id); err != nil {
			t.Fatal(err)
		}
	}
	// the owner takes the first key back
	if err := area.Release(keys[0]); err != nil {
		t.Fatal(err)
	}
	if got := keyProxy(t, keys[0]); got != "" {
		t.Fatalf("taken back, the key still goes through %q", got)
	}
	// the account is forgotten
	if err := ForgetLogin("codex", "Area@Example.com"); err != nil {
		t.Fatal(err)
	}
	if p, _ := storedPicks("codex"); p.AccountProxies["area@example.com"] != "" {
		t.Fatalf("forgotten, the account still has %v", p.AccountProxies)
	}
	// the second key's value replaced: the proxy moves with it
	_, now, err := ReplaceKey("relay", KeyID("sk-area-0000000002"), "sk-area-0000000003")
	if err != nil {
		t.Fatal(err)
	}
	moved := "relay/" + now
	if got := keyProxy(t, moved); got != areaProxyA {
		t.Fatalf("the new value goes through %q", got)
	}
	got, _ := area.Get(a.ID)
	if len(got.Accounts) != 1 || got.Accounts[0].Proxy == nil || got.Accounts[0].Proxy.Key != now || AreaProxyOverridden("relay", *got.Accounts[0].Proxy) {
		t.Fatalf("after the replace: %+v", got.Accounts)
	}
	// the owner sets it: overridden, kept on release
	if err := setOwnProxy("relay", now, ownerProxy, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = area.Get(a.ID)
	if !AreaProxyOverridden("relay", *got.Accounts[0].Proxy) {
		t.Fatal("the owner's value is not overridden")
	}
	if err := AttachAreaProxy(a.ID, moved, "203.0.113.9", id); !errors.Is(err, ErrOverridden) {
		t.Fatalf("the area put its proxy over the owner's: %v", err)
	}
	if err := DetachAreaProxy(a.ID, moved); err != nil || keyProxy(t, moved) != ownerProxy {
		t.Fatalf("the area took the owner's value off: %q %v", keyProxy(t, moved), err)
	}
	// with the record gone the value is the owner's still
	if err := AttachAreaProxy(a.ID, moved, "203.0.113.9", id); !errors.Is(err, ErrOverridden) || keyProxy(t, moved) != ownerProxy {
		t.Fatalf("with no record, put over the owner's value: %v", err)
	}
}

// SweepAreaProxies takes an area's proxy off its accounts once the owner
// removed its IP, and leaves the owner's value.
func TestSweepAreaProxiesAfterOwnerRemoval(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	id := renewal.ProxyID(areaProxyA)
	if err := AttachAreaProxy(a.ID, keys[0], "203.0.113.9", id); err != nil {
		t.Fatal(err)
	}
	ips, rev, _ := renewal.IPs()
	if err := renewal.SetIPs(ips[1:], rev, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SweepAreaProxies(); err != nil {
		t.Fatal(err)
	}
	if got := keyProxy(t, keys[0]); got != "" {
		t.Fatalf("the IP removed, the key still goes through %q", got)
	}
	if got, _ := area.Get(a.ID); got.Accounts[0].Proxy != nil {
		t.Fatalf("the record stays: %+v", got.Accounts[0].Proxy)
	}
}

// dialThrough sends one request through choice by netproxy's dispatcher,
// as the gateway's upstreams do, and says what it returned.
func dialThrough(t *testing.T, choice string) error {
	t.Helper()
	base := &http.Transport{Proxy: netproxy.Func, DisableKeepAlives: true}
	defer base.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(netproxy.With(context.Background(), choice), http.MethodGet, "http://vendor.example/v1/models", nil)
	res, err := (&http.Client{Transport: netproxy.Dispatch(base)}).Do(req)
	if err == nil {
		res.Body.Close()
	}
	return err
}

// countingListener is an HTTP proxy on loopback answering 200, counting
// connections.
func countingListener(t *testing.T) (port string, conns *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conns = &atomic.Int32{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				if _, err := http.ReadRequest(bufio.NewReader(c)); err == nil {
					io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port, conns
}

// rebindHome has every *.example name answer *answer.
func rebindHome(t *testing.T, answer *atomic.Value) {
	t.Helper()
	was := netproxy.LookupIP
	t.Cleanup(func() { netproxy.LookupIP = was })
	netproxy.LookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr(answer.Load().(string))}, nil
	}
}

// The review's High (a): an IP removed while an attach is between its
// check and its write. The removal waits for the attach (ipsMu), then
// finds the proxy on the account and is refused: no URL is ever written
// that ips.json no longer lists.
func TestAttachAndRemoveDontPass(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	id := renewal.ProxyID(areaProxyA)
	removed := make(chan error, 1)
	t.Cleanup(func() { beforeAttachWrite, renewal.IPsWait = nil, nil })
	beforeAttachWrite = func() {
		beforeAttachWrite = nil
		waiting := make(chan struct{})
		renewal.IPsWait = func() { close(waiting) }
		go func() { removed <- renewal.RemoveAreaIP(a.ID, "203.0.113.9", AreaIPInUse(a.ID, nil)) }()
		select {
		case <-waiting: // held by the attach: it goes on, then the removal
		case err := <-removed: // ran through: put back for the check below
			removed <- err
		}
	}
	if err := AttachAreaProxy(a.ID, keys[0], "203.0.113.9", id); err != nil && !errors.Is(err, renewal.ErrIPNotFound) {
		t.Fatal(err)
	}
	err := <-removed
	ips, _, _ := renewal.IPs()
	listed := slices.ContainsFunc(ips, func(x renewal.IP) bool { return x.IP == "203.0.113.9" })
	if got := keyProxy(t, keys[0]); got != "" && !listed {
		t.Fatalf("the IP was removed (%v) and the key goes through %q all the same", err, got)
	}
	var used *InUseError
	if !errors.As(err, &used) || !listed {
		t.Fatalf("the removal after the attach: %v (listed %v)", err, listed)
	}
}

// The review's High (b) and Medium 1: a proxy on an area's account is
// held to public addresses by areas.json's record even once ips.json no
// longer lists it, or can't be read at all; the owner's own proxy on
// loopback is not held for an unreadable ips.json.
func TestAttachedProxyHeldWhateverIPsSays(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	port, conns := countingListener(t)
	url := "http://p.example:" + port
	answer := &atomic.Value{}
	answer.Store("93.184.216.34")
	rebindHome(t, answer)
	if _, err := renewal.AddAreaIP(a.ID, renewal.IP{IP: "203.0.113.20", Proxies: []string{url}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := AttachAreaProxy(a.ID, keys[0], "203.0.113.20", renewal.ProxyID(url)); err != nil {
		t.Fatal(err)
	}
	answer.Store("127.0.0.1")
	// ips.json written by something that knows no areas: the IP gone, no
	// sweep
	ips, rev, _ := renewal.IPs()
	if err := renewal.SetIPs(slices.DeleteFunc(ips, func(x renewal.IP) bool { return x.IP == "203.0.113.20" }), rev, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := dialThrough(t, url); !errors.Is(err, netproxy.ErrNotPublic) {
		t.Fatalf("an orphaned area proxy rebound home: %v", err)
	}
	// and ips.json unreadable: still held; the owner's loopback proxy not
	os.WriteFile(filepath.Join(appdir.Config(), "ips.json"), []byte(`{"ips":[`), 0o600)
	if err := dialThrough(t, url); !errors.Is(err, netproxy.ErrNotPublic) {
		t.Fatalf("with ips.json unreadable: %v", err)
	}
	if n := conns.Load(); n != 0 {
		t.Fatalf("connections to the rebound proxy: %d", n)
	}
	if err := dialThrough(t, "http://127.0.0.1:"+port); err != nil {
		t.Fatalf("the owner's loopback proxy with ips.json unreadable: %v", err)
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("the owner's proxy got %d connections, want 1", n)
	}
}

// The review's Medium 2: an owner's value written between the attach's
// look and its write (in providers.json's lock) is not written over.
func TestAttachLeavesAnOwnerWriteOfTheSameMoment(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	fp := strings.TrimPrefix(keys[0], "relay/")
	t.Cleanup(func() { beforeOwnProxyWrite = nil })
	beforeOwnProxyWrite = func() {
		beforeOwnProxyWrite = nil
		// the owner's Providers editor, at that moment
		p, _ := Find("relay")
		p.AccountProxies = map[string]string{fp: ownerProxy}
		if err := Save(*p); err != nil {
			t.Error(err)
		}
	}
	err := AttachAreaProxy(a.ID, keys[0], "203.0.113.9", renewal.ProxyID(areaProxyA))
	if !errors.Is(err, ErrOverridden) {
		t.Fatalf("attach over the owner's value of the same moment: %v", err)
	}
	if got := keyProxy(t, keys[0]); got != ownerProxy {
		t.Fatalf("the key goes through %q, want the owner's", got)
	}
	if rec := recordOf(a.ID, keys[0]); rec != nil {
		t.Fatalf("a record kept for a refused attach: %+v", rec)
	}
}

// A sweep detaches the record it read alone: a direct the area put on
// the account since (or any other record) stays, its own value with it.
func TestAreaSweepLeavesARecordSetSince(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	ref := keys[0]
	if err := AttachAreaProxy(a.ID, ref, "203.0.113.9", renewal.ProxyID(areaProxyA)); err != nil {
		t.Fatal(err)
	}
	read := recordOf(a.ID, ref)
	if read == nil {
		t.Fatal("no record")
	}
	stale := *read
	if err := AttachAreaDirect(a.ID, ref); err != nil {
		t.Fatal(err)
	}
	if err := detachIf(a.ID, ref, &stale); err != nil {
		t.Fatal(err)
	}
	if rec := recordOf(a.ID, ref); rec == nil || !rec.Direct {
		t.Fatalf("the direct set since: %+v", rec)
	}
	if v := keyProxy(t, ref); v != "direct" {
		t.Fatalf("its own value: %q", v)
	}
	now := *recordOf(a.ID, ref)
	if err := detachIf(a.ID, ref, &now); err != nil || recordOf(a.ID, ref) != nil {
		t.Fatal("the record read now stays:", err)
	}
}

// TestAreaSweepBesideARereadMove (T-214 code review M1): a sweep that read
// ips.json before a reread moved a proxy, and areas.json after, takes the
// moved proxy off no account: the detach is judged again with both files
// read under ipsMu.
func TestAreaSweepBesideARereadMove(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	id := renewal.ProxyID(areaProxyA)
	if err := AttachAreaProxy(a.ID, keys[0], "203.0.113.9", id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sweepBetween = nil })
	moved := false
	sweepBetween = func() {
		if moved {
			return
		}
		moved = true
		if err := renewal.MoveAreaProxy(a.ID, "203.0.113.9", id, "203.0.113.20", time.Now(), func(to string) error {
			return area.MoveProxy(a.ID, "203.0.113.9", id, to)
		}); err != nil {
			t.Error(err)
		}
	}
	if err := SweepAreaProxies(); err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Fatal("the move didn't run")
	}
	if got := keyProxy(t, keys[0]); got != areaProxyA {
		t.Fatalf("the moved proxy was taken off: the key goes through %q", got)
	}
	if got, _ := area.Get(a.ID); got.Accounts[0].Proxy == nil || got.Accounts[0].Proxy.IP != "203.0.113.20" {
		t.Fatalf("the record: %+v", got.Accounts[0].Proxy)
	}
	// a sweep after it still takes off what is gone
	ips, rev, _ := renewal.IPs()
	keep := ips[:0]
	for _, x := range ips {
		if x.IP != "203.0.113.20" {
			keep = append(keep, x)
		}
	}
	if err := renewal.SetIPs(keep, rev, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SweepAreaProxies(); err != nil {
		t.Fatal(err)
	}
	if got := keyProxy(t, keys[0]); got != "" {
		t.Fatalf("the IP removed, the key still goes through %q", got)
	}
}
