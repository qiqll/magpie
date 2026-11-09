package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// bannedOf is whether Logins lists the codex account user as banned.
func bannedOf(t *testing.T, user string) bool {
	t.Helper()
	ForgetAccounts()
	for _, l := range Logins("codex") {
		if l.User == user {
			return l.Banned
		}
	}
	t.Fatalf("%s isn't listed", user)
	return false
}

// signInAgainAs signs Codex in to the saved account user again, the same
// account (its own identity) with a new refresh token, as a sign-in again
// in Codex would.
func signInAgainAs(t *testing.T, home, user, refresh string) {
	t.Helper()
	for _, l := range readLogins() {
		if l.Agent != "codex" || l.User != user {
			continue
		}
		var a map[string]any
		if err := json.Unmarshal(l.Auth, &a); err != nil {
			t.Fatal(err)
		}
		a["tokens"].(map[string]any)["refresh_token"] = refresh
		writeFile(t, filepath.Join(home, ".codex", "auth.json"), a)
		return
	}
	t.Fatalf("%s isn't saved", user)
}

// A banned mark is the user's: a sign-in to the account again, the look
// that saves the agents' sign-ins (rememberLogins) and the keep-alive
// write the entry anew, and it stays; only the user lifts it.
func TestBannedKeptUntilTheUserLiftsIt(t *testing.T) {
	home := signIn(t)
	rememberLogins(true) // me@ saved…
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true) // …and codex is on work@ now
	if err := SetLoginBanned("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if !bannedOf(t, "me@example.com") || bannedOf(t, "work@example.com") {
		t.Fatal("not listed banned, or another is")
	}
	if !AccountUnusable(&Account{Agent: "codex", User: "ME@example.com"}) {
		t.Fatal("the mark is matched in one case only")
	}

	// signed in to it again, and away from it again
	signInAgainAs(t, home, "me@example.com", "r-again")
	rememberLogins(true)
	if !bannedOf(t, "me@example.com") || !strings.Contains(string(savedOf(t, "me@example.com").Auth), "r-again") {
		t.Fatal("a sign-in again took the mark away")
	}
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	if !bannedOf(t, "me@example.com") {
		t.Fatal("rememberLogins took the mark away")
	}
	// the entry written anew as a sign-in does, and two copies folded
	loginsMu.Lock()
	ls := readLogins()
	for _, l := range ls {
		if l.User == "me@example.com" {
			fresh := l
			fresh.Banned, fresh.BannedAt, fresh.Auth = false, time.Time{}, json.RawMessage(`{"tokens":{"refresh_token":"r-new"}}`)
			ls = upsertLogin(ls, fresh)
			if folded := dedupeLogins([]savedLogin{fresh, l}); len(folded) != 1 || !folded[0].Banned {
				t.Errorf("folded: %+v", folded)
			}
		}
	}
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if !bannedOf(t, "me@example.com") {
		t.Fatal("upsertLogin took the mark away")
	}

	// lifted, it is as it was
	if err := SetLoginBanned("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if bannedOf(t, "me@example.com") {
		t.Fatal("not lifted")
	}
}

// A banned account's sign-in isn't kept alive and its allowance isn't
// read; lifted, it is renewed again.
func TestBannedNotRenewedNorRead(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	var got []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body["refresh_token"])
		json.NewEncoder(w).Encode(map[string]any{"access_token": fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": body["refresh_token"] + "-2"})
	}))
	defer fake.Close()
	old := codexTokenURL
	codexTokenURL = fake.URL
	t.Cleanup(func() { codexTokenURL = old })

	if err := SetLoginBanned("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if rs := RenewLogins(context.Background(), 0); len(got) != 0 || slices.ContainsFunc(rs, func(r Renewal) bool { return r.User == "me@example.com" }) {
		t.Fatalf("renewed the banned account: %+v %v", rs, got)
	}
	ForgetAccounts()
	logins, _ := usageLogins("codex")
	if slices.ContainsFunc(logins, func(l Login) bool { return l.User == "me@example.com" }) {
		t.Fatalf("its allowance is read: %+v", logins)
	}

	if err := SetLoginBanned("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if rs := RenewLogins(context.Background(), 0); len(got) != 1 || !slices.ContainsFunc(rs, func(r Renewal) bool { return r.User == "me@example.com" && r.Renewed }) {
		t.Fatalf("lifted, not renewed: %+v %v", rs, got)
	}
}

// A request asked of a banned account is never signed: whatever path
// reached it, it goes nowhere.
func TestBannedAccountNeverSigned(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	if err := SetLoginBanned("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "codex", Account: &Account{Agent: "codex", User: "me@example.com"}}
	req := httptest.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != ErrAccountBanned {
		t.Fatalf("signed: %v", err)
	}
	if _, _, err := p.Account.Token(context.Background()); err != ErrAccountBanned {
		t.Fatalf("token given: %v", err)
	}
}

// While Claude Code is signed out, an account marked banned never stands
// in for its own; one entry of two named alike is marked, not both.
func TestBannedNeverStandsInAndMarksOneEntry(t *testing.T) {
	now := time.Now()
	ls := []savedLogin{
		{Agent: "claude", User: "a@x.y", Banned: true, Seen: now},
		{Agent: "claude", User: "b@x.y", Seen: now.Add(-time.Hour)},
	}
	if got := claudeStandIn(ls); got != "b@x.y" {
		t.Fatalf("stand-in %q", got)
	}
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	// two entries named alike that a read keeps apart (only Claude's and
	// Codex's are folded on read), as two seats of one name are
	loginsMu.Lock()
	ls = append(readLogins(),
		savedLogin{Agent: "grok", User: "twin@x.y", Home: "/a", Seen: now},
		savedLogin{Agent: "grok", User: "twin@x.y", Home: "/b", Seen: now})
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if err := SetLoginBanned("grok", "twin@x.y", true); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range readLogins() {
		if l.User == "twin@x.y" && l.Banned {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d entries marked", n)
	}
	if err := SetLoginBanned("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := WarmNow(context.Background(), "codex", "me@example.com"); err == nil || !strings.Contains(err.Error(), "banned") {
		t.Fatalf("warmed: %v", err)
	}
}

// Disabled is kept as banned is, beside it and apart from it: a sign-in
// again, rememberLogins, upsertLogin and dedupeLogins keep it; it isn't
// renewed, read, signed or switched to; the two marks are lifted apart, and
// an account with both stays out until both are.
func TestDisabledKeptAndHeldApartFromBanned(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	var got []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body["refresh_token"])
		json.NewEncoder(w).Encode(map[string]any{"access_token": fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": body["refresh_token"] + "-2"})
	}))
	defer fake.Close()
	old := codexTokenURL
	codexTokenURL = fake.URL
	t.Cleanup(func() { codexTokenURL = old })
	listed := func() Login {
		ForgetAccounts()
		for _, l := range Logins("codex") {
			if l.User == "me@example.com" {
				return l
			}
		}
		t.Fatal("me@ isn't listed")
		return Login{}
	}

	if err := SetLoginDisabled("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if l := listed(); !l.Disabled || l.Banned || !AccountUnusable(&Account{Agent: "codex", User: "ME@example.com"}) {
		t.Fatalf("not disabled alone: %+v", l)
	}
	// a sign-in again, rememberLogins, upsertLogin and dedupeLogins keep it
	signInAgainAs(t, home, "me@example.com", "r-again")
	rememberLogins(true)
	if !listed().Disabled || !strings.Contains(string(savedOf(t, "me@example.com").Auth), "r-again") {
		t.Fatal("a sign-in again took the mark away, or wasn't the same account")
	}
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	loginsMu.Lock()
	ls := readLogins()
	for _, l := range ls {
		if l.User == "me@example.com" {
			fresh := l
			fresh.Disabled, fresh.DisabledAt = false, time.Time{}
			ls = upsertLogin(ls, fresh)
			if folded := dedupeLogins([]savedLogin{fresh, l}); len(folded) != 1 || !folded[0].Disabled {
				t.Errorf("folded: %+v", folded)
			}
		}
	}
	writeLogins(ls)
	loginsMu.Unlock()
	if !listed().Disabled {
		t.Fatal("a rewrite took the mark away")
	}
	// nothing renewed, read, signed or switched to
	if rs := RenewLogins(context.Background(), 0); len(got) != 0 || slices.ContainsFunc(rs, func(r Renewal) bool { return r.User == "me@example.com" }) {
		t.Fatalf("renewed: %+v %v", rs, got)
	}
	if logins, _ := usageLogins("codex"); slices.ContainsFunc(logins, func(l Login) bool { return l.User == "me@example.com" }) {
		t.Fatal("its allowance is read")
	}
	p := Provider{ID: "codex", Account: &Account{Agent: "codex", User: "me@example.com"}}
	if err := p.Sign(context.Background(), httptest.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil), Responses, nil); err != ErrAccountDisabled {
		t.Fatalf("signed: %v", err)
	}
	if err := SwitchLogin("codex", "me@example.com"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("switched: %v", err)
	}
	// banned too: both kept, banned said first; enabled, still out
	if err := SetLoginBanned("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if l := listed(); !l.Disabled || !l.Banned {
		t.Fatalf("both: %+v", l)
	}
	if err := SetLoginDisabled("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if l := listed(); l.Disabled || !l.Banned || !AccountUnusable(p.Account) {
		t.Fatalf("enabled, still banned: %+v", l)
	}
	if err := SetLoginBanned("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if AccountUnusable(p.Account) {
		t.Fatal("both lifted, still out")
	}
	if rs := RenewLogins(context.Background(), 0); len(got) != 1 || !slices.ContainsFunc(rs, func(r Renewal) bool { return r.User == "me@example.com" && r.Renewed }) {
		t.Fatalf("lifted, not renewed: %+v %v", rs, got)
	}
}

// savedOf is the saved codex account user, the only entry of its name.
func savedOf(t *testing.T, user string) savedLogin {
	t.Helper()
	var out []savedLogin
	for _, l := range readLogins() {
		if l.Agent == "codex" && strings.EqualFold(l.User, user) {
			out = append(out, l)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d entries for %s: %+v", len(out), user, out)
	}
	return out[0]
}

// The account the agent is signed in to can't be paused while the only
// other one on is disabled: it would take nothing, and pausing would go
// quiet, the gateway using the paused one all the same.
func TestPauseRefusedWhenTheOtherOnIsDisabled(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginDisabled("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("codex", "work@example.com", false); err == nil || !strings.Contains(err.Error(), "only") {
		t.Fatalf("paused: %v", err)
	}
	if err := SetLoginDisabled("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("codex", "work@example.com", false); err != nil {
		t.Fatalf("enabled again, still refused: %v", err)
	}
}

// An account that has to be signed in again can be disabled: the mark
// leaves its lapse in logins.json, the row says no lapse while disabled,
// and enabled again it says Sign-in required as before.
func TestDisablingKeepsTheLapse(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	why := "me@example.com's sign-in has expired — add the account again to use it"
	loginsMu.Lock()
	ls := readLogins()
	for i := range ls {
		if ls[i].User == "me@example.com" {
			ls[i].Lapsed = why
		}
	}
	writeLogins(ls)
	loginsMu.Unlock()
	lapse := func() (string, bool) {
		ForgetAccounts()
		for _, l := range Logins("codex") {
			if l.User == "me@example.com" {
				return l.Lapsed, l.Disabled
			}
		}
		t.Fatal("me@ isn't listed")
		return "", false
	}
	if got, _ := lapse(); got != why {
		t.Fatalf("lapse before %q", got)
	}
	if err := SetLoginDisabled("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if got, off := lapse(); got != "" || !off {
		t.Fatalf("disabled: lapse %q, disabled %v", got, off)
	}
	if l := savedOf(t, "me@example.com"); l.Lapsed != why {
		t.Fatalf("the mark took the lapse away: %+v", l)
	}
	if err := SetLoginDisabled("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := lapse(); got != why {
		t.Fatalf("enabled: lapse %q", got)
	}
}
