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

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func unbindUser(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	gatewayUserRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/gateway/users/unbind", strings.NewReader(body)))
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The Gateway page's Unbind: one of a user's accounts let go of, of its
// gateway key and provider, the seat as the page names it; the user's
// spare is its first then under that key, the same id's other key kept as
// it is, and a user left with none under a key is gone from its keys, from
// the list with none left; one let go of meanwhile is said so; a request that names nobody
// changes nothing, nor one while the gateway binds by session; a file
// that doesn't read is said why, and left as it is.
func TestGatewayUsersUnbind(t *testing.T) {
	sandboxHome(t)
	noGateway(t)
	t.Setenv(gateway.BindEnv, "user")
	at := time.Now().UTC().Format(time.RFC3339)
	writeUsers(t, fmt.Sprintf(`{
  "users": {"antigravity": {"1": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q, "more": [{"who": "antigravity@b@x.com", "account": "b@x.com", "at": %q}]}}},
  "keys": {"k1": {"antigravity": {"1": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q}}}}
}`, at, at, at))
	// the seat as the page has it, lower-cased; the same id under the
	// key and without one is one user, each key's binding apart
	keysOf := func(out gatewayUsersOut) map[string][]string {
		t.Helper()
		if len(out.Users) != 1 || out.Users[0].User != "1" {
			t.Fatalf("users: %+v", out.Users)
		}
		keys := map[string][]string{}
		for _, k := range out.Users[0].Keys {
			keys[k.Key] = k.Seats
		}
		return keys
	}
	_, out := gatewayUsers(t, "")
	var seat string
	if s := keysOf(out)[""]; len(s) == 2 {
		seat = s[0]
	}
	if seat != "antigravity@a@x.com" {
		t.Fatalf("seat %q: %+v", seat, out.Users)
	}
	if code, out := unbindUser(t, `{"provider":"antigravity","user":"1","seat":"`+seat+`"}`); code != 200 || out["unbound"] != true {
		t.Fatalf("unbind: %d %v", code, out)
	}
	_, out = gatewayUsers(t, "")
	keys := keysOf(out)
	if fmt.Sprint(keys[""]) != "[antigravity@b@x.com]" {
		t.Fatalf("the user without a key: %+v", out.Users[0])
	}
	if fmt.Sprint(keys["k1"]) != "[antigravity@a@x.com]" {
		t.Fatalf("the key's user of the same id was changed: %+v", out.Users[0])
	}
	// let go of already: said so
	if code, out := unbindUser(t, `{"provider":"antigravity","user":"1","seat":"`+seat+`"}`); code != 200 || out["unbound"] != false {
		t.Fatalf("again: %d %v", code, out)
	}
	// the key's user's only account: gone from the list
	if code, out := unbindUser(t, `{"key":"k1","provider":"antigravity","user":"1","seat":"antigravity@a@x.com"}`); code != 200 || out["unbound"] != true {
		t.Fatalf("the key's: %d %v", code, out)
	}
	if _, out := gatewayUsers(t, ""); out.Total != 1 || len(out.Users[0].Keys) != 1 || out.Users[0].Keys[0].Key != "" || out.Users[0].Account != "b@x.com" {
		t.Fatalf("after: %+v", out)
	}
	// naming nobody
	for _, body := range []string{``, `{`, `{"provider":"antigravity","user":"1"}`, `{"user":"1","seat":"x"}`} {
		if code, _ := unbindUser(t, body); code != 400 {
			t.Fatalf("%q: %d", body, code)
		}
	}
	path := filepath.Join(filepath.Dir(provider.Path()), "user-accounts.json")
	// bound by session: kept as they are
	t.Setenv(gateway.BindEnv, "session")
	before, _ := os.ReadFile(path)
	if code, _ := unbindUser(t, `{"provider":"antigravity","user":"1","seat":"antigravity@b@x.com"}`); code != http.StatusConflict {
		t.Fatalf("by session: %d", code)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("changed by session: %s", after)
	}
	t.Setenv(gateway.BindEnv, "user")
	// a file that doesn't read
	bad := `{"users": `
	writeUsers(t, bad)
	code, out2 := unbindUser(t, `{"provider":"antigravity","user":"1","seat":"antigravity@b@x.com"}`)
	if code != http.StatusInternalServerError || !strings.Contains(fmt.Sprint(out2["error"]), "user-accounts.json") || out2["why"] == "" {
		t.Fatalf("unreadable: %d %v", code, out2)
	}
	if b, _ := os.ReadFile(path); string(b) != bad {
		t.Fatalf("written over: %s", b)
	}
}
