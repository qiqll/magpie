package gui

import (
	"encoding/json"
	"maps"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Settings › Growth policy sets how many users an account of a provider
// may be bound to by tier (T-243): POST /api/settings/provider-shares
// keeps each tier by its name in user-tiers.json (an alias, another
// case), takes 0 to 1000, and refuses a tier the file hasn't, a share out
// of range or a provider not in Providers, writing nothing; none clears
// the provider, and the page is told the tiers, their shares and the
// providers.
func TestProviderSharesSaved(t *testing.T) {
	sandboxHome(t)
	if _, err := provider.Add(provider.Provider{ID: "own", Name: "Own", Chat: "http://127.0.0.1:1/v1", Key: "sk-own", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	srv := Handler(nil, nil)
	post := func(body string) (int, settingsJSON) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings/provider-shares", strings.NewReader(body)))
		var s settingsJSON
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, s
	}
	code, s := post(`{"provider":"own","shares":{"FREE":10,"2":2,"pro":0}}`)
	if want := map[string]int{"free": 10, "plus": 2, "pro": 0}; code != 200 || !maps.Equal(settings.Load().ProviderShares["own"], want) || !maps.Equal(s.ProviderShares["own"], want) {
		t.Fatalf("%d %v", code, settings.Load().ProviderShares)
	}
	if len(s.ShareTiers) != 4 || s.ShareTiers[0].Name != "free" || s.ShareTiers[0].Share != 2 || s.ShareTiers[1].Aliases[0] != "2" || s.ShareMost != 1000 {
		t.Fatalf("tiers: %+v %d", s.ShareTiers, s.ShareMost)
	}
	if !strings.Contains(func() string { b, _ := json.Marshal(s.ShareProviders); return string(b) }(), `{"id":"own","name":"Own"}`) {
		t.Fatalf("providers: %+v", s.ShareProviders)
	}
	for _, body := range []string{
		`{"provider":"own","shares":{"gold":2}}`,
		`{"provider":"own","shares":{"free":-1}}`,
		`{"provider":"own","shares":{"free":1001}}`,
		`{"provider":"own","shares":{"free":2,"1":3}}`,
		`{"provider":"gone","shares":{"free":2}}`,
		`{"provider":"","shares":{"free":2}}`,
	} {
		if code, _ := post(body); code != 400 || settings.Load().ProviderShares["own"]["free"] != 10 || len(settings.Load().ProviderShares) != 1 {
			t.Fatalf("%s: %d %v", body, code, settings.Load().ProviderShares)
		}
	}
	if code, _ := post(`{"provider":"own","shares":null}`); code != 200 || settings.Load().ProviderShares != nil {
		t.Fatalf("cleared: %d %v", code, settings.Load().ProviderShares)
	}
	// a provider gone from Providers can still be cleared
	if code, _ := post(`{"provider":"gone","shares":{}}`); code != 200 {
		t.Fatalf("gone cleared: %d", code)
	}
}
