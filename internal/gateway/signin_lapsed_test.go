package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

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

// A user bound to an account whose sign-in has to be made again is bound
// to one that can answer, the lapsed one not asked (--bind=user).
func TestUserLetGoOfALapsedAccountUnasked(t *testing.T) {
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
	if b := boundIn(t, "codex"); b["bob"] != "me@example.com" {
		t.Fatalf("not bound afresh: %v", b)
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
// other account is another user's alone stays on its own and is sent
// there (to be told to sign in again), not refused as the pool full.
func TestUserKeptOnItsLapsedAccountWithNoSpare(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	lapseSaved(t, "spare@example.com")
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC(), Tier: "pro"},
		"bob":   {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "pro"},
	}}})
	s := byUser(New())
	rec := tierPost(s, "bob", "pro", "hi")
	if got := asked(true); rec.Code == http.StatusTooManyRequests || !slices.Equal(got, []string{"acct-2"}) {
		t.Fatalf("bob: %d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	if b := boundIn(t, "codex"); b["bob"] != "spare@example.com" || b["alice"] != "me@example.com" {
		t.Fatalf("bindings: %v", b)
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

// A user with no account but the lapsed one pinned is bound afresh to a
// spare its tier has room on, and served there (b).
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
	if b := boundIn(t, "codex"); b["bob"] != "me@example.com" {
		t.Fatalf("not bound to the spare: %v", b)
	}
}

// With nothing to take it in its place — the other account one the key
// may not use, or another user's alone — the pin is refused 403 with why,
// nobody asked (c). A pin to an account the key may not use is still told
// so first.
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
		New().Handler().ServeHTTP(rec, req)
		if got := asked(true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "has expired") || len(got) != 0 {
			t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
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
		if got := asked(true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "has expired") || len(got) != 0 {
			t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
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
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: now, Tier: "free",
			More: []userSeat{{Who: "codex@third@example.com", Account: "third@example.com", At: now}}},
	}}})
	rec := pinPost(byUser(New()), "spare@example.com", "bob", "free")
	if got := asked(true); rec.Code != 200 || !slices.Equal(got, []string{"acct-1"}) {
		t.Fatalf("%d %s, asked %v", rec.Code, rec.Body.String(), got)
	}
	if got := seatsIn(t, "codex")["bob"]; !slices.Contains(got, "me@example.com") {
		t.Fatalf("not bound the spare: %v", got)
	}
}

// A pin to a lapsed account of a fallback's provider: under --bind=user
// with a user, no account of that provider is the user's (one is
// another user's), so it is refused with why, nobody asked; bound by
// session, it goes to that provider's other account (review of
// df779d95, H2).
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
		rec := postAt(byUser(New()), "/v1/responses", body, map[string]string{AccountHeader: "spare@example.com", UserHeader: "u", UserTierHeader: "pro"})
		if got := asked(true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "has expired") || len(got) != 0 || fb.n != 0 {
			t.Fatalf("%d %s, asked %v and the primary %d times", rec.Code, rec.Body.String(), got, fb.n)
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
