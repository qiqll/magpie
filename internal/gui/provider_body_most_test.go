package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// T-165: a provider's save, and every other body providerRoutes reads, is
// read up to a bound: a save of thousands of keys still goes through, one
// over providerBodyMost is refused and saves nothing.
func TestProviderBodiesAreBounded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(path string, body []byte) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(string(body))))
		return w.Code, w.Body.String()
	}
	body := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// a large save as a user makes it: 40,000 keys pasted, about 2.6MB
	keys := make([]string, 40000)
	for i := range keys {
		keys[i] = fmt.Sprintf("sk-%060d", i)
	}
	big := body(map[string]any{"id": "relay", "name": "Relay", "chat": "http://127.0.0.1:1/v1", "key": strings.Join(keys, ","), "new": true})
	if len(big) < 2<<20 || len(big) > providerBodyMost {
		t.Fatalf("the large save is %d bytes", len(big))
	}
	if code, out := post("/api/provider/save", big); code != 200 {
		t.Fatalf("a large save: %d %.200s", code, out)
	}
	if p, err := provider.Find("relay"); err != nil || len(p.Keys)+1 != len(keys) {
		t.Fatalf("the large save kept %v: %v", p, err)
	}

	over := strings.Repeat("x", providerBodyMost)
	if code, out := post("/api/provider/save", body(map[string]any{"id": "huge", "name": over, "chat": "http://127.0.0.1:1/v1", "key": "sk-h", "new": true})); code != http.StatusBadRequest || !strings.Contains(out, "too large") {
		t.Fatalf("a save over the bound: %d %.200s", code, out)
	}
	if _, err := provider.Find("huge"); err == nil {
		t.Fatal("a save over the bound was kept")
	}
	if code, out := post("/api/keys/import", body(map[string]any{"id": "relay", "key": "sk-new," + over})); code != http.StatusBadRequest || !strings.Contains(out, "too large") {
		t.Fatalf("a paste over the bound: %d %.200s", code, out)
	}
	if p, _ := provider.Find("relay"); len(p.Keys)+1 != len(keys) {
		t.Fatalf("a paste over the bound added keys: %d", len(p.Keys)+1)
	}

	// the small bodies beside them have small bounds
	small := strings.Repeat("x", 1<<20)
	for _, path := range []string{"/api/providers/arrange", "/api/login/switch", "/api/auth-holds/hours", "/api/auth-holds/release", "/api/codex/app/reopen", "/api/signin"} {
		if code, out := post(path, body(map[string]any{"order": []string{small}, "agent": small, "holder": small, "bundle": small, "user": small})); code != http.StatusBadRequest || !strings.Contains(out, "too large") {
			t.Errorf("%s over its bound: %d %.200s", path, code, out)
		}
	}
}
