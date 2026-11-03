package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// growthUsage stands in for the accounts' windows as last read, named as
// the vendor names them: the five hours and the week of each, by user.
func growthUsage(t *testing.T, five, week map[string]float64) {
	t.Helper()
	old := allowances
	t.Cleanup(func() { allowances = old })
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, n := range five {
			out[u] = provider.Allowance{
				{Name: "5 hours", Used: n, Resets: time.Now().Add(3 * time.Hour), Span: 5 * time.Hour},
				{Name: "Weekly", Used: week[u], Resets: time.Now().Add(4 * 24 * time.Hour), Span: 7 * 24 * time.Hour},
			}
		}
		return out
	}
}

// growthDefault puts magpie's default growth policy back, which
// codexSignedIn turns off.
func growthDefault(t *testing.T) {
	t.Helper()
	s := settings.Load()
	s.Growth = nil
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// addedAgo has the account user of id added ago before now in
// providers.json, as days going by would.
func addedAgo(t *testing.T, id, user string, ago time.Duration) {
	t.Helper()
	ps, err := provider.Stored()
	if err != nil {
		t.Fatal(err)
	}
	for i := range ps {
		if ps[i].ID == id {
			if ps[i].AccountAdded[user].IsZero() {
				t.Fatalf("%s's %s has no day added: %v", id, user, ps[i].AccountAdded)
			}
			ps[i].AccountAdded[user] = time.Now().Add(-ago).UTC()
			if _, _, err := provider.Restore(ps[i : i+1]); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no %s in providers.json", id)
}

// Codex's own account, found after the spare was kept, is new: on its
// second day the growth policy holds it at 50% of its five hours, and a
// request goes to the spare though the new one is first in the order. On
// its eighth day the week is no longer held, and at 85% of it the new one
// takes the request again.
func TestGrowthMovesOnFromANewAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	growthDefault(t)
	b := newResetBackend(t)
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	provider.All() // logins.json written with Codex's own account
	addedAgo(t, "codex", "me@example.com", 36*time.Hour)
	growthUsage(t, map[string]float64{"me@example.com": 55, "spare@example.com": 20}, map[string]float64{"me@example.com": 10})
	code, body := resetPost(t, New())
	if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-2" {
		t.Fatalf("second day at 55%%: %d %s tried %s", code, body, tried)
	}

	addedAgo(t, "codex", "me@example.com", 7*24*time.Hour+12*time.Hour)
	growthUsage(t, map[string]float64{"me@example.com": 55, "spare@example.com": 20}, map[string]float64{"me@example.com": 85})
	b.mu.Lock()
	b.tried = nil
	b.mu.Unlock()
	code, body = resetPost(t, New())
	if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1" {
		t.Fatalf("eighth day at 85%% of its week: %d %s tried %s", code, body, tried)
	}
}

// The account magpie had before it kept days is never held by the policy:
// the spare, at 70% of its five hours, takes the request first.
func TestGrowthLeavesAnOldAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	growthDefault(t)
	b := newResetBackend(t)
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountOrder("codex", []string{"spare@example.com", "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	growthUsage(t, map[string]float64{"me@example.com": 10, "spare@example.com": 70}, nil)
	code, body := resetPost(t, New())
	if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-2" {
		t.Fatalf("an old account at 70%%: %d %s tried %s", code, body, tried)
	}
}
