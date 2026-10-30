package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// fakeSignIns is the fake plugin's provider served by a gui handler, and a
// way to send it a request.
func fakeSignIns(t *testing.T) func(method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
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
	return func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(b)))
		return rec
	}
}

// A code the vendor's refusal says back bare, with no address around it
// (nothing the log rule knows), is left out of the answer to the code
// pasted and of the sign-in's state the page follows: what was pasted is
// recorded on the sign-in, not only matched in the one reply. The page is
// told the sentence's key, to say it in the reader's language.
func TestSignInFailureSaysNoBareCode(t *testing.T) {
	serve := fakeSignIns(t)
	for _, code := range []string{"echo-Zm9vYmFy", "0123456789abcdef0123456789abcdef"} {
		rec := serve("POST", "/api/plugin-signin", map[string]any{"provider": "fakeco", "method": 1, "inputs": map[string]string{"where": "home"}})
		var st provider.SignInState
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.State != "waiting" {
			t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
		}
		t.Cleanup(func() { provider.CancelSignIn(st.ID) })
		pasted := serve("POST", "/api/signin/"+st.ID+"/callback", map[string]string{"url": code})
		var out map[string]string
		if pasted.Code != http.StatusBadRequest || json.Unmarshal(pasted.Body.Bytes(), &out) != nil || strings.Contains(pasted.Body.String(), code) || out["said"] != provider.SignInHeld {
			t.Fatalf("%s: the pasted code's refusal: %d %s", code, pasted.Code, pasted.Body)
		}
		rec = serve("GET", "/api/signin/"+st.ID, nil)
		var raw map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &raw) != nil {
			t.Fatalf("status: %d %s", rec.Code, rec.Body)
		}
		if raw["state"] != "failed" || strings.Contains(rec.Body.String(), code) || raw["said"] != provider.SignInHeld {
			t.Fatalf("%s: the sign-in's state: %s", code, rec.Body)
		}
	}

	// a refusal of the code pasted that says back an answer typed before
	// the page (the PIN, unseen): left out of the answer to the paste too,
	// though the paste itself doesn't hold it
	const pin = "4821"
	rec := serve("POST", "/api/plugin-signin", map[string]any{"provider": "fakeco", "method": 1, "inputs": map[string]string{"where": "vault", "pin": pin}})
	var st provider.SignInState
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.State != "waiting" {
		t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
	}
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	pasted := serve("POST", "/api/signin/"+st.ID+"/callback", map[string]string{"url": "say-pin"})
	if pasted.Code != http.StatusBadRequest || strings.Contains(pasted.Body.String(), pin) || !strings.Contains(pasted.Body.String(), provider.SignInHeld) {
		t.Fatalf("the paste's refusal saying the PIN: %d %s", pasted.Code, pasted.Body)
	}
}

// A plugin's refusal saying back what was typed for it is left out: an
// "api" method's credential, refused by the plugin, and a question's
// answer its check says back. A choice from a list is no secret; an
// answer typed unseen is one at any length.
func TestPluginSignInSaysNoAnswerBack(t *testing.T) {
	t.Setenv("FAKE_KEYLESS", "1")
	serve := fakeSignIns(t)

	const cred = "echo-Y3JlZGVudGlhbA"
	rec := serve("POST", "/api/plugin-signin", map[string]any{"provider": "fakeco", "method": 0, "inputs": map[string]string{"credential": cred}})
	var out map[string]string
	if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &out) != nil || strings.Contains(rec.Body.String(), cred) || out["said"] != provider.SignInHeld {
		t.Fatalf("the key's refusal: %d %s", rec.Code, rec.Body)
	}

	const team = "echo-dGVhbW5hbWU"
	rec = serve("POST", "/api/plugin-signin/prompt", map[string]any{"provider": "fakeco", "method": 1, "inputs": map[string]string{"where": "work"}, "key": "team", "value": team})
	out = nil
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out["error"] == "" || strings.Contains(rec.Body.String(), team) || out["said"] != provider.SignInHeld {
		t.Fatalf("the answer's check: %d %s", rec.Code, rec.Body)
	}

	got := provider.PluginInputSecrets("fakeco", 1, map[string]string{"where": "workplace-1", "team": "team-name-1", "pin": "42"})
	if !slices.Equal(got.Given, []string{"team-name-1"}) || !slices.Equal(got.Hidden, []string{"42"}) {
		t.Fatalf("the answers kept back: %+v", got)
	}
}

// The sentence said in place of a left-out reason is a key i18n.js has in
// every language, as the page looks it up (signInWhy, api()'s said).
func TestSignInHeldIsTranslated(t *testing.T) {
	words, err := os.ReadFile("assets/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, said := range []string{provider.SignInHeld, provider.SignInHeldStatus} {
		key, _ := json.Marshal(said)
		if n := strings.Count(string(words), string(key)+":"); n != 4 {
			t.Errorf("i18n.js has %q in %d languages, want zh, zh-TW, ja and de", said, n)
		}
	}
}
