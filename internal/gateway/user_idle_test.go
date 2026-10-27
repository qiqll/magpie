package gateway

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
)

// usedIn is the Used user-accounts.json keeps for user of pid.
func usedIn(t *testing.T, pid, user string) time.Time {
	t.Helper()
	return bindingIn(t, pid, user).Used
}

// usersFile is user-accounts.json as it is.
func usersFile(t *testing.T) userFile {
	t.Helper()
	b, err := os.ReadFile(usersPath())
	if err != nil {
		t.Fatalf("user-accounts.json: %v", err)
	}
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("user-accounts.json: %v: %s", err, b)
	}
	return f
}

// bindingIn is the binding user-accounts.json keeps for user of pid.
func bindingIn(t *testing.T, pid, user string) userBinding {
	t.Helper()
	return usersFile(t).Users[pid][user]
}

// bound is a binding to account (of codex) of tier, last used at used.
func bound(account, tier string, used time.Time) userBinding {
	return userBinding{Who: "codex@" + account, Account: account, At: used, Tier: tier, Used: used}
}

// ago is now less d, to the second, in UTC as the file keeps it.
func ago(d time.Duration) time.Time { return time.Now().UTC().Add(-d).Truncate(time.Second) }

// days is n days.
func days(n float64) time.Duration { return time.Duration(n * 24 * float64(time.Hour)) }

// forgetSeen forgets what this process saw, as a restart does.
func forgetSeen() {
	userSeen.Lock()
	clear(userSeen.m)
	userSeen.Unlock()
}

// other is the one of me@ and b@example.com that isn't account.
func other(account string) string {
	if account == "me@example.com" {
		return "b@example.com"
	}
	return "me@example.com"
}

// Idle is three days, not a value that moves with the constant.
func TestUserIdleIsThreeDays(t *testing.T) {
	if userIdle != 72*time.Hour {
		t.Fatalf("userIdle %v", userIdle)
	}
}

// A user that sent the provider nothing for 3 days, while another sent it
// a request an hour ago, lets go of its accounts, and a pro user that
// comes after is bound to the one it was on; back, it is bound afresh,
// never onto the pro user's.
func TestUserIdleLetsGoOfItsAccounts(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound("me@example.com", "pro", ago(72*time.Hour+time.Hour)),
		"bob":   bound("b@example.com", "pro", ago(time.Hour))}}})
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	seats := seatsIn(t, "codex")
	if len(seats["alice"]) != 1 || len(seats["carol"]) != 0 || len(seats["bob"]) != 1 {
		t.Fatalf("alice with carol idle: %v", seats)
	}
	if rec := tierPost(s, "carol", "pro", "back"); rec.Code != 200 {
		t.Fatalf("carol back: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	seats = seatsIn(t, "codex")
	if c, a := seats["carol"], seats["alice"]; len(c) != 1 || c[0] == a[0] {
		t.Fatalf("carol back: %v", seats)
	}
	if used := usedIn(t, "codex", "carol"); !near(used, 0) {
		t.Fatalf("carol's last use %v", used)
	}
}

// Under 3 days a user keeps its accounts, though the pool is full, and
// its last use is left as it is.
func TestUserIdleUnderThreeDaysKeepsItsAccounts(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	kept := ago(72*time.Hour - time.Hour)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound("me@example.com", "pro", kept),
		"bob":   bound("b@example.com", "pro", ago(time.Minute))}}})
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 429 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if used := usedIn(t, "codex", "carol"); !used.Equal(kept) {
		t.Fatalf("carol's last use %v, kept %v", used, kept)
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
}

// A gateway down for a long weekend lets nobody go: idle is counted up to
// the last request any user sent the provider, the requesting user's own
// before this one among them.
func TestUserIdleNotCountedWhileTheGatewayWasDown(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound("me@example.com", "pro", ago(days(4.5))),
		"bob":   bound("b@example.com", "pro", ago(days(4)))}}})
	// bob back first after the weekend: carol was idle half a day before it
	if rec := tierPost(s, "bob", "pro", "monday"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	// a new user: nobody of the provider since the weekend but bob
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 429 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if seats := seatsIn(t, "codex"); len(seats["carol"]) != 1 || len(seats["bob"]) != 1 {
		t.Fatalf("a weekend down let users go: %v", seats)
	}
}

// A binding kept before its last use was (no `used`) is stamped now, not
// let go: an upgrade frees nobody at once, and a pro user is still refused
// its account.
func TestUserBoundBeforeUsedIsStampedNotLetGo(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound("me@example.com", "pro", time.Time{})}}})
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 429 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(seatsIn(t, "codex")["carol"]); n != 1 {
		t.Fatalf("carol on %d accounts", n)
	}
	if used := usedIn(t, "codex", "carol"); !near(used, 0) {
		t.Fatalf("carol not stamped: %v", used)
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
}

// The user whose request it is keeps its accounts however long it was
// away, bound as before (its first and when it was bound unchanged),
// while another away as long is let go.
func TestUserIdleUserKeepsItsOwnOnItsRequest(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com")
	usersUpstream(t, nil)
	s := byUser(New())
	away := ago(days(5))
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound("c@example.com", "free", away),
		"bob":   bound("b@example.com", "free", away),
		"dave":  bound("me@example.com", "pro", ago(time.Hour))}}})
	if rec := tierPost(s, "carol", "free", "back"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.User == nil || r.User.Why != "kept" {
		t.Fatalf("carol bound afresh: %+v", r.User)
	}
	if c := bindingIn(t, "codex", "carol"); c.Account != "c@example.com" || !c.At.Equal(away) || !near(c.Used, 0) {
		t.Fatalf("carol: %+v", c)
	}
	if n := len(seatsIn(t, "codex")["bob"]); n != 0 {
		t.Fatalf("bob kept %d accounts", n)
	}
}

// A user's last use is written once an hour: one kept a quarter of an
// hour ago is left as it is, one kept two hours ago, or ahead of now, is
// written now.
func TestUserUsedWrittenOnceAnHour(t *testing.T) {
	codexSignedIn(t)
	usersUpstream(t, nil)
	s := byUser(New())
	for _, c := range []struct {
		user string // each its own, for the file's size to change
		at   time.Time
		same bool
	}{
		{"carol", ago(15 * time.Minute), true},
		{"carol-two-hours", ago(2 * time.Hour), false},
		{"carol-ahead-of-now", ago(-days(2)), false},
	} {
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {c.user: bound("me@example.com", "pro", c.at)}}})
		if rec := tierPost(s, c.user, "pro", "hi"); rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.user, rec.Code, rec.Body.String())
		}
		used := usedIn(t, "codex", c.user)
		if c.same && !used.Equal(c.at) {
			t.Fatalf("%s: used %v, want %v", c.user, used, c.at)
		}
		if !c.same && !near(used, 0) {
			t.Fatalf("%s: used not written now: %v", c.user, used)
		}
	}
}

// Adding a spare keeps the user's last use with its accounts.
func TestUserUsedKeptWhenASpareIsAdded(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	out, asked := limitUpstream(t)
	s := byUser(New())
	if rec := tierPost(s, "carol", "free", "hi"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	out(asked(true)[0], 7200)
	if rec := tierPost(s, "carol", "free", "spent"); rec.Code != 200 {
		t.Fatalf("carol spent: %d %s", rec.Code, rec.Body.String())
	}
	if seats := seatsIn(t, "codex")["carol"]; len(seats) != 2 {
		t.Fatalf("carol on %v", seats)
	}
	if used := usedIn(t, "codex", "carol"); !near(used, 0) {
		t.Fatalf("carol's last use %v", used)
	}
}

// A user served while the file didn't read, its `used` there old, isn't
// idle once it reads again: this process saw its requests.
func TestUserServedFromMemoryIsNotIdle(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	usersUpstream(t, nil)
	s := byUser(New())
	if rec := tierPost(s, "carol", "pro", "hi"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	mine := bindingIn(t, "codex", "carol").Account
	// the file as it was before the days it didn't read
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound(mine, "pro", ago(days(4))),
		"bob":   bound(other(mine), "pro", ago(time.Minute))}}})
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 429 {
		t.Fatalf("alice took carol's: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(seatsIn(t, "codex")["carol"]); n != 1 {
		t.Fatalf("carol on %d accounts", n)
	}
}

// A user told to wait out its account's rest, longer than idle, keeps
// its accounts while it waits, a restart between.
func TestUserWaitingOutARestIsNotIdle(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	out, asked := limitUpstream(t)
	s := byUserUpTo(t, New(), 1) // its one account: no spare
	if rec := tierPost(s, "carol", "", "hi"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	mine := bindingIn(t, "codex", "carol").Account
	out(asked(true)[0], int(days(5).Seconds()))
	if rec := tierPost(s, "carol", "", "the week's used up"); rec.Code != 429 {
		t.Fatalf("carol out: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound(mine, "free", ago(days(4))),
		"bob":   bound(other(mine), "free", ago(time.Minute))}}})
	forgetSeen() // only the rest is known
	tierPost(s, "alice", "", "hi")
	if n := len(seatsIn(t, "codex")["carol"]); n != 1 {
		t.Fatalf("carol on %d accounts", n)
	}
}

// Of two gateway keys the same user id is two users: one key's carol
// sending a request lets the other key's idle carol go, and keeps its own.
func TestUserIdleOfAnotherKey(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	usersUpstream(t, nil)
	a, err := access.Update("add-key", access.Change{Name: "Tenant A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := access.Update("add-key", access.Change{Name: "Tenant B"})
	if err != nil {
		t.Fatal(err)
	}
	s := byUser(New())
	if rec := userPostWith(s, a, "carol", "a's"); rec.Code != 200 {
		t.Fatalf("a's carol: %d %s", rec.Code, rec.Body.String())
	}
	var aKey string
	for k := range usersFile(t).Keys {
		aKey = k
	}
	if rec := userPostWith(s, b, "carol", "b's"); rec.Code != 200 {
		t.Fatalf("b's carol: %d %s", rec.Code, rec.Body.String())
	}
	f := usersFile(t)
	if len(f.Keys) != 2 || aKey == "" {
		t.Fatalf("keys %v", f.Keys)
	}
	c := f.Keys[aKey]["codex"]["carol"]
	c.Used = ago(days(4))
	f.Keys[aKey]["codex"]["carol"] = c
	writeUsers(t, f)
	forgetSeen()
	if rec := userPostWith(s, b, "carol", "b's again"); rec.Code != 200 {
		t.Fatalf("b's carol again: %d %s", rec.Code, rec.Body.String())
	}
	f = usersFile(t)
	if _, kept := f.Keys[aKey]["codex"]["carol"]; kept {
		t.Fatalf("a's idle carol kept: %+v", f.Keys)
	}
	if len(f.Keys) != 1 {
		t.Fatalf("b's carol let go: %+v", f.Keys)
	}
}

// The Gateway page is told a user's last use as this process saw it (the
// file is written once an hour), and never ahead of now.
func TestUserUsedShownAsSeen(t *testing.T) {
	codexSignedIn(t)
	usersUpstream(t, nil)
	s := byUser(New())
	kept := ago(30 * time.Minute)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound("me@example.com", "pro", kept),
		"ahead": bound("me@example.com", "free", ago(-days(1)))}}})
	if rec := tierPost(s, "carol", "pro", "hi"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	if used := usedIn(t, "codex", "carol"); !used.Equal(kept) {
		t.Fatalf("the file written within the hour: %v", used)
	}
	rows, err := UserBindings()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]time.Time{}
	for _, r := range rows {
		got[r.User] = r.Used
	}
	if !near(got["carol"], 0) {
		t.Errorf("carol shown at %v, not as seen now", got["carol"])
	}
	if got["ahead"].After(time.Now()) || !near(got["ahead"], 0) {
		t.Errorf("ahead shown at %v", got["ahead"])
	}
}
