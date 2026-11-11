package renewal

import (
	"os"
	"path/filepath"
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
	if ips, err := IPs(); err != nil || ips != nil {
		t.Fatalf("no file: %v %v", ips, err)
	}
	at := now.Add(-26 * 24 * time.Hour).Truncate(time.Second)
	in := []IP{{IP: " 203.0.113.7 ", Name: " Tokyo ", Opened: at}, {IP: "2001:DB8::1"}}
	if err := SetIPs(in, now); err != nil {
		t.Fatal(err)
	}
	if in[0].IP != " 203.0.113.7 " {
		t.Error("the list sent was changed")
	}
	ips, err := IPs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 2 || ips[0].IP != "203.0.113.7" || ips[0].Name != "Tokyo" || !ips[0].Opened.Equal(at) || ips[1].IP != "2001:db8::1" || !ips[1].Opened.IsZero() {
		t.Fatalf("kept %+v", ips)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"opened": "0001`) {
		t.Errorf("an IP with no time is written with one: %s", raw)
	}
	if err := SetIPs(nil, now); err != nil {
		t.Fatal(err)
	}
	if ips, err := IPs(); err != nil || len(ips) != 0 {
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
	if err := SetIPs([]IP{{IP: "198.51.100.1"}}, now); err != nil {
		t.Fatal(err)
	}
	for name, ips := range map[string][]IP{
		"not an address": {{IP: "proxy.example.com"}},
		"none":           {{IP: ""}},
		"twice":          {{IP: "198.51.100.1"}, {IP: " 198.51.100.1"}},
		"a long name":    {{IP: "198.51.100.2", Name: strings.Repeat("名", 81)}},
		"a year typo":    {{IP: "198.51.100.2", Opened: time.Date(202, 1, 1, 0, 0, 0, 0, time.UTC)}},
		"far ahead":      {{IP: "198.51.100.2", Opened: now.AddDate(1, 0, 0)}},
		"too many":       make([]IP, MaxIPs+1),
	} {
		if err := SetIPs(ips, now); err == nil {
			t.Errorf("%s: kept", name)
		}
	}
	if ips, _ := IPs(); len(ips) != 1 || ips[0].IP != "198.51.100.1" {
		t.Fatalf("after the refusals: %+v", ips)
	}

	// a file that can't be parsed is an error, and is never written over
	if err := os.WriteFile(path, []byte(`{"ips": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := IPs(); err == nil {
		t.Error("a broken file read as a list")
	}
	if err := SetIPs([]IP{{IP: "198.51.100.3"}}, now); err == nil {
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
	ips, err := IPs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 2 || ips[0].IP != "192.0.2.9" || !ips[0].Opened.IsZero() || ips[1].Name != "HK" || !ips[1].Opened.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("read %+v", ips)
	}
}
