package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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
	if !AccountBanned(&Account{Agent: "codex", User: "ME@example.com"}) {
		t.Fatal("the mark is matched in one case only")
	}

	// signed in to it again, and away from it again
	codexSignIn(t, home, "me@example.com", "r-again")
	rememberLogins(true)
	if !bannedOf(t, "me@example.com") {
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
