package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// One of a user's accounts as the Gateway page tells it: resting as the
// rests say, a rest with no why still out; at its usage cap as its
// windows read; with a cap set and its windows not read, the cap
// unknown, never "it can"; with no cap set, nothing to read.
func TestUserSeatOut(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var codex provider.Provider
	for _, p := range provider.All() {
		if p.ID == "codex" {
			codex = p
		}
	}
	if codex.Account == nil {
		t.Fatal("no codex account")
	}
	if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	rests := map[string]Rest{"codex@me@example.com": {Until: until}}
	if out, found := UserSeatOut(codex, "codex@ME@example.com", rests); !found || out.Why != failOther || !out.Until.Equal(until) {
		t.Errorf("a rest with no why: %+v %v", out, found)
	}
	if _, found := UserSeatOut(codex, "codex@nobody@example.com", rests); found {
		t.Error("an account the provider hasn't found")
	}

	// windows not read: the cap set on it can't be told
	capUsage(t, map[string]float64{})
	if out, _ := UserSeatOut(codex, "codex@me@example.com", map[string]Rest{}); !out.CapUnknown || out.Why != "" {
		t.Errorf("a cap with its windows unread: %+v", out)
	}
	if out, _ := UserSeatOut(codex, "codex@spare@example.com", map[string]Rest{}); out.CapUnknown || out.Why != "" {
		t.Errorf("no cap set, nothing to read: %+v", out)
	}
	// read: at its cap, and under it
	capUsage(t, map[string]float64{"me@example.com": 75})
	if out, _ := UserSeatOut(codex, "codex@me@example.com", map[string]Rest{}); out.Why != "cap" || out.CapUnknown || out.Until.IsZero() {
		t.Errorf("at its cap: %+v", out)
	}
	capUsage(t, map[string]float64{"me@example.com": 20})
	if out, _ := UserSeatOut(codex, "codex@me@example.com", map[string]Rest{}); out.Why != "" || out.CapUnknown {
		t.Errorf("under its cap: %+v", out)
	}
}
