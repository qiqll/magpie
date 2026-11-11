package gui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/renewal"
)

// Models › Proxy IPs' list (T-155) is read and kept whole through
// /api/ips, each IP with its period worked out by renewal.Of; a list that
// can't be kept is refused and the one there stays.
func TestIPsRoute(t *testing.T) {
	sandboxHome(t)
	h := Handler(nil, nil)
	type answer struct {
		IPs []struct {
			IP      string         `json:"ip"`
			Name    string         `json:"name"`
			Opened  *time.Time     `json:"opened"`
			Renewal *renewal.State `json:"renewal"`
		} `json:"ips"`
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
	code, a := serve("POST", `{"ips":[{"ip":"203.0.113.7","name":"Tokyo","opened":"`+opened.Format(time.RFC3339)+`"},{"ip":"2001:db8::1"}]}`)
	if code != 200 || len(a.IPs) != 2 {
		t.Fatalf("set: %d %+v", code, a)
	}
	if x := a.IPs[0]; x.IP != "203.0.113.7" || x.Name != "Tokyo" || x.Renewal == nil || x.Renewal.DaysLeft != 4 || x.Renewal.Due != renewal.DueSoon {
		t.Errorf("the first: %+v %+v", x, x.Renewal)
	}
	if x := a.IPs[1]; x.Opened != nil || x.Renewal != nil {
		t.Errorf("one with no time is said with one: %+v", x)
	}
	if code, _ := serve("POST", `{"ips":[{"ip":"not-an-ip"}]}`); code == 200 {
		t.Error("an address that is no IP kept")
	}
	if code, a := serve("GET", ""); code != 200 || len(a.IPs) != 2 {
		t.Fatalf("after a refusal: %d %+v", code, a)
	}
}
