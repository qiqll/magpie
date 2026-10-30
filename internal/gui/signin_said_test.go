package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A sign-in that failed says why on the page, and never with the code,
// token or address the vendor's refusal held (as the log leaves them
// out): not in the answer to the code pasted, not in the sign-in's state
// the page follows. A refusal holding none of them is said as it is.
func TestSignInFailureSaysNoSecret(t *testing.T) {
	bun := testenv.Bun(t)
	dir := t.TempDir()
	testenv.SetHome(t, dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	t.Cleanup(plugin.Settle)
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
	paste := func(code string) (*httptest.ResponseRecorder, provider.SignInState) {
		t.Helper()
		rec := serve("POST", "/api/plugin-signin", map[string]any{"provider": "fakeco", "method": 1, "inputs": map[string]string{"where": "home"}})
		var st provider.SignInState
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.State != "waiting" {
			t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
		}
		t.Cleanup(func() { provider.CancelSignIn(st.ID) })
		pasted := serve("POST", "/api/signin/"+st.ID+"/callback", map[string]string{"url": code})
		rec = serve("GET", "/api/signin/"+st.ID, nil)
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("status: %d %s", rec.Code, rec.Body)
		}
		return pasted, st
	}
	const code = "leak-Zm9vYmFyYmF6cXV4"
	pasted, st := paste(code)
	if pasted.Code != http.StatusBadRequest || strings.Contains(pasted.Body.String(), code) || strings.Contains(pasted.Body.String(), "fake.invalid") || !strings.Contains(pasted.Body.String(), "left out") {
		t.Fatalf("the pasted code's refusal: %d %s", pasted.Code, pasted.Body)
	}
	if st.State != "failed" || strings.Contains(st.Error, code) || strings.Contains(st.Error, "fake.invalid") || !strings.Contains(st.Error, "left out") {
		t.Fatalf("the sign-in's state: %+v", st)
	}
	pasted, st = paste("expired")
	if !strings.Contains(pasted.Body.String(), "the sign-in page expired") || st.State != "failed" || !strings.Contains(st.Error, "the sign-in page expired") {
		t.Fatalf("a refusal with nothing to leave out isn't said: %s %+v", pasted.Body, st)
	}
}
