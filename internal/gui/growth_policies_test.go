package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The growth policies' routes (T-129): the state lists every policy, the
// built-ins first, and names the one in effect, Standard for a user who
// set nothing. growth-policies saves the user's own whole and refuses a
// built-in one or a default gone; growth's stages, as Settings › Growth
// policy's rows send them, are the user's stages policy and the default,
// and null makes Standard the default again with that policy kept.
func TestGrowthPolicyRoutes(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	post := func(path, body string) (int, settingsJSON) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		var s settingsJSON
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, s
	}
	s := settingsState()
	if s.GrowthInEffect != settings.GrowthStandard || len(s.GrowthPolicyList) != 4 || s.Growth != nil || len(s.GrowthStages) != 0 {
		t.Fatalf("nothing set: in effect %q, %d policies, growth %v, stages %v", s.GrowthInEffect, len(s.GrowthPolicyList), s.Growth, s.GrowthStages)
	}

	code, s := post("/api/settings/growth-policies", `{"policies":[{"id":"slow","name":"Slow","kind":"ramp","start":20,"days":60}],"default":"slow"}`)
	if code != 200 || s.GrowthInEffect != "slow" || len(s.GrowthPolicyList) != 5 || s.GrowthPolicyList[4].Name != "Slow" || !s.GrowthPolicyList[0].Builtin {
		t.Fatalf("saved: %d %q %+v", code, s.GrowthInEffect, s.GrowthPolicyList)
	}
	for _, bad := range []string{
		`{"policies":[{"id":"standard","name":"Mine","kind":"ramp","start":60,"days":14}]}`,
		`{"policies":[{"id":"slow","name":"Slow","kind":"ramp","start":0,"days":60}]}`,
		`{"policies":[],"default":"slow"}`,
	} {
		if code, _ := post("/api/settings/growth-policies", bad); code == 200 {
			t.Errorf("took %s", bad)
		}
	}
	if settings.Load().GrowthDefault != "slow" {
		t.Fatal("a refused save changed the default")
	}

	code, s = post("/api/settings/growth", `{"stages":[{"from":0,"window":"5h","cap":40}]}`)
	if code != 200 || s.GrowthInEffect != settings.GrowthMine || s.Growth == nil || len(s.GrowthStages) != 1 || s.GrowthStages[0].Cap != 40 {
		t.Fatalf("stages saved: %d %q %v %v", code, s.GrowthInEffect, s.Growth, s.GrowthStages)
	}
	code, s = post("/api/settings/growth", `{"stages":[{"from":2,"window":"7d","cap":70}]}`)
	if code != 200 || len(s.GrowthPolicyList) != 6 || s.GrowthStages[0].Cap != 70 {
		t.Fatalf("stages saved again, not to the same policy: %d %+v", code, s.GrowthPolicyList)
	}
	code, s = post("/api/settings/growth", `{"stages":null}`)
	if code != 200 || s.GrowthInEffect != settings.GrowthStandard || s.Growth != nil || len(s.GrowthPolicyList) != 6 {
		t.Fatalf("back to the default: %d %q %v %d", code, s.GrowthInEffect, s.Growth, len(s.GrowthPolicyList))
	}
	if code, _ := post("/api/settings/growth", `{"stages":[{"from":0,"window":"1h","cap":40}]}`); code == 200 {
		t.Fatal("took a window there's none of")
	}

	// stages set before policies were kept are the page's own, the default
	stages := []settings.GrowthStage{{From: 1, Window: settings.GrowthWeek, Cap: 60}}
	if err := settings.Save(settings.Settings{Growth: &stages}); err != nil {
		t.Fatal(err)
	}
	if s := settingsState(); s.GrowthInEffect != settings.GrowthMine || s.Growth == nil || len(s.GrowthStages) != 1 || len(s.GrowthPolicyList) != 5 {
		t.Fatalf("from before: %q %v %v %d", s.GrowthInEffect, s.Growth, s.GrowthStages, len(s.GrowthPolicyList))
	}
}
