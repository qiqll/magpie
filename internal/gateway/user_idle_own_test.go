package gateway

import (
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// bindIdleHours saves Settings' idle time of a bound user, nil for the
// default (T-242).
func bindIdleHours(t *testing.T, h *int) {
	t.Helper()
	s := settings.Load()
	s.BindIdleHours = h
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// withSpare is b with the spare account of codex added.
func withSpare(b userBinding, account string) userBinding {
	b.More = append(b.More, userSeat{Who: "codex@" + account, Account: account, At: b.At})
	return b
}

// Settings' idle time is the one a user is let go after (T-242): 10 hours
// set, a user idle for 11 lets go of its account, and the next user takes
// it; idle for 9 it keeps it. Unset, 11 hours is far from the 60 of
// before, and it keeps it too.
func TestUserIdleAfterSettingsTime(t *testing.T) {
	for _, c := range []struct {
		name  string
		hours *int
		idle  time.Duration
		freed bool
	}{
		{"set, past it", new(10), 11 * time.Hour, true},
		{"set, within it", new(10), 9 * time.Hour, false},
		{"unset", nil, 11 * time.Hour, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexSignedIn(t, "b@example.com")
			asked, _ := usersUpstream(t, nil)
			bindIdleHours(t, c.hours)
			s := byUser(New())
			writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
				"carol": bound("me@example.com", "pro", ago(c.idle)),
				"bob":   bound("b@example.com", "pro", ago(time.Minute))}}})
			forgetSeen()
			rec := tierPost(s, "alice", "pro", "hi")
			asked(true)
			seats := seatsIn(t, "codex")
			if c.freed {
				if rec.Code != 200 || len(seats["carol"]) != 0 || !slices.Equal(seats["alice"], []string{"me@example.com"}) {
					t.Fatalf("carol idle %v past 10 hours: %d %v", c.idle, rec.Code, seats)
				}
				return
			}
			if rec.Code != 429 || !slices.Equal(seats["carol"], []string{"me@example.com"}) {
				t.Fatalf("carol idle %v kept: %d %v", c.idle, rec.Code, seats)
			}
		})
	}
}

// An account's own idle time lets go of it alone (T-242): carol, on
// me@ with the spare b@ and idle 11 hours, loses b@, whose own time is
// 10 hours, and keeps me@, which follows Settings' 60; its own time taken
// off, she keeps both.
func TestUserIdleAfterAnAccountsOwnTime(t *testing.T) {
	for _, own := range []int{10, 0} {
		codexSignedIn(t, "b@example.com", "c@example.com")
		asked, _ := usersUpstream(t, nil)
		if own != 0 {
			if err := provider.SetAccountBindIdle("codex", "B@example.com", own); err != nil {
				t.Fatal(err)
			}
		}
		s := byUser(New())
		used := ago(11 * time.Hour)
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"carol": withSpare(bound("me@example.com", "pro", used), "b@example.com")}}})
		forgetSeen()
		if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 200 {
			t.Fatalf("own %d: alice: %d %s", own, rec.Code, rec.Body.String())
		}
		asked(true)
		want := []string{"me@example.com", "b@example.com"}
		if own != 0 {
			want = want[:1]
		}
		b := bindingIn(t, "codex", "carol")
		if got := seatsIn(t, "codex")["carol"]; !slices.Equal(got, want) {
			t.Fatalf("own %d: carol's accounts %v, want %v", own, got, want)
		}
		if b.Tier != "pro" || !b.Used.Equal(used) {
			t.Fatalf("own %d: carol's tier %q, last use %v", own, b.Tier, b.Used)
		}
	}
}

// An account's own time longer than Settings' keeps its user past
// Settings' (T-242): carol idle 70 hours keeps me@, whose own time is
// 100 hours.
func TestUserIdleAccountsOwnTimeLonger(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	if err := provider.SetAccountBindIdle("codex", "me@example.com", 100); err != nil {
		t.Fatal(err)
	}
	s := byUser(New())
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"carol": bound("me@example.com", "pro", ago(70*time.Hour)),
		"bob":   bound("b@example.com", "pro", ago(time.Minute))}}})
	forgetSeen()
	if rec := tierPost(s, "alice", "pro", "hi"); rec.Code != 429 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if got := seatsIn(t, "codex")["carol"]; !slices.Equal(got, []string{"me@example.com"}) {
		t.Fatalf("carol's accounts %v", got)
	}
}

// An account's idle time is held to 1 to 720 hours, 0 taking it off; one
// out of range is refused and nothing kept (T-242).
func TestSetAccountBindIdleRange(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	for _, h := range []int{-1, settings.BindIdleMost + 1} {
		if err := provider.SetAccountBindIdle("codex", "b@example.com", h); err == nil {
			t.Errorf("%d hours taken", h)
		}
	}
	if err := provider.SetAccountBindIdle("codex", "nobody@example.com", 5); err == nil {
		t.Error("an account codex doesn't have taken")
	}
	if got := provider.AccountBindIdles("codex"); len(got) != 0 {
		t.Fatalf("kept %v", got)
	}
	if err := provider.SetAccountBindIdle("codex", "b@example.com", 5); err != nil {
		t.Fatal(err)
	}
	if got := provider.AccountBindIdles("codex"); got["b@example.com"] != 5*time.Hour {
		t.Fatalf("kept %v", got)
	}
	if err := provider.SetAccountBindIdle("codex", "b@example.com", 0); err != nil {
		t.Fatal(err)
	}
	if got := provider.AccountBindIdles("codex"); len(got) != 0 {
		t.Fatalf("after 0: %v", got)
	}
}
