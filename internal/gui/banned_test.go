package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Mark banned and Lift ban (POST /api/login/ban, /unban) set the user's
// mark on the account, the providers' view says it as banned, and the
// models a gateway key can be held to list it no more while it is.
func TestLoginBanRoutes(t *testing.T) {
	codexSignedInSandbox(t)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	callerKeyRoutes(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	pickable := func() (accounts []string, banned []string) {
		var l struct {
			Accounts []keyModelJSON
			Banned   []string
		}
		if w := request("GET", "/api/caller-keys/models", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
			t.Fatal(w.Code, w.Body)
		}
		for _, a := range l.Accounts {
			if a.Account {
				accounts = append(accounts, a.Name)
			}
		}
		return accounts, l.Banned
	}
	if as, _ := pickable(); !slices.Contains(as, "a@x.y") {
		t.Fatalf("not offered to a key before: %v", as)
	}
	if w := request("POST", "/api/login/ban", `{"agent":"codex","user":"a@x.y"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	provider.ForgetAccounts()
	if !provider.LoginBanned("codex", "a@x.y") {
		t.Fatal("not marked")
	}
	if as, banned := pickable(); slices.Contains(as, "a@x.y") || len(banned) != 1 {
		t.Fatalf("offered to a key while banned: %v %v", as, banned)
	}
	if w := request("POST", "/api/login/unban", `{"agent":"codex","user":"a@x.y"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	provider.ForgetAccounts()
	if as, _ := pickable(); !slices.Contains(as, "a@x.y") {
		t.Fatalf("lifted, not offered: %v", as)
	}
}
