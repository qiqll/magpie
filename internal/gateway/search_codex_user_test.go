package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Bound by user, a ChatGPT account's own web search (the backend's
// /alpha/search, codexSearchBy) is made on the account the user is on,
// never on the provider's first for every user (usersSearchKey, as a
// Kimi Code plan's search service is); a search a call without a user
// makes is DefaultUser's (T-157), on the account it is on.
func TestChatGPTWebSearchOnTheUsersAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	reply, err := os.ReadFile("testdata/codex_alpha_search.json")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var tried []string
	leaked := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, "/alpha/search") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		if r.Header.Get(UserHeader) != "" {
			leaked++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	if err := catalog.SaveLive("codex", provider.CodexBase, []catalog.Model{{ID: "gpt-6.1-sol"}, {ID: "gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	if p, m, ok := searcher(); !ok || p.ID != "codex" || m != codexSearchModel {
		t.Fatalf("searcher = %s/%s %v, want codex by its web search", p.ID, m, ok)
	}
	h := lanGuard(byUserUpTo(t, New(), 1).Handler())
	const here = "127.0.0.1:5000"

	// without a user: DefaultUser's (T-157), on the one account it is
	// bound to
	m := readMCP(t, mcpPost(h, here, mcpSearchCall))
	if m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "go.dev") {
		t.Fatalf("without a user: %+v", m)
	}
	mu.Lock()
	if len(tried) != 1 {
		t.Fatalf("searched without a user on %q", tried)
	}
	mu.Unlock()
	if b := boundIn(t, "codex"); len(b) != 1 || b[DefaultUser] == "" {
		t.Fatalf("without a user bound %v, want %s", b, DefaultUser)
	}

	var alice, bob []string
	for i := 0; i < 2; i++ {
		for _, u := range []string{"alice", "bob"} {
			m := readMCP(t, mcpPost(h, here, mcpSearchCall, UserHeader, u))
			if m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "go.dev") {
				t.Fatalf("%s: %+v", u, m)
			}
			mu.Lock()
			last := tried[len(tried)-1]
			mu.Unlock()
			if u == "alice" {
				alice = append(alice, last)
			} else {
				bob = append(bob, last)
			}
		}
	}
	oneKeyEach(t, "ChatGPT's web search", alice, bob)
	if b := boundIn(t, "codex"); len(b) != 3 || b["alice"] == "" || b["bob"] == "" {
		t.Fatalf("bound %v, want %s, alice and bob", b, DefaultUser)
	}
	if leaked != 0 {
		t.Fatalf("%d searches took the user header to ChatGPT", leaked)
	}
}
