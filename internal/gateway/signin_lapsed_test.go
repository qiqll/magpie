package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// limitedNotLapsed fails t unless rec is a user's 429 with a time to try
// again that says nothing of a sign-in, in its body or its headers.
func limitedNotLapsed(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || rec.Header().Get(UserLimitHeader) == "" || rec.Header().Get(UserUntilHeader) == "" {
		t.Fatalf("not a limit with a time to try again: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	saysNoLapse(t, rec)
}

// saysNoLapse fails t when rec's body or headers say anything of a
// sign-in, or name the lapsed account.
func saysNoLapse(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	all := rec.Body.String()
	for k, vs := range rec.Header() {
		all += k + strings.Join(vs, "")
	}
	for _, w := range []string{"sign in", "sign-in", "signed in", "expired", "revoked", "authenticate", "spare@", "@example.com"} {
		if strings.Contains(strings.ToLower(all), w) {
			t.Fatalf("the caller is told %q: %v %s", w, rec.Header(), rec.Body.String())
		}
	}
}

// lapseSaved marks the saved account user as one whose sign-in has to be
// made again, as a refused refresh does (savedRefreshFailed): its access
// token still in date, so only routing's word keeps it from being asked.
func lapseSaved(t *testing.T, user string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var ls []map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &ls); err != nil {
		t.Fatal(err)
	}
	why := user + "'s sign-in has expired — add the account again to use it"
	for _, l := range ls {
		if l["user"] == user {
			l["lapsed"] = why
		}
	}
	if err := os.WriteFile(path, mustJSON(ls), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	return why
}

// An account whose sign-in has to be made again is never asked while
// another of the provider's can be: here the one left is out of its
// allowance, and the agent is told that, not the lapse of an account that
// couldn't have answered. The route says why it was left out.
func TestLapsedAccountLeftOutWhileAnotherCanAnswer(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, map[string]int{"acct-1": http.StatusTooManyRequests})
	why := lapseSaved(t, "spare@example.com")
	s := New()
	rec := codexPostTo(s, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if got := asked(true); slices.Contains(got, "acct-2") || !slices.Contains(got, "acct-1") {
		t.Fatalf("asked %v: the lapsed account was asked", got)
	}
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "has expired") {
		t.Fatalf("told %d %s", rec.Code, rec.Body.String())
	}
	r := lastRoute(s)
	if !slices.ContainsFunc(r.Left, func(w Weighed) bool { return w.Who == "spare@example.com" && w.Lapsed == why }) {
		t.Fatalf("the route doesn't say why it was left out: %+v", r.Left)
	}
	if slices.ContainsFunc(r.Order, func(w Weighed) bool { return w.Who == "spare@example.com" }) {
		t.Fatalf("the lapsed account is in the order: %+v", r.Order)
	}

	// signed in again: asked again
	path := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var ls []map[string]any
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &ls)
	for _, l := range ls {
		delete(l, "lapsed")
	}
	os.WriteFile(path, mustJSON(ls), 0o600)
	provider.ForgetAccounts()
	if rec := codexPostTo(New(), `{"model":"gpt-5.5","stream":true,"input":"again"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-2") {
		t.Fatalf("signed in again, not asked: %d %s", rec.Code, rec.Body.String())
	}
}

// A user bound to an account whose sign-in has to be made again, its tier
// having room for another, is given a spare beside it and served there,
// the lapsed one not asked; it stays bound to the lapsed one
// (--bind=user; the owner's decision, 2026-10-29).
func TestUserGivenASpareBesideALapsedAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	os.WriteFile(usersPath(), mustJSON(userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com"},
	}}}), 0o600)
	s := byUser(New())
	if rec := userPost(s, "codex/gpt-5.5", "bob", "ping"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-1" {
		t.Fatalf("asked %v", got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com", "me@example.com"}) {
		t.Fatalf("not kept on the lapsed one with a spare beside it: %v", got)
	}
	// signed in again: its requests go back to it, the spare kept beside
	path := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var ls []map[string]any
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &ls)
	for _, l := range ls {
		delete(l, "lapsed")
	}
	os.WriteFile(path, mustJSON(ls), 0o600)
	provider.ForgetAccounts()
	if rec := userPost(s, "codex/gpt-5.5", "bob", "back"); rec.Code != 200 {
		t.Fatalf("back: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); !slices.Equal(got, []string{"acct-2"}) {
		t.Fatalf("signed in again, asked %v, not its own", got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com", "me@example.com"}) {
		t.Fatalf("binding changed: %v", got)
	}
}

// A user whose accounts all have to be signed in again, at its tier's
// most accounts, is given no spare: a 429 with a time to try again that
// says nothing of a sign-in, nobody asked, its binding kept.
func TestUserLimitedWithItsAccountsLapsedAtItsTier(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	lapseSaved(t, "third@example.com")
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "free",
			More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}},
	}}})
	rec := tierPost(byUser(New()), "bob", "free", "hi")
	limitedNotLapsed(t, rec)
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com", "third@example.com"}) {
		t.Fatalf("binding changed: %v", got)
	}
}

// A compaction moved off Codex's paused sign-in, and a web search on a
// ChatGPT account, leave out an account whose sign-in has to be made
// again as routing does.
func TestLapsedAccountNotCompactedOnOrSearchedWith(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", CodexPath+"/responses/compact", nil)
	on := compactOn(r, "gpt-5.5")
	if on == nil || on.Account.User != "third@example.com" {
		t.Fatalf("compacted on %+v", on)
	}
	ons, _ := chatGPTSearchers(httptest.NewRequest("POST", "/v1/responses", nil), "codex/gpt-5.5")
	var users []string
	for _, o := range ons {
		users = append(users, o.Account.User)
	}
	if slices.Contains(users, "spare@example.com") || !slices.Contains(users, "third@example.com") {
		t.Fatalf("searches with %v", users)
	}
	if got := asked(false); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
}

// Left out only for one that could answer: with the other account at its
// usage cap, or one the calling gateway key may not use, the lapsed one
// is tried and the agent told to sign in again, not that every account
// is at its cap or outside the key (review of the first cut). A pin to a
// lapsed account the key may not use is told so, not how the account is.
func TestLapsedAccountKeptWhenNoOtherCanAnswer(t *testing.T) {
	t.Run("the other at its cap", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		capUsage(t, map[string]float64{"me@example.com": 75, "spare@example.com": 10})
		if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
			t.Fatal(err)
		}
		s := New()
		rec := codexPostTo(s, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
		if strings.Contains(rec.Body.String(), "usage cap") {
			t.Fatalf("told the cap, not the lapse: %d %s", rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); slices.ContainsFunc(r.Left, func(w Weighed) bool { return w.Lapsed != "" }) {
			t.Fatalf("left out for a capped one: %+v", r.Left)
		}
		if got := asked(true); !slices.Contains(got, "acct-2") {
			t.Fatalf("the lapsed one isn't tried: asked %v", got)
		}
	})
	t.Run("the other outside the key", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		keys, secrets := newCaller(t, "Spare only")
		if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/spare@example.com"}}); err != nil {
			t.Fatal(err)
		}
		s := New()
		post := func(pin string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`))
			req.Header.Set("Authorization", "Bearer "+secrets[0])
			if pin != "" {
				req.Header.Set(AccountHeader, pin)
			}
			s.Handler().ServeHTTP(rec, req)
			return rec
		}
		rec := post("")
		if rec.Code == http.StatusForbidden || slices.Contains(asked(true), "acct-1") {
			t.Fatalf("held off the account the key may use: %d %s", rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); !slices.ContainsFunc(r.Order, func(w Weighed) bool { return w.Who == "spare@example.com" }) {
			t.Fatalf("the lapsed account the key may use isn't tried: %+v %+v", r.Order, r.Left)
		}
	})
	t.Run("a pin outside the key", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com", "third@example.com")
		usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		keys, secrets := newCaller(t, "Me only")
		if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/me@example.com"}}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`))
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		req.Header.Set(AccountHeader, "spare@example.com")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "may not use") || strings.Contains(rec.Body.String(), "has expired") {
			t.Fatalf("pinned outside the key: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// Under --bind=user, a user whose account is lapsed while the provider's
// other account is another user's alone has no spare to be given: a 429
// with a time to try again, nothing sent, the lapsed one not asked, and
// nothing of a sign-in said to the caller; its binding is kept, the
// account's row says it lapsed and the log says why (the owner's
// decision, 2026-10-29; it used to be sent to the lapsed one).
func TestUserLimitedWithOnlyALapsedAccountAndNoSpare(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "pro"},
		"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "pro"},
	}}})
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	seatLapsedSaid.Clear()
	s := byUser(New())
	rec := tierPost(s, "bob", "pro", "hi")
	limitedNotLapsed(t, rec)
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
	if b := boundIn(t, "codex"); b["bob"] != "spare@example.com" || b["alice"] != "me@example.com" {
		t.Fatalf("bindings: %v", b)
	}
	if got := codexLapsed(t, "spare@example.com"); !strings.Contains(got, "has expired") {
		t.Fatalf("the account's row doesn't say it lapsed: %q", got)
	}
	if l := logged.String(); !strings.Contains(l, "codex account spare@example.com has to be signed in again") || !strings.Contains(l, "has expired") {
		t.Fatalf("not logged with provider, account and why: %s", l)
	}
	if c := s.Recent(); len(c) == 0 || !strings.Contains(c[0].Error, "to be signed in again") {
		t.Fatalf("the usage log doesn't say why: %+v", c)
	}
}

// Codex's compaction moved off its paused sign-in goes to an account the
// calling key may use, a lapsed one before one the key may not use
// (compactSigninHeld would refuse that one).
func TestLapsedCompactionOnAnAccountTheKeyMayUse(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Spare only")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/spare@example.com"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses/compact", strings.NewReader(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer "+secrets[0])
	New().Handler().ServeHTTP(rec, req)
	if got := asked(true); !slices.Equal(got, []string{"acct-2"}) {
		t.Fatalf("compacted on %v: %d %s", got, rec.Code, rec.Body.String())
	}
}

// pinPost asks for codex/gpt-5.5 pinned to account, as user when one is
// given, on magpie's /v1/responses.
func pinPost(s *Server, account, user, tier string) *httptest.ResponseRecorder {
	h := map[string]string{AccountHeader: account}
	if user != "" {
		h[UserHeader] = user
	}
	if tier != "" {
		h[UserTierHeader] = tier
	}
	return postAt(s, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`, h)
}

// A pin to an account to be signed in again goes to another the request
// may go to that can answer now (the owner's decision, 2026-10-29): of the
// provider's accounts when no user is bound, the user's own under
// --bind=user, never another user's (a).
func TestPinnedLapsedGoesToAnotherThatCanAnswer(t *testing.T) {
	t.Run("not bound", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		rec := pinPost(New(), "spare@example.com", "", "")
		if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-1"}) {
			t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
		}
	})
	t.Run("the user's own", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com", "third@example.com")
		asked, _ := usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "free"},
			"bob":   {Who: "codex@third@example.com", Account: "third@example.com", At: time.Now().UTC(), Tier: "free"},
		}}})
		rec := pinPost(byUser(New()), "spare@example.com", "bob", "free")
		if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-3"}) {
			t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
		}
	})
}

// A user with no account but the lapsed one pinned is given a spare its
// tier has room for, beside the lapsed one, and served there (b).
func TestPinnedLapsedBindsTheUserASpare(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "pro"},
	}}})
	rec := pinPost(byUser(New()), "spare@example.com", "bob", "pro")
	if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-1"}) {
		t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com", "me@example.com"}) {
		t.Fatalf("not given the spare beside it: %v", got)
	}
}

// With nothing to take it in its place, nobody is asked (c): not bound by
// user, with the other account one the key may not use, and a user's,
// with the other another user's alone, each is a 429 with a time to try
// again that says nothing of a sign-in, why in the usage log and the log
// (the owner's decisions, 2026-10-29; not bound, it was a 403 with why).
// A pin to an account the key may not use is still told so first.
func TestPinnedLapsedRefusedWithNothingInItsPlace(t *testing.T) {
	t.Run("the other outside the key", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		keys, secrets := newCaller(t, "Spare only")
		if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/spare@example.com"}}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`))
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		req.Header.Set(AccountHeader, "spare@example.com")
		var logged strings.Builder
		log.SetOutput(&logged)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		lapseRefusedSaid.Clear()
		s := New()
		s.Handler().ServeHTTP(rec, req)
		if got := asked(true); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || len(got) != 0 {
			t.Fatalf("%d %v %s, asked %v", rec.Code, rec.Header(), rec.Body.String(), got)
		}
		saysNoLapse(t, rec)
		if c := s.Recent(); len(c) == 0 || !strings.Contains(c[0].Error, "has expired") {
			t.Fatalf("the usage log doesn't say why: %+v", c)
		}
		if l := logged.String(); !strings.Contains(l, "codex account spare@example.com has to be signed in again") || !strings.Contains(l, "has expired") {
			t.Fatalf("not logged with provider, account and why: %s", l)
		}
	})
	t.Run("the other another user's alone", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "pro"},
			"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "pro"},
		}}})
		rec := pinPost(byUser(New()), "spare@example.com", "bob", "pro")
		limitedNotLapsed(t, rec)
		if got := asked(true); len(got) != 0 {
			t.Fatalf("asked %v", got)
		}
	})
}

// restLong has the account who of codex rest two hours out of its
// allowance, as a 429 that said so would.
func restLong(t *testing.T, who string) {
	t.Helper()
	key := "codex@" + who
	restingUntil.Lock()
	if restingUntil.m == nil {
		restingUntil.m = map[string]time.Time{}
	}
	if restingUntil.note == nil {
		restingUntil.note = map[string]Rest{}
	}
	restingUntil.m[key] = time.Now().Add(2 * time.Hour)
	restingUntil.note[key] = Rest{Why: failQuota, Status: 429, By: "quota"}
	restingUntil.Unlock()
	t.Cleanup(func() {
		restingUntil.Lock()
		delete(restingUntil.m, key)
		delete(restingUntil.note, key)
		restingUntil.Unlock()
	})
}

// (b) with the user's own other account resting a while: a pin to its
// lapsed one is bound a spare its tier has room for, as growUser does,
// not refused because none of its own can answer now (review of
// df779d95, H1).
func TestPinnedLapsedGrowsWhenTheUsersOwnRest(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	restLong(t, "third@example.com")
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "pro",
			More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}},
	}}})
	rec := pinPost(byUser(New()), "spare@example.com", "bob", "pro")
	if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-1"}) {
		t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Contains(got, "me@example.com") {
		t.Fatalf("not bound the spare: %v", got)
	}
}

// A pin to a lapsed account of a fallback's provider: under --bind=user
// with a user it is served as if unpinned, on the user's own accounts of
// the provider asked for (none of the fallback's, one another user's),
// and a user the header can't take is the 400 any request of it is
// (review of 966144c7, M5); bound by session, it goes to that provider's
// other account (review of df779d95, H2).
func TestPinnedLapsedOnAFallbackProvider(t *testing.T) {
	setup := func(t *testing.T) (func(bool) []string, *scripted) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		fb := &scripted{replies: []reply{{200, "text/event-stream", sse(
			`data: {"type":"response.output_text.delta","delta":"from the primary"}`,
			`data: {"type":"response.completed","response":{"id":"f1","status":"completed","output":[]}}`)}}}
		up := httptest.NewServer(fb)
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: "fb", Name: "FB", Key: "k", Models: []string{"m"}, Responses: up.URL + "/v1", Fallback: []string{"codex/gpt-5.5"}}); err != nil {
			t.Fatal(err)
		}
		return asked, fb
	}
	body := `{"model":"fb/m","stream":true,"input":"ping"}`
	t.Run("bound by user", func(t *testing.T) {
		asked, fb := setup(t)
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"v": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "pro"},
		}}})
		s := byUser(New())
		rec := postAt(s, "/v1/responses", body, map[string]string{AccountHeader: "spare@example.com", UserHeader: "u", UserTierHeader: "pro"})
		if got := asked(true); rec.Code != 200 || len(got) != 0 || fb.n != 1 {
			t.Fatalf("%d %s, asked %v and the primary %d times", rec.Code, rec.Body.String(), got, fb.n)
		}
		saysNoLapse(t, rec)
		rec = postAt(s, "/v1/responses", body, map[string]string{AccountHeader: "spare@example.com", UserHeader: "u\x01", UserTierHeader: "pro"})
		if got := asked(true); rec.Code != http.StatusBadRequest || len(got) != 0 || fb.n != 1 {
			t.Fatalf("a user the header can't take: %d %s, asked %v and the primary %d times", rec.Code, rec.Body.String(), got, fb.n)
		}
	})
	t.Run("bound by session", func(t *testing.T) {
		asked, fb := setup(t)
		rec := postAt(New(), "/v1/responses", body, map[string]string{AccountHeader: "spare@example.com"})
		if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-1"}) || fb.n != 0 {
			t.Fatalf("%d %s, asked %v and the primary %d times", rec.Code, rec.Body.String(), got, fb.n)
		}
	})
}

// A user-accounts.json that can't be read is unknown: a pin to a lapsed
// account is answered 503 as any of the user's requests is, not a 403
// (review of df779d95, M1).
func TestPinnedLapsedUnreadBindingsStay503(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	os.MkdirAll(filepath.Dir(usersPath()), 0o700)
	if err := os.WriteFile(usersPath(), []byte(`{"users": {"codex": {"bob": `), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := pinPost(byUser(New()), "spare@example.com", "bob", "pro")
	if got := asked(true); rec.Code != http.StatusServiceUnavailable || len(got) != 0 {
		t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
}

// The same name lapsed on the provider asked for and serving on a
// fallback's (a key named so): the pin goes to the one that serves, as
// before, not to the lapsed one's provider (review of df779d95, M2).
func TestPinnedNameServingElsewhereKeepsItsPin(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	rel := &scripted{replies: []reply{{200, "text/event-stream", sse(
		`data: {"type":"response.output_text.delta","delta":"from the relay"}`,
		`data: {"type":"response.completed","response":{"id":"f1","status":"completed","output":[]}}`)}}}
	up := httptest.NewServer(rel)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "rel", Name: "Rel", Models: []string{"m"}, Responses: up.URL + "/v1",
		Key: "k1", KeyName: "spare@example.com", Keys: []provider.KeyAccount{{Name: "other", Key: "k2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "codex", Fallback: []string{"rel/m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := pinPost(s, "spare@example.com", "", "")
	if got := asked(true); rec.Code != 200 || len(got) != 0 || rel.n != 1 {
		r := lastRoute(s)
		t.Fatalf("%d, codex asked %v, the relay %d times; order %+v left %+v", rec.Code, got, rel.n, r.Order, r.Left)
	}
}

// A pin to an account not lapsed goes to it as before, and a group's
// request pinned to a lapsed member account goes to the provider's other
// one (L2).
func TestPinnedUnlapsedAndGroupPins(t *testing.T) {
	t.Run("not lapsed", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, nil)
		rec := pinPost(New(), "spare@example.com", "", "")
		if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-2"}) {
			t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
		}
	})
	t.Run("a group's", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		asked, _ := usersUpstream(t, nil)
		lapseSaved(t, "spare@example.com")
		if err := provider.SaveGroup(provider.Group{Name: "Solo", Members: []string{"codex/gpt-5.5"}}); err != nil {
			t.Fatal(err)
		}
		rec := postAt(New(), "/v1/responses", `{"model":"group/solo","stream":true,"input":"ping"}`, map[string]string{AccountHeader: "spare@example.com"})
		if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-1"}) {
			t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
		}
	})
}

// A user-accounts.json read once and broken since is unknown: a user whose
// accounts all have to be signed in again is answered 503, no spare added
// over a file that doesn't read (review of 966144c7, H1).
func TestUserLapsedUnreadBindingsStay503(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "pro"},
	}}})
	s := byUser(New())
	// read once, alice bound alongside
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if err := os.WriteFile(usersPath(), []byte(`{"users": {"codex": {"bob": `), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := tierPost(s, "bob", "pro", "hi")
	if got := asked(true); rec.Code != http.StatusServiceUnavailable || len(got) != 0 {
		t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	saysNoLapse(t, rec)
	if b, _ := os.ReadFile(usersPath()); string(b) != `{"users": {"codex": {"bob": ` {
		t.Fatalf("the file was written over: %s", b)
	}
}

// One of a user's accounts lapsed and the other's plan without the model:
// the 400 that one is, not a limit, and the lapsed one never said not to
// serve it (review of 966144c7, M4).
func TestUserLapsedBesideOneWithoutTheModel(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	if err := provider.SetAccountModels("codex", "third@example.com", []string{"gpt-other"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: now, Tier: "pro"},
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "plus",
			More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}},
	}}})
	rec := tierPost(byUser(New()), "bob", "plus", "hi")
	if got := asked(true); rec.Code != http.StatusBadRequest || len(got) != 0 {
		t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	saysNoLapse(t, rec)
}

// A Claude account whose run fails as a sign-in Anthropic refused, its
// mark not written (no saved credential of its to mark), is out for the
// spare all the same: one added in the same request answers (review of
// 966144c7, M2).
func TestUserGivenASpareWhenClaudeRefusesTheSignIn(t *testing.T) {
	claudeMadeFirst(t, false)
	calls := filepath.Join(t.TempDir(), "calls")
	claudeRefuses(t, calls, "tok-a")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"claude": {
		"bob": {Who: "claude@a@example.com", Account: "a@example.com", At: time.Now().UTC(), Tier: "pro"},
	}}})
	s := byUser(New())
	t.Cleanup(s.subscription.abortAll)
	rec := postAt(s, "/v1/messages", `{"model":"claude/claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{UserHeader: "bob", UserTierHeader: "pro"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "healthy account") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	saysNoLapse(t, rec)
	if got := seatsIn(t, "claude")["bob"]; !slices.Equal(got, []string{"a@example.com", "b@example.com"}) {
		t.Fatalf("seats %v", got)
	}
}

// Two requests of one user bound before either adds a spare: the second
// takes the spare the first added, not one more (review of 966144c7, M3).
func TestUserLapsedOneSpareForRequestsAtOnce(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "pro"},
	}}})
	s := New()
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	tier := defaultTiers.Tiers[2] // pro
	var wg sync.WaitGroup
	bound := make([]*UserBound, 2)
	pools := make([][]candidate, 2)
	pls := make([]planned, 2)
	for i := range bound {
		pools[i], pls[i] = s.plan(*p, "gpt-5.5", provider.Responses)
		var refused userRefusal
		var opl planned
		_, opl, bound[i], refused = bindUser("", "bob", tier, *p, pools[i], pls[i])
		if refused.status != 0 || bound[i].Why != "lapsed" {
			t.Fatalf("bound %+v %+v", bound[i], refused)
		}
		pls[i] = opl
	}
	got := make([][]candidate, 2)
	for i := range bound {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool, poolPl := s.plan(*p, "gpt-5.5", provider.Responses)
			got[i], _, _, _, _ = growUser(bound[i], pool, poolPl, nil, pls[i])
		}()
	}
	wg.Wait()
	if seats := seatsIn(t, "codex")["bob"]; len(seats) != 2 {
		t.Fatalf("a spare each: %v", seats)
	}
	for i, cs := range got {
		if len(cs) == 0 || cs[0].who() != got[0][0].who() {
			t.Fatalf("request %d on %v", i, cs)
		}
	}
}

// Codex's web.run for a user whose ChatGPT accounts all have to be signed
// in again goes to a spare added beside them, as a turn would (review of
// 966144c7, M1). Kimi Code's search, the other caller of usersOwn, is
// asked with keys, which never lapse.
func TestUsersOwnGivesALapsedUserASpare(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "pro"},
	}}})
	s := byUser(New())
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), askedForKey{}, askedFor{"", "bob", "pro"})
	own, bound, err := s.usersOwn(ctx, *p, "gpt-5.5", provider.Responses)
	if err != nil || !bound || len(own) == 0 || own[0].p.Account.User != "me@example.com" {
		t.Fatalf("own %v bound %v err %v", own, bound, err)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Equal(got, []string{"spare@example.com", "me@example.com"}) {
		t.Fatalf("seats %v", got)
	}
	_ = asked
}

// A lapse is logged once a change, by the account, with a stable why: the
// same again isn't logged, another why is, and one back among the user's
// accounts is logged again when it lapses again (review of 966144c7, M6).
func TestSeatLapseLoggedOnceAChange(t *testing.T) {
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	seatLapsedSaid.Clear()
	lines := func() int { return strings.Count(logged.String(), "has to be signed in again") }
	noteSeatLapsed("codex@Spare@example.com", "why one")
	noteSeatLapsed("codex@spare@example.com", "why one")
	if n := lines(); n != 1 || !strings.Contains(logged.String(), "codex account Spare@example.com has to be signed in again (why one)") {
		t.Fatalf("%d: %s", n, logged.String())
	}
	noteSeatLapsed("codex@spare@example.com", "why two")
	if n := lines(); n != 2 {
		t.Fatalf("another why not logged: %s", logged.String())
	}
	clearSeatLapsed("codex@SPARE@example.com")
	noteSeatLapsed("codex@spare@example.com", "why two")
	if n := lines(); n != 3 {
		t.Fatalf("lapsed again after it was back, not logged: %s", logged.String())
	}
}

// growUser counts an account that failed as lapsed in this request as out
// though nothing marks it yet (a refresh still mending a revoke, Claude's
// refusal of a sign-in it can't mark), and adds a spare beside it (review
// of 966144c7, M2).
func TestGrowUserCountsTheFailedAsLapsed(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "pro"},
	}}})
	s := New()
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	pool, poolPl := s.plan(*p, "gpt-5.5", provider.Responses)
	_, _, ub, refused := bindUser("", "bob", defaultTiers.Tiers[2], *p, pool, poolPl)
	if refused.status != 0 || ub.Why != "kept" {
		t.Fatalf("bound %+v %+v", ub, refused)
	}
	got, _, _, grew, _ := growUser(ub, pool, poolPl, nil, planned{left: ub.left}, "codex@me@example.com")
	if !grew || len(got) == 0 || got[0].p.Account.User != "spare@example.com" {
		t.Fatalf("grew %v on %v", grew, got)
	}
}

// A user whose accounts all have to be signed in again keeps its last use
// stamped as any bound user does, so its binding isn't let go as idle
// while it waits (review of 966144c7, L1).
func TestUserLapsedKeepsItsUseStamped(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	old := time.Now().UTC().Add(-2 * time.Hour)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: old, Tier: "pro", Used: old},
		"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com", At: old, Tier: "pro", Used: old},
	}}})
	limitedNotLapsed(t, tierPost(byUser(New()), "bob", "pro", "hi"))
	b, _ := os.ReadFile(usersPath())
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if used := f.Users["codex"]["bob"].Used; time.Since(used) > time.Minute {
		t.Fatalf("bob's use not stamped: %v", used)
	}
}

// revokedEverywhere has the ChatGPT backend refuse the accounts accts as
// revoked whatever token they go with, and every saved account's refresh
// go through: a revoke a refresh is still mending, marked on none of them
// (NoteRevokedSignIn marks nothing when the refresh goes through).
func revokedEverywhere(t *testing.T, accts ...string) func(bool) []string {
	t.Helper()
	return revokedOrOut(t, accts, nil)
}

// revokedOrOut is revokedEverywhere, the accounts out answering 429 out of
// their allowance for two hours.
func revokedOrOut(t *testing.T, accts, out []string) func(bool) []string {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		acct := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		asked = append(asked, acct)
		mu.Unlock()
		if slices.Contains(out, acct) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
			return
		}
		if slices.Contains(accts, acct) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"Encountered invalidated oauth token for user, failing request","code":"token_revoked"},"status":401}`)
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
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "n": time.Now().UnixNano()})
		json.NewEncoder(w).Encode(map[string]any{"access_token": "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s", "refresh_token": "r-new"})
	}))
	t.Cleanup(fake.Close)
	t.Cleanup(provider.CodexTokenURLForTest(fake.URL))
	return func(reset bool) []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(asked)
		if reset {
			asked = nil
		}
		return out
	}
}

// A user's two accounts both fail as lapsed in one request, neither
// marked: the first, failed before the last, counts as out too, so a
// spare is added and answers; at its tier's most, a 429 that names no
// account, never the 503 of an account "paused or sealed" (review of
// 70945cfd, Medium-2 and Low-1).
func TestUserBothAccountsFailLapsedUnmarked(t *testing.T) {
	for _, c := range []struct {
		tier string
		want int
	}{{"pro", 200}, {"free", 429}} {
		t.Run(c.tier, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com", "third@example.com")
			asked := revokedEverywhere(t, "acct-2", "acct-3")
			now := time.Now().UTC()
			writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
				"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: c.tier,
					More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}},
			}}})
			rec := tierPost(byUser(New()), "bob", c.tier, "hi")
			got := asked(true)
			if rec.Code != c.want || !slices.Contains(got, "acct-2") || !slices.Contains(got, "acct-3") {
				t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
			}
			if c.want == 200 {
				if got[len(got)-1] != "acct-1" {
					t.Fatalf("not answered on the spare: %v", got)
				}
				saysNoLapse(t, rec)
				return
			}
			limitedNotLapsed(t, rec)
			if strings.Contains(rec.Body.String(), "third@") {
				t.Fatalf("an account named: %s", rec.Body.String())
			}
		})
	}
}

// spareOf and fullRefusal go by the lapses read before the lock, not by
// reading logins.json under it (review of 70945cfd, Medium-1).
func TestSpareOfGoesByTheLapsesReadBefore(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	usersUpstream(t, nil)
	s := New()
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	pool, pl := s.plan(*p, "gpt-5.5", provider.Responses)
	tier := defaultTiers.Tiers[2]
	lapsed := map[string]string{"codex@me@example.com": "why"}
	userBinds.Lock()
	if err := loadUsers(); err != nil { // this test's file, none: nobody bound
		userBinds.Unlock()
		t.Fatal(err)
	}
	uses := seatUsesOf("codex", defaultTiers, "bob")
	i := firstOf("codex", pool, pl, tier, uses, lapsed)
	r := fullRefusal("bob", tier, "codex", pool, pl, uses, lapsed)
	userBinds.Unlock()
	if i < 0 || pool[i].p.Account.User != "spare@example.com" {
		t.Fatalf("picked %d", i)
	}
	if !strings.Contains(r.msg, "signed out by its vendor") {
		t.Fatalf("not said: %s", r.msg)
	}
}

// After a try a user's request made on an account that failed as lapsed,
// only a limit or the file not reading goes to the caller as growUser
// said it; a refusal naming the user's accounts is the lapse's 429
// (review of 70945cfd, Low-1).
func TestUserTryRefusal(t *testing.T) {
	u := &UserBound{User: "bob", seats: []userSeat{{Who: "codex@a@x", Account: "a@x"}}}
	for _, c := range []struct {
		in   userRefusal
		want int
	}{
		{userRefusal{status: 503, msg: "bob is bound to a@x, which can't take this request (paused, or the conversation was sealed by another account)"}, 429},
		{userRefusal{status: 400, msg: "bound to a@x, which doesn't serve m"}, 429},
		{userRefusal{status: 503, unknown: true, msg: "user-accounts.json can't be read"}, 503},
		{userRefusal{status: 429, msg: "rests", limit: failQuota}, 429},
	} {
		if got := userTryRefusal(u, c.in); got.status != c.want || got.status == 429 && got.limit == "" {
			t.Errorf("%+v: %+v", c.in, got)
		}
	}
}

// A try's lapse is logged only for an account whose mark says so: not a
// key, not a 401 that marks nothing (review of 70945cfd, Low-2 and Low-3).
func TestTryLapseLoggedOnlyWhenMarked(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	seatLapsedSaid.Clear()
	noteTryLapsed(candidate{p: provider.Provider{ID: "relay", Key: "k"}, rest: "relay#abc"})
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	var spare provider.Provider
	for _, q := range p.AlsoOn() {
		if q.Account.User == "spare@example.com" {
			spare = q
		}
	}
	noteTryLapsed(candidate{p: spare, rest: "codex@spare@example.com"})
	if l := logged.String(); strings.Contains(l, "has to be signed in again") {
		t.Fatalf("logged with no mark: %s", l)
	}
	lapseSaved(t, "spare@example.com")
	noteTryLapsed(candidate{p: spare, rest: "codex@spare@example.com"})
	if l := logged.String(); strings.Count(l, "has to be signed in again") != 1 || !strings.Contains(l, "codex account spare@example.com") {
		t.Fatalf("%s", l)
	}
}

// One of a user's accounts fails as lapsed (unmarked) before the last,
// out of its allowance for hours: both count out, and a spare is added
// and answers in the same request (review of 70945cfd, Medium-2, the
// allowance's spare).
func TestUserLapsedThenOutGivenASpare(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	asked := revokedOrOut(t, []string{"acct-2"}, []string{"acct-3"})
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "pro",
			More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}},
	}}})
	rec := tierPost(byUser(New()), "bob", "pro", "hi")
	got := asked(true)
	if rec.Code != 200 || len(got) == 0 || got[len(got)-1] != "acct-1" {
		t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	saysNoLapse(t, rec)
}
