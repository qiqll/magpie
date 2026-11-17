package renewal

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestOldIPsFileIsOwners (P3): an ips.json from before areas holds only
// the owner's IPs, and none of its proxies is held to public addresses.
func TestOldIPsFileIsOwners(t *testing.T) {
	path := ipsHome(t)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"ips":[{"ip":"203.0.113.7","name":"东京","proxies":["socks5://u:p@127.0.0.1:1080"]}]}`), 0o600)
	ips, _, err := IPs()
	if err != nil || len(ips) != 1 || ips[0].Area != "" || ips[0].AddedBy != "" {
		t.Fatalf("read %+v %v", ips, err)
	}
	if _, err := ChangeAreaIP("ar_a", "203.0.113.7", time.Now(), func(*IP) error { return nil }); !errors.Is(err, ErrIPNotFound) {
		t.Fatalf("an area changed the owner's IP: %v", err)
	}
	if err := RemoveAreaIP("ar_a", "203.0.113.7"); !errors.Is(err, ErrIPNotFound) {
		t.Fatalf("an area removed the owner's IP: %v", err)
	}
	if err := RemoveAreaIP("", "203.0.113.7"); !errors.Is(err, ErrIPNotFound) {
		t.Fatalf("an area of no id removed the owner's IP: %v", err)
	}
	if AreaAddedProxy("socks5://u:p@127.0.0.1:1080") {
		t.Fatal("the owner's proxy is held to public addresses")
	}
}

// TestOwnerSaveKeepsAreaOfIPs (P2): the owner's whole list keeps whose
// each IP is from the file, whatever it says or leaves out; GiveIP and
// TakeBackIP alone change it.
func TestOwnerSaveKeepsAreaOfIPs(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	if err := SetIPs([]IP{{IP: "198.51.100.1"}}, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := AddAreaIP("ar_a", IP{IP: "203.0.113.9", Proxies: []string{"http://93.184.216.34:3128"}}, now); err != nil {
		t.Fatal(err)
	}
	ips, rev, _ := IPs()
	// the page leaves the fields out, then names another area
	for _, sent := range [][]IP{
		{{IP: "198.51.100.1"}, {IP: "203.0.113.9", Name: "renamed", Proxies: ips[1].Proxies}},
		{{IP: "198.51.100.1", Area: "ar_b", AddedBy: AddedByArea}, {IP: "203.0.113.9", Area: "ar_b", AddedBy: "", Proxies: ips[1].Proxies}},
	} {
		if err := SetIPs(sent, rev, now); err != nil {
			t.Fatal(err)
		}
		ips, rev, _ = IPs()
		if ips[0].Area != "" || ips[0].AddedBy != "" || ips[1].Area != "ar_a" || ips[1].AddedBy != AddedByArea {
			t.Fatalf("the whole list changed whose they are: %+v", ips)
		}
	}
	if err := GiveIP("198.51.100.1", "ar_b"); err != nil {
		t.Fatal(err)
	}
	if err := GiveIP("203.0.113.9", "ar_b"); !errors.Is(err, ErrIPGiven) {
		t.Fatalf("gave another area's IP: %v", err)
	}
	ips, _, _ = IPs()
	if ips[0].Area != "ar_b" || ips[0].AddedBy != "" {
		t.Fatalf("given: %+v", ips[0])
	}
	if err := TakeBackIP("203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	ips, _, _ = IPs()
	if ips[1].Area != "" || ips[1].AddedBy != AddedByArea {
		t.Fatalf("taken back, an area's proxies are no longer held: %+v", ips[1])
	}
	if !AreaAddedProxy("http://93.184.216.34:3128") {
		t.Fatal("an area's proxy taken back is not held to public addresses")
	}
}

// TestAreaIPWritesCheckTheArea: each of an area's writes reads the file
// afresh and refuses an IP that isn't the area's, the owner's or another
// area's alike, whoever called it (the store's half of P1).
func TestAreaIPWritesCheckTheArea(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	SetIPs([]IP{{IP: "198.51.100.1"}}, "", now)
	if _, err := AddAreaIP("ar_b", IP{IP: "203.0.113.2"}, now); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"198.51.100.1", "203.0.113.2", " 203.0.113.2 "} {
		if _, err := ChangeAreaIP("ar_a", ip, now, func(x *IP) error { x.Name = "x"; return nil }); !errors.Is(err, ErrIPNotFound) {
			t.Errorf("A changed %s: %v", ip, err)
		}
		if err := RemoveAreaIP("ar_a", ip); !errors.Is(err, ErrIPNotFound) {
			t.Errorf("A removed %s: %v", ip, err)
		}
	}
	for _, ip := range []string{"198.51.100.1", "203.0.113.2", "203.000.113.2"} {
		if _, err := AddAreaIP("ar_a", IP{IP: ip}, now); !errors.Is(err, ErrIPTaken) && !(ip == "203.000.113.2" && err != nil) {
			t.Errorf("A added %s: %v", ip, err)
		}
	}
	// what fn does to the address or the owner stays undone
	got, err := ChangeAreaIP("ar_b", "203.0.113.2", now, func(x *IP) error { x.IP, x.Area, x.AddedBy = "198.51.100.1", "", ""; return nil })
	if err != nil || got.IP != "203.0.113.2" || got.Area != "ar_b" || got.AddedBy != AddedByArea {
		t.Fatalf("fn changed whose it is: %+v %v", got, err)
	}
	for i := range MaxAreaIPs - 1 {
		if _, err := AddAreaIP("ar_b", IP{IP: "192.0.2." + itoa(i+1)}, now); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := AddAreaIP("ar_b", IP{IP: "192.0.2.200"}, now); !errors.Is(err, ErrAreaFull) {
		t.Fatalf("past %d: %v", MaxAreaIPs, err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// An ips.json that can't be read is unknown: no write of an area's goes
// over it, and every proxy is held to public addresses until it reads.
func TestAreaIPsUnreadable(t *testing.T) {
	path := ipsHome(t)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"ips": [`), 0o600)
	if _, err := AddAreaIP("ar_a", IP{IP: "203.0.113.2"}, time.Now()); !errors.Is(err, ErrIPsUnreadable) {
		t.Fatalf("added over an unreadable file: %v", err)
	}
	if err := GiveIP("203.0.113.2", "ar_a"); !errors.Is(err, ErrIPsUnreadable) {
		t.Fatalf("gave over an unreadable file: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"ips": [` {
		t.Fatalf("written over: %s", b)
	}
	areaAdded.Lock()
	areaAdded.read = false
	areaAdded.Unlock()
	if !AreaAddedProxy("http://127.0.0.1:7890") {
		t.Fatal("with ips.json unknown, a proxy is not held")
	}
}
