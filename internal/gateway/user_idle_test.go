package gateway

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// usedIn is the Used user-accounts.json keeps for user of pid.
func usedIn(t *testing.T, pid, user string) time.Time {
	t.Helper()
	b, err := os.ReadFile(usersPath())
	if err != nil {
		t.Fatalf("user-accounts.json: %v", err)
	}
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("user-accounts.json: %v: %s", err, b)
	}
	return f.Users[pid][user].Used
}

// oneAccountUsed binds user to the provider's only account, of tier, as
// it last sent it a request used.
func oneAccountUsed(t *testing.T, user, tier string, used time.Time) {
	t.Helper()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		user: {Who: "codex@me@example.com", Account: "me@example.com", At: used, Tier: tier, Used: used}}}})
}

// A user that sent the provider nothing for 3 days lets go of its
// accounts, and a pro user that comes after is bound to one it was on;
// back, the idle user is bound afresh, never onto the pro user's.
func TestUserIdleLetsGoOfItsAccounts(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	oneAccountUsed(t, "carol", "pro", time.Now().UTC().Add(-userIdle-time.Hour))
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	seats := seatsIn(t, "codex")
	if len(seats["alice"]) != 1 || len(seats["carol"]) != 0 {
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
	if used := usedIn(t, "codex", "carol"); time.Since(used) > time.Minute {
		t.Fatalf("carol's last use %v", used)
	}
}

// Under 3 days a user keeps its accounts, though the pool is full.
func TestUserIdleUnderThreeDaysKeepsItsAccounts(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	kept := time.Now().UTC().Add(-userIdle + time.Hour).Truncate(time.Second)
	oneAccountUsed(t, "carol", "pro", kept)
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 429 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if used := usedIn(t, "codex", "carol"); !used.Equal(kept) {
		t.Fatalf("carol's last use %v, kept %v", used, kept)
	}
	if n := len(seatsIn(t, "codex")["carol"]); n != 1 {
		t.Fatalf("carol on %d accounts", n)
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
}

// A binding kept before its last use was (no `used`) is stamped now, not
// let go: an upgrade frees nobody at once, and a pro user is still refused
// its account.
func TestUserBoundBeforeUsedIsStampedNotLetGo(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	oneAccountUsed(t, "carol", "pro", time.Time{})
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 429 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(seatsIn(t, "codex")["carol"]); n != 1 {
		t.Fatalf("carol on %d accounts", n)
	}
	used := usedIn(t, "codex", "carol")
	if used.IsZero() || time.Since(used) > time.Minute {
		t.Fatalf("carol not stamped: %v", used)
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
}

// A user idle for days that sends a request keeps its accounts: the
// sweep is never its own request's.
func TestUserIdleUserSendingIsNotSwept(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	s := byUser(New())
	oneAccountUsed(t, "carol", "pro", time.Now().UTC().Add(-userIdle-time.Hour))
	if rec := tierPost(s, "carol", "pro", "hi"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 {
		t.Fatalf("asked %v", got)
	}
	if n := len(seatsIn(t, "codex")["carol"]); n != 1 {
		t.Fatalf("carol on %d accounts", n)
	}
	if used := usedIn(t, "codex", "carol"); time.Since(used) > time.Minute {
		t.Fatalf("carol's last use %v", used)
	}
}

// A user's last use is written once an hour: one kept a quarter of an
// hour ago is left as it is, one kept two hours ago is written.
func TestUserUsedWrittenOnceAnHour(t *testing.T) {
	codexSignedIn(t)
	usersUpstream(t, nil)
	s := byUser(New())
	for _, c := range []struct {
		ago  time.Duration
		same bool
	}{
		{15 * time.Minute, true},
		{2 * time.Hour, false},
	} {
		kept := time.Now().UTC().Add(-c.ago).Truncate(time.Second)
		oneAccountUsed(t, "carol", "pro", kept)
		if rec := tierPost(s, "carol", "pro", "hi"); rec.Code != 200 {
			t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
		}
		used := usedIn(t, "codex", "carol")
		if c.same && !used.Equal(kept) {
			t.Fatalf("kept %v ago: used %v, want %v", c.ago, used, kept)
		}
		if !c.same && time.Since(used) > time.Minute {
			t.Fatalf("kept %v ago: used not written: %v", c.ago, used)
		}
	}
}

// The user whose request it is keeps its accounts however long it was
// away, while another user that was away as long is let go: carol stays
// on hers, never moved to bob's freed one.
func TestUserIdleUserKeepsItsOwnOnItsRequest(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	usersUpstream(t, nil)
	s := byUser(New())
	away := time.Now().UTC().Add(-userIdle - time.Hour)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": {Who: "codex@me@example.com", Account: "me@example.com", At: away, Tier: "free", Used: away},
		"bob":   {Who: "codex@b@example.com", Account: "b@example.com", At: away, Tier: "free", Used: away}}}})
	if rec := tierPost(s, "carol", "free", "back"); rec.Code != 200 {
		t.Fatalf("carol: %d %s", rec.Code, rec.Body.String())
	}
	seats := seatsIn(t, "codex")
	if c := seats["carol"]; len(c) != 1 || c[0] != "me@example.com" {
		t.Fatalf("carol moved: %v", seats)
	}
	if n := len(seats["bob"]); n != 0 {
		t.Fatalf("bob kept %d accounts", n)
	}
	if used := usedIn(t, "codex", "carol"); time.Since(used) > time.Minute {
		t.Fatalf("carol's last use %v", used)
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
	if used := usedIn(t, "codex", "carol"); used.IsZero() {
		t.Fatal("carol's last use lost with the spare")
	}
}
