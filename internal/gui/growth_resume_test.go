package gui

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A skipped ramp-up is said and undone (T-158): after provider/skipgrowth
// the providers list sends the account in accountGrowthSkipped, by its
// name in lower case, and provider/resumegrowth clears it, so the list
// sends it no more and providers.json keeps no skip.
func TestGrowthSkipResumeRoutes(t *testing.T) {
	home := sandboxHome(t)
	jwt := func(claims any) string {
		b, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     jwt(map[string]any{"email": "skip@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "skip"}}),
		"access_token": jwt(map[string]any{"exp": time.Now().Add(24 * time.Hour).Unix()}), "refresh_token": "fake-refresh", "account_id": "skip",
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
	skipped := func(rec *httptest.ResponseRecorder) map[string]time.Time {
		t.Helper()
		var out struct {
			Providers []providerJSON `json:"providers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		for _, p := range out.Providers {
			if p.ID == "codex" {
				return p.AccountGrowthSkipped
			}
		}
		t.Fatalf("no Codex listed: %s", rec.Body)
		return nil
	}

	rec := post("/api/provider/skipgrowth", `{"id":"codex","account":"Skip@example.com"}`)
	if rec.Code != 200 {
		t.Fatalf("skip: %d %s", rec.Code, rec.Body)
	}
	if at := skipped(rec)["skip@example.com"]; at.IsZero() || time.Since(at) > time.Minute {
		t.Fatalf("skipped, the list says %v: %s", skipped(rec), rec.Body)
	}
	rec = post("/api/provider/resumegrowth", `{"id":"codex","account":"SKIP@example.com"}`)
	if rec.Code != 200 || skipped(rec) != nil {
		t.Fatalf("resumed, the list says %v: %d %s", skipped(rec), rec.Code, rec.Body)
	}
	if at := provider.AccountGrowthSkippedOf("codex", "skip@example.com"); !at.IsZero() {
		t.Fatalf("resumed, providers.json keeps the skip of %v", at)
	}
	if rec := post("/api/provider/resumegrowth", `{"id":"nobody","account":"skip@example.com"}`); rec.Code == 200 {
		t.Fatal("resumed an account of a provider there is none of")
	}
}
