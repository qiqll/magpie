package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	areas "github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
)

// The owner's Areas: an area is made with its admin key, given an
// account magpie has and refused one another area owns, not deleted
// while it owns one, and its admin key and the console's are kept out
// of the window's gateway keys, whose order the store keeps.
func TestAreasRoutes(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-owner-000000001", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	areaRoutes(mux)
	callerKeyRoutes(mux)
	do := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		method := "POST"
		if body == nil {
			method = "GET"
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(b))))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if _, err := access.Update("add-key", access.Change{Name: "Laptop"}); err != nil {
		t.Fatal(err)
	}
	c, out := do("/api/areas/create", map[string]any{"note": "学习", "providers": []string{"relay"}})
	if c != 200 || !strings.HasPrefix(out["secret"].(string), access.Prefix) {
		t.Fatal("create:", c, out)
	}
	do("/api/areas/create", map[string]any{"note": "工作"})
	all, _ := areas.List()
	study, work := all[0].ID, all[1].ID
	if who, ok := access.Authenticate(out["secret"].(string)); !ok || who.Admin != access.AdminArea || who.Area != study {
		t.Fatal("its key:", who, ok)
	}
	ref := "relay/" + provider.KeyID("sk-owner-000000001")
	if c, out := do("/api/areas/claim", map[string]any{"id": study, "ref": ref}); c != 200 {
		t.Fatal("claim:", c, out)
	}
	if c, _ := do("/api/areas/claim", map[string]any{"id": work, "ref": ref}); c == 200 {
		t.Fatal("one account given to two areas")
	}
	if c, _ := do("/api/areas/delete", map[string]any{"id": study}); c == 200 {
		t.Fatal("deleted while it owns an account")
	}
	if c, out := do("/api/areas/console-add", map[string]any{}); c != 200 || out["secret"] == "" {
		t.Fatal("console key:", c, out)
	}
	if c, _ := do("/api/areas/console-add", map[string]any{}); c == 200 {
		t.Fatal("a second console key")
	}
	if c, out := do("/api/areas/allow-ips", map[string]any{"ips": []string{"10.0.0.0/24"}}); c != 200 {
		t.Fatal("allow list:", c, out)
	}
	if c, _ := do("/api/areas/allow-ips", map[string]any{"ips": []string{"not an address"}}); c == 200 {
		t.Fatal("a bad allow list saved")
	}
	c, out = do("/api/caller-keys", nil)
	keys := out["keys"].([]any)
	if c != 200 || len(keys) != 1 || keys[0].(map[string]any)["name"] != "Laptop" {
		t.Fatal("the window's gateway keys:", c, keys)
	}
	c, out = do("/api/areas", nil)
	st, _ := json.Marshal(out)
	if c != 200 || !strings.Contains(string(st), `"adminAllowIPs":["10.0.0.0/24"]`) || !strings.Contains(string(st), `"consoleKey":{`) {
		t.Fatal("state:", c, string(st))
	}
	if strings.Contains(string(st), "sk-magpie-key-") && !strings.Contains(string(st), "…") {
		t.Fatal("the state tells a key's value:", string(st))
	}
	do("/api/areas/release", map[string]any{"ref": ref})
	if c, out := do("/api/areas/delete", map[string]any{"id": study}); c != 200 {
		t.Fatal("delete once empty:", c, out)
	}
	if _, ok, _ := access.AreaKey(study); ok {
		t.Fatal("the deleted area's admin key is left")
	}
}
