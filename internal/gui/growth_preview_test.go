package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Settings › Growth policy's chart (T-130): growth/preview gives each day's
// shares of the policy sent, read to a quarter past its last change, and
// the accounts with a day (none here). A policy the page can't save is
// refused, Off is drawn level at 100%, and a body with no policy is a 400.
func TestGrowthPreviewRoute(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	type point struct{ Day, FiveHours, Week int }
	post := func(body string) (int, []point, []any) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/growth/preview", strings.NewReader(body)))
		var out struct {
			Points   []point `json:"points"`
			Accounts []any   `json:"accounts"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, out.Points, out.Accounts
	}
	code, pts, accts := post(`{"policy":{"id":"standard","kind":"ramp","start":50,"days":14}}`)
	if code != 200 || len(pts) != 19 || pts[0] != (point{0, 50, 50}) || pts[7] != (point{7, 75, 75}) || pts[14] != (point{14, 100, 100}) {
		t.Fatalf("Standard: %d %v", code, pts)
	}
	if accts == nil || len(accts) != 0 {
		t.Errorf("no accounts: %v", accts)
	}
	code, pts, _ = post(`{"policy":{"kind":"stages","stages":[{"from":0,"window":"5h","cap":40},{"from":3,"window":"7d","cap":70}]}}`)
	if code != 200 || len(pts) != 7+1 || pts[0] != (point{0, 40, 100}) || pts[3] != (point{3, 40, 70}) {
		t.Fatalf("stages: %d %v", code, pts)
	}
	code, pts, _ = post(`{"policy":{"id":"off","kind":"off"}}`)
	if code != 200 || len(pts) != 8 || pts[0] != (point{0, 100, 100}) {
		t.Fatalf("Off: %d %v", code, pts)
	}
	for _, bad := range []string{
		`{}`,
		`{"policy":{"kind":"ramp","start":0,"days":14}}`,
		`{"policy":{"kind":"ramp","start":50,"days":400}}`,
		`{"policy":{"kind":"stages","stages":[{"from":0,"window":"1d","cap":40}]}}`,
		`{"policy":{"kind":"curve"}}`,
	} {
		if code, _, _ := post(bad); code == 200 {
			t.Errorf("took %s", bad)
		}
	}
}
