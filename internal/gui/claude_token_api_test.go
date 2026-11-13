package gui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// /api/signin/claude-token keeps the proxy picked for the account it adds
// (需求方 2026-11-13), refuses a bad one before adding anything, and
// leaves the proxies as they are when none is sent.
func TestClaudeTokenRouteKeepsItsProxy(t *testing.T) {
	sandboxHome(t)
	h := Handler(nil, nil)
	const tok = "sk-ant-oat01-setuptoken-abcdefghijklmnopqrstuvwxyz0123456789"
	post := func(body string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/signin/claude-token", strings.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := post(`{"token":"` + tok + `","proxy":"ftp://x:1"}`); code == 200 {
		t.Fatal("a bad proxy taken")
	}
	if n := len(provider.Logins("claude")); n != 0 {
		t.Fatalf("a refused proxy left %d accounts added", n)
	}
	code, out := post(`{"token":"` + tok + `","proxy":"socks5://u:p@203.0.113.7:1080"}`)
	user, _ := out["user"].(string)
	if code != 200 || user == "" || out["added"] != true || out["error"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	if p := provider.ProxyOfLogin("claude", user); p != "socks5://u:p@203.0.113.7:1080" {
		t.Fatalf("its proxy is %q", p)
	}
	if strings.Contains(user, "u:p@") {
		t.Fatal("the proxy's login is in the answer")
	}
	// the same token again, none sent: the same account, its proxy kept
	code, out = post(`{"token":"` + tok + `"}`)
	if code != 200 || out["user"] != user || out["added"] != false {
		t.Fatalf("again: %d %v", code, out)
	}
	if p := provider.ProxyOfLogin("claude", user); p != "socks5://u:p@203.0.113.7:1080" {
		t.Fatalf("again, its proxy is %q", p)
	}
}
