package gateway

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// With no user bound, a request whose last account left has to be signed
// in again is told a limit to try again after, never the vendor's 401 or
// magpie's words to sign in again (the owner's decision, 2026-10-29, as
// a user's is under --bind=user): the account's row says it lapsed, and
// the usage log and the log say why.
func TestUnboundLapsedToldAsALimit(t *testing.T) {
	logTo := func(t *testing.T) *strings.Builder {
		var logged strings.Builder
		log.SetOutput(&logged)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		lapseRefusedSaid.Clear()
		return &logged
	}
	t.Run("its only account revoked", func(t *testing.T) {
		codexSignedIn(t)
		tried := revokedUpstream(t, codexAccessToken(t))
		logged := logTo(t)
		s := New()
		for _, input := range []string{"ping", "again"} {
			rec := postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`, nil)
			limitedNotLapsed(t, rec)
			if c := s.Recent(); len(c) == 0 || !strings.Contains(c[0].Error, "sign-in lapsed: codex@me@example.com") {
				t.Fatalf("%s: the usage log doesn't say why: %+v", input, c)
			}
		}
		if got := tried(true); len(got) == 0 {
			t.Fatal("the account was never asked")
		}
		if got := codexLapsed(t, "me@example.com"); !strings.Contains(got, "sign in again") {
			t.Fatalf("the account's row doesn't say it lapsed: %q", got)
		}
		if l := logged.String(); !strings.Contains(l, "codex account me@example.com has to be signed in again") {
			t.Fatalf("not logged with provider and account: %s", l)
		}
	})
	t.Run("brought back as the other is outside the key", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, map[string]int{"acct-2": http.StatusUnauthorized})
		lapseSaved(t, "spare@example.com")
		keys, secrets := newCaller(t, "Spare only")
		if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/spare@example.com"}}); err != nil {
			t.Fatal(err)
		}
		logged := logTo(t)
		s := New()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`))
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		s.Handler().ServeHTTP(rec, req)
		if got := asked(true); !slices.Equal(got, []string{"acct-2"}) {
			t.Fatalf("asked %v, not the lapsed one alone: %d %s", got, rec.Code, rec.Body.String())
		}
		limitedNotLapsed(t, rec)
		if c := s.Recent(); len(c) == 0 || !strings.Contains(c[0].Error, "sign-in lapsed") {
			t.Fatalf("the usage log doesn't say why: %+v", c)
		}
		if got := codexLapsed(t, "spare@example.com"); !strings.Contains(got, "has expired") {
			t.Fatalf("the account's row doesn't say it lapsed: %q", got)
		}
		if l := logged.String(); !strings.Contains(l, "codex account spare@example.com has to be signed in again") || !strings.Contains(l, "has expired") {
			t.Fatalf("not logged with provider, account and why: %s", l)
		}
	})
	t.Run("an account not marked keeps its error", func(t *testing.T) {
		// a 401 of an account with nothing marked (a key's or a sign-in
		// a refresh may mend) isn't a lapse: it goes as it came
		codexSignedIn(t)
		usersUpstream(t, map[string]int{"acct-1": http.StatusUnauthorized})
		rec := postAt(New(), "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
}

// A pin to an account left out as it has to be signed in again says so to
// serve, which tells the caller only a limit (lapsedRetryAfter).
func TestPinToSaysLapsed(t *testing.T) {
	pl := planned{left: []Weighed{{Who: "spare@example.com", Model: "gpt-5.5", Lapsed: "spare@example.com's sign-in has expired"}}}
	pl.left[0].ID, pl.left[0].Provider = "codex@spare@example.com", "codex"
	if _, _, status, msg, lapsed := pinTo("spare@example.com", nil, pl); status != http.StatusTooManyRequests || lapsed.Lapsed != pl.left[0].Lapsed || lapsed.ID != "codex@spare@example.com" || !strings.Contains(msg, "has expired") {
		t.Fatalf("%d %q %+v", status, msg, lapsed)
	}
	pl.left[0].Lapsed = ""
	if _, _, _, _, lapsed := pinTo("spare@example.com", nil, pl); lapsed.Lapsed != "" {
		t.Fatalf("not lapsed, said %+v", lapsed)
	}
}

// A drawing with no user bound, on an account the vendor answers revoked,
// is told a limit as a turn is, nothing of the sign-in; with another
// account it is drawn there instead.
func TestUnboundDrawingOnARevokedAccount(t *testing.T) {
	draw := `{"model":"codex/gpt-image-2","prompt":"a magpie"}`
	t.Run("its only account", func(t *testing.T) {
		codexSignedIn(t)
		drawn := revokedDrawn(t)
		s := New()
		rec := postAt(s, "/v1/images/generations", draw, nil)
		limitedNotLapsed(t, rec)
		if got := drawn(); !slices.Equal(got, []string{"acct-1"}) {
			t.Fatalf("drew on %v", got)
		}
		if c := s.Recent(); len(c) == 0 || !strings.Contains(c[0].Error, "sign-in lapsed: codex@me@example.com") {
			t.Fatalf("the usage log doesn't say why: %+v", c)
		}
	})
	t.Run("with another account", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		drawn := revokedDrawn(t)
		rec := postAt(New(), "/v1/images/generations", draw, nil)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		saysNoLapse(t, rec)
		if got := drawn(); !slices.Equal(got, []string{"acct-1", "acct-2"}) {
			t.Fatalf("drew on %v", got)
		}
	})
}

// The routes that aren't turns (a drawing, a video and asks after it, an
// embedding) answer a try's failure through lapsedAs and saidOf: on an
// account to be signed in again, only a limit, the why the call's error;
// a lapse tried before is never among the tries the caller is told of.
func TestLapsedAsToldOnlyAsALimit(t *testing.T) {
	revoked := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}, rest: "codex"}
	said := errors.New(`401 {"error":{"message":"Encountered invalidated oauth token for user, failing request","code":"token_revoked"}}`)
	lapseRefusedSaid.Clear()
	code, err := lapsedAs(revoked, http.StatusUnauthorized, said)
	if code != http.StatusTooManyRequests || !isLapsed(err) || !strings.Contains(err.Error(), "sign-in lapsed: codex@me@example.com") || !strings.Contains(err.Error(), "token_revoked") {
		t.Fatalf("%d %v", code, err)
	}
	rec := httptest.NewRecorder()
	writeError(rec, provider.Chat, code, saidOf(rec.Header(), err, []string{"other/m: 500 down"}))
	limitedNotLapsed(t, rec)

	// another failure, or one of a key, goes as it was, with the tries
	// before it
	key := candidate{p: provider.Provider{ID: "k", Key: "sk-1"}, rest: "k"}
	for _, c := range []candidate{revoked, key} {
		e := said
		if c.p.Account != nil {
			e = errors.New("500 down")
		}
		code, err := lapsedAs(c, http.StatusInternalServerError, e)
		if c.p.Account == nil {
			code, err = lapsedAs(c, http.StatusUnauthorized, e)
		}
		if isLapsed(err) || code == http.StatusTooManyRequests {
			t.Fatalf("%s: told as a lapse: %d %v", c.p.ID, code, err)
		}
		if got := saidOf(http.Header{}, err, []string{"x/m: 500"}); !strings.Contains(got, e.Error()+" (tried first: x/m: 500)") {
			t.Fatalf("%s: said %q", c.p.ID, got)
		}
	}

	rec = httptest.NewRecorder()
	videoAskFailed(rec, revoked.p, http.StatusUnauthorized, said)
	limitedNotLapsed(t, rec)
}

// A Claude account with no name (Claude Code's own sign-in) has its error
// held as any account's, for its refused sign-in (failAuth) to be told as
// a limit, not passed on as Anthropic said it.
func TestUnnamedAccountsErrorHeld(t *testing.T) {
	unnamed := candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude"}}, rest: "claude"}
	if !holdsErrors(nil, unnamed) {
		t.Fatal("an unnamed account's error isn't held")
	}
	if !lapsedTry(unnamed, http.StatusUnauthorized, []byte(`{"error":{"message":"OAuth token revoked"}}`)) {
		t.Fatal("its refused sign-in isn't a lapse")
	}
	if holdsErrors(nil, candidate{p: provider.Provider{ID: "k", Key: "sk-1"}}) {
		t.Fatal("a key's error is held with no user")
	}
}

// An error held only for what it says (holdErrors) keeps its first
// heldErrorMost bytes, not a reply of any length.
func TestHeldErrorKeptToItsHead(t *testing.T) {
	h := newHoldWriter(httptest.NewRecorder(), false)
	h.holdErrors = true
	h.header.Set("Content-Type", "application/json")
	h.WriteHeader(http.StatusUnauthorized)
	chunk := []byte(strings.Repeat("x", 64<<10))
	for range 40 {
		if n, err := h.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("%d %v", n, err)
		}
	}
	if got := h.held.Len(); got != heldErrorMost {
		t.Fatalf("held %d bytes, not %d", got, heldErrorMost)
	}
}

// codexAnswers stands in for the ChatGPT backend answering the account
// acct status with body, after wait, and every other account served;
// asked is the accounts asked.
func codexAnswers(t *testing.T, acct string, status int, body string, wait time.Duration) (asked func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	done := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		if r.Header.Get("chatgpt-account-id") == acct {
			select {
			case <-time.After(wait):
			case <-r.Context().Done():
				return
			case <-done:
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(func() { close(done); up.Close() })
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// An account marked to be signed in again that fails for another reason
// says that reason, as any account's failure does: a proxy down (502) or
// a region its vendor doesn't serve (403) is no lapse, and isn't told as
// a limit (review of ba9789dc, M5).
func TestLapsedAccountsOtherFailuresToldAsThemselves(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"a proxy down", http.StatusBadGateway, `proxyconnect tcp: dial tcp 127.0.0.1:9: connect: connection refused`},
		{"a region refused", http.StatusForbidden, `{"error":{"message":"Country, region, or territory not supported","code":"unsupported_country_region_territory"}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			asked := codexAnswers(t, "acct-2", c.status, c.body, 0)
			lapseSaved(t, "spare@example.com")
			keys, secrets := newCaller(t, "Spare only")
			if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/spare@example.com"}}); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`))
			req.Header.Set("Authorization", "Bearer "+secrets[0])
			New().Handler().ServeHTTP(rec, req)
			if got := asked(); len(got) == 0 || slices.Contains(got, "acct-1") {
				t.Fatalf("asked %v", got)
			}
			if rec.Code != c.status || strings.Contains(rec.Body.String(), userLimitSaid) {
				t.Fatalf("told %d %s, not its own %d", rec.Code, rec.Body.String(), c.status)
			}
		})
	}
}

// A request kept alive while its last account is asked (the stream's
// headers sent) is told the lapse found then as a limit in the stream,
// nothing of the sign-in.
func TestUnboundLapseKeptAliveToldAsALimit(t *testing.T) {
	k, w := keepHeldAfter, watchEvery
	keepHeldAfter, watchEvery = 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery = k, w })
	codexSignedIn(t)
	codexAnswers(t, "acct-1", http.StatusUnauthorized, `{"error":{"message":"Encountered invalidated oauth token for user, failing request","code":"token_revoked"},"status":401}`, 150*time.Millisecond)
	rec := postAt(New(), "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`, nil)
	if !strings.Contains(rec.Body.String(), ": keepalive") || !strings.Contains(rec.Body.String(), userLimitSaid) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	saysNoLapse(t, rec)
}

// A fallback whose last member is an account to be signed in again is
// told a limit, as a provider of its own is (the member before it busy).
func TestUnboundFallbacksLastMemberLapsed(t *testing.T) {
	codexSignedIn(t)
	revokedUpstream(t, codexAccessToken(t))
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	fb := &scripted{replies: []reply{{503, "application/json", `{"error":{"message":"the primary is busy"}}`}}}
	up := httptest.NewServer(fb)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fb", Name: "FB", Key: "k", Models: []string{"m"}, Responses: up.URL + "/v1", Fallback: []string{"codex/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	rec := postAt(New(), "/v1/responses", `{"model":"fb/m","stream":true,"input":"ping"}`, nil)
	if fb.n == 0 {
		t.Fatalf("the primary wasn't asked: %d %s", rec.Code, rec.Body.String())
	}
	limitedNotLapsed(t, rec)
}

// A conversation natively compacted by an account to be signed in again
// can go on only once it is: its turns and its compacts are told a limit
// until then, nothing of the sign-in or the account, and nothing is asked
// (the owner's decision B, 2026-10-29; review of ba9789dc, H2).
func TestCompactionSealedByALapsedAccountToldAsALimit(t *testing.T) {
	enc := "gAAAAB-sealed-by-lapsed"
	said := func(t *testing.T, code int, body string) {
		t.Helper()
		if code != http.StatusTooManyRequests || !strings.Contains(body, userLimitSaid) {
			t.Fatalf("%d %s", code, body)
		}
		for _, w := range []string{"sign", "lapsed", "expired", "@example.com", "an account"} {
			if strings.Contains(strings.ToLower(body), w) {
				t.Fatalf("told %q: %s", w, body)
			}
		}
	}
	t.Run("a turn", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		b := newResetBackend(t)
		lapseSaved(t, "spare@example.com")
		wroteCheckpoint(enc, sealedBy("spare@example.com"))
		code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":[{"type":"compaction","encrypted_content":"`+enc+`"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
		if tried, _ := b.seen(); tried != "" {
			t.Fatalf("asked %q", tried)
		}
		said(t, code, body)
	})
	t.Run("a compact", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		nativeOn(t, "codex")
		lapseSaved(t, "spare@example.com")
		wroteCheckpoint(enc, sealedBy("spare@example.com"))
		code, body, seen := sealedCompactAsked(t, enc, "chatgpt-token", "")
		if seen != "" {
			t.Fatalf("asked %q", seen)
		}
		said(t, code, body)
	})
}

// A web search whose ChatGPT account the vendor answers revoked, searched
// by magpie in its place, says only that ChatGPT's search failed: why is
// the log's (review of ba9789dc, M3).
func TestWebSearchKeepsALapseOutOfItsOutput(t *testing.T) {
	codexSignedIn(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Encountered invalidated oauth token for user, failing request","code":"token_revoked"},"status":401}`)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	apis := newSearchAPIs(t)
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	got := readSearchReply(t, codexSearchPost(New().Handler(), "/v1/alpha/search", codexSearchBody("codex/gpt-5.5")))
	if !strings.Contains(got.Output, "ChatGPT's web search failed") {
		t.Fatalf("answered %q", got.Output)
	}
	for _, w := range []string{"revoked", "invalidated", "oauth", "me@example.com", "sign"} {
		if strings.Contains(strings.ToLower(got.Output), w) {
			t.Fatalf("told %q: %q", w, got.Output)
		}
	}
}
