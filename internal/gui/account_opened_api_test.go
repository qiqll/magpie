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
	"github.com/yetone/magpie/internal/renewal"
)

// An account's opening time is set, changed and cleared through
// provider/accountopened (T-154), and the providers list says its period
// as renewal.Of works it out, by the account in lower case: 6 days left
// asks nothing, 5 asks for a renewal, and one past its end has ended.
func TestAccountOpenedRoute(t *testing.T) {
	home := sandboxHome(t)
	jwt := func(claims any) string {
		b, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     jwt(map[string]any{"email": "paid@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "paid"}}),
		"access_token": jwt(map[string]any{"exp": time.Now().Add(24 * time.Hour).Unix()}), "refresh_token": "fake-refresh", "account_id": "paid",
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
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/provider/accountopened", strings.NewReader(body)))
		return rec
	}
	said := func(rec *httptest.ResponseRecorder) map[string]*renewal.State {
		t.Helper()
		var out struct {
			Providers []providerJSON `json:"providers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		for _, p := range out.Providers {
			if p.ID == "codex" {
				return p.AccountRenewal
			}
		}
		t.Fatalf("no Codex listed: %s", rec.Body)
		return nil
	}
	day := 24 * time.Hour
	for _, c := range []struct {
		left time.Duration
		days int
		due  string
	}{
		{6*day + time.Minute, 7, ""},
		{6*day - time.Minute, 6, ""},
		{5*day - time.Minute, 5, renewal.DueSoon},
		{-time.Minute, 0, renewal.DueEnded},
	} {
		opened := time.Now().Add(c.left - renewal.Days*day).UTC().Truncate(time.Second)
		rec := post(`{"id":"codex","account":"Paid@example.com","opened":"` + opened.Format(time.RFC3339) + `"}`)
		if rec.Code != 200 {
			t.Fatalf("%v left: %d %s", c.left, rec.Code, rec.Body)
		}
		s := said(rec)["paid@example.com"]
		if s == nil || !s.Opened.Equal(opened) || s.DaysLeft != c.days || s.Due != c.due {
			t.Errorf("%v left: the list says %+v, want %d days, due %q", c.left, s, c.days, c.due)
		}
	}
	rec := post(`{"id":"codex","account":"paid@example.com","opened":""}`)
	if rec.Code != 200 || said(rec) != nil {
		t.Fatalf("cleared, the list says %v: %d %s", said(rec), rec.Code, rec.Body)
	}
	if at := provider.AccountOpenedOf("codex", "paid@example.com"); !at.IsZero() {
		t.Fatalf("cleared, providers.json keeps %v", at)
	}
	for _, body := range []string{
		`{"id":"codex","account":"paid@example.com","opened":"2026-11-01"}`,
		`{"id":"codex","account":"paid@example.com","opened":"1999-01-01T00:00:00Z"}`,
		`{"id":"codex","account":"nobody@example.com","opened":"` + time.Now().UTC().Format(time.RFC3339) + `"}`,
	} {
		if rec := post(body); rec.Code == 200 {
			t.Errorf("%s: kept", body)
		}
	}
}
