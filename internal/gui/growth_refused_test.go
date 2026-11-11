package gui

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A refused growth policy is sent with its rule's code and values (T-139),
// so Settings › Growth policy says it in the page's language: a save of
// the policies, the chart of one, a policy a subscription picked deleted,
// providers.json or settings.json that can't be read. The English error
// is as it was, for a page that doesn't know the code. The page says
// every code there is (growthWhy).
func TestGrowthRefusalsSayTheirCode(t *testing.T) {
	sandboxHome(t)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	h := Handler(nil, nil)
	post := func(path, body string) map[string]string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		var out map[string]string
		if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("%s %s: %d %s", path, body, rec.Code, rec.Body)
		}
		return out
	}
	said := func(rec *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out
	}

	got := post("/api/settings/growth-policies", `{"policies":[{"id":"slow","name":"Slow","kind":"ramp","start":0,"days":60}],"default":""}`)
	if got["code"] != "growth_refused" || got["growth"] != "ramp_start" || got["got"] != "0" || got["error"] != "a growth ramp starts at 1% to 99%, not 0%" {
		t.Errorf("a ramp out of range: %v", got)
	}
	got = post("/api/settings/growth-policies", `{"policies":[{"id":"r","name":"R","kind":"ramp","start":50,"days":14}],"default":"gone"}`)
	if got["code"] != "growth_refused" || got["growth"] != "default_gone" || got["id"] != "gone" {
		t.Errorf("a default gone: %v", got)
	}
	got = post("/api/growth/preview", `{"policy":{"id":"s","name":"S","kind":"stages","stages":[{"from":0,"window":"1h","cap":50}]}}`)
	if got["code"] != "growth_refused" || got["growth"] != "stage_window" || got["got"] != "1h" {
		t.Errorf("a chart of a window there's none of: %v", got)
	}

	rec := httptest.NewRecorder()
	fail(rec, &pickedPolicyError{Policy: "Slow", Subs: []string{"Codex", "Codex · a@example.com"}})
	if got := said(rec); got["code"] != "growth_refused" || got["growth"] != growthPicked || got["policy"] != "Slow" || got["subs"] != "Codex, Codex · a@example.com" || !strings.Contains(got["error"], "picked the growth policy") {
		t.Errorf("a picked policy deleted: %v", got)
	}

	// providers.json, which says who picked a policy, can't be read: a
	// folder in its place
	if err := os.MkdirAll(provider.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	err := growthPickedKept([]settings.GrowthPolicy{{ID: "slow"}}, nil)
	var ge *settings.GrowthError
	if !errors.As(err, &ge) || errors.Unwrap(err) == nil {
		t.Fatalf("providers.json unread: %v", err)
	}
	rec = httptest.NewRecorder()
	fail(rec, err)
	if got := said(rec); got["growth"] != growthPicksUnreadable || got["cause"] == "" || !strings.HasPrefix(got["error"], "which subscriptions picked a growth policy can't be read") {
		t.Errorf("providers.json unread: %v", got)
	}
	if err := os.Remove(provider.Path()); err != nil {
		t.Fatal(err)
	}

	// settings.json can't be read
	if err := os.MkdirAll(settings.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	lastgood.Reset()
	t.Cleanup(lastgood.Reset)
	got = post("/api/settings/growth-policies", `{"policies":[],"default":""}`)
	if got["growth"] != settings.GrowthUnreadable || got["path"] != settings.Path() || got["cause"] == "" {
		t.Errorf("settings.json unread: %v", got)
	}

	// a value never writes over a key api() reads as something else
	rec = httptest.NewRecorder()
	fail(rec, &settings.GrowthError{Code: "name_twice", Vals: map[string]string{"name": "N", "code": "x", "error": "x", "growth": "x", "said": "x", "why": "x"}, Msg: "the English"})
	if got := said(rec); got["code"] != "growth_refused" || got["growth"] != "name_twice" || got["error"] != "the English" || got["said"] != "" || got["why"] != "" || got["name"] != "N" {
		t.Errorf("values over the reply's own keys: %v", got)
	}

	page, err := os.ReadFile("assets/plugins/growth/settings.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range slices.Concat(settings.GrowthCodes, []string{growthPicked, growthPicksUnreadable}) {
		if !regexp.MustCompile(`\n    ` + code + `: \(\) => t\("`).Match(page) {
			t.Errorf("the growth page doesn't say %q in its words", code)
		}
	}
}
