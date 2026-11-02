package gateway

import (
	"bytes"
	"io"
	"log"
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
// failed, and so does magpie's own search failing after it (a search
// API's words untold). The account and the backend's words are magpie's
// log's. With no user bound the output is as it was: the account and
// the backend's and search API's words.
func TestCodexWebRunBoundFailureNamesNoAccount(t *testing.T) {
	for _, c := range []struct {
		name       string
		bound      bool
		code       int
		body       string
		apiKey     string // the search API's key; a wrong one is refused
		says, nots []string
		logs       []string
	}{
		{name: "bound limit", bound: true, code: 429,
			body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
			says: []string{"ChatGPT's web search failed (" + userLimitSaid + ")"},
			nots: []string{"usage limit", "usage_limit_reached", "429", "OpenAI ("},
			logs: []string{"OpenAI (me@example.com): 429 Too Many Requests", "The usage limit has been reached"}},
		{name: "bound down", bound: true, code: 500,
			body: `{"error":"backend down at shard-7"}`,
			says: []string{"ChatGPT's web search failed; magpie searched in its place."},
			nots: []string{"shard-7", "500", userLimitSaid, "OpenAI ("},
			logs: []string{"OpenAI (me@example.com): 500", "backend down at shard-7"}},
		{name: "bound prompt refused", bound: true, code: 429,
			body: `{"error":{"message":"unapproved channel for this prompt"}}`,
			says: []string{"ChatGPT's web search failed; magpie searched in its place."},
			nots: []string{"unapproved", userLimitSaid, "OpenAI ("}},
		{name: "bound down, magpie's search refused", bound: true, code: 500,
			body: `{"error":"backend down at shard-7"}`, apiKey: "tvly-wrong",
			says: []string{"ChatGPT's web search failed; magpie searched in its place.", "magpie couldn't search the web for OpenAI news site:openai.com"},
			nots: []string{"shard-7", "bad key", "tvly-wrong", "401", "OpenAI ("},
			logs: []string{"backend down at shard-7", "bad key"}},
		{name: "unbound, magpie's search refused", bound: false, code: 500,
			body: `{"error":"backend down at shard-7"}`, apiKey: "tvly-wrong",
			says: []string{"OpenAI (", "backend down at shard-7", "magpie couldn't search the web for OpenAI news site:openai.com: ", "bad key"}},
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
			key := c.apiKey
			if key == "" {
				key = "tvly-k"
			}
			if err := provider.SetSearchAPI(apis.api("tavily", key)); err != nil {
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
			var logs bytes.Buffer
			logWas := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(logWas) })
			for _, path := range []string{"/v1/alpha/search", CodexPath + "/alpha/search"} {
				got := readSearchReply(t, codexSearchFor(h, path, codexSearchBody("codex/gpt-5.5"), "alice"))
				if searched := strings.Contains(got.Output, "go1.27.1 is the latest release."); searched != (c.apiKey == "") {
					t.Fatalf("%s: magpie's search in its place answered %v: %q", path, searched, got.Output)
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
				for _, w := range c.logs {
					if !strings.Contains(logs.String(), w) {
						t.Fatalf("%s: log lacks %q: %s", path, w, logs.String())
					}
				}
			}
		})
	}
}
