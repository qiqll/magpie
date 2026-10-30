package gateway

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
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
		rec := serveAs(s, c.from, c.body, c.h)
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
	"searchWith":      "a web search, its request's user (forUser, askAsUser)",
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
