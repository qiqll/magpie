package gui

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// T-083: the Providers page's kept list (keptProvider) has no proxies,
// headers or extra body, and an editor opened on it saved none, which
// cleared what the user had set. A save that leaves one of them out keeps
// it, a custom provider's and a preset's alike; one that sends "" or {}
// still clears it. Proxies and extra body left out were kept before
// T-083 already; only the headers check is red on the code before it.
func TestProviderSaveKeepsWhatItLeavesOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	find := func(id string) provider.Provider {
		t.Helper()
		got, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		return *got
	}
	for _, c := range []struct{ name, seed, save string }{
		{"custom", "", `"name":"Relay","chat":"https://relay.example.invalid/v1"`},
		{"preset", "deepseek", `"name":"DeepSeek","preset":"deepseek"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := provider.Provider{ID: "relay-" + c.name, Name: "Relay", Chat: "https://relay.example.invalid/v1"}
			if c.seed != "" {
				var err error
				if p, err = provider.FromPreset(c.seed); err != nil {
					t.Fatal(err)
				}
				p.ID = "relay-" + c.name
			}
			p.Key, p.Keys = "sk-primary-one", []provider.KeyAccount{{Key: "sk-second-two"}}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			ks := find(p.ID).KeyList()
			if len(ks) != 2 {
				t.Fatalf("keys %v", ks)
			}
			own := map[string]string{ks[0].ID: "socks5://10.0.0.5:1080", ks[1].ID: "direct"}
			p = find(p.ID)
			p.Proxy, p.AccountProxies = "http://10.0.0.2:7890", own
			p.Headers = map[string]string{"X-Team": "t1"}
			p.ExtraBody = provider.ExtraBody{"prompt_cache_key": []byte(`"k"`)}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			head := `{"id":"` + p.ID + `",` + c.save
			post(head + `}`)
			got := find(p.ID)
			if got.Proxy != "http://10.0.0.2:7890" || !maps.Equal(got.AccountProxies, own) || got.Headers["X-Team"] != "t1" || string(got.ExtraBody["prompt_cache_key"]) != `"k"` {
				t.Fatalf("left out, not kept: proxy %q, own %v, headers %v, extra body %v", got.Proxy, got.AccountProxies, got.Headers, got.ExtraBody)
			}
			post(head + `,"proxy":"","accountProxies":{},"headers":{},"extraBody":{}}`)
			got = find(p.ID)
			if got.Proxy != "" || len(got.AccountProxies) != 0 || len(got.Headers) != 0 || len(got.ExtraBody) != 0 {
				t.Fatalf("cleared, still kept: proxy %q, own %v, headers %v, extra body %v", got.Proxy, got.AccountProxies, got.Headers, got.ExtraBody)
			}
		})
	}
}
