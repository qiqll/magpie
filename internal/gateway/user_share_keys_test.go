package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
)

// sharedAcrossKeys signs Codex in with kaAAw@post.com (acct-2) beside
// it, makes the gateway keys other and qmcloud, qmcloud held to kaAAw@
// alone, writes tiers (each user's default tier) and binds users of each
// key as bound says, key name → user → accounts, each of the tier tierOf
// names ("<key>/<user>" → tier), if any. It answers how to ask
// as a user with qmcloud.
func sharedAcrossKeys(t *testing.T, tiers string, bound map[string]map[string][]string, tierOf ...map[string]string) (s *Server, ask func(user string) *httptest.ResponseRecorder, ids map[string]string) {
	t.Helper()
	codexSignedIn(t, "kaAAw@post.com")
	keys, secrets := newCaller(t, "other", "qmcloud")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[1].ID, Accounts: []string{"codex/kaAAw@post.com"}}); err != nil {
		t.Fatal(err)
	}
	ids = map[string]string{"other": keys[0].ID, "qmcloud": keys[1].ID}
	writeTiers(t, tiers)
	now := time.Now().UTC()
	f := userFile{Keys: map[string]map[string]map[string]userBinding{}}
	for key, users := range bound {
		f.Keys[ids[key]] = map[string]map[string]userBinding{"codex": {}}
		for user, accounts := range users {
			var seats []userSeat
			for _, a := range accounts {
				seats = append(seats, userSeat{Who: "codex@" + strings.ToLower(a), Account: a, At: now})
			}
			b := bindingOf(seats)
			b.Used = now
			for _, m := range tierOf {
				b.Tier = m[key+"/"+user]
			}
			f.Keys[ids[key]]["codex"][user] = b
		}
	}
	writeUsers(t, f)
	s = byUser(New())
	return s, func(user string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`))
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Authorization", "Bearer "+secrets[1])
		req.Header.Set(UserHeader, user)
		lanGuard(s.Handler()).ServeHTTP(rec, req)
		return rec
	}, ids
}

// A user on an account under one gateway key isn't kept off it under
// another by its own seat, its tier having it alone (share 1): user "2",
// on kaAAw@ under other, is bound to it under qmcloud and served. User
// "3", another name, is still refused it, as it is taken.
func TestUserOwnSeatUnderAnotherKeyKeepsNobodyOff(t *testing.T) {
	_, ask, ids := sharedAcrossKeys(t, `{"default":"solo","tiers":{"solo":{"credits":1000,"max_accounts":2,"share":1}}}`,
		map[string]map[string][]string{"other": {"2": {"kaAAw@post.com"}}})
	_, asked := limitUpstream(t)
	rec := ask("2")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("user 2 under qmcloud: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	userBinds.Lock()
	err := loadUsers()
	b := userBinds.m[ids["qmcloud"]]["codex"]["2"]
	userBinds.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if b.Account != "kaAAw@post.com" {
		t.Fatalf("user 2 under qmcloud bound to %+v", b)
	}
	rec = ask("3")
	if got := asked(true); rec.Code != 429 || len(got) != 0 {
		t.Fatalf("user 3, the account had alone by 2: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
}

// One name bound to an account under two gateway keys is one user of it:
// with a tier that shares one with one other user (share 2), user "3" is
// that other, and user "4" is refused it.
func TestUserUnderTwoKeysCountedOnce(t *testing.T) {
	_, ask, _ := sharedAcrossKeys(t, `{"default":"duo","tiers":{"duo":{"credits":1000,"max_accounts":2,"share":2}}}`,
		map[string]map[string][]string{
			"other":   {"2": {"kaAAw@post.com"}},
			"qmcloud": {"2": {"kaAAw@post.com"}},
		})
	_, asked := limitUpstream(t)
	rec := ask("3")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("user 3 beside 2 alone: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
	rec = ask("4")
	if got := asked(true); rec.Code != 429 || len(got) != 0 {
		t.Fatalf("user 4, a third: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
}

// A name is the caller's to send, tied to no key: the caller of
// qmcloud sending "2", the name a user of the key other goes by alone
// on kaAAw@ (share 1), is put on kaAAw@ beside it as that same user.
// The behaviour the owner chose (option A2, 2026-10-30: every key one
// trust domain), held here so a change to it is deliberate; the risk is
// in seatUsesOf's comment.
func TestUserNameOfAnotherKeySharesItsSeats(t *testing.T) {
	_, ask, _ := sharedAcrossKeys(t, `{"default":"solo","tiers":{"solo":{"credits":1000,"max_accounts":2,"share":1}}}`,
		map[string]map[string][]string{"other": {"2": {"kaAAw@post.com"}}})
	_, asked := limitUpstream(t)
	rec := ask("2")
	if got := asked(true); rec.Code != 200 || len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("qmcloud's caller as other's \"2\": %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
}

// One name under two keys with tiers that share an account differently
// holds it to the least of them: "2", alone on kaAAw@ under other (share
// 1) and sharing it under qmcloud (share 3), keeps user "3" off it.
func TestUserUnderTwoKeysTheLeastShare(t *testing.T) {
	_, ask, _ := sharedAcrossKeys(t, `{"default":"trio","tiers":{"solo":{"credits":1000,"max_accounts":2,"share":1},"trio":{"credits":1000,"max_accounts":2,"share":3}}}`,
		map[string]map[string][]string{
			"other":   {"2": {"kaAAw@post.com"}},
			"qmcloud": {"2": {"kaAAw@post.com"}},
		}, map[string]string{"other/2": "solo", "qmcloud/2": "trio"})
	_, asked := limitUpstream(t)
	rec := ask("3")
	if got := asked(true); rec.Code != 429 || len(got) != 0 {
		t.Fatalf("user 3 beside 2, who has it alone under other: %d asked %v: %s", rec.Code, got, rec.Body.String())
	}
}
