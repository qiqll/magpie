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
			Banned   []string `json:"unusable"`
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
	if !provider.LoginUnusable("codex", "a@x.y") {
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

// Disable and Enable (POST /api/login/disable, /enable) turn the account
// off as Mark banned does: no gateway key is offered it while it is.
func TestLoginDisableRoutes(t *testing.T) {
	codexSignedInSandbox(t)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	callerKeyRoutes(mux)
	post := func(path string) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"agent":"codex","user":"a@x.y"}`)))
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body)
		}
		provider.ForgetAccounts()
	}
	offered := func() bool {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/caller-keys/models", nil))
		var l struct{ Accounts []keyModelJSON }
		json.Unmarshal(w.Body.Bytes(), &l)
		return slices.ContainsFunc(l.Accounts, func(a keyModelJSON) bool { return a.Account && a.Name == "a@x.y" })
	}
	post("/api/login/disable")
	if !provider.LoginUnusable("codex", "a@x.y") || offered() {
		t.Fatal("disabled, still offered")
	}
	post("/api/login/enable")
	if provider.LoginUnusable("codex", "a@x.y") || !offered() {
		t.Fatal("enabled, not offered")
	}
}
