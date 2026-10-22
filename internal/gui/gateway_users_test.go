package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

type gatewayUsersOut struct {
	Total    int               `json:"total"`
	Matched  int               `json:"matched"`
	Accounts []userAccountJSON `json:"accounts"`
	Users    []userJSON        `json:"users"`
	Error    string            `json:"error"`
}

func gatewayUsers(t *testing.T, q string) (int, gatewayUsersOut) {
	t.Helper()
	mux := http.NewServeMux()
	gatewayUserRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/gateway/users"+q, nil))
	var out gatewayUsersOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return w.Code, out
}

func writeUsers(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(provider.Path()), "user-accounts.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The Gateway page's User bindings: every binding of user-accounts.json,
// a key's and those without one, each account counted with its users, the
// key named, narrowed by words; no file is nobody, one that doesn't read
// an error, never nobody.
func TestGatewayUsersRoute(t *testing.T) {
	sandboxHome(t)
	if code, out := gatewayUsers(t, ""); code != 200 || out.Total != 0 || len(out.Users) != 0 {
		t.Fatalf("no file: %d %+v", code, out)
	}
	if _, err := access.Update("add-key", access.Change{Name: "Tenant A"}); err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	var keyID string
	for _, k := range keys {
		if k.Name == "Tenant A" {
			keyID = k.ID
		}
	}
	at := time.Date(2026, 10, 22, 4, 21, 33, 0, time.UTC).Format(time.RFC3339)
	writeUsers(t, fmt.Sprintf(`{
  "users": {"antigravity": {"local-1": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q}}},
  "keys": {%q: {"antigravity": {
    "1": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q},
    "2": {"who": "antigravity@b@x.com", "account": "b@x.com", "at": %q}
  }}, "gone-key": {"codex": {"9": {"who": "codex@c@x.com", "account": "c@x.com", "at": %q}}}}
}`, at, keyID, at, at, at))
	code, out := gatewayUsers(t, "")
	if code != 200 || out.Total != 4 || out.Matched != 4 || len(out.Users) != 4 {
		t.Fatalf("%d %+v", code, out)
	}
	counts := map[string]int{}
	for _, a := range out.Accounts {
		counts[a.Provider+" "+a.Account] = a.Users
	}
	if counts["antigravity a@x.com"] != 2 || counts["antigravity b@x.com"] != 1 || counts["codex c@x.com"] != 1 || len(counts) != 3 {
		t.Fatalf("accounts %+v", out.Accounts)
	}
	named := map[string]string{}
	for _, u := range out.Users {
		named[u.User] = u.KeyName
		if u.At.IsZero() || u.Account == "" {
			t.Fatalf("user %+v", u)
		}
	}
	if named["1"] != "Tenant A" || named["local-1"] != "" || named["9"] != "" {
		t.Fatalf("key names %v", named)
	}
	// narrowed: every word must match the user, account, provider or key
	if _, out := gatewayUsers(t, "?q=b%40x"); out.Matched != 1 || len(out.Users) != 1 || out.Users[0].User != "2" || out.Total != 4 || len(out.Accounts) != 3 {
		t.Fatalf("q=b@x: %+v", out)
	}
	if _, out := gatewayUsers(t, "?q=tenant+a%40x"); out.Matched != 1 || out.Users[0].User != "1" {
		t.Fatalf("q=tenant a@x: %+v", out)
	}

	// many: a page of them, and how many match
	var b strings.Builder
	b.WriteString(`{"users": {"antigravity": {`)
	for i := range usersShown + 50 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"u%d": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q}`, i, at)
	}
	b.WriteString(`}}}`)
	writeUsers(t, b.String())
	if _, out := gatewayUsers(t, ""); out.Total != usersShown+50 || out.Matched != usersShown+50 || len(out.Users) != usersShown {
		t.Fatalf("many: total %d matched %d shown %d", out.Total, out.Matched, len(out.Users))
	}

	writeUsers(t, `{"users": `)
	if code, out := gatewayUsers(t, ""); code == 200 || !strings.Contains(out.Error, "user-accounts.json") {
		t.Fatalf("unreadable: %d %+v", code, out)
	}
}
