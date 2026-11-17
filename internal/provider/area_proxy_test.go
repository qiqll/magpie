package provider

import (
	"errors"
	"testing"
	"time"

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
	if err := setOwnProxy("relay", now, ownerProxy, ""); err != nil {
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
