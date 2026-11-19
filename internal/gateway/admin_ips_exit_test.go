package gateway

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/renewal"
)

// T-225: an area's proxy is kept only under the IP a check reads it
// leaves from, one check spent a proxy. An area can't hold an address it
// has no proxy at, so the owner adds it after; nor put a proxy that
// leaves from elsewhere (the owner's, which lets magpie in by its source
// address) under an IP of its own.

// areaIPs are the IPs ips.json keeps for the area id.
func areaIPs(t *testing.T, id string) []renewal.IP {
	t.Helper()
	ips, _, err := renewal.IPs()
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(ips, func(x renewal.IP) bool { return x.Area != id })
}

// exitRefused says body is 400 proxy_exit whose check says ok and error
// as want, and tells no login.
func exitRefused(t *testing.T, c int, body string, ok bool, errKind string) {
	t.Helper()
	var out struct {
		Error struct{ Code string } `json:"error"`
		Check adminProxyCheck       `json:"check"`
	}
	json.Unmarshal([]byte(body), &out)
	if c != http.StatusBadRequest || out.Error.Code != adminProxyExit || out.Check.OK != ok || out.Check.Error != errKind || out.Check.Matches {
		t.Fatalf("want 400 %s (ok %v, %q): %d %s", adminProxyExit, ok, errKind, c, body)
	}
	if strings.Contains(body, "pw") || strings.Contains(body, "user") {
		t.Fatalf("the login is told: %s", body)
	}
}

// TestAdminIPAddNeedsItsExit: POST /ips keeps an IP only with proxies a
// check reads leave from it. None, one that leaves from elsewhere or
// doesn't answer, or one of two that doesn't, is refused with nothing
// written, so the owner adds that IP after. Each proxy spends a check,
// all at once: past the budget, and past checksMost proxies, nothing is
// connected to. The console's add for an area is held the same.
func TestAdminIPAddNeedsItsExit(t *testing.T) {
	w := newIPsWorld(t)
	add := func(secret, path, ip string, proxies ...string) (int, string) {
		return w.post(t, secret, path, map[string]any{"ip": ip, "name": "x", "proxies": proxies})
	}
	// the owner's next IP, which A would hold first
	const next = "198.51.100.9"
	if c, body := add(w.a, "/ips", next); c != http.StatusBadRequest || !strings.Contains(body, adminBadRequest) {
		t.Fatalf("no proxy: %d %s", c, body)
	}
	before := w.proxy.conns.Load()
	c, body := add(w.a, "/ips", next, "http://user:pw@"+proxyHost(next)+":"+w.proxy.port()) // leaves from .7
	exitRefused(t, c, body, true, "")
	if !strings.Contains(body, `"ip":"203.0.113.7"`) || w.proxy.conns.Load() != before+1 {
		t.Fatalf("the check isn't told, or wasn't made once: %s", body)
	}
	// nothing at the port: the dial is refused (stubProxyDNS)
	c, body = add(w.a, "/ips", next, "http://user:pw@"+pubIP+"62:1")
	exitRefused(t, c, body, false, "refused")
	c, body = add(w.a, "/ips", next, w.proxyOf(t, next), "http://"+proxyHost("203.0.113.60")+":"+w.proxy.port())
	exitRefused(t, c, body, true, "")
	if got := areaIPs(t, w.aID); len(got) != 0 {
		t.Fatalf("A holds %+v", got)
	}
	ownerIPs(t, next)

	// one that leaves from its IP is kept
	if c, body := add(w.a, "/ips", "203.0.113.64", w.proxyOf(t, "203.0.113.64")); c != http.StatusCreated || !strings.Contains(body, `"ip":"203.0.113.64"`) {
		t.Fatalf("a proxy that leaves from its IP: %d %s", c, body)
	}
	// 5 spent; more proxies than a request may check connect nowhere
	var many []string
	for i := range checksMost + 1 {
		many = append(many, "http://"+proxyHost("203.0.113.61")+":"+strconv.Itoa(1080+i))
	}
	before = w.proxy.conns.Load()
	if c, body := add(w.a, "/ips", "203.0.113.61", many...); c != http.StatusBadRequest || !strings.Contains(body, adminBadRequest) || w.proxy.conns.Load() != before {
		t.Fatalf("%d proxies: %d %s", len(many), c, body)
	}
	// 5 left: six proxies are none of them checked
	var six []string
	var fakes []*traceProxy
	for range 6 {
		p := startTraceProxy(t, "203.0.113.62")
		fakes = append(fakes, p)
		six = append(six, "http://"+proxyHost("203.0.113.62")+":"+p.port())
	}
	reached := func() (n int32) {
		for _, p := range fakes {
			n += p.conns.Load()
		}
		return n
	}
	if c, body := add(w.a, "/ips", "203.0.113.62", six...); c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyChecks) || reached() != 0 {
		t.Fatalf("past the budget: %d %s", c, body)
	}
	if c, body := add(w.a, "/ips", "203.0.113.62", six[:5]...); c != http.StatusCreated || reached() != 5 {
		t.Fatalf("five, the five left: %d %s", c, body)
	}
	if c, body := w.post(t, w.a, "/ips/probe", map[string]any{"proxy": w.proxyOf(t, "203.0.113.7")}); c != http.StatusTooManyRequests {
		t.Fatalf("the adds spent no checks: %d %s", c, body)
	}

	// the console's add for B, held to its exit as B's own is
	c, body = add(w.console, "/areas/"+w.bID+"/ips", "203.0.113.63", "http://user:pw@"+proxyHost("203.0.113.63")+":"+w.proxy.port())
	exitRefused(t, c, body, true, "")
	if c, body := add(w.console, "/areas/"+w.bID+"/ips", "203.0.113.63", w.proxyOf(t, "203.0.113.63")); c != http.StatusCreated {
		t.Fatalf("the console's add that leaves from its IP: %d %s", c, body)
	}
}

// TestAdminIPUpdateAddsOnlyItsExit: a proxy ips/update adds to an IP of
// the area's is kept only once a check reads it leaves from that IP. The
// owner's proxy, at a public address and letting magpie in by its source
// address, leaves from the owner's IP: it is refused, the IP keeps its
// proxies, and no account of the area's can have it put on.
func TestAdminIPUpdateAddsOnlyItsExit(t *testing.T) {
	w := newIPsWorld(t)
	w.addIP(t, w.a, "203.0.113.7")
	owners := startTraceProxy(t, "198.51.100.1")
	stolen := "http://" + proxyHost("198.51.100.1") + ":" + owners.port()
	c, body := w.post(t, w.a, "/ips/update", map[string]any{"ip": "203.0.113.7", "addProxies": []string{stolen}})
	exitRefused(t, c, body, true, "")
	if owners.conns.Load() != 1 {
		t.Fatalf("the owner's proxy was checked %d times, want once", owners.conns.Load())
	}
	got := areaIPs(t, w.aID)
	if len(got) != 1 || !slices.Equal(got[0].Proxies, []string{w.proxyOf(t, "203.0.113.7")}) {
		t.Fatalf("A's IP after: %+v", got)
	}
	if c, body := w.post(t, w.a, "/accounts/proxy", map[string]any{"id": w.aKey, "ip": "203.0.113.7", "proxy": renewal.ProxyID(stolen)}); c != http.StatusNotFound {
		t.Fatalf("the owner's proxy on A's account: %d %s", c, body)
	}
	// the first that doesn't leave from the IP stops it: the one after
	// is neither checked nor spent
	later := startTraceProxy(t, "203.0.113.7")
	c, body = w.post(t, w.a, "/ips/update", map[string]any{"ip": "203.0.113.7", "addProxies": []string{stolen, "http://" + proxyHost("203.0.113.7") + ":" + later.port()}})
	exitRefused(t, c, body, true, "")
	if later.conns.Load() != 0 {
		t.Fatal("a proxy after the refused one was checked")
	}
	// one that leaves from the IP is added; one it has is no check
	mine := "http://" + proxyHost("203.0.113.7") + ":" + later.port()
	before := w.proxy.conns.Load()
	c, body = w.post(t, w.a, "/ips/update", map[string]any{"ip": "203.0.113.7", "addProxies": []string{mine, w.proxyOf(t, "203.0.113.7")}})
	if c != http.StatusOK || len(ipsOf(t, "["+body+"]")[0].Proxies) != 2 || w.proxy.conns.Load() != before {
		t.Fatalf("adding one that leaves from it: %d %s", c, body)
	}
	if c, body := w.post(t, w.a, "/ips/update", map[string]any{"ip": "203.0.113.7", "name": "改名"}); c != http.StatusOK {
		t.Fatalf("a change adding none: %d %s", c, body)
	}
}

// TestAdminBatchIPLinesNeedTheirExit (§16.3): a line whose host is an IP
// is checked whether or not check is sent, and kept only when its exit is
// that IP: one that leaves from elsewhere or doesn't answer fails and is
// not written, so the owner adds that IP after. A dry run still connects
// nowhere. The console's batch for an area is held the same.
func TestAdminBatchIPLinesNeedTheirExit(t *testing.T) {
	w := newIPsWorld(t)
	text := strings.Join([]string{
		pubIP + "60:" + w.proxy.port() + ":user:pw", // leaves from 203.0.113.7
		pubIP + "62:1", // nothing there: refused (stubProxyDNS)
		pubIP + "61:" + w.proxyAt(t, pubIP+"61").port(),
	}, "\n")
	before := w.proxy.conns.Load()
	c, out, body := w.batch(t, w.a, map[string]any{"text": text, "type": "http", "dryRun": true})
	if c != 200 || !slices.Equal(statuses(out), []string{batchOK, batchOK, batchOK}) || w.proxy.conns.Load() != before {
		t.Fatalf("a dry run: %d %s", c, body)
	}
	c, out, body = w.batch(t, w.a, map[string]any{"text": text, "type": "http"})
	if c != 200 || !slices.Equal(statuses(out), []string{batchFailed, batchFailed, batchAdded}) || out.Added != 1 {
		t.Fatalf("%d %v: %s", c, statuses(out), body)
	}
	if out.Rows[0].Message != "the proxy leaves from 203.0.113.7, not this IP" || out.Rows[1].Message != "the proxy didn't carry the check: refused" || strings.Contains(body, "pw") {
		t.Fatalf("rows: %s", body)
	}
	if w.proxy.conns.Load() != before+1 || w.proxyAt(t, pubIP+"61").conns.Load() != 1 {
		t.Fatalf("each IP line wasn't checked once: %s", body)
	}
	got := areaIPs(t, w.aID)
	if len(got) != 1 || got[0].IP != pubIP+"61" {
		t.Fatalf("A holds %+v", got)
	}
	ownerIPs(t, pubIP+"60", pubIP+"62")

	// the console's batch for B
	c, out, body = w.batch(t, w.console, map[string]any{"text": pubIP + "63:" + w.proxy.port(), "type": "http"})
	if c != http.StatusForbidden {
		t.Fatalf("the console's own batch route: %d %s", c, body)
	}
	c, body = w.post(t, w.console, "/areas/"+w.bID+"/ips/batch", map[string]any{"text": pubIP + "63:" + w.proxy.port(), "type": "http"})
	if c != 200 || !strings.Contains(body, "not this IP") || len(areaIPs(t, w.bID)) != 0 {
		t.Fatalf("the console's batch for B: %d %s", c, body)
	}
}

// TestAdminProxyChecksRoom: room says whether each budget has n checks
// left, spending none, with how long until the oldest it needs let go
// and whether the console's total is the longer wait; more than a budget
// may ever have is never room.
func TestAdminProxyChecksRoom(t *testing.T) {
	var p proxyChecks
	now := time.Now()
	ids := []string{"ar_x", consoleBudget}
	for i := range checksMost - 2 {
		if !p.spend(ids, now.Add(time.Duration(i)*time.Second)) {
			t.Fatal("spend", i)
		}
	}
	if _, _, ok := p.room(ids, 2, now); !ok {
		t.Fatal("2 of the 2 left")
	}
	wait, console, ok := p.room(ids, 3, now)
	if ok || console || wait != checksWindow {
		t.Fatalf("3 of 2 left: %v %v %v, want the oldest's %v", wait, console, ok, checksWindow)
	}
	if !p.spend(ids, now) {
		t.Fatal("room spent")
	}
	if _, _, ok := p.room([]string{"ar_n"}, checksMost+1, now); ok {
		t.Fatal("room for more than an area ever has")
	}
	// the console's total: other areas spent the rest of it
	for i := range consoleChecksMost - checksMost + 1 {
		if !p.spend([]string{"ar_y" + strconv.Itoa(i/checksMost), consoleBudget}, now) {
			t.Fatal("the console's", i)
		}
	}
	if wait, console, ok := p.room([]string{"ar_z", consoleBudget}, 1, now); ok || !console || wait <= 0 {
		t.Fatalf("past the console's total: %v %v %v", wait, console, ok)
	}
}
