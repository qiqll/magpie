package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// T-110: Settings › Network's saved proxies are set as one list, checked
// as settings.Save checks a proxy, and come back in /api/settings; a
// refused list keeps what was there.
func TestSavedProxiesAPI(t *testing.T) {
	dir := t.TempDir()
	testenv.SetHome(t, dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	h := Handler(&openCounter{}, nil)
	serve := func(body any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings/proxies", bytes.NewReader(b)))
		return rec
	}
	rec := serve(map[string]any{"proxies": []map[string]string{{"name": "Home", "url": "http://me:s3cret@127.0.0.1:7890"}}})
	var st struct {
		Proxies []settings.SavedProxy `json:"proxies"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || len(st.Proxies) != 1 || st.Proxies[0].Name != "Home" {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if rec = serve(map[string]any{"proxies": []map[string]string{{"name": "Bad", "url": "ftp://me:s3cret@x:1"}}}); rec.Code < 400 || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("a bad proxy: %d %s", rec.Code, rec.Body)
	}
	if got := settings.Load().Proxies; len(got) != 1 || got[0].URL != "http://me:s3cret@127.0.0.1:7890" {
		t.Fatalf("kept %+v", got)
	}
	// a whole Settings save from the page leaves them as they are
	b, _ := json.Marshal(map[string]any{"theme": "dark"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", bytes.NewReader(b)))
	if rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	if got := settings.Load().Proxies; len(got) != 1 {
		t.Fatalf("a Settings save changed the saved proxies: %+v", got)
	}
}

// A saved proxy's Test asks the trace page through that proxy, whatever
// the proxy in force, and says it back without its login.
func TestProxyCheckOfASavedProxy(t *testing.T) {
	client, proxyURL, asked := proxyCheckRig(t)
	withLogin := strings.Replace(proxyURL, "http://", "http://me:s3cret@", 1)
	got, err := checkNamedProxy(context.Background(), client, withLogin)
	if err != nil {
		t.Fatal(err)
	}
	if *asked != 1 || got.Through == nil || got.Through.IP != "203.0.113.9" || got.Direct.IP != "198.51.100.7" || got.Source != "saved" {
		t.Fatalf("check %+v (asked %d)", got, *asked)
	}
	if strings.Contains(got.Proxy, "s3cret") || got.Proxy == "" {
		t.Fatalf("said the proxy as %q", got.Proxy)
	}
	for _, bad := range []string{"direct", "ftp://x:1"} {
		if _, err := checkNamedProxy(context.Background(), client, bad); err == nil {
			t.Fatalf("%q was checked as a proxy", bad)
		}
	}
}

// The sign-in's Callback URL field sends the proxy picked beside it: the
// pasted end is traded through that proxy, the state says it, and it is
// kept as the account's; a proxy that isn't one is refused.
func TestSignInCallbackThroughAPickedProxy(t *testing.T) {
	bun := testenv.Bun(t)
	dir := t.TempDir()
	testenv.SetHome(t, dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	t.Setenv("FAKE_SIGNIN", "http://vendor.invalid/signin")
	t.Cleanup(plugin.Settle)
	var mu sync.Mutex
	var seen []string
	picked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
	}))
	t.Cleanup(picked.Close)
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(t.Context(), abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := Handler(&openCounter{}, nil)
	serve := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(b)))
		return rec
	}
	start := func() provider.SignInState {
		t.Helper()
		rec := serve("POST", "/api/plugin-signin", map[string]any{"provider": "fakeco", "method": 1, "inputs": map[string]string{"where": "home"}})
		var st provider.SignInState
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.State != "waiting" {
			t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
		}
		t.Cleanup(func() { provider.CancelSignIn(st.ID) })
		return st
	}

	st := start()
	if rec := serve("POST", "/api/signin/"+st.ID+"/callback", map[string]any{"url": "good", "proxy": "ftp://x"}); rec.Code < 400 {
		t.Fatalf("ftp://x taken: %d %s", rec.Code, rec.Body)
	}
	if now, _ := provider.SignInStatus(st.ID); now.State != "waiting" {
		t.Fatalf("a refused proxy ended the sign-in: %+v", now)
	}
	if rec := serve("POST", "/api/signin/"+st.ID+"/callback", map[string]any{"url": "good", "proxy": picked.URL}); rec.Code != http.StatusNoContent {
		t.Fatalf("pasted: %d %s", rec.Code, rec.Body)
	}
	rec := serve("GET", "/api/signin/"+st.ID, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.State != "done" || st.Proxy != picked.URL {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "http://vendor.invalid/signin/token" {
		t.Fatalf("the picked proxy saw %q", got)
	}
	if l := provider.ProxyOfLogin(provider.PluginID("fakeco"), st.User); l != picked.URL {
		t.Fatalf("%s's requests go through %q", st.User, l)
	}
	if g := settings.Load().Proxy; g != "" {
		t.Fatalf("the global proxy became %q", g)
	}
}
