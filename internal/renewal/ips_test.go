package renewal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func ipsHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	return filepath.Join(h, ".config", "magpie", "ips.json")
}

// The list is kept whole, each address written as Go writes it and its
// name trimmed, in the user's order; none before the first save.
func TestSetIPs(t *testing.T) {
	path := ipsHome(t)
	now := time.Now()
	ips, rev, err := IPs()
	if err != nil || ips != nil || rev != "" {
		t.Fatalf("no file: %v %q %v", ips, rev, err)
	}
	at := now.Add(-26 * 24 * time.Hour).Truncate(time.Second)
	in := []IP{{IP: " 203.0.113.7 ", Name: " Tokyo ", Opened: at}, {IP: "2001:DB8::1"}}
	if err := SetIPs(in, rev, now); err != nil {
		t.Fatal(err)
	}
	if in[0].IP != " 203.0.113.7 " {
		t.Error("the list sent was changed")
	}
	ips, rev, err = IPs()
	if err != nil || rev == "" {
		t.Fatal(rev, err)
	}
	if len(ips) != 2 || ips[0].IP != "203.0.113.7" || ips[0].Name != "Tokyo" || !ips[0].Opened.Equal(at) || ips[1].IP != "2001:db8::1" || !ips[1].Opened.IsZero() {
		t.Fatalf("kept %+v", ips)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"opened": "0001`) {
		t.Errorf("an IP with no time is written with one: %s", raw)
	}
	if err := SetIPs(nil, rev, now); err != nil {
		t.Fatal(err)
	}
	if ips, _, err := IPs(); err != nil || len(ips) != 0 {
		t.Fatalf("emptied: %v %v", ips, err)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".ips-*"))
	if len(matches) != 0 {
		t.Errorf("temp files left: %v", matches)
	}
}

// What can't be kept is refused, and the file stays as it was.
func TestSetIPsRefused(t *testing.T) {
	path := ipsHome(t)
	now := time.Now()
	if err := SetIPs([]IP{{IP: "198.51.100.1"}}, "", now); err != nil {
		t.Fatal(err)
	}
	_, rev, _ := IPs()
	for name, ips := range map[string][]IP{
		"not an address": {{IP: "proxy.example.com"}},
		"none":           {{IP: ""}},
		"twice":          {{IP: "198.51.100.1"}, {IP: " 198.51.100.1"}},
		"a long name":    {{IP: "198.51.100.2", Name: strings.Repeat("名", 81)}},
		"a year typo":    {{IP: "198.51.100.2", Opened: time.Date(202, 1, 1, 0, 0, 0, 0, time.UTC)}},
		"far ahead":      {{IP: "198.51.100.2", Opened: now.AddDate(1, 0, 0)}},
		"too many":       make([]IP, MaxIPs+1),
	} {
		if err := SetIPs(ips, rev, now); err == nil {
			t.Errorf("%s: kept", name)
		}
	}
	if ips, _, _ := IPs(); len(ips) != 1 || ips[0].IP != "198.51.100.1" {
		t.Fatalf("after the refusals: %+v", ips)
	}

	// a file that can't be parsed is an error, and is never written over
	if err := os.WriteFile(path, []byte(`{"ips": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := IPs(); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("a broken file read as a list, or its error doesn't name it: %v", err)
	}
	if err := SetIPs([]IP{{IP: "198.51.100.3"}}, rev, now); err == nil {
		t.Error("a broken file was written over")
	}
	if raw, _ := os.ReadFile(path); string(raw) != `{"ips": [` {
		t.Errorf("the broken file is now %s", raw)
	}
}

// A file with fields this magpie doesn't know, or none of the optional
// ones, reads as it is.
func TestIPsReadAsWritten(t *testing.T) {
	path := ipsHome(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"ips":[{"ip":"192.0.2.9"},{"ip":"192.0.2.10","name":"HK","opened":"2026-11-01T00:00:00Z","later":1}],"more":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ips, _, err := IPs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 2 || ips[0].IP != "192.0.2.9" || !ips[0].Opened.IsZero() || ips[1].Name != "HK" || !ips[1].Opened.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("read %+v", ips)
	}
}

// A list made from an older read is refused, so another window's edit is
// never written over (T-155 review M2); one made from the read now is kept.
func TestSetIPsMadeFromAnOlderRead(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	if err := SetIPs([]IP{{IP: "198.51.100.1"}}, "", now); err != nil {
		t.Fatal(err)
	}
	_, mine, _ := IPs()
	// another window adds one from the same read
	if err := SetIPs([]IP{{IP: "198.51.100.1"}, {IP: "198.51.100.2"}}, mine, now); err != nil {
		t.Fatal(err)
	}
	if err := SetIPs(nil, mine, now); !errors.Is(err, ErrIPsChanged) {
		t.Fatalf("a removal made from the older read: %v", err)
	}
	if err := SetIPs([]IP{{IP: "198.51.100.3"}}, "", now); !errors.Is(err, ErrIPsChanged) {
		t.Fatalf("a list made from no read over a file: %v", err)
	}
	ips, rev, _ := IPs()
	if len(ips) != 2 {
		t.Fatalf("the other window's list is gone: %+v", ips)
	}
	if err := SetIPs(ips[:1], rev, now); err != nil {
		t.Fatal(err)
	}
}

// A time kept before is not checked again: a clock set back since leaves
// it more than a period ahead, and it holds up no other edit (T-155
// review L2). A new time that far ahead is still refused.
func TestSetIPsKeepsAnOldTime(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	ahead := now.Add(20 * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := SetIPs([]IP{{IP: "198.51.100.1", Opened: ahead}}, "", now); err != nil {
		t.Fatal(err)
	}
	back := now.Add(-15 * 24 * time.Hour) // the clock set back
	ips, rev, _ := IPs()
	if err := SetIPs(append(ips, IP{IP: "198.51.100.2"}), rev, back); err != nil {
		t.Fatalf("an edit beside a time kept before: %v", err)
	}
	ips, rev, _ = IPs()
	if err := SetIPs(append(ips, IP{IP: "198.51.100.3", Opened: ahead}), rev, back); err == nil {
		t.Fatal("a new time over a period ahead kept")
	}
}

// The proxies an IP is the exit of (T-170) are kept trimmed and once
// each, the list sent left as it was; one that isn't a proxy, "direct",
// or more than MaxIPProxies of them are refused.
func TestSetIPsProxies(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	sent := []string{" socks5://u:p@203.0.113.7:1080 ", "http://hk.example:3128", "socks5://u:p@203.0.113.7:1080"}
	in := []IP{{IP: "203.0.113.7", Proxies: sent}}
	if err := SetIPs(in, "", now); err != nil {
		t.Fatal(err)
	}
	if sent[0] != " socks5://u:p@203.0.113.7:1080 " || len(in[0].Proxies) != 3 {
		t.Errorf("the list sent was changed: %q", in[0].Proxies)
	}
	ips, rev, err := IPs()
	if err != nil || len(ips) != 1 || !slices.Equal(ips[0].Proxies, []string{"socks5://u:p@203.0.113.7:1080", "http://hk.example:3128"}) {
		t.Fatalf("kept %+v %v", ips, err)
	}
	many := make([]string, MaxIPProxies+1)
	for i := range many {
		many[i] = fmt.Sprintf("http://203.0.113.7:%d", 1000+i)
	}
	for name, ps := range map[string][]string{
		"not a proxy": {"ftp://203.0.113.7"},
		"direct":      {"direct"},
		"none":        {"  "},
		"too many":    many,
	} {
		if err := SetIPs([]IP{{IP: "203.0.113.7", Proxies: ps}}, rev, now); err == nil {
			t.Errorf("%s: kept", name)
		}
	}
	if ips, _, _ := IPs(); len(ips) != 1 || len(ips[0].Proxies) != 2 {
		t.Fatalf("after the refusals: %+v", ips)
	}
}
