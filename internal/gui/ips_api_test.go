package gui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	areas "github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/renewal"
)

// Models › Proxy IPs' list (T-155) is read and kept whole through
// /api/ips, each IP with its period worked out by renewal.Of; a list that
// can't be kept is refused and the one there stays.
func TestIPsRoute(t *testing.T) {
	sandboxHome(t)
	// periods end by the calendar in this computer's zone (renewal.Of):
	// held in UTC, the days below count the same wherever it runs
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	h := Handler(nil, nil)
	type answer struct {
		IPs []struct {
			IP      string         `json:"ip"`
			Name    string         `json:"name"`
			Opened  *time.Time     `json:"opened"`
			Renewal *renewal.State `json:"renewal"`
		} `json:"ips"`
		Rev            string
		Days, WarnDays int
	}
	serve := func(method, body string) (int, answer) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/api/ips", strings.NewReader(body)))
		var a answer
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
				t.Fatalf("%s", rec.Body)
			}
		}
		return rec.Code, a
	}
	if code, a := serve("GET", ""); code != 200 || len(a.IPs) != 0 || a.Days != 30 || a.WarnDays != 5 {
		t.Fatalf("none yet: %d %+v", code, a)
	}
	opened := time.Now().Add(-26 * 24 * time.Hour).UTC().Truncate(time.Second)
	if code, _ := serve("POST", `{"rev":""}`); code == 200 {
		t.Fatal("a body with no list kept as none")
	}
	if code, _ := serve("POST", `{"ips":[]}`); code == 200 {
		t.Fatal("a list made from no read kept")
	}
	code, a := serve("POST", `{"rev":"","ips":[{"ip":"203.0.113.7","name":"Tokyo","opened":"`+opened.Format(time.RFC3339)+`"},{"ip":"2001:db8::1"}]}`)
	if code != 200 || len(a.IPs) != 2 {
		t.Fatalf("set: %d %+v", code, a)
	}
	if x := a.IPs[0]; x.IP != "203.0.113.7" || x.Name != "Tokyo" || x.Renewal == nil || x.Renewal.DaysLeft != 4 || x.Renewal.Due != renewal.DueSoon {
		t.Errorf("the first: %+v %+v", x, x.Renewal)
	}
	if x := a.IPs[1]; x.Opened != nil || x.Renewal != nil {
		t.Errorf("one with no time is said with one: %+v", x)
	}
	if code, _ := serve("POST", `{"rev":"`+a.Rev+`","ips":[{"ip":"not-an-ip"}]}`); code == 200 {
		t.Error("an address that is no IP kept")
	}
	if code, _ := serve("POST", `{"rev":"","ips":[]}`); code == 200 {
		t.Error("a list made from an older read kept")
	}
	if code, a := serve("GET", ""); code != 200 || len(a.IPs) != 2 {
		t.Fatalf("after a refusal: %d %+v", code, a)
	}
}

// The owner gives an IP to an area and takes it back (/api/ips/area,
// T-198); the whole list sent from the page keeps whose each is, and an
// area's proxy on an account comes off when the owner takes its IP back
// or removes it.
func TestIPAreaRoute(t *testing.T) {
	sandboxHome(t)
	h := Handler(nil, nil)
	serve := func(path, body string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-owner-00000001", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	provider.AddKey("relay", "", "sk-area-0000000001", "")
	a, _ := areas.Create("学习", nil, nil)
	ref := "relay/" + provider.KeyID("sk-area-0000000001")
	areas.Claim(a.ID, ref)
	proxy := "http://192.168.1.2:7890"
	if err := renewal.SetIPs([]renewal.IP{{IP: "198.51.100.1", Proxies: []string{proxy}}, {IP: "198.51.100.2"}}, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if c, body := serve("/api/ips/area", `{"ip":"198.51.100.1","area":"ar_nosuch"}`); c == 200 {
		t.Fatalf("given to no area: %s", body)
	}
	c, body := serve("/api/ips/area", `{"ip":"198.51.100.1","area":"`+a.ID+`"}`)
	if c != 200 || !strings.Contains(body, `"area":"`+a.ID+`"`) || !strings.Contains(body, `"note":"学习"`) {
		t.Fatalf("given: %d %s", c, body)
	}
	// the page sends the whole list back without the field
	_, rev, _ := renewal.IPs()
	if c, body := serve("/api/ips", `{"rev":"`+rev+`","ips":[{"ip":"198.51.100.1","proxies":["`+proxy+`"]},{"ip":"198.51.100.2","area":"`+a.ID+`"}]}`); c != 200 {
		t.Fatal(c, body)
	}
	ips, _, _ := renewal.IPs()
	if ips[0].Area != a.ID || ips[1].Area != "" {
		t.Fatalf("the whole list changed whose they are: %+v", ips)
	}
	if err := provider.AttachAreaProxy(a.ID, ref, "198.51.100.1", renewal.ProxyID(proxy)); err != nil {
		t.Fatal(err)
	}
	if c, body := serve("/api/ips/area", `{"ip":"198.51.100.1","area":""}`); c != 200 {
		t.Fatal(c, body)
	}
	if p, _ := provider.Find("relay"); len(p.AccountProxies) != 0 {
		t.Fatalf("taken back, the area's account still goes through it: %v", p.AccountProxies)
	}
	// given again, put on, then the owner removes the IP
	serve("/api/ips/area", `{"ip":"198.51.100.1","area":"`+a.ID+`"}`)
	if err := provider.AttachAreaProxy(a.ID, ref, "198.51.100.1", renewal.ProxyID(proxy)); err != nil {
		t.Fatal(err)
	}
	_, rev, _ = renewal.IPs()
	if c, body := serve("/api/ips", `{"rev":"`+rev+`","ips":[{"ip":"198.51.100.2"}]}`); c != 200 {
		t.Fatal(c, body)
	}
	if p, _ := provider.Find("relay"); len(p.AccountProxies) != 0 {
		t.Fatalf("removed, the area's account still goes through it: %v", p.AccountProxies)
	}
}
