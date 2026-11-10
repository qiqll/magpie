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

// An account's own growth policy (T-133): provider/accountgrowthpolicy
// sets the pick by the account's name, which the providers list says as
// accountGrowthPolicy, and refuses a policy there is none of. The policy
// page's In use counts the account's pick on its subscription though the
// subscription follows the default; a save of the policies that deletes
// one an account picked is refused with the subscription and the account
// named, and goes through once the account follows its subscription.
func TestGrowthAccountPickRoutes(t *testing.T) {
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
	picked := func(rec *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		var out struct {
			Providers []providerJSON `json:"providers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		for _, p := range out.Providers {
			if p.ID == "codex" {
				return p.AccountGrowthPolicy
			}
		}
		t.Fatalf("no Codex listed: %s", rec.Body)
		return nil
	}

	if rec := post("/api/settings/growth-policies", `{"policies":[{"id":"slow","name":"Slow","kind":"ramp","start":20,"days":60}],"default":""}`); rec.Code != 200 {
		t.Fatalf("policies: %d %s", rec.Code, rec.Body)
	}
	rec := post("/api/provider/accountgrowthpolicy", `{"id":"codex","account":"Pick@example.com","growthPolicy":"slow"}`)
	if rec.Code != 200 || picked(rec)["pick@example.com"] != "slow" {
		t.Fatalf("picked Slow for the account: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/provider/accountgrowthpolicy", `{"id":"codex","account":"pick@example.com","growthPolicy":"nope"}`); rec.Code == 200 {
		t.Fatal("took a policy there is none of")
	}
	if s, _ := provider.Find("codex"); s.AccountGrowthPolicy["pick@example.com"] != "slow" || s.GrowthPolicy != "" {
		t.Fatalf("a refused pick changed it: %v %q", s.AccountGrowthPolicy, s.GrowthPolicy)
	}

	rec = post("/api/growth/preview", `{"policy":{"id":"slow","name":"Slow","kind":"ramp","start":20,"days":60}}`)
	var pv struct {
		Uses []provider.GrowthUse `json:"uses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil || rec.Code != 200 || len(pv.Uses) != 1 || pv.Uses[0].Provider != "codex" || pv.Uses[0].Picked || !pv.Uses[0].AccountsOnly || pv.Uses[0].AccountsPicked != 1 {
		t.Fatalf("Slow's In use: %d %s", rec.Code, rec.Body)
	}

	rec = post("/api/settings/growth-policies", `{"policies":[],"default":""}`)
	if rec.Code == 200 || !strings.Contains(rec.Body.String(), "Codex · pick@example.com") {
		t.Fatalf("deleted a policy an account picked: %d %s", rec.Code, rec.Body)
	}
	if !settings.Load().GrowthKnows("slow") {
		t.Fatal("the refused save deleted it")
	}
	if rec := post("/api/provider/accountgrowthpolicy", `{"id":"codex","account":"pick@example.com","growthPolicy":""}`); rec.Code != 200 || picked(rec) != nil {
		t.Fatalf("back to its subscription's: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/settings/growth-policies", `{"policies":[],"default":""}`); rec.Code != http.StatusOK {
		t.Fatalf("deleted once no account picks it: %d %s", rec.Code, rec.Body)
	}
}
