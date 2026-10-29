package gateway

import (
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
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
			if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("%s: not a limit with a time to try again: %d %v %s", input, rec.Code, rec.Header(), rec.Body.String())
			}
			saysNoLapse(t, rec)
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
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("not a limit with a time to try again: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
		}
		saysNoLapse(t, rec)
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
