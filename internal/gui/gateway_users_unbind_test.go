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
// provider, the seat as the page names it, under every gateway key that
// shares its users the user is bound under and without one, as the
// gateway binds the same id one user (T-075, T-089); the user's spare is
// its first then under each, and a user left with none is gone from the
// list; one let go of meanwhile is said so; naming the key or none, the
// same group, changes nothing of that; a
// request that names nobody changes nothing, nor one while the gateway
// binds by session; a file that doesn't read is said why, and left as it
// is.
func TestGatewayUsersUnbind(t *testing.T) {
	sandboxHome(t)
	noGateway(t)
	t.Setenv(gateway.BindEnv, "user")
	at := time.Now().UTC().Format(time.RFC3339)
	// a key that shares its users (T-089): its user 1 is the one without a key
	k1 := sharingKey(t, "Front")
	writeUsers(t, fmt.Sprintf(`{
  "users": {"antigravity": {"1": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q, "more": [{"who": "antigravity@b@x.com", "account": "b@x.com", "at": %q}]}}},
  "keys": {%q: {"antigravity": {"1": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q}}}}
}`, at, at, k1, at))
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
	if fmt.Sprint(keys[k1]) != "[antigravity@b@x.com]" {
		t.Fatalf("the same id under the key, not let go of it alike: %+v", out.Users[0])
	}
	// let go of already, a key named or not: said so
	for _, body := range []string{`{"provider":"antigravity","user":"1","seat":"` + seat + `"}`, `{"key":"` + k1 + `","provider":"antigravity","user":"1","seat":"` + seat + `"}`} {
		if code, out := unbindUser(t, body); code != 200 || out["unbound"] != false {
			t.Fatalf("again %s: %d %v", body, code, out)
		}
	}
	// its only account, a key named: let go of under every key, and the
	// user gone from the list
	if code, out := unbindUser(t, `{"key":"`+k1+`","provider":"antigravity","user":"1","seat":"antigravity@b@x.com"}`); code != 200 || out["unbound"] != true {
		t.Fatalf("its only one: %d %v", code, out)
	}
	if _, out := gatewayUsers(t, ""); out.Total != 0 || len(out.Users) != 0 {
		t.Fatalf("after: %+v", out)
	}
	writeUsers(t, fmt.Sprintf(`{"users": {"antigravity": {"1": {"who": "antigravity@b@x.com", "account": "b@x.com", "at": %q}}}}`, at))
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
