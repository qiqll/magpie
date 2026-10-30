package gateway

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
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

// The account the gateway key may not use held, not free: De@post.com
// out of its five hours and set not to spend its credits (held at 100%,
// back in 3 hours), or to be signed in again. Either way it is still one
// the key may not use: a spare is added among the key's own, and with
// none to add the user is back when the first of the key's own is (4
// hours), never at De's 3.
func TestUserKeyHeldSeatHeldOtherwise(t *testing.T) {
	t.Run("credits", func(t *testing.T) {
		noRests()
		_, ask, keyID := keyHeldUser(t, "free@post.com")
		_, asked := limitUpstream(t)
		creditsHeldDe(t)
		restSeat("codex@kaaaw@post.com", 4*time.Hour)
		restSeat("codex@so33@post.com", 5*time.Hour)
		rec := ask("hi")
		if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-5" {
			t.Fatalf("no spare among the key's own: %d asked %v %v", rec.Code, got, rec.Header())
		}
		if got := keyBound(t, keyID); len(got) != 4 || got[0] != "De@post.com" {
			t.Fatalf("user 2 bound to %v", got)
		}
	})
	t.Run("credits-no-spare", func(t *testing.T) {
		noRests()
		s, ask, _ := keyHeldUser(t)
		_, asked := limitUpstream(t)
		creditsHeldDe(t)
		restSeat("codex@kaaaw@post.com", 4*time.Hour)
		restSeat("codex@so33@post.com", 5*time.Hour)
		rec := ask("hi")
		if got := asked(true); rec.Code != 429 || len(got) != 0 {
			t.Fatalf("%d asked %v", rec.Code, got)
		}
		if limit, until := limitOf(t, rec); limit != "quota" || !near(until, 4*time.Hour) {
			t.Fatalf("%q until %v, want kaAAw@'s 4 hours: %v %s", limit, until, rec.Header(), refusedWhy(s))
		}
	})
	t.Run("lapsed", func(t *testing.T) {
		noRests()
		_, ask, keyID := keyHeldUser(t, "free@post.com")
		_, asked := limitUpstream(t)
		lapseSaved(t, "De@post.com")
		restSeat("codex@kaaaw@post.com", 4*time.Hour)
		restSeat("codex@so33@post.com", 5*time.Hour)
		rec := ask("hi")
		if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-5" {
			t.Fatalf("no spare among the key's own: %d asked %v %v", rec.Code, got, rec.Header())
		}
		if got := keyBound(t, keyID); len(got) != 4 || got[0] != "De@post.com" {
			t.Fatalf("user 2 bound to %v", got)
		}
	})
}

// creditsHeldDe holds De@post.com at 100% of its five hours, back in 3
// hours, set not to spend its credits; the others have room.
func creditsHeldDe(t *testing.T) {
	t.Helper()
	capUsage(t, map[string]float64{"De@post.com": 100, "de@post.com": 100, "kaAAw@post.com": 10, "so33@post.com": 10, "free@post.com": 10})
	if err := provider.SetCodexCredits("De@post.com", false); err != nil {
		t.Fatal(err)
	}
}

// Every one of the user's accounts is one the gateway key may not use:
// a spare among the key's own is added and asked, the first kept; with
// none free, it is refused and De@post.com isn't asked.
func TestUserOnlyKeyHeldSeats(t *testing.T) {
	bindDeAlone := func(t *testing.T, keyID string) {
		now := time.Now().UTC()
		writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{keyID: {"codex": {
			"2": {Who: "codex@de@post.com", Account: "De@post.com", At: now, Tier: "max", Used: now},
		}}}})
	}
	t.Run("spare", func(t *testing.T) {
		noRests()
		_, ask, keyID := keyHeldUser(t)
		_, asked := limitUpstream(t)
		bindDeAlone(t, keyID)
		rec := ask("hi")
		if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] == "acct-2" {
			t.Fatalf("%d asked %v: %s", rec.Code, got, rec.Body.String())
		}
		if got := keyBound(t, keyID); len(got) != 2 || got[0] != "De@post.com" {
			t.Fatalf("user 2 bound to %v", got)
		}
	})
	t.Run("none-free", func(t *testing.T) {
		noRests()
		_, ask, keyID := keyHeldUser(t)
		_, asked := limitUpstream(t)
		bindDeAlone(t, keyID)
		restSeat("codex@kaaaw@post.com", 4*time.Hour)
		restSeat("codex@so33@post.com", 5*time.Hour)
		rec := ask("hi")
		if got := asked(true); rec.Code == 200 || len(got) != 0 {
			t.Fatalf("%d asked %v: %s", rec.Code, got, rec.Body.String())
		}
		if got := keyBound(t, keyID); len(got) != 1 || got[0] != "De@post.com" {
			t.Fatalf("user 2 bound to %v", got)
		}
	})
}

// The key's own account asked runs out for hours as it is asked, the
// other resting too: a spare is added among the key's own in the same
// request, and answers.
func TestUserKeyHeldSeatSpareAfterTheTry(t *testing.T) {
	noRests()
	_, ask, keyID := keyHeldUser(t, "free@post.com")
	out, asked := limitUpstream(t)
	out("acct-3", 7200)
	restSeat("codex@so33@post.com", 3*time.Hour)
	rec := ask("hi")
	if got := asked(true); rec.Code != 200 || len(got) != 2 || got[0] != "acct-3" || got[1] != "acct-5" {
		t.Fatalf("%d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	if got := keyBound(t, keyID); len(got) != 4 || got[0] != "De@post.com" || got[3] != "free@post.com" {
		t.Fatalf("user 2 bound to %v", got)
	}
}

// Why a user's accounts can't take a request is said of one the key may
// use: De@post.com first but held from the key, kaAAw@ not serving the
// model, is the 400 of the model, not a 403 for the key; bound to only
// such as De, it is the key's 403.
func TestUserBoundRefusalPassesOverKeyHeld(t *testing.T) {
	de := Weighed{Provider: "codex", Kind: "account", Who: "De@post.com", Model: "gpt-5.5", Held: true}
	ka := Weighed{Provider: "codex", Kind: "account", Who: "kaAAw@post.com", Model: "gpt-5.5", Barred: true}
	both := userBinding{Who: "codex@de@post.com", Account: "De@post.com", More: []userSeat{{Who: "codex@kaaaw@post.com", Account: "kaAAw@post.com"}}}
	if r := boundRefusal("2", both, "codex", nil, planned{left: []Weighed{de, ka}}); r.status != 400 || !strings.Contains(r.msg, "doesn't serve gpt-5.5") {
		t.Fatalf("De held, kaAAw not serving: %d %s", r.status, r.msg)
	}
	alone := userBinding{Who: "codex@de@post.com", Account: "De@post.com"}
	if r := boundRefusal("2", alone, "codex", nil, planned{left: []Weighed{de}}); r.status != 403 {
		t.Fatalf("De alone, held: %d %s", r.status, r.msg)
	}
}

// Every account held at its cap: the user is back when the first of its
// own the key may use is, never at one held from the key.
func TestUserCappedForPassesOverKeyHeld(t *testing.T) {
	fresh(t)
	userBinds.Lock()
	userBinds.m, userBinds.stale = nil, false
	userBinds.Unlock()
	now := time.Now().UTC()
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{"k1": {"codex": {
		"2": {Who: "codex@de@post.com", Account: "De@post.com", At: now, Used: now, More: []userSeat{{Who: "codex@kaaaw@post.com", Account: "kaAAw@post.com", At: now}}},
	}}}})
	soon, later := now.Add(time.Hour), now.Add(3*time.Hour)
	ws := []Weighed{
		{Provider: "codex", Kind: "account", Who: "De@post.com", Model: "gpt-5.5", Capped: 90, Used: 95, CapBack: &soon, Held: true},
		{Provider: "codex", Kind: "account", Who: "kaAAw@post.com", Model: "gpt-5.5", Capped: 90, Used: 95, CapBack: &later},
	}
	if limit, until := cappedFor("k1", "2", "codex", "gpt-5.5", ws, soon); limit != "cap" || !until.Equal(later) {
		t.Fatalf("%q until %v, want kaAAw@'s %v", limit, until, later)
	}
}
