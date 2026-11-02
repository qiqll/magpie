package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Bound by user, Codex's web.run that fails on every one of the user's
// ChatGPT accounts is searched by magpie, and the tool's output, which
// goes on to the model's vendor, names neither the account nor the
// ChatGPT backend's words (T-071): a limit says userLimitSaid alone, as a
// bound user's 429 does (T-067), anything else only that the search
// failed. With no user bound the output is as it was: the account and
// the backend's words.
func TestCodexWebRunBoundFailureNamesNoAccount(t *testing.T) {
	for _, c := range []struct {
		name       string
		bound      bool
		code       int
		body       string
		says, nots []string
	}{
		{name: "bound limit", bound: true, code: 429,
			body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
			says: []string{"ChatGPT's web search failed (" + userLimitSaid + ")"},
			nots: []string{"usage limit", "usage_limit_reached", "429", "OpenAI ("}},
		{name: "bound down", bound: true, code: 500,
			body: `{"error":"backend down at shard-7"}`,
			says: []string{"ChatGPT's web search failed; magpie searched in its place."},
			nots: []string{"shard-7", "500", userLimitSaid, "OpenAI ("}},
		{name: "bound prompt refused", bound: true, code: 429,
			body: `{"error":{"message":"unapproved channel for this prompt"}}`,
			says: []string{"ChatGPT's web search failed; magpie searched in its place."},
			nots: []string{"unapproved", userLimitSaid, "OpenAI ("}},
		{name: "unbound limit", bound: false, code: 429,
			body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
			says: []string{"OpenAI (", "429 Too Many Requests", "The usage limit has been reached"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.code)
				io.WriteString(w, c.body)
			}))
			t.Cleanup(up.Close)
			was := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = was })
			apis := newSearchAPIs(t)
			if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
				t.Fatal(err)
			}
			st := settings.Load()
			st.Searcher = "off"
			if err := settings.Save(st); err != nil {
				t.Fatal(err)
			}
			s := New()
			if c.bound {
				s = byUserUpTo(t, s, 2)
			}
			h := s.Handler()
			for _, path := range []string{"/v1/alpha/search", CodexPath + "/alpha/search"} {
				got := readSearchReply(t, codexSearchFor(h, path, codexSearchBody("codex/gpt-5.5"), "alice"))
				if !strings.Contains(got.Output, "go1.27.1 is the latest release.") {
					t.Fatalf("%s: magpie didn't search in its place: %q", path, got.Output)
				}
				for _, w := range c.says {
					if !strings.Contains(got.Output, w) {
						t.Fatalf("%s: output lacks %q: %q", path, w, got.Output)
					}
				}
				nots := c.nots
				if c.bound {
					nots = append(nots, "me@example.com", "spare@example.com")
				}
				for _, w := range nots {
					if strings.Contains(got.Output, w) {
						t.Fatalf("%s: output tells %q: %q", path, w, got.Output)
					}
				}
			}
		})
	}
}
