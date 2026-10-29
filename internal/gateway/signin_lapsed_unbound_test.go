package gateway

import (
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

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
	if _, _, status, msg, lapsed := pinTo("spare@example.com", nil, pl); status != http.StatusTooManyRequests || lapsed != pl.left[0].Lapsed || !strings.Contains(msg, "has expired") {
		t.Fatalf("%d %q %q", status, msg, lapsed)
	}
	pl.left[0].Lapsed = ""
	if _, _, _, _, lapsed := pinTo("spare@example.com", nil, pl); lapsed != "" {
		t.Fatalf("not lapsed, said %q", lapsed)
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
