package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/renewal"
)

// T-214: magpie-console's docs/magpie-api.md §16.

// TestAdminIPRenewalDays (§16.1): an IP with an opening date tells its
// period's state and days left, below 0 the days since it ended; due
// stays the boolean it was. One with no date tells no renewal.
func TestAdminIPRenewalDays(t *testing.T) {
	w := newIPsWorld(t)
	now := time.Now()
	for _, c := range []struct {
		ip     string
		opened time.Time
		state  string
		days   int
		due    bool
	}{
		{"203.0.113.30", now.Add(-time.Hour), "", 30, false},
		{"203.0.113.31", now.Add(-27*24*time.Hour - 12*time.Hour), "soon", 3, true},
		{"203.0.113.32", now.Add(-32*24*time.Hour - 12*time.Hour), "ended", -2, true},
	} {
		if code, body := w.post(t, w.a, "/ips", map[string]any{"ip": c.ip, "name": "x", "opened": c.opened, "proxies": []string{w.proxyOf(t, c.ip)}}); code != http.StatusCreated {
			t.Fatal(code, body)
		}
		_, body := w.do("GET", AdminPath+"/ips", lanPeer, w.a, "")
		i := slices.IndexFunc(ipsOf(t, body), func(x adminIP) bool { return x.IP == c.ip })
		r := ipsOf(t, body)[i].Renewal
		if r == nil || r.State != c.state || r.DaysLeft != c.days || r.Due != c.due || !r.Opened.Equal(c.opened.UTC().Truncate(time.Second)) && !r.Opened.Equal(c.opened.UTC()) {
			t.Errorf("%s: %+v, want state %q, %d days, due %v", c.ip, r, c.state, c.days, c.due)
		}
		if !strings.Contains(body, `"state":`) || !strings.Contains(body, `"daysLeft":`) {
			t.Errorf("the fields aren't sent: %s", body)
		}
	}
	w.addIP(t, w.a, "203.0.113.7")
	_, body := w.do("GET", AdminPath+"/ips", lanPeer, w.a, "")
	for _, x := range ipsOf(t, body) {
		if x.IP == "203.0.113.7" && x.Renewal != nil {
			t.Errorf("an IP with no date tells a renewal: %+v", x.Renewal)
		}
	}
}

// TestAdminProbeProxy (§16.2): a proxy not saved is checked as a saved
// one is, its login never told; one not public is 400 and connects
// nowhere; every probe spends one of the checks ips/check spends.
func TestAdminProbeProxy(t *testing.T) {
	w := newIPsWorld(t)
	proxy := "http://user:pw@" + proxyHost("203.0.113.40") + ":" + w.proxy.port()
	c, body := w.post(t, w.a, "/ips/probe", map[string]any{"proxy": proxy})
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	if c != 200 || out["ok"] != true || out["ip"] != "203.0.113.7" || out["matches"] != false || out["proxy"] != "http://"+proxyHost("203.0.113.40")+":"+w.proxy.port() {
		t.Fatalf("a probe: %d %s", c, body)
	}
	if strings.Contains(body, "pw") || strings.Contains(body, "user") {
		t.Fatalf("a probe tells the login: %s", body)
	}
	before := w.proxy.conns.Load()
	spent := 1
	for _, p := range []string{"http://user:pw@192.168.1.1:" + w.proxy.port(), "socks5://localhost:1080", "http://127.0.0.1:" + w.proxy.port()} {
		c, body := w.post(t, w.a, "/ips/probe", map[string]any{"proxy": p})
		spent++
		if c != http.StatusBadRequest || !strings.Contains(body, adminProxyNotPublic) || strings.Contains(body, "pw") {
			t.Fatalf("%s: %d %s", p, c, body)
		}
	}
	w.answer.Store("10.0.0.1")
	if c, body := w.post(t, w.a, "/ips/probe", map[string]any{"proxy": proxy}); c != http.StatusBadRequest || !strings.Contains(body, adminProxyNotPublic) {
		t.Fatalf("a name that resolves home: %d %s", c, body)
	}
	spent++
	w.answer.Store(ipsPublic)
	for _, p := range []string{"ftp://203.0.113.9:21", "direct", "", "http://203.0.113.9", "socks5://203.0.113.9:1080/path"} {
		if c, body := w.post(t, w.a, "/ips/probe", map[string]any{"proxy": p}); c != http.StatusBadRequest || !strings.Contains(body, adminBadRequest) {
			t.Fatalf("%q: %d %s", p, c, body)
		}
		spent++
	}
	if n := w.proxy.conns.Load(); n != before {
		t.Fatalf("a refused probe connected: %d, want %d", n, before)
	}
	if spent != checksMost {
		t.Fatalf("spent %d, want %d", spent, checksMost)
	}
	// held before the IP is looked up: adding one would spend a check
	// itself (T-225)
	if c, body := w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": "px_0000000000"}); c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyChecks) {
		t.Fatalf("a check after %d probes: %d %s", spent, c, body)
	}
	if c, body := w.post(t, w.a, "/ips/probe", map[string]any{"proxy": proxy}); c != http.StatusTooManyRequests {
		t.Fatalf("an 11th probe: %d %s", c, body)
	}
	if c, body := w.post(t, w.b, "/ips/probe", map[string]any{"proxy": proxy}); c != 200 {
		t.Fatalf("B was held by A's probes: %d %s", c, body)
	}
}

// ownerIPs adds the owner's IPs ips, as Models › Proxy IPs would.
func ownerIPs(t *testing.T, ips ...string) {
	t.Helper()
	was, rev, err := renewal.IPs()
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range ips {
		was = append(was, renewal.IP{IP: ip})
	}
	if err := renewal.SetIPs(was, rev, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// The addresses batch lines are written at: public (netguard), unlike
// the documentation ranges, and never connected to (a line whose host is
// an IP is kept under it unchecked).
const pubIP = "93.184.217."

type batchOut struct {
	Rows  []batchRow `json:"rows"`
	Added int        `json:"added"`
}

func (w *ipsWorld) batch(t *testing.T, secret string, in map[string]any) (int, batchOut, string) {
	t.Helper()
	c, body := w.post(t, secret, "/ips/batch", in)
	var out batchOut
	if c == 200 {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(body, err)
		}
	}
	return c, out, body
}

func statuses(out batchOut) []string {
	var s []string
	for _, r := range out.Rows {
		s = append(s, r.Status)
	}
	return s
}

// TestAdminBatchIPs (§16.3): each line in the window's formats is kept
// under its IP, its host when that is one and its exit when it is a
// name; a second proxy of one IP merges into it; the owner's and another
// area's IPs are conflict, a private one not_public, a line not read
// invalid. The login is never told. (T-225: a host that is an IP is
// kept once its exit is that IP, so each such line has a fake proxy that
// leaves from it.)
func TestAdminBatchIPs(t *testing.T) {
	w := newIPsWorld(t)
	ownerIPs(t, pubIP+"1", pubIP+"2")
	at := func(ip string) string { return ip + ":" + w.proxyAt(t, ip).port() }
	if c, out, body := w.batch(t, w.b, map[string]any{"text": at(pubIP + "200"), "type": "http"}); c != 200 || out.Rows[0].Status != batchAdded {
		t.Fatalf("B's: %d %s", c, body)
	}
	second := startTraceProxy(t, pubIP+"20") // another proxy of the IP
	name := proxyHost("203.0.113.41") + ":" + w.proxy.port() + ":user:pw3"
	text := strings.Join([]string{
		"# A's proxies",
		at(pubIP+"20") + ":user:pw1",
		"http://" + pubIP + "20:" + second.port(),
		at(pubIP+"20") + ":user:pw1",
		"http://user:pw2@192.168.1.5:3128",
		at(pubIP + "1"),
		at(pubIP + "200"),
		name,
		"ssh://" + pubIP + "21:22",
		"not a proxy",
	}, "\n")
	before := w.proxy.conns.Load()
	c, out, body := w.batch(t, w.a, map[string]any{"text": text, "type": "http", "check": true})
	want := []string{batchAdded, batchMerged, batchInvalid, batchNotPublic, batchConflict, batchConflict, batchAdded, batchInvalid, batchInvalid}
	if c != 200 || !slices.Equal(statuses(out), want) || out.Added != 3 {
		t.Fatalf("%d %v added %d, want %v added 3: %s", c, statuses(out), out.Added, want, body)
	}
	if out.Rows[0].Line != 2 || out.Rows[0].IP != pubIP+"20" || out.Rows[0].Proxy.URL != "http://"+at(pubIP+"20") || out.Rows[6].IP != "203.0.113.7" {
		t.Fatalf("rows: %s", body)
	}
	for _, leak := range []string{"pw1", "pw2", "pw3", "user"} {
		if strings.Contains(body, leak) {
			t.Errorf("the batch tells %q: %s", leak, body)
		}
	}
	if strings.Contains(body, "owner") && !strings.Contains(body, "owner gave") {
		t.Errorf("a conflict names whose: %s", body)
	}
	if n := w.proxy.conns.Load(); n != before+1 {
		t.Fatalf("connections %d, want the name's check alone (%d)", n, before+1)
	}
	if n := w.proxyAt(t, pubIP+"20").conns.Load() + second.conns.Load(); n != 2 {
		t.Fatalf("the IP's two lines were checked %d times, want 2", n)
	}
	_, list := w.do("GET", AdminPath+"/ips", lanPeer, w.a, "")
	ips := ipsOf(t, list)
	if len(ips) != 2 || ips[0].IP != pubIP+"20" || len(ips[0].Proxies) != 2 || ips[1].IP != "203.0.113.7" || ips[1].GivenByOwner {
		t.Fatalf("A's IPs after: %s", list)
	}
	all, _, _ := renewal.IPs()
	for _, x := range all {
		if x.IP == pubIP+"1" && len(x.Proxies) != 0 || x.IP == pubIP+"200" && len(x.Proxies) != 1 {
			t.Fatalf("another's IP was written: %+v", x)
		}
	}

	// sent again: each is there already, nothing written
	c, out, body = w.batch(t, w.a, map[string]any{"text": at(pubIP+"20") + ":user:pw1\nhttp://" + pubIP + "20:" + second.port(), "type": "http"})
	if c != 200 || !slices.Equal(statuses(out), []string{batchExists, batchExists}) || out.Added != 0 {
		t.Fatalf("again: %d %s", c, body)
	}
	// a name with no check is not connected to
	before = w.proxy.conns.Load()
	c, out, body = w.batch(t, w.a, map[string]any{"text": name})
	if c != 200 || out.Rows[0].Status != batchFailed || out.Rows[0].Message != "a host name needs check" || w.proxy.conns.Load() != before {
		t.Fatalf("a name unchecked: %d %s", c, body)
	}
	// an IP the owner gave: its proxies are the owner's
	if err := renewal.GiveIP(pubIP+"2", w.aID); err != nil {
		t.Fatal(err)
	}
	if _, out, body = w.batch(t, w.a, map[string]any{"text": at(pubIP + "2"), "type": "http"}); out.Rows[0].Status != batchFailed || !strings.Contains(out.Rows[0].Message, "owner gave") {
		t.Fatalf("a given IP: %s", body)
	}
	// bounds
	if c, body := w.post(t, w.a, "/ips/batch", map[string]any{"text": strings.Repeat(pubIP+"22:1080\n", renewal.BatchMost+1)}); c != http.StatusBadRequest {
		t.Fatalf("101 lines: %d %s", c, body)
	}
	if c, body := w.post(t, w.a, "/ips/batch", map[string]any{"text": strings.Repeat("a", 200<<10), "dryRun": true}); c != http.StatusBadRequest {
		t.Fatalf("a 200KB line: %d %s", c, body)
	}
	if c, body := w.post(t, w.a, "/ips/batch", map[string]any{"text": "# nothing\n\n"}); c != http.StatusBadRequest {
		t.Fatalf("no lines: %d %s", c, body)
	}
	if c, body := w.post(t, w.a, "/ips/batch", map[string]any{"text": pubIP + "22:1080", "type": "ssh"}); c != http.StatusBadRequest {
		t.Fatalf("a type not kept: %d %s", c, body)
	}
}

// TestAdminBatchDryRun (§16.3): a dry run reads each line and judges a
// host that is an IP public, and no more: the owner's IP is ok like any,
// a name is ok unresolved, nothing is written, connected to or spent.
func TestAdminBatchDryRun(t *testing.T) {
	w := newIPsWorld(t)
	var resolved atomic.Int32
	lookup := netproxy.LookupIP
	t.Cleanup(func() { netproxy.LookupIP = lookup })
	netproxy.LookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		resolved.Add(1)
		return lookup(ctx, host)
	}
	before := w.proxy.conns.Load()
	ownerIPs(t, pubIP+"1")
	text := pubIP + "1:1080\n" + proxyHost("203.0.113.41") + ":" + w.proxy.port() + "\n10.0.0.1:1080\nnot a proxy\nlocalhost:1080"
	for range checksMost + 1 {
		c, out, body := w.batch(t, w.a, map[string]any{"text": text, "check": true, "dryRun": true})
		want := []string{batchOK, batchOK, batchNotPublic, batchInvalid, batchOK}
		if c != 200 || !slices.Equal(statuses(out), want) || out.Added != 0 {
			t.Fatalf("%d %v, want %v: %s", c, statuses(out), want, body)
		}
	}
	if n := w.proxy.conns.Load(); n != before || resolved.Load() != 0 {
		t.Fatalf("a dry run connected %d, resolved %d", n-before, resolved.Load())
	}
	if _, body := w.do("GET", AdminPath+"/ips", lanPeer, w.a, ""); len(ipsOf(t, body)) != 0 {
		t.Fatalf("a dry run wrote: %s", body)
	}
	px := w.addIP(t, w.a, "203.0.113.7")
	if c, body := w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": px}); c != 200 {
		t.Fatalf("dry runs spent checks: %d %s", c, body)
	}
}

// TestAdminBatchBudget (§16.3, §18): each line spends one of the area's
// checks, a name's and, since T-225, an IP's; past the budget the lines
// left fail "check limit reached" and the rest are kept. With none left,
// a batch is 429 at the door, one with IPs alone too (T-225: an IP's
// line is never kept unchecked), and connects nowhere.
func TestAdminBatchBudget(t *testing.T) {
	w := newIPsWorld(t)
	px := w.addIP(t, w.a, "203.0.113.7") // the first check
	for i := range checksMost - 4 {
		if c, body := w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": px}); c != 200 {
			t.Fatal(i, c, body)
		}
	}
	port := w.proxy.port()
	ip := func(n string) string { return pubIP + n + ":" + w.proxyAt(t, pubIP+n).port() }
	text := proxyHost("203.0.113.42") + ":" + port + "\n" + ip("23") + "\n" + proxyHost("203.0.113.43") + ":" + port + "\n" + ip("25")
	c, out, body := w.batch(t, w.a, map[string]any{"text": text, "type": "http", "check": true})
	if c != 200 || !slices.Equal(statuses(out), []string{batchMerged, batchAdded, batchMerged, batchFailed}) || out.Rows[3].Message != "check limit reached" {
		t.Fatalf("%d %v: %s", c, statuses(out), body)
	}
	if n := w.proxyAt(t, pubIP+"25").conns.Load(); n != 0 {
		t.Fatalf("a line past the budget connected: %d", n)
	}
	if c, body := w.post(t, w.a, "/ips/batch", map[string]any{"text": proxyHost("203.0.113.44") + ":" + port, "type": "http", "check": true}); c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyChecks) {
		t.Fatalf("a check past the budget: %d %s", c, body)
	}
	if c, body := w.post(t, w.a, "/ips/batch", map[string]any{"text": ip("24"), "type": "http"}); c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyChecks) || w.proxyAt(t, pubIP+"24").conns.Load() != 0 {
		t.Fatalf("IPs alone past the budget: %d %s", c, body)
	}
}

// TestAdminBatchConflicts (§16.3, §18): each line whose IP magpie has is
// a conflict counted with the add routes'; the one that reaches the line
// fails the rest, and the area's next batch is 429 at the door. (T-225:
// each line spends a check, 10 an area, so the area meets its first 17
// conflicts on accounts/key and the batch the last 3.)
func TestAdminBatchConflicts(t *testing.T) {
	w := newIPsWorld(t)
	const before = conflictMost - 3
	for i := range before {
		if c, body := w.post(t, w.a, "/accounts/key", map[string]any{"provider": "plan", "key": "upstream-secret-plan"}); c != http.StatusConflict {
			t.Fatal(i, c, body)
		}
	}
	at := func(ip string) string { return ip + ":" + w.proxyAt(t, ip).port() }
	var lines, owned []string
	for i := range conflictMost - before + 2 {
		ip := fmt.Sprintf(pubIP+"%d", 10+i)
		owned = append(owned, ip)
		lines = append(lines, at(ip))
	}
	ownerIPs(t, owned...)
	lines = append(lines, at(pubIP+"250"))
	c, out, body := w.batch(t, w.a, map[string]any{"text": strings.Join(lines, "\n"), "type": "http"})
	if c != 200 || len(out.Rows) != len(lines) {
		t.Fatal(c, body)
	}
	for i, r := range out.Rows {
		want := batchConflict
		if before+i >= conflictMost {
			want = batchFailed
		}
		if r.Status != want || before+i >= conflictMost && r.Message != "too many conflicts; try again later" {
			t.Fatalf("row %d: %+v, want %s", i, r, want)
		}
	}
	if c, body := w.post(t, w.a, "/ips/batch", map[string]any{"text": at(pubIP + "251"), "type": "http"}); c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyConflicts) {
		t.Fatalf("after the line: %d %s", c, body)
	}
	if c, body := w.post(t, w.a, "/accounts/key", map[string]any{"provider": "plan", "key": "sk-area-9999999999"}); c != http.StatusTooManyRequests {
		t.Fatalf("the add routes aren't held with it: %d %s", c, body)
	}
	if c, out, body := w.batch(t, w.b, map[string]any{"text": at(pubIP + "251"), "type": "http"}); c != 200 || out.Rows[0].Status != batchAdded {
		t.Fatalf("B held by A: %d %s", c, body)
	}
}

// accountProxyOf is the area's proxy on its account ref as GET /accounts
// tells it.
func (w *ipsWorld) accountProxyOf(t *testing.T, secret, ref string) *adminAccountProxy {
	t.Helper()
	_, body := w.do("GET", AdminPath+"/accounts", lanPeer, secret, "")
	var accs []struct {
		ID    string             `json:"id"`
		Proxy *adminAccountProxy `json:"proxy"`
	}
	if err := json.Unmarshal([]byte(body), &accs); err != nil {
		t.Fatal(body, err)
	}
	for _, a := range accs {
		if a.ID == ref {
			return a.Proxy
		}
	}
	t.Fatalf("no %s in %s", ref, body)
	return nil
}

// TestAdminRereadIPs (§16.4): the exit of each proxy of an IP the area
// made that is on its accounts is read again. One that left from another
// IP moves to it, a new IP of the area's, with its account; one that
// didn't is unchanged; an exit that is the owner's IP fails, whose not
// said. A proxy on no account, and an IP the owner gave, aren't read.
func TestAdminRereadIPs(t *testing.T) {
	w := newIPsWorld(t)
	// each leaves from its IP as it is added (T-225), and from another
	// once it is
	mover, other, idle := startTraceProxy(t, "203.0.113.50"), startTraceProxy(t, "203.0.113.51"), startTraceProxy(t, "203.0.113.52")
	add := func(ip, port string) string {
		proxy := "http://user:pw@" + proxyHost(ip) + ":" + port
		if c, body := w.post(t, w.a, "/ips", map[string]any{"ip": ip, "name": "x", "proxies": []string{proxy}}); c != http.StatusCreated {
			t.Fatal(c, body)
		}
		return renewal.ProxyID(proxy)
	}
	moving := add("203.0.113.50", mover.port()) // leaves from 203.0.113.7
	taken := add("203.0.113.51", other.port())  // leaves from the owner's
	add("203.0.113.52", idle.port())            // on no account
	mover.exit.Store("203.0.113.7")
	other.exit.Store("198.51.100.1")
	if err := renewal.GiveIP("198.51.100.2", w.aID); err != nil {
		t.Fatal(err)
	}
	// a second key of A's for the second proxy
	if c, body := w.post(t, w.a, "/accounts/key", map[string]any{"provider": "plan", "name": "k2", "key": "sk-area-0000000002"}); c != http.StatusCreated {
		t.Fatal(c, body)
	}
	aKey2 := "plan/" + provider.KeyID("sk-area-0000000002")
	for _, p := range [][3]string{{w.aKey, "203.0.113.50", moving}, {aKey2, "203.0.113.51", taken}} {
		if c, body := w.post(t, w.a, "/accounts/proxy", map[string]any{"id": p[0], "ip": p[1], "proxy": p[2]}); c != 200 {
			t.Fatal(c, body)
		}
	}
	reads := mover.conns.Load() + other.conns.Load() + idle.conns.Load()
	c, body := w.post(t, w.a, "/ips/reread", map[string]any{})
	var out struct {
		Moved     []rereadMove `json:"moved"`
		Failed    []string     `json:"failed"`
		Unchanged int          `json:"unchanged"`
	}
	json.Unmarshal([]byte(body), &out)
	if c != 200 || len(out.Moved) != 1 || out.Moved[0] != (rereadMove{Proxy: moving, From: "203.0.113.50", To: "203.0.113.7"}) || !slices.Equal(out.Failed, []string{taken}) || out.Unchanged != 0 {
		t.Fatalf("a reread: %d %s", c, body)
	}
	if n := mover.conns.Load() + other.conns.Load() + idle.conns.Load() - reads; n != 2 {
		t.Fatalf("%d proxies read, want the 2 on accounts", n)
	}
	for _, leak := range []string{"pw", "user", "198.51.100"} {
		if strings.Contains(body, leak) {
			t.Errorf("a reread tells %q: %s", leak, body)
		}
	}
	if err := provider.SweepAreaProxies(); err != nil {
		t.Fatal(err)
	}
	if p := w.accountProxyOf(t, w.a, w.aKey); p == nil || p.IP != "203.0.113.7" || p.ID != moving || p.Overridden {
		t.Fatalf("the moved proxy on its account: %+v", p)
	}
	if p := w.accountProxyOf(t, w.a, aKey2); p == nil || p.IP != "203.0.113.51" || p.ID != taken {
		t.Fatalf("the proxy that couldn't move: %+v", p)
	}
	_, list := w.do("GET", AdminPath+"/ips", lanPeer, w.a, "")
	for _, x := range ipsOf(t, list) {
		switch x.IP {
		case "203.0.113.7":
			if x.GivenByOwner || len(x.Proxies) != 1 || x.Proxies[0].ID != moving || !slices.Equal(x.Accounts, []string{w.aKey}) {
				t.Fatalf("the new IP: %+v", x)
			}
		case "203.0.113.50":
			if len(x.Proxies) != 0 || len(x.Accounts) != 0 {
				t.Fatalf("the IP it left: %+v", x)
			}
		}
	}
	all, _, _ := renewal.IPs()
	for _, x := range all {
		if x.IP == "198.51.100.1" && len(x.Proxies) != 1 {
			t.Fatalf("the owner's IP was written: %+v", x)
		}
	}
	a, _ := area.Get(w.aID)
	if i := slices.IndexFunc(a.Accounts, func(c area.Account) bool { return c.Ref == w.aKey }); a.Accounts[i].Proxy.IP != "203.0.113.7" {
		t.Fatalf("areas.json: %+v", a.Accounts[i].Proxy)
	}

	// again: the moved one left from where it is now
	c, body = w.post(t, w.a, "/ips/reread", nil)
	json.Unmarshal([]byte(body), &out)
	if c != 200 || len(out.Moved) != 0 || out.Unchanged != 1 || !slices.Equal(out.Failed, []string{taken}) {
		t.Fatalf("again: %d %s", c, body)
	}
	// B has none of A's
	c, body = w.post(t, w.b, "/ips/reread", map[string]any{})
	if c != 200 || body != `{"moved":[],"failed":[],"unchanged":0}`+"\n" && body != `{"moved":[],"failed":[],"unchanged":0}` {
		t.Fatalf("B: %d %s", c, body)
	}
}

// TestAdminRereadBudget (§16.4): each proxy read spends one of the
// area's checks; those past the budget are failed unread, and with none
// left the reread is 429 at the door.
func TestAdminRereadBudget(t *testing.T) {
	w := newIPsWorld(t)
	px := w.addIP(t, w.a, "203.0.113.7") // the first check (T-225)
	if c, body := w.post(t, w.a, "/accounts/proxy", map[string]any{"id": w.aKey, "ip": "203.0.113.7", "proxy": px}); c != 200 {
		t.Fatal(c, body)
	}
	for i := range checksMost - 2 {
		if c, body := w.post(t, w.a, "/ips/check", map[string]any{"ip": "203.0.113.7", "proxy": px}); c != 200 {
			t.Fatal(i, c, body)
		}
	}
	if c, body := w.post(t, w.a, "/ips/reread", map[string]any{}); c != 200 || !strings.Contains(body, `"unchanged":1`) {
		t.Fatalf("the 10th: %d %s", c, body)
	}
	before := w.proxy.conns.Load()
	if c, body := w.post(t, w.a, "/ips/reread", map[string]any{}); c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyChecks) {
		t.Fatalf("the 11th: %d %s", c, body)
	}
	if w.proxy.conns.Load() != before {
		t.Fatal("a held reread connected")
	}
}

// TestAdminProxyChecksHoldSpend: a request holding an area's checks runs alone
// and spends one a check; once spent, none is left for anyone.
func TestAdminProxyChecksHoldSpend(t *testing.T) {
	var p proxyChecks
	now := time.Now()
	x := []string{"x"}
	if _, _, ok := p.hold(x, now); !ok {
		t.Fatal("hold")
	}
	if _, _, ok := p.begin(x, now); ok {
		t.Fatal("a check ran beside a hold")
	}
	if _, _, ok := p.hold(x, now); ok {
		t.Fatal("two holds")
	}
	for i := range checksMost {
		if !p.spend(x, now) {
			t.Fatal("spend", i)
		}
	}
	if p.spend(x, now) {
		t.Fatal("an 11th spent")
	}
	p.end("x")
	if wait, _, ok := p.begin(x, now); ok || wait <= 0 || wait > checksWindow {
		t.Fatalf("a check after the budget: %v %v", wait, ok)
	}
	if _, _, ok := p.begin(x, now.Add(checksWindow)); !ok {
		t.Fatal("the window let go of none")
	}
}
