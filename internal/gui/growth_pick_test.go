package gui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A subscription's growth policy (T-131): provider/growthpolicy sets the
// pick, which the providers list says, and refuses a policy there is none
// of. The policy page's chart is told the subscriptions the policy holds,
// the pick among them; a save of the policies that deletes one a
// subscription picked is refused with its name, and goes through once it
// follows another.
func TestGrowthPickRoutes(t *testing.T) {
	home := sandboxHome(t)
	jwt := func(claims any) string {
		b, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     jwt(map[string]any{"email": "pick@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "pick"}}),
		"access_token": jwt(map[string]any{"exp": time.Now().Add(24 * time.Hour).Unix()}), "refresh_token": "fake-refresh", "account_id": "pick",
	}}
	b, _ := json.Marshal(auth)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	h := Handler(nil, nil)
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	picked := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var out struct {
			Providers []providerJSON `json:"providers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		for _, p := range out.Providers {
			if p.ID == "codex" {
				return p.GrowthPolicy
			}
		}
		t.Fatalf("no Codex listed: %s", rec.Body)
		return ""
	}

	if rec := post("/api/settings/growth-policies", `{"policies":[{"id":"slow","name":"Slow","kind":"ramp","start":20,"days":60}],"default":""}`); rec.Code != 200 {
		t.Fatalf("policies: %d %s", rec.Code, rec.Body)
	}
	rec := post("/api/provider/growthpolicy", `{"id":"codex","growthPolicy":"slow"}`)
	if rec.Code != 200 || picked(rec) != "slow" {
		t.Fatalf("picked Slow: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/provider/growthpolicy", `{"id":"codex","growthPolicy":"nope"}`); rec.Code == 200 {
		t.Fatal("took a policy there is none of")
	}
	if s, _ := provider.Find("codex"); s.GrowthPolicy != "slow" {
		t.Fatalf("a refused pick changed it: %q", s.GrowthPolicy)
	}

	rec = post("/api/growth/preview", `{"policy":{"id":"slow","name":"Slow","kind":"ramp","start":20,"days":60}}`)
	var pv struct {
		Uses []provider.GrowthUse `json:"uses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil || rec.Code != 200 || len(pv.Uses) != 1 || pv.Uses[0].Provider != "codex" || !pv.Uses[0].Picked {
		t.Fatalf("Slow's In use: %d %s", rec.Code, rec.Body)
	}
	rec = post("/api/growth/preview", `{"policy":{"id":"standard","kind":"ramp","start":50,"days":14}}`)
	pv.Uses = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil || pv.Uses == nil || len(pv.Uses) != 0 {
		t.Fatalf("Standard holds Codex still: %s", rec.Body)
	}

	rec = post("/api/settings/growth-policies", `{"policies":[],"default":""}`)
	if rec.Code == 200 || !strings.Contains(rec.Body.String(), "Codex") {
		t.Fatalf("deleted a policy Codex picked: %d %s", rec.Code, rec.Body)
	}
	if !settings.Load().GrowthKnows("slow") {
		t.Fatal("the refused save deleted it")
	}
	if rec := post("/api/provider/growthpolicy", `{"id":"codex","growthPolicy":""}`); rec.Code != 200 || picked(rec) != "" {
		t.Fatalf("back to the default: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/settings/growth-policies", `{"policies":[],"default":""}`); rec.Code != http.StatusOK {
		t.Fatalf("deleted once no subscription picks it: %d %s", rec.Code, rec.Body)
	}
}
