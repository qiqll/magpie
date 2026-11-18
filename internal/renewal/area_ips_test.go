package renewal

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	if err := RemoveAreaIP("ar_a", "203.0.113.7", nil); !errors.Is(err, ErrIPNotFound) {
		t.Fatalf("an area removed the owner's IP: %v", err)
	}
	if err := RemoveAreaIP("", "203.0.113.7", nil); !errors.Is(err, ErrIPNotFound) {
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
		if err := RemoveAreaIP("ar_a", ip, nil); !errors.Is(err, ErrIPNotFound) {
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
	resetAreaAdded()
	// never read well: the owner's own proxy on loopback is not held, and
	// the failed read is not made again on every dial
	reads := areaAddedReads.Load()
	for range 5 {
		if AreaAddedProxy("http://127.0.0.1:7890") {
			t.Fatal("with ips.json never read, the owner's proxy is held")
		}
	}
	if n := areaAddedReads.Load() - reads; n != 1 {
		t.Fatalf("a failed read made %d times over 5 dials, want 1", n)
	}
}

func resetAreaAdded() {
	areaAdded.Lock()
	areaAdded.read, areaAdded.proxies, areaAdded.failedAt = false, nil, time.Time{}
	areaAdded.Unlock()
}

// AreaAddedProxy names an area's proxies by their text or as netproxy
// writes them, sees a write at once (in the same tick too), and keeps its
// last good read through a file that stops parsing.
func TestAreaAddedProxy(t *testing.T) {
	path := ipsHome(t)
	resetAreaAdded()
	now := time.Now()
	if AreaAddedProxy("http://93.184.216.34:3128") {
		t.Fatal("no file, held")
	}
	SetIPs([]IP{{IP: "198.51.100.1", Proxies: []string{"http://127.0.0.1:7890"}}}, "", now)
	if _, err := AddAreaIP("ar_a", IP{IP: "203.0.113.9", Proxies: []string{"HTTP://u:p@93.184.216.34:3128"}}, now); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"HTTP://u:p@93.184.216.34:3128", " http://u:p@93.184.216.34:3128 "} {
		if !AreaAddedProxy(raw) {
			t.Errorf("%q not held", raw)
		}
	}
	if AreaAddedProxy("http://127.0.0.1:7890") {
		t.Fatal("the owner's proxy is held")
	}
	// written over by the owner in the same tick: seen at once
	_, rev, _ := IPs()
	fi, _ := os.Stat(path)
	if err := SetIPs([]IP{{IP: "198.51.100.1"}}, rev, now); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, fi.ModTime(), fi.ModTime())
	if AreaAddedProxy("http://u:p@93.184.216.34:3128") {
		t.Fatal("removed, still held")
	}
	if _, err := AddAreaIP("ar_a", IP{IP: "203.0.113.9", Proxies: []string{"http://93.184.216.34:3128"}}, now); err != nil {
		t.Fatal(err)
	}
	if !AreaAddedProxy("http://93.184.216.34:3128") {
		t.Fatal("added again, not held")
	}
	os.WriteFile(path, []byte(`{"ips": [`), 0o600)
	if !AreaAddedProxy("http://93.184.216.34:3128") {
		t.Fatal("a file that stopped parsing lost the last good read")
	}
}

// TestAddAreaProxiesJudgedInTheLock (T-214): a batch's adds are judged
// against the file as it is: a new IP is made then merged into, one the
// area has is merged into, the owner's and another area's are taken
// (stop asked each time), one the owner gave is given, and all is
// written at once. An add after taken says stop is stopped.
func TestAddAreaProxiesJudgedInTheLock(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	if err := SetIPs([]IP{{IP: "198.51.100.1"}, {IP: "198.51.100.2"}}, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := AddAreaIP("ar_b", IP{IP: "203.0.113.8"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := AddAreaIP("ar_a", IP{IP: "203.0.113.7", Proxies: []string{"socks5://u:p@93.184.216.34:1080"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := GiveIP("198.51.100.2", "ar_a"); err != nil {
		t.Fatal(err)
	}
	asked := 0
	got, err := AddAreaProxies("ar_a", []AreaProxyAdd{
		{"203.0.113.20", "http://93.184.216.40:80"},
		{"203.0.113.20", "http://93.184.216.41:80"},
		{"203.0.113.7", "http://93.184.216.42:80"},
		{"203.0.113.7", "SOCKS5://u:p@93.184.216.34:1080"},
		{"198.51.100.1", "http://93.184.216.43:80"},
		{"203.0.113.8", "http://93.184.216.44:80"},
		{"198.51.100.2", "http://93.184.216.45:80"},
	}, now, func() bool { asked++; return false })
	want := []string{BatchAdded, BatchMerged, BatchMerged, BatchExists, BatchTaken, BatchTaken, BatchGiven}
	if err != nil || !slices.Equal(got, want) || asked != 2 {
		t.Fatalf("%v %v (asked %d), want %v", got, err, asked, want)
	}
	ips, _, _ := IPs()
	byIP := map[string]IP{}
	for _, x := range ips {
		byIP[x.IP] = x
	}
	if x := byIP["203.0.113.20"]; x.Area != "ar_a" || x.AddedBy != AddedByArea || len(x.Proxies) != 2 {
		t.Fatalf("the new IP: %+v", x)
	}
	if len(byIP["203.0.113.7"].Proxies) != 2 || len(byIP["198.51.100.1"].Proxies) != 0 || len(byIP["203.0.113.8"].Proxies) != 0 || len(byIP["198.51.100.2"].Proxies) != 0 {
		t.Fatalf("written: %+v", ips)
	}
	got, err = AddAreaProxies("ar_a", []AreaProxyAdd{{"198.51.100.1", "http://93.184.216.46:80"}, {"203.0.113.21", "http://93.184.216.47:80"}}, now, func() bool { return true })
	if err != nil || !slices.Equal(got, []string{BatchTaken, BatchStopped}) {
		t.Fatalf("stopped: %v %v", got, err)
	}
	if ips2, _, _ := IPs(); len(ips2) != len(ips) {
		t.Fatal("a stopped add was written")
	}
	if _, err := AddAreaProxies("", []AreaProxyAdd{{"203.0.113.22", "http://93.184.216.48:80"}}, now, nil); !errors.Is(err, ErrIPNotFound) {
		t.Fatalf("no area: %v", err)
	}
}

// TestAddAreaProxiesBounds (T-214): an area's 50 IPs and an IP's 20
// proxies are judged add by add; what fits is kept.
func TestAddAreaProxiesBounds(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	var adds []AreaProxyAdd
	for i := range MaxAreaIPs + 1 {
		adds = append(adds, AreaProxyAdd{"203.0.113." + strconv.Itoa(i+1), "http://93.184.216.34:" + strconv.Itoa(1000+i)})
	}
	for i := range MaxIPProxies {
		adds = append(adds, AreaProxyAdd{"203.0.113.1", "http://93.184.216.35:" + strconv.Itoa(2000+i)})
	}
	got, err := AddAreaProxies("ar_a", adds, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[MaxAreaIPs-1] != BatchAdded || got[MaxAreaIPs] != BatchAreaFull {
		t.Fatalf("the 50th and 51st: %v %v", got[MaxAreaIPs-1], got[MaxAreaIPs])
	}
	rest := got[MaxAreaIPs+1:]
	if rest[MaxIPProxies-2] != BatchMerged || rest[MaxIPProxies-1] != BatchIPFull {
		t.Fatalf("the 20th and 21st proxy of one IP: %v", rest)
	}
}

// TestAreaMoveProxyToItsExit (T-214): a proxy moves from an IP the area made to
// its exit, an IP the area made or a new one, and moved is called under
// the lock; an exit magpie has otherwise is ErrIPTaken; moved failing
// puts the file back.
func TestAreaMoveProxyToItsExit(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	p1, p2 := "http://u:p@93.184.216.34:1080", "http://93.184.216.35:1080"
	if err := SetIPs([]IP{{IP: "198.51.100.1"}}, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := AddAreaIP("ar_a", IP{IP: "203.0.113.50", Proxies: []string{p1, p2}}, now); err != nil {
		t.Fatal(err)
	}
	if err := MoveAreaProxy("ar_b", "203.0.113.50", ProxyID(p1), "203.0.113.7", now, nil); !errors.Is(err, ErrIPNotFound) {
		t.Fatalf("another area moved it: %v", err)
	}
	if err := MoveAreaProxy("ar_a", "203.0.113.50", ProxyID(p1), "198.51.100.1", now, nil); !errors.Is(err, ErrIPTaken) {
		t.Fatalf("onto the owner's: %v", err)
	}
	if err := MoveAreaProxy("ar_a", "198.51.100.1", "px_x", "203.0.113.7", now, nil); !errors.Is(err, ErrIPNotFound) {
		t.Fatalf("from the owner's: %v", err)
	}
	before, _, _ := IPs()
	boom := errors.New("areas.json")
	if err := MoveAreaProxy("ar_a", "203.0.113.50", ProxyID(p1), "203.0.113.7", now, func(string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("moved failing: %v", err)
	}
	if after, _, _ := IPs(); len(after) != len(before) || len(after[1].Proxies) != 2 {
		t.Fatalf("not put back: %+v", after)
	}
	var to string
	if err := MoveAreaProxy("ar_a", "203.0.113.50", ProxyID(p1), "203.0.113.7", now, func(ip string) error {
		to = ip
		if ipsMu.TryLock() {
			ipsMu.Unlock()
			t.Error("moved was called without ipsMu")
		}
		return nil
	}); err != nil || to != "203.0.113.7" {
		t.Fatalf("a move: %v %q", err, to)
	}
	if err := MoveAreaProxy("ar_a", "203.0.113.50", ProxyID(p2), "203.0.113.7", now, nil); err != nil {
		t.Fatal(err)
	}
	ips, _, _ := IPs()
	if len(ips) != 3 || len(ips[1].Proxies) != 0 || ips[2].IP != "203.0.113.7" || ips[2].Area != "ar_a" || ips[2].AddedBy != AddedByArea || !slices.Equal(ips[2].Proxies, []string{p1, p2}) {
		t.Fatalf("after: %+v", ips)
	}
}

// TestAreaFullBeforeTaken (security review M1): once the area keeps
// MaxAreaIPs, an IP magpie has and one it doesn't are both area_full,
// and no conflict is asked; its own IPs still take proxies.
func TestAreaFullBeforeTaken(t *testing.T) {
	ipsHome(t)
	now := time.Now()
	if err := SetIPs([]IP{{IP: "198.51.100.1"}}, "", now); err != nil {
		t.Fatal(err)
	}
	var adds []AreaProxyAdd
	for i := range MaxAreaIPs {
		adds = append(adds, AreaProxyAdd{"203.0.113." + strconv.Itoa(i+1), "http://93.184.216.34:" + strconv.Itoa(1000+i)})
	}
	if _, err := AddAreaProxies("ar_a", adds, now, nil); err != nil {
		t.Fatal(err)
	}
	asked := 0
	got, err := AddAreaProxies("ar_a", []AreaProxyAdd{
		{"198.51.100.1", "http://93.184.216.35:80"},
		{"198.51.100.9", "http://93.184.216.36:80"},
		{"203.0.113.1", "http://93.184.216.37:80"},
	}, now, func() bool { asked++; return false })
	if err != nil || !slices.Equal(got, []string{BatchAreaFull, BatchAreaFull, BatchMerged}) || asked != 0 {
		t.Fatalf("%v %v asked %d", got, err, asked)
	}
	if err := MoveAreaProxy("ar_a", "203.0.113.2", ProxyID("http://93.184.216.34:1001"), "198.51.100.1", now, nil); !errors.Is(err, ErrAreaFull) {
		t.Fatalf("a move onto the owner's from a full area: %v", err)
	}
}
