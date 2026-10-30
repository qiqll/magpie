package gateway

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
)

// keyHeldUser stands up the reporter's case (T-014): user "2", of tier
// max (three accounts here), bound first to De@post.com, which is signed
// in and free but which the gateway key qmcloud may not use, then to
// kaAAw@ and so33@, which it may. The key may use the accounts allowed
// too. It answers how to ask as that user with the key.
func keyHeldUser(t *testing.T, allowed ...string) (s *Server, ask func(input string) *httptest.ResponseRecorder, keyID string) {
	t.Helper()
	codexSignedIn(t, "De@post.com", "kaAAw@post.com", "so33@post.com", "free@post.com")
	t.Cleanup(noRests)
	keys, secrets := newCaller(t, "qmcloud")
	accounts := []string{"codex/kaAAw@post.com", "codex/so33@post.com"}
	for _, a := range allowed {
		accounts = append(accounts, "codex/"+a)
	}
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: accounts}); err != nil {
		t.Fatal(err)
	}
	writeTiers(t, `{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":2},"max":{"credits":10000,"max_accounts":3,"share":1}}}`)
	now := time.Now().UTC()
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{keys[0].ID: {"codex": {
		"2": {Who: "codex@de@post.com", Account: "De@post.com", At: now, Tier: "max", Used: now, More: []userSeat{
			{Who: "codex@kaaaw@post.com", Account: "kaAAw@post.com", At: now},
			{Who: "codex@so33@post.com", Account: "so33@post.com", At: now},
		}},
	}}}})
	s = byUser(New())
	return s, func(input string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"`+input+`"}`))
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		req.Header.Set(UserHeader, "2")
		req.Header.Set(UserTierHeader, "max")
		lanGuard(s.Handler()).ServeHTTP(rec, req)
		return rec
	}, keys[0].ID
}

// restSeat rests the account who for d, out of its allowance.
func restSeat(who string, d time.Duration) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	restingUntil.m[who] = time.Now().Add(d)
	restingUntil.note[who] = Rest{Why: failQuota, Status: 429, By: "resets", Key: who}
}

// keyBound is the accounts user "2" of the gateway key keyID is bound to
// for codex, as user-accounts.json has them.
func keyBound(t *testing.T, keyID string) []string {
	t.Helper()
	userBinds.Lock()
	defer userBinds.Unlock()
	if err := loadUsers(); err != nil {
		t.Fatal(err)
	}
	return accountsOf(userBinds.m[keyID]["codex"]["2"].seats())
}

// Bound to an account the gateway key may not use, free, and to two the
// key may, both out for hours: the one it may not use neither answers
// for it nor counts toward its tier's three, so a spare is added among
// the key's own, and asked; the one it may not use stays its first.
func TestUserKeyHeldSeatGivesASpare(t *testing.T) {
	noRests()
	_, ask, keyID := keyHeldUser(t, "free@post.com")
	_, asked := limitUpstream(t)
	restSeat("codex@kaaaw@post.com", 2*time.Hour)
	restSeat("codex@so33@post.com", 3*time.Hour)
	rec := ask("hi")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-5" {
		t.Fatalf("no spare among the key's own: %d asked %v %v: %s", rec.Code, got, rec.Header(), rec.Body.String())
	}
	if got := keyBound(t, keyID); len(got) != 4 || got[0] != "De@post.com" || got[3] != "free@post.com" {
		t.Fatalf("user 2 bound to %v", got)
	}
}

// With no spare to add (the key may use no other account), user "2" is
// told it is limited until the first of the key's own accounts is back,
// never at once for the one the key may not use: refused before anything
// is asked, and when the account asked runs out, its rest and the
// other's the first back; the one the key may not use is never asked,
// resting soon or not.
func TestUserKeyHeldSeatSaysTheKeysFirstBack(t *testing.T) {
	for _, deRests := range []bool{false, true} {
		name := "de-free"
		if deRests {
			name = "de-rests-sooner"
		}
		t.Run(name, func(t *testing.T) {
			noRests()
			s, ask, keyID := keyHeldUser(t)
			out, asked := limitUpstream(t)
			if deRests {
				restSeat("codex@de@post.com", 10*time.Minute)
			}
			// kaAAw@ (acct-3) out of its allowance for two hours as it is
			// asked, so33@ resting an hour
			out("acct-3", 7200)
			restSeat("codex@so33@post.com", time.Hour)
			rec := ask("hi")
			if got := asked(true); rec.Code != 429 || slices.ContainsFunc(got, func(a string) bool { return a != "acct-3" }) {
				t.Fatalf("asked %v: %d %s", got, rec.Code, rec.Body.String())
			}
			if limit, until := limitOf(t, rec); limit != "quota" || !near(until, time.Hour) {
				t.Fatalf("after the try: %q until %v, want so33@'s hour: %v %s", limit, until, rec.Header(), refusedWhy(s))
			}
			// both of the key's resting now: refused at once, the same
			rec = ask("again")
			if got := asked(true); rec.Code != 429 || len(got) != 0 {
				t.Fatalf("resting: %d asked %v: %s", rec.Code, got, rec.Body.String())
			}
			if limit, until := limitOf(t, rec); limit != "quota" || !near(until, time.Hour) {
				t.Fatalf("resting: %q until %v, want so33@'s hour: %v", limit, until, rec.Header())
			}
			if got := keyBound(t, keyID); len(got) != 3 || got[0] != "De@post.com" {
				t.Fatalf("user 2 bound to %v", got)
			}
		})
	}
}

// The same user of a gateway key that may use every account is served
// by its first, De@post.com, as before.
func TestUserKeyHeldSeatServedWithoutTheHold(t *testing.T) {
	noRests()
	t.Cleanup(noRests)
	codexSignedIn(t, "De@post.com", "kaAAw@post.com", "so33@post.com")
	_, asked := limitUpstream(t)
	writeTiers(t, `{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":3}}}`)
	now := time.Now().UTC()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"2": {Who: "codex@de@post.com", Account: "De@post.com", At: now, Used: now, More: []userSeat{
			{Who: "codex@kaaaw@post.com", Account: "kaAAw@post.com", At: now},
			{Who: "codex@so33@post.com", Account: "so33@post.com", At: now},
		}},
	}}})
	restSeat("codex@kaaaw@post.com", 2*time.Hour)
	restSeat("codex@so33@post.com", 3*time.Hour)
	s := byUser(New())
	if rec := userPost(s, "codex/gpt-5.5", "2", "hi"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("asked %v, want De@post.com's acct-2", got)
	}
}
