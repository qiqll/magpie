package gateway

import (
	"bytes"
	"cmp"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// keysSeen serves next behind a server that records the key each model was
// asked with.
func keysSeen(t *testing.T, next http.Handler) (up *httptest.Server, keys func(model string) []string) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string][]string{}
	up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		mu.Lock()
		if !strings.HasSuffix(r.URL.Path, "/models") {
			key := cmp.Or(r.Header.Get("x-api-key"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			seen[modelOf(b)] = append(seen[modelOf(b)], key)
		}
		mu.Unlock()
		next.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	return up, func(model string) []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen[model]...)
	}
}

// dsKeyed is a DeepSeek that searches on its API on, with two keys, the
// searcher magpie picks by itself.
func dsKeyed(t *testing.T, on provider.Protocol) (keys func(model string) []string, _ func() int) {
	t.Helper()
	searchSandbox(t)
	ds := newFakeDeepSeek(t)
	up, keys := keysSeen(t, ds.Config.Handler)
	searchesOn(t, on, up.URL)
	p := provider.Provider{ID: "ds", Name: "DeepSeek", Key: "ka", Keys: []provider.KeyAccount{{Key: "kb"}}, Models: []string{"deepseek-flash"}}
	if on == provider.Anthropic {
		p.Anthropic = up.URL + "/anthropic"
	} else {
		p.Chat, p.Responses = up.URL+"/v1", up.URL+"/v1"
	}
	save(t, p)
	st := settings.Load()
	st.Searcher = ""
	settings.Save(st)
	return keys, func() int { return 0 }
}
