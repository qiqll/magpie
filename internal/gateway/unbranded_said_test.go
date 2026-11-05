package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// brandedAs is r as Front hands it on for a caller that keeps magpie's
// words (keep) or not, its brandWriter in its context.
func brandedAs(r *http.Request, keep bool) *http.Request {
	b := &brandWriter{w: httptest.NewRecorder(), r: r, length: -1, marked: true, keep: keep}
	return r.WithContext(context.WithValue(r.Context(), brandKey{}, b))
}

// The tool-loop notice is magpie's own answer to a turn: a caller that
// isn't magpie's reads no magpie in it, the calls it names as they were
// made. The owner and another magpie read it as it always was (T-120).
func TestToolLoopNoticeSaysNoMagpie(t *testing.T) {
	v := &countingVendor{}
	toolLoopOn(t, v)
	t.Setenv("MAGPIE_ADDR", "")
	_, secrets := newCaller(t, "Laptop")
	h := New().Front()
	body := toolTurns(provider.Anthropic, toolLoopRepeats, codexRead, false)
	for _, c := range brandCallers(secrets[0]) {
		code, got := brandAsk(h, c, "POST", "/v1/messages", body)
		if code != 200 || !strings.Contains(got, "times in a row and got the same result") {
			t.Fatalf("%s: %d %.600s", c.name, code, got)
		}
		if c.keeps != strings.Contains(got, "magpie stopped this turn before it reached the model") || saysMagpie(got) != c.keeps {
			t.Errorf("%s: %.600s", c.name, got)
		}
		if !c.keeps && !strings.Contains(got, "The gateway stopped this turn") {
			t.Errorf("%s: %.600s", c.name, got)
		}
	}
	if n := v.asked.Load(); n != 0 {
		t.Fatalf("the vendor was asked %d times", n)
	}
}

// A summary compacted without a model opens in magpie's words, which a
// caller that isn't magpie's reads without magpie; what the user asked and
// the last reply, magpie in them, are as they were.
func TestLocalSummarySaysNoMagpie(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ask the magpie why"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"the magpie flew"}]}]}`)
	why := "magpie's summary request timed out"
	for _, keep := range []bool{true, false} {
		r := brandedAs(httptest.NewRequest("POST", "/v1/responses", nil), keep)
		got := localSummary(r.Context(), body, why)
		head, rest, _ := strings.Cut(got, "\n\n## The user's messages\n")
		if !strings.Contains(rest, "ask the magpie why") || !strings.Contains(rest, "the magpie flew") {
			t.Fatalf("keep %v: the conversation changed: %q", keep, got)
		}
		if keep {
			if head != "magpie compacted this conversation without a model (magpie's summary request timed out), so this is no summary: it is what the user asked and the last reply. Look at the workspace for where the work stands before going on." {
				t.Errorf("the owner's opening: %q", head)
			}
			continue
		}
		if saysMagpie(head) || !strings.HasPrefix(head, "The gateway compacted this conversation") {
			t.Errorf("a key holder's opening: %q", head)
		}
	}
}

// Codex's web.run answered by magpie's own search: when it can't search,
// what it says is magpie's words, which a caller that isn't magpie's
// reads without magpie; the query is said as it was asked.
func TestCodexWebRunSaysNoMagpie(t *testing.T) {
	fresh(t)
	searchSandbox(t)
	st := settings.Load()
	st.Searcher = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := `{"model":"glm/glm-flash","commands":{"search_query":[{"q":"magpie migration"}]}}`
	empty := `{"model":"glm/glm-flash","commands":{"open":[{"ref_id":"https://example.com"}]}}`
	for _, keep := range []bool{true, false} {
		for _, b := range []string{body, empty} {
			w := httptest.NewRecorder()
			s.alphaSearchBody(w, brandedAs(httptest.NewRequest("POST", "/v1"+AlphaSearchPath, nil), keep), []byte(b))
			got := readSearchReply(t, w).Output
			if b == body && !strings.Contains(got, "magpie migration") {
				t.Fatalf("keep %v: the query changed: %q", keep, got)
			}
			said := strings.ReplaceAll(got, "magpie migration", "")
			if saysMagpie(said) != keep {
				t.Errorf("keep %v: %q", keep, got)
			}
		}
	}
}

// A search that fails is told to the model as its tool result, which goes
// to its vendor and is said on to the caller as the model likes: a caller
// that isn't magpie's has it said without magpie, the owner and another
// magpie as it was. The searcher's words are an error's, said as T-118
// says one.
func TestSearchFailureToModelSaysNoMagpie(t *testing.T) {
	fresh(t)
	searchSandbox(t)
	t.Setenv("MAGPIE_ADDR", "")
	glm, result := newBlindModel(t)
	save(t, provider.Provider{ID: "glm", Name: "GLM", Key: "k", Chat: glm.URL, Models: []string{"glm-flash"}})
	// the one search API set up refuses its key, in words that say magpie
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"this key is revoked for magpie"}`, 401)
	}))
	t.Cleanup(api.Close)
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: "tvly-k", URL: api.URL + "/tavily"}); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.Searcher = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	_, secrets := newCaller(t, "Laptop")
	h := New().Front()
	body := `{"model":"glm/glm-flash","max_tokens":3000,"stream":true,"messages":[{"role":"user","content":"search the web: today's weather"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`
	for _, c := range brandCallers(secrets[0]) {
		if code, got := brandAsk(h, c, "POST", "/v1/messages", body); code != 200 {
			t.Fatalf("%s: %d %.400s", c.name, code, got)
		}
		got := result()
		if !strings.Contains(got, "search failed") || saysMagpie(got) != c.keeps {
			t.Errorf("%s: the model was told %q", c.name, got)
		}
	}
}

// A Claude Code run's search comes back through its callback from this
// computer, no key on it: its failure is said as the turn's caller reads
// magpie's words, and is still the error it was. A search through magpie's
// web search MCP fails in words said as its own caller reads them.
func TestRunSearchFailureSaysNoMagpie(t *testing.T) {
	fresh(t)
	searchSandbox(t)
	st := settings.Load()
	st.Searcher = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, keep := range []bool{true, false} {
		search := s.runSearch(brandedAs(httptest.NewRequest("POST", "/v1/messages", nil), keep))
		_, _, err := search(context.Background(), "weather")
		if err == nil || !errors.Is(err, errNoSearcher) && !errors.Is(err, errSearchOff) {
			t.Fatalf("keep %v: %v", keep, err)
		}
		if said := "search failed: " + sideSaid(err); saysMagpie(said) != keep {
			t.Errorf("keep %v: %q", keep, said)
		}
		// magpie's web search MCP tells its caller the same way
		_, err = s.searchMCPRun(brandedAs(httptest.NewRequest("POST", SearchMCPPath, nil), keep), "weather")
		if err == nil || saysMagpie(err.Error()) != keep {
			t.Errorf("keep %v: the MCP's search: %v", keep, err)
		}
	}
}
