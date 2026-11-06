package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// heavyUsage stands in for the accounts' windows as last read: each
// user's week at the share given; a user not listed isn't known.
func heavyUsage(t *testing.T, used map[string]float64) {
	t.Helper()
	old := allowances
	t.Cleanup(func() { allowances = old })
	week := time.Now().Add(4 * 24 * time.Hour)
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, n := range used {
			out[u] = provider.Allowance{{Name: "7 days", Used: n, Resets: week, Span: 7 * 24 * time.Hour}}
		}
		return out
	}
}

func heavyAcct(user string) candidate {
	return candidate{p: provider.Provider{ID: "heavy", Account: &provider.Account{Agent: "claude", User: user}}, model: "m", rest: "heavy:" + user}
}

func forgetHeavySticks(t *testing.T, prefix string) {
	t.Cleanup(func() {
		sticks.Lock()
		defer sticks.Unlock()
		for k := range sticks.m {
			if strings.HasPrefix(k, prefix) {
				delete(sticks.m, k)
			}
		}
		for k := range sticks.opening {
			if strings.HasPrefix(k, prefix) {
				delete(sticks.opening, k)
			}
		}
	})
}

// A heavy user's new conversation isn't put on its first account with 15
// points left (85% of its week) while another has 90: it goes where it
// fits. A user who isn't heavy is spread as before, to the first; so is a
// heavy one whose first has room, or none of whose others is known to.
func TestHeavyUserConversationGoesWhereItFits(t *testing.T) {
	forgetHeavySticks(t, "heavy|")
	a, b, c := heavyAcct("a@example.com"), heavyAcct("b@example.com"), heavyAcct("c@example.com")
	order := planned{order: []Weighed{{Known: true, Used: 85}, {Known: true, Used: 10}}}
	for _, tc := range []struct {
		name  string
		used  map[string]float64
		cs    []candidate
		heavy bool
		want  string
	}{
		{"heavy, first won't fit", map[string]float64{"a@example.com": 85, "b@example.com": 10}, []candidate{a, b}, true, b.who()},
		{"not heavy", map[string]float64{"a@example.com": 85, "b@example.com": 10}, []candidate{a, b}, false, a.who()},
		{"heavy, first fits", map[string]float64{"a@example.com": 70, "b@example.com": 10}, []candidate{a, b}, true, a.who()},
		{"heavy, the other not known", map[string]float64{"a@example.com": 85}, []candidate{a, c}, true, a.who()},
		{"heavy, the other won't fit either", map[string]float64{"a@example.com": 85, "b@example.com": 88}, []candidate{a, b}, true, a.who()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			heavyUsage(t, tc.used)
			// a scope of its own: each placement is held from then on
			scope := "heavy|" + strings.ReplaceAll(tc.name, " ", "-")
			if got, _ := ownLeastHeld(scope, scope+"|new", tc.cs, order, tc.heavy); got[0].who() != tc.want {
				t.Errorf("went to %s, want %s", got[0].who(), tc.want)
			}
		})
	}
}

// What an account has is told to the usage cap the user set: 60% of a
// week capped at 75 leaves 15, so a heavy user's conversation goes on.
func TestHeavyUserFitReadsTheCap(t *testing.T) {
	forgetHeavySticks(t, "heavy|")
	a, b := heavyAcct("a@example.com"), heavyAcct("b@example.com")
	a.p.AccountCaps = map[string]int{"a@example.com": 75}
	heavyUsage(t, map[string]float64{"a@example.com": 60, "b@example.com": 10})
	order := planned{order: []Weighed{{Known: true, Used: 60}, {Known: true, Used: 10}}}
	if got, _ := ownLeastHeld("heavy", "heavy|capped", []candidate{a, b}, order, true); got[0].who() != b.who() {
		t.Errorf("went to %s, want %s", got[0].who(), b.who())
	}
	a.p.AccountCaps = nil
	if got, _ := ownLeastHeld("heavy", "heavy|uncapped", []candidate{a, b}, order, true); got[0].who() != a.who() {
		t.Errorf("uncapped: went to %s, want %s", got[0].who(), a.who())
	}
}

// Of those it fits, the one the fewest conversations are held on.
func TestHeavyUserFitKeepsSpreading(t *testing.T) {
	forgetHeavySticks(t, "heavy|")
	a, b, c := heavyAcct("a@example.com"), heavyAcct("b@example.com"), heavyAcct("c@example.com")
	heavyUsage(t, map[string]float64{"a@example.com": 85, "b@example.com": 10, "c@example.com": 20})
	sticks.Lock()
	sticks.m["heavy|other"] = stick{rest: b.rest, who: b.who(), model: "m", at: time.Now()}
	sticks.Unlock()
	order := planned{order: []Weighed{{Known: true, Used: 85}, {Known: true, Used: 10}, {Known: true, Used: 20}}}
	if got, _ := ownLeastHeld("heavy", "heavy|new", []candidate{a, b, c}, order, true); got[0].who() != c.who() {
		t.Errorf("went to %s, want %s", got[0].who(), c.who())
	}
}

func TestUserBoundHeavy(t *testing.T) {
	for _, tc := range []struct {
		u    *UserBound
		want bool
	}{
		{nil, false},
		{&UserBound{User: "u"}, false},
		{&UserBound{User: "u", Account: "a", Spares: []string{"b", "c"}}, false},
		{&UserBound{User: "u", Account: "a", Spares: []string{"b", "c", "d"}}, true},
	} {
		if got := tc.u.heavy(); got != tc.want {
			t.Errorf("%+v heavy %v, want %v", tc.u, got, tc.want)
		}
	}
}

// Of those it fits, where the conversation was answered goes first on a
// tie (T-049): a conversation routed afresh each turn isn't moved between
// two the same for nothing.
func TestHeavyUserFitKeepsWhereItWas(t *testing.T) {
	forgetHeavySticks(t, "heavyw|")
	a, z, y := heavyAcct("a@example.com"), heavyAcct("z@example.com"), heavyAcct("y@example.com")
	heavyUsage(t, map[string]float64{"a@example.com": 85, "z@example.com": 10, "y@example.com": 10})
	now := time.Now()
	sticks.Lock()
	sticks.m["heavyw|1"] = stick{rest: z.rest, who: z.who(), model: "m", at: now}
	sticks.m["heavyw|2"] = stick{rest: y.rest, who: y.who(), model: "m", at: now}
	sticks.m["heavyw|me"] = stick{rest: y.rest, who: y.who(), model: "m", at: now}
	sticks.Unlock()
	order := planned{order: []Weighed{{Known: true, Used: 85}, {Known: true, Used: 10}, {Known: true, Used: 10}}}
	if got, _ := ownLeastHeld("heavyw", "heavyw|me", []candidate{a, z, y}, order, true); got[0].who() != y.who() {
		t.Errorf("went to %s, want %s, where it was", got[0].who(), y.who())
	}
}

// A heavy user's account picked whose allowance isn't known stays, though
// another is known to fit.
func TestHeavyUserUnknownPickStays(t *testing.T) {
	forgetHeavySticks(t, "heavyu|")
	a, b := heavyAcct("a@example.com"), heavyAcct("b@example.com")
	heavyUsage(t, map[string]float64{"b@example.com": 10})
	order := planned{order: []Weighed{{}, {Known: true, Used: 10}}}
	if got, _ := ownLeastHeld("heavyu", "heavyu|new", []candidate{a, b}, order, true); got[0].who() != a.who() {
		t.Errorf("went to %s, want %s", got[0].who(), a.who())
	}
}

// A group judges heavy by the user's binding at the member's provider:
// bound to two of one member's accounts and four of another's, the user
// is heavy at the second alone, whatever was narrowed first.
func TestHeavyUserGroupByTheMembersBinding(t *testing.T) {
	two := &UserBound{User: "u", Account: "a", Spares: []string{"b"}}
	four := &UserBound{User: "u", Account: "c", Spares: []string{"d", "e", "f"}}
	g := &groupTurn{ub: two, us: map[string]*userTry{"claude": {ub: two}, "codex": {ub: four}}}
	if g.heavyAt("claude") || !g.heavyAt("codex") || g.heavyAt("other") {
		t.Errorf("claude %v, codex %v, other %v; want false, true, false", g.heavyAt("claude"), g.heavyAt("codex"), g.heavyAt("other"))
	}
	var none *groupTurn
	if none.heavyAt("codex") {
		t.Error("no group turn is heavy")
	}
}
