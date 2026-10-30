package gateway

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// Bound by user, serve itself refuses a request that names no user, one
// UserHeader can't take, or a tier user-tiers.json hasn't, whoever calls
// it: a 400 that says why, nobody asked, and the log has the refusal. A
// route's own check (userNeeded) is not what holds it. With the user it
// is served; bound by session it needs none.
func TestServeNeedsTheUserWhoeverCalls(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	if err := provider.SaveGroup(provider.Group{Name: "Pair", Members: []string{"codex/gpt-5.5", "codex/gpt-5.4-mini"}}); err != nil {
		t.Fatal(err)
	}
	s := byUser(New())
	serveAs := func(s *Server, from provider.Protocol, body string, h map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://magpie/v1/x", nil)
		r.Header.Set("Content-Type", "application/json")
		for k, v := range h {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.serve(w, r, from, []byte(body))
		return w
	}
	resp := `{"model":"%s","stream":true,"input":"hi"}`
	for _, c := range []struct {
		name string
		from provider.Protocol
		body string
		h    map[string]string
		says string
	}{
		{"responses", provider.Responses, strings.Replace(resp, "%s", "codex/gpt-5.5", 1), nil, "--bind=user"},
		{"chat", provider.Chat, `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, nil, "--bind=user"},
		{"messages", provider.Anthropic, `{"model":"codex/gpt-5.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil, "--bind=user"},
		{"blank", provider.Responses, strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{UserHeader: "  "}, "--bind=user"},
		{"control", provider.Responses, strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{UserHeader: "a\tb"}, "control"},
		{"tier", provider.Responses, strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{UserHeader: "alice", UserTierHeader: "gold"}, "gold"},
		{"group", provider.Responses, strings.Replace(resp, "%s", "group/pair", 1), nil, "--bind=user"},
		{"pinned", provider.Responses, strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{AccountHeader: "spare@example.com"}, "--bind=user"},
		{"magpie's own", provider.Chat, `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, map[string]string{"User-Agent": RouterAgent}, "--bind=user"},
	} {
		was := len(s.Recent())
		rec := serveAs(s, c.from, c.body, c.h)
		if n := len(s.Recent()); n != was+1 {
			t.Errorf("%s: %d calls logged, want 1", c.name, n-was)
		}
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), c.says) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if why := refusedWhy(s); !strings.Contains(why, c.says) {
			t.Errorf("%s: logged as %q", c.name, why)
		}
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked without a user: %v", got)
	}
	if rec := serveAs(s, provider.Responses, strings.Replace(resp, "%s", "codex/gpt-5.5", 1), map[string]string{UserHeader: "alice"}); rec.Code != 200 {
		t.Fatalf("with its user: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 {
		t.Fatalf("with its user asked %v", got)
	}
	if rec := serveAs(New(), provider.Responses, strings.Replace(resp, "%s", "codex/gpt-5.5", 1), nil); rec.Code != 200 {
		t.Fatalf("bound by session, no user: %d %s", rec.Code, rec.Body.String())
	}
}

// Bound by user, magpie's own asks through serve — a routing group's
// classifier, a web search, an image description — made for no user's
// request are refused by serve, nobody asked; made for a user's (forUser)
// they go to that user's account.
func TestOwnAsksWithoutAUserAskNobody(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, heads := usersUpstream(t, nil)
	s := byUser(New())
	p, model, ok := provider.Resolve("codex/gpt-5.5")
	if !ok {
		t.Fatal("codex/gpt-5.5 not served")
	}
	img := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	asks := []struct {
		name string
		ask  func(ctx context.Context) error
	}{
		{"classifier", func(ctx context.Context) error {
			_, err := s.askChat(ctx, "codex/gpt-5.5", classifyBody("codex/gpt-5.5", "", []string{"feature", "bug"}, before{}, "fix this"))
			return err
		}},
		{"search", func(ctx context.Context) error {
			_, _, err := s.searchWith(ctx, p, model, "magpie", "")
			return err
		}},
		{"description", func(ctx context.Context) error {
			_, err := s.describe(ctx, "codex/gpt-5.5", img)
			return err
		}},
	}
	none := access.WithIdentity(context.Background(), access.Identity{})
	for _, a := range asks {
		if err := a.ask(none); err == nil {
			t.Errorf("%s: answered for no user", a.name)
		}
		if got := asked(true); len(got) != 0 {
			t.Errorf("%s: asked for no user: %v", a.name, got)
		}
	}
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	r.Header.Set(UserHeader, "alice")
	for _, a := range asks {
		*heads = nil
		a.ask(s.forUser(context.Background(), r))
		if got := asked(true); len(got) == 0 {
			t.Errorf("%s: alice's ask went nowhere", a.name)
		}
		for _, h := range *heads {
			if h.Get(UserHeader) != "" {
				t.Errorf("%s: %s went upstream", a.name, UserHeader)
			}
		}
	}
	if b := boundIn(t, "codex"); b["alice"] == "" || len(b) != 1 {
		t.Fatalf("bound %v", b)
	}
}

// serveCallers is every function of this package that calls serve, and how
// the request it serves names its user bound by user. serve refuses one
// that names none (TestServeNeedsTheUserWhoeverCalls); a new caller is
// added here once it is known to carry the user it is made for.
var serveCallers = map[string]string{
	"serveAgent":      "an agent's request, past its route's userNeeded or noUser",
	"codexFallBack":   "Codex's turn on CodexPath, past noUser",
	"codexCompact":    "Codex's compaction on CodexPath or handle, past noUser or userNeeded",
	"codexTitle":      "Codex's request on CodexPath or handle, as it came",
	"codexShaped":     "Codex's request on CodexPath or handle, as it came",
	"codexPrediction": "Codex's request on CodexPath or handle, as it came",
	"askChat":         "a classifier's ask, its turn's user (askFor, askAsUser)",
	"searchWith":      "a web search, its request's user (forUser, askAsUser), a Claude run's its turn's (runSearch)",
	"askVisionWith":   "an image description or question, its request's user (forUser, askAsUser)",
}

// Every call of serve in the package is from a function serveCallers names,
// and every one it names still calls it: a caller added without a look
// at how it names its user fails here.
func TestEveryServeCallerIsKnown(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var found []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// s.serve(...), and serve passed on as a value (s.serve)
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "serve" {
					found = append(found, fn.Name.Name)
				}
				for _, arg := range call.Args {
					if sel, ok := arg.(*ast.SelectorExpr); ok && sel.Sel.Name == "serve" {
						t.Errorf("%s: %s passes serve on as a value; its callers can't be checked here", fset.Position(arg.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	for _, fn := range found {
		if _, ok := serveCallers[fn]; !ok {
			t.Errorf("%s calls serve: say in serveCallers how its request names its user bound by user", fn)
		}
	}
	for fn := range serveCallers {
		if !slices.Contains(found, fn) {
			t.Errorf("%s no longer calls serve: take it out of serveCallers", fn)
		}
	}
}

// Bound by user, a Claude Code run's web search, asked back through its
// MCP callback with no user of its own, is asked of the user of the turn
// that started the run, on that user's account, never another's; a run
// started for no user's turn searches nothing.
func TestClaudeRunSearchesAsItsTurnsUser(t *testing.T) {
	keys, users := dsKeyed(t, provider.Responses)
	s := byUserUpTo(t, New(), 1)
	turnOf := func(user string) *http.Request {
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		if user != "" {
			r.Header.Set(UserHeader, user)
		}
		return r
	}
	// the callback's ctx: no user, as mcpCall's request carries none
	callback := context.Background()
	if _, _, err := s.runSearch(turnOf(""))(callback, "weather in Hangzhou"); err == nil {
		t.Fatal("searched for no user's turn")
	}
	if got := keys("deepseek-flash"); len(got) != 0 {
		t.Fatalf("searched without a user: %q", got)
	}
	var alice, bob []string
	for range 2 {
		for _, u := range []string{"alice", "bob"} {
			if _, _, err := s.runSearch(turnOf(u))(callback, "weather in Hangzhou"); err != nil {
				t.Fatalf("%s: %v", u, err)
			}
			got := keys("deepseek-flash")
			if u == "alice" {
				alice = append(alice, got[len(got)-1])
			} else {
				bob = append(bob, got[len(got)-1])
			}
		}
	}
	oneKeyEach(t, "a Claude run's search", alice, bob)
	if n := users(); n != 0 {
		t.Fatalf("%d searches took the user header to the vendor", n)
	}
}

// Bound by user, usersOn and usersOwn refuse a request or an ask that
// names no user, as noUser does, whichever route let it through: never
// the whole pool of the provider, and nobody bound. With the user each
// gives that user's accounts; bound by session, the pool as it is.
func TestUsersOnAndOwnRefuseNoUser(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	cs := seatCandidates(*p)
	var pl planned
	for range cs {
		pl.order = append(pl.order, Weighed{Provider: "codex"})
	}
	if len(cs) < 2 {
		t.Fatalf("%d accounts, want two to tell a pool from a user's", len(cs))
	}
	s := byUser(New())
	none := httptest.NewRequest("POST", "/v1/images/generations", nil)
	own, _, u, err := s.usersOn(none, *p, cs, pl)
	if err == nil || len(own) != 0 || u != nil {
		t.Fatalf("usersOn with no user: %d accounts, try %v, err %v", len(own), u, err)
	}
	if st := userRefusedStatus(err); st != http.StatusBadRequest || !strings.Contains(err.Error(), "--bind=user") {
		t.Fatalf("usersOn with no user: %d %v", st, err)
	}
	mine, bound, err := s.usersOwn(context.Background(), *p, "gpt-5.5", provider.Responses)
	if err == nil || !bound || len(mine) != 0 || !strings.Contains(err.Error(), "--bind=user") {
		t.Fatalf("usersOwn with no user: %d accounts, bound %v, err %v", len(mine), bound, err)
	}
	if _, err := os.Stat(usersPath()); !os.IsNotExist(err) {
		t.Fatalf("user-accounts.json written with no user: %v", err)
	}

	alice := httptest.NewRequest("POST", "/v1/images/generations", nil)
	alice.Header.Set(UserHeader, "alice")
	if own, _, u, err := s.usersOn(alice, *p, cs, pl); err != nil || len(own) != 1 || u == nil {
		t.Fatalf("usersOn for alice: %d accounts, try %v, err %v", len(own), u, err)
	}
	if mine, bound, err := s.usersOwn(s.forUser(context.Background(), alice), *p, "gpt-5.5", provider.Responses); err != nil || !bound || len(mine) != 1 {
		t.Fatalf("usersOwn for alice: %d accounts, bound %v, err %v", len(mine), bound, err)
	}

	if b := boundIn(t, "codex"); len(b) != 1 || b["alice"] == "" {
		t.Fatalf("bound %v", b)
	}

	sess := New()
	if own, _, u, err := sess.usersOn(none, *p, cs, pl); err != nil || len(own) != len(cs) || u != nil {
		t.Fatalf("bound by session, usersOn: %d of %d, try %v, err %v", len(own), len(cs), u, err)
	}
	if _, bound, err := sess.usersOwn(context.Background(), *p, "gpt-5.5", provider.Responses); bound || err != nil {
		t.Fatalf("bound by session, usersOwn: bound %v err %v", bound, err)
	}
}
