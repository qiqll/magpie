package gateway

import (
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// providerLowUse saves Settings' low use by provider, nil for none
// (T-279).
func providerLowUse(t *testing.T, m map[string]int) {
	t.Helper()
	s := settings.Load()
	s.ProviderLowUse = m
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// week is an account's 7 days as last read: the share used, begun ran
// ago; credits says it goes on past it on its credits.
type lowWeek struct {
	used    float64
	ran     time.Duration
	credits bool
}

// weeksRan stands in for each Codex account's week as last read.
func weeksRan(t *testing.T, weeks map[string]lowWeek) {
	t.Helper()
	old := allowances
	t.Cleanup(func() { allowances = old })
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, w := range weeks {
			out[u] = provider.Allowance{{Used: w.used, Resets: time.Now().Add(lowUseWeek - w.ran), Span: lowUseWeek, Credits: w.credits}}
		}
		return out
	}
}

// fourFree binds alice and bob, free, to me@example.com, and carol and
// dave to b@example.com, each used an hour ago: two to an account, the
// built-in free share, both full.
func fourFree(t *testing.T) {
	t.Helper()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": bound("me@example.com", "free", ago(time.Hour)),
		"bob":   bound("me@example.com", "free", ago(time.Hour)),
		"carol": bound("b@example.com", "free", ago(time.Hour)),
		"dave":  bound("b@example.com", "free", ago(time.Hour))}}})
}

// The owner's case (2026-11-25): codex's low use 10% a day. me@'s lowWeek
// ran 3 days and is 24% used, 8% a day: it takes a third free user. b@'s,
// 36%, 12% a day, doesn't; and once me@ has its third, a fourth free user
// is refused, however low its use: one more, never two.
func TestLowUseTierTakesOneFreeUserMore(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	fourFree(t)
	providerLowUse(t, map[string]int{"codex": 10})
	weeksRan(t, map[string]lowWeek{"me@example.com": {used: 24, ran: days(3)}, "b@example.com": {used: 36, ran: days(3)}})
	s := byUser(New())
	if rec := tierPost(s, "erin", "free", "hi"); rec.Code != 200 {
		t.Fatalf("erin: %d %s", rec.Code, rec.Body.String())
	}
	if got := seatsIn(t, "codex")["erin"]; !slices.Equal(got, []string{"me@example.com"}) {
		t.Fatalf("erin on %v, not the account of low use", got)
	}
	asked(true)
	weeksRan(t, map[string]lowWeek{"me@example.com": {used: 3, ran: days(3)}, "b@example.com": {used: 36, ran: days(3)}})
	rec := tierPost(s, "frank", "free", "hi")
	if limit, _ := limitOf(t, rec); rec.Code != 429 || limit != "seats" {
		t.Fatalf("a fourth on me@: %d %q %s", rec.Code, limit, rec.Body.String())
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
}

// An account takes no user more when its week's use isn't told a day: not
// a day old, on credits, with no low use set, or settings naming another
// provider, or with its use at the low use exactly.
func TestLowUseTierUntoldTakesNoneMore(t *testing.T) {
	for name, c := range map[string]struct {
		low  map[string]int
		week lowWeek
	}{
		"under a day":    {map[string]int{"codex": 10}, lowWeek{used: 1, ran: 20 * time.Hour}},
		"on credits":     {map[string]int{"codex": 10}, lowWeek{used: 1, ran: days(3), credits: true}},
		"none set":       {nil, lowWeek{used: 1, ran: days(3)}},
		"another's":      {map[string]int{"claude": 10}, lowWeek{used: 1, ran: days(3)}},
		"at it":          {map[string]int{"codex": 10}, lowWeek{used: 30, ran: days(3)}},
		"its week ended": {map[string]int{"codex": 10}, lowWeek{used: 1, ran: lowUseWeek + time.Hour}},
	} {
		t.Run(name, func(t *testing.T) {
			codexSignedIn(t, "b@example.com")
			asked, _ := usersUpstream(t, nil)
			fourFree(t)
			providerLowUse(t, c.low)
			weeksRan(t, map[string]lowWeek{"me@example.com": c.week, "b@example.com": c.week})
			s := byUser(New())
			rec := tierPost(s, "erin", "free", "hi")
			if limit, _ := limitOf(t, rec); rec.Code != 429 || limit != "seats" {
				t.Fatalf("%d %q %s", rec.Code, limit, rec.Body.String())
			}
			if got := asked(true); len(got) != 0 {
				t.Fatalf("asked: %v", got)
			}
		})
	}
}

// An account whose week isn't read takes none more.
func TestLowUseTierUnreadTakesNoneMore(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	fourFree(t)
	providerLowUse(t, map[string]int{"codex": 10})
	weeksRan(t, map[string]lowWeek{"b@example.com": {used: 36, ran: days(3)}})
	s := byUser(New())
	if rec := tierPost(s, "erin", "free", "hi"); rec.Code != 429 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	asked(true)
}

// Only free takes one more: plus, shared two to an account in Settings,
// takes no third on an account of low use; and a free share of 1 or 0
// isn't raised (oneMore).
func TestLowUseTierIsFreeOnly(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	providerShares(t, map[string]map[string]int{"codex": {"plus": 2}})
	providerLowUse(t, map[string]int{"codex": 10})
	weeksRan(t, map[string]lowWeek{"me@example.com": {used: 3, ran: days(3)}})
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"p1": bound("me@example.com", "plus", ago(time.Hour)),
		"p2": bound("me@example.com", "plus", ago(time.Hour))}}})
	s := byUser(New())
	rec := tierPost(s, "p3", "plus", "hi")
	if limit, _ := limitOf(t, rec); rec.Code != 429 || limit != "seats" {
		t.Fatalf("a third plus user: %d %q %s", rec.Code, limit, rec.Body.String())
	}
	asked(true)
	for share, want := range map[int]int{0: 0, 1: 1, 2: 3, 10: 11} {
		if got := oneMore(share); got != want {
			t.Fatalf("oneMore(%d) %d, not %d", share, got, want)
		}
	}
}

// The seat isn't taken back: me@'s week renews, or its use rises past
// the low use, and its three free users are still served there, none
// moved; a fourth is refused.
func TestLowUseSeatKeptPastItsWeek(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": bound("me@example.com", "free", ago(time.Hour)),
		"bob":   bound("me@example.com", "free", ago(time.Hour)),
		"erin":  bound("me@example.com", "free", ago(time.Hour)),
		"carol": bound("b@example.com", "free", ago(time.Hour)),
		"dave":  bound("b@example.com", "free", ago(time.Hour))}}})
	providerLowUse(t, map[string]int{"codex": 10})
	for _, w := range []lowWeek{{used: 1, ran: time.Hour}, {used: 60, ran: days(3)}} {
		weeksRan(t, map[string]lowWeek{"me@example.com": w, "b@example.com": {used: 36, ran: days(3)}})
		s := byUser(New())
		for _, u := range []string{"alice", "bob", "erin"} {
			if rec := tierPost(s, u, "free", "hi"); rec.Code != 200 {
				t.Fatalf("%v %s: %d %s", w, u, rec.Code, rec.Body.String())
			}
			if got := asked(true); !slices.Equal(got, []string{"acct-1"}) {
				t.Fatalf("%v %s asked %v", w, u, got)
			}
		}
		if got := seatsIn(t, "codex"); !slices.Equal(got["erin"], []string{"me@example.com"}) {
			t.Fatalf("%v: %v", w, got)
		}
		if rec := tierPost(s, "frank", "free", "hi"); rec.Code != 429 {
			t.Fatalf("%v frank: %d %s", w, rec.Code, rec.Body.String())
		}
	}
}

// A seat a user lets go of as idle is taken again by the account's use
// then: still low, a new free user is its third; past it, the account
// keeps its two.
func TestLowUseSeatJudgedAgainWhenIdleLetsGo(t *testing.T) {
	for _, low := range []bool{true, false} {
		codexSignedIn(t, "b@example.com")
		asked, _ := usersUpstream(t, nil)
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"alice": bound("me@example.com", "free", ago(time.Hour)),
			"bob":   bound("me@example.com", "free", ago(time.Hour)),
			"erin":  bound("me@example.com", "free", ago(userIdle+time.Hour)),
			"carol": bound("b@example.com", "free", ago(time.Hour)),
			"dave":  bound("b@example.com", "free", ago(time.Hour))}}})
		forgetSeen()
		providerLowUse(t, map[string]int{"codex": 10})
		me := lowWeek{used: 60, ran: days(3)}
		if low {
			me.used = 24
		}
		weeksRan(t, map[string]lowWeek{"me@example.com": me, "b@example.com": {used: 36, ran: days(3)}})
		s := byUser(New())
		rec := tierPost(s, "frank", "free", "hi")
		seats := seatsIn(t, "codex")
		if _, kept := seats["erin"]; kept {
			t.Fatalf("low %v: erin idle kept: %v", low, seats)
		}
		if low && (rec.Code != 200 || !slices.Equal(seats["frank"], []string{"me@example.com"})) {
			t.Fatalf("still low: %d %v %s", rec.Code, seats, rec.Body.String())
		}
		if !low {
			if limit, _ := limitOf(t, rec); rec.Code != 429 || limit != "seats" || seats["frank"] != nil {
				t.Fatalf("past it: %d %q %v %s", rec.Code, limit, seats, rec.Body.String())
			}
		}
		asked(true)
	}
}

// lowUseOf reads each account's week: 8% a day under 10 is low, 12 isn't;
// of two weeks of every model, the busier; a week of one model alone
// isn't one.
func TestLowUseOfReadsTheWeek(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com")
	providerLowUse(t, map[string]int{"codex": 10})
	now := time.Now()
	old := allowances
	t.Cleanup(func() { allowances = old })
	w := func(used float64, model string) provider.Limit {
		return provider.Limit{Used: used, Resets: now.Add(days(4)), Span: lowUseWeek, Model: model}
	}
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{
			"me@example.com": {w(24, ""), {Used: 90, Resets: now.Add(time.Hour), Span: 5 * time.Hour}, w(90, "opus")},
			"b@example.com":  {w(36, "")},
			"c@example.com":  {w(24, ""), w(33, "")},
		}
	}
	var cands []candidate
	for _, p := range provider.All() {
		if p.ID == "codex" {
			cands = append(cands, seatCandidates(p)...)
		}
	}
	got := lowUseOf("codex", cands, now)
	if got.below != 10 || len(got.daily) != 1 || int(got.daily["codex@me@example.com"]+0.5) != 8 {
		t.Fatalf("%+v", got)
	}
}
