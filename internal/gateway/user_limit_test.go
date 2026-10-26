package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// limitUpstream stands in for the ChatGPT backend as usersUpstream does,
// an account set in out answering out of its allowance, back in the
// seconds out names.
func limitUpstream(t *testing.T) (out func(acct string, secs int), asked func(reset bool) []string) {
	t.Helper()
	var mu sync.Mutex
	back := map[string]int{}
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		acct := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		got = append(got, acct)
		secs := back[acct]
		mu.Unlock()
		if secs > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprintf(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":%d}}`, secs)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"from `+acct+`"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return func(acct string, secs int) {
			mu.Lock()
			defer mu.Unlock()
			back[acct] = secs
		}, func(reset bool) []string {
			mu.Lock()
			defer mu.Unlock()
			out := slices.Clone(got)
			if reset {
				got = nil
			}
			return out
		}
}

// limitOf is the limit a reply says its user is at, and until when; a
// reply that says until when says it in Retry-After too.
func limitOf(t *testing.T, rec *httptest.ResponseRecorder) (string, time.Time) {
	t.Helper()
	limit, said := rec.Header().Get(UserLimitHeader), rec.Header().Get(UserUntilHeader)
	if said == "" {
		return limit, time.Time{}
	}
	until, err := time.Parse(time.RFC3339, said)
	if err != nil {
		t.Fatalf("%s %q: %v", UserUntilHeader, said, err)
	}
	after, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After %q beside %s %s", rec.Header().Get("Retry-After"), UserUntilHeader, said)
	}
	if d := time.Until(until) - time.Duration(after)*time.Second; d > 2*time.Second || d < -2*time.Second {
		t.Fatalf("Retry-After %ds, %s %s: %s apart", after, UserUntilHeader, said, d)
	}
	return limit, until
}

// near says t is d from now, give or take a minute.
func near(t time.Time, d time.Duration) bool {
	off := time.Until(t) - d
	return off < time.Minute && off > -time.Minute
}

// A user whose account is out of its allowance is told so, and when it is
// back, by the reply it fails with and by every reply until then, which
// asks nobody; a user on another account, or a request with none, isn't.
// A new user bound to none, every account out, is told when the one asked
// is back.
func TestUserLimitSaysWhenItsAccountIsBack(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	out, asked := limitUpstream(t)
	s := byUser(New())
	rec := userPost(s, "codex/gpt-5.5", "alice", "hi")
	if limit, _ := limitOf(t, rec); rec.Code != 200 || limit != "" {
		t.Fatalf("first: %d %q %s", rec.Code, limit, rec.Body.String())
	}
	bound := asked(true)[0]
	out(bound, 7200)
	rec = userPost(s, "codex/gpt-5.5", "alice", "again")
	limit, until := limitOf(t, rec)
	if rec.Code != 429 || limit != "quota" || !near(until, 2*time.Hour) {
		t.Fatalf("out of its allowance: %d %q until %v: %v %s", rec.Code, limit, until, rec.Header(), rec.Body.String())
	}
	if got := asked(true); slices.ContainsFunc(got, func(a string) bool { return a != bound }) {
		t.Fatalf("another account was asked: %v", got)
	}
	// resting: told the same, nobody asked
	rec = userPost(s, "codex/gpt-5.5", "alice", "and again")
	if l, u := limitOf(t, rec); rec.Code != 429 || l != "quota" || !u.Equal(until) {
		t.Fatalf("resting: %d %q until %v, first said %v: %s", rec.Code, l, u, until, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked while resting: %v", got)
	}
	// another user, on the other account
	rec = userPost(s, "codex/gpt-5.5", "bob", "hello")
	if l, _ := limitOf(t, rec); rec.Code != 200 || l != "" || rec.Header().Get(UserUntilHeader) != "" {
		t.Fatalf("bob: %d %q %s", rec.Code, l, rec.Body.String())
	}
	other := asked(true)[0]
	// every account out: a new user is bound to none, and told when the
	// one asked for it is back
	out(other, 3600)
	userPost(s, "codex/gpt-5.5", "bob", "spent") // other rests now
	asked(true)
	rec = userPost(s, "codex/gpt-5.5", "carol", "hi")
	limit, until = limitOf(t, rec)
	if r := lastRoute(s); r.User == nil || r.User.Why != "none" {
		t.Fatalf("carol: route %+v", r.User)
	}
	if rec.Code != 429 || limit != "quota" || until.IsZero() {
		t.Fatalf("bound to none, every account out: %d %q until %v: %v %s", rec.Code, limit, until, rec.Header(), rec.Body.String())
	}
}

// A user whose account is at the usage cap set on it in magpie is told
// "cap", and when its window renews: with the other account free, and
// with every account capped too. A request without a user isn't.
func TestUserLimitSaysTheCap(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	if rec := userPost(s, "codex/gpt-5.5", "alice", "hi"); rec.Code != 200 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	bound := lastRoute(s).User.Account
	other := "spare@example.com"
	if bound == other {
		other = "me@example.com"
	}
	capUsage(t, map[string]float64{bound: 75, other: 20})
	for _, a := range []string{bound, other} {
		if err := provider.SetAccountCap("codex", a, 70); err != nil {
			t.Fatal(err)
		}
	}
	rec := userPost(s, "codex/gpt-5.5", "alice", "capped")
	if limit, until := limitOf(t, rec); rec.Code != 429 || limit != "cap" || !near(until, 3*time.Hour) {
		t.Fatalf("its account capped: %d %q until %v: %s", rec.Code, limit, until, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked past the cap: %v", got)
	}
	if rec := userPost(s, "codex/gpt-5.5", "bob", "hi"); rec.Code != 200 || rec.Header().Get(UserLimitHeader) != "" {
		t.Fatalf("bob, on the other: %d %v", rec.Code, rec.Header())
	}
	// every account capped, the other's window renewing first: alice is
	// told when her own is back, not the other's
	old := allowances
	t.Cleanup(func() { allowances = old })
	renews := map[string]time.Duration{bound: 3 * time.Hour, other: time.Hour}
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, d := range renews {
			out[u] = provider.Allowance{{Used: 75, Resets: time.Now().Add(d), Span: 5 * time.Hour}}
		}
		return out
	}
	rec = userPost(s, "codex/gpt-5.5", "alice", "all capped")
	if limit, until := limitOf(t, rec); rec.Code != 429 || limit != "cap" || !near(until, 3*time.Hour) {
		t.Fatalf("every account capped: %d %q until %v: %s", rec.Code, limit, until, rec.Body.String())
	}
	// pinned to an account, a user isn't bound: no limit of its own
	rec = postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"pinned"}`, map[string]string{UserHeader: "alice", AccountHeader: bound})
	if rec.Code < 400 || rec.Header().Get(UserLimitHeader) != "" {
		t.Fatalf("pinned, every account capped: %d %v", rec.Code, rec.Header())
	}
}

// A rest of one model of the account, its others still answered, is said
// with the model it is for.
func TestUserLimitOfOneModel(t *testing.T) {
	back := time.Now().Add(time.Hour)
	b := userBinding{Who: "codex@me@example.com", Account: "me@example.com"}
	refused := boundRefusal("alice", b, "codex", []Rest{{Why: failQuota, Until: back, Model: "gpt-5.5"}}, planned{})
	if refused.status != 429 || refused.limit != "quota" || refused.model != "gpt-5.5" || !refused.until.Equal(back) {
		t.Fatalf("%+v", refused)
	}
	h := http.Header{}
	userLimited(h, refused.limit, refused.until, refused.model)
	if h.Get(UserModelHeader) != "gpt-5.5" || h.Get(UserLimitHeader) != "quota" || h.Get(UserUntilHeader) == "" {
		t.Fatalf("%v", h)
	}
	h = http.Header{}
	userLimited(h, "quota", back, "")
	if _, ok := h[UserModelHeader]; ok {
		t.Fatalf("the account as a whole said as a model: %v", h)
	}
}
