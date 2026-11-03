package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// byUseTiers is user-tiers.json with free two to an account and pro its
// own alone, by_use as on says ("" leaves it out).
func byUseTiers(t *testing.T, byUse string) {
	t.Helper()
	body := `{"default": "free", "tiers": {"free": {"max_accounts": 2, "share": 2}, "pro": {"max_accounts": 4, "share": 1}}`
	if byUse != "" {
		body += `, "by_use": ` + byUse
	}
	writeTiers(t, body+"}")
}

// weekUsed stands in for each Codex account's week as last read: the
// share used, renewing in an hour, so it began 7 days less an hour ago.
func weekUsed(t *testing.T, used map[string]float64) {
	t.Helper()
	old := allowances
	t.Cleanup(func() { allowances = old })
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, n := range used {
			out[u] = provider.Allowance{{Used: n, Resets: time.Now().Add(time.Hour), Span: 7 * 24 * time.Hour}}
		}
		return out
	}
}

// spread is tokens user sent codex on account, in ten calls from days
// ago to an hour ago.
func spread(user, account string, tokens int, days float64) []usage.Record {
	var out []usage.Record
	from := time.Now().Add(-time.Duration(days * 24 * float64(time.Hour)))
	step := (time.Since(from) - time.Hour) / 9
	for i := range 10 {
		out = append(out, usage.Record{Time: from.Add(time.Duration(i) * step), Provider: "codex", ProviderAccount: account, User: user, Model: "gpt-5.5", Input: tokens / 10, Status: 200})
	}
	return out
}

// usedLately stands in for the usage log as by_use last read it: recs,
// read now, looking back 7 days.
func usedLately(t *testing.T, recs ...[]usage.Record) {
	t.Helper()
	usedLatelyBack(t, 7, recs...)
}

// usedLatelyBack is usedLately looking back days.
func usedLatelyBack(t *testing.T, days int, recs ...[]usage.Record) {
	t.Helper()
	back := time.Duration(days) * 24 * time.Hour
	var all []usage.Record
	for _, r := range recs {
		all = append(all, r...)
	}
	now := time.Now()
	s := buildUseSnapshot(now, now.Add(-back), func(fn func(usage.Record)) {
		for _, r := range all {
			fn(r)
		}
	})
	useSnap.Lock()
	useSnap.s, useSnap.lookback = s, back
	useSnap.Unlock()
	t.Cleanup(func() {
		useSnap.Lock()
		useSnap.s, useSnap.lookback = nil, 0
		useSnap.Unlock()
	})
}

// bobOnMe binds bob, of free, to me@example.com, used a minute ago, and
// has him send 1.2M tokens through it over 6.5 days: me@'s week, 60%
// used, holds 2M, and bob's week is reckoned at ~1.29M of it, ~65%.
func bobOnMe(t *testing.T) []usage.Record {
	t.Helper()
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": bound("me@example.com", "free", ago(time.Minute))}}})
	return spread("bob", "me@example.com", 1_200_000, 6.5)
}

// A free user whose use, beside the one already on a shared account,
// would be past the account's week target is bound to the empty one
// instead; without by_use it shares bob's, as the most users are on it.
func TestByUseKeepsAHeavyUserOffASharedAccount(t *testing.T) {
	for _, on := range []bool{true, false} {
		name := "by_use off"
		if on {
			name = "by_use on"
		}
		t.Run(name, func(t *testing.T) {
			codexSignedIn(t, "b@example.com")
			asked, _ := usersUpstream(t, nil)
			if on {
				byUseTiers(t, `{"on": true}`)
			} else {
				byUseTiers(t, "")
			}
			weekUsed(t, map[string]float64{"me@example.com": 60, "b@example.com": 10})
			// alice's ~0.86M a week is ~43% of me@'s week: 108% with bob's
			usedLately(t, bobOnMe(t), spread("alice", "b@example.com", 800_000, 6.5))
			s := byUser(New())
			if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
				t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
			}
			asked(true)
			want := "me@example.com"
			if on {
				want = "b@example.com"
			}
			if a := seatsIn(t, "codex")["alice"]; len(a) != 1 || a[0] != want {
				t.Fatalf("alice on %v, want %s", a, want)
			}
		})
	}
}

// A light user shares the account by_use or not, and so does one with
// under a day of use whose 1/share fits; one with none whose 1/share
// doesn't is kept off it.
func TestByUseLetsALightUserShare(t *testing.T) {
	for _, c := range []struct {
		name  string
		alice []usage.Record
		want  string
	}{
		// ~0.22M a week, ~11%: 76% with bob's
		{"light", spread("alice", "b@example.com", 200_000, 6.5), "me@example.com"},
		// a few hours of use only: counted as half an account, 115%
		{"new", spread("alice", "b@example.com", 10_000_000, 0.2), "b@example.com"},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexSignedIn(t, "b@example.com")
			asked, _ := usersUpstream(t, nil)
			byUseTiers(t, `{"on": true}`)
			weekUsed(t, map[string]float64{"me@example.com": 60, "b@example.com": 10})
			usedLately(t, bobOnMe(t), c.alice)
			s := byUser(New())
			if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
				t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
			}
			asked(true)
			if a := seatsIn(t, "codex")["alice"]; len(a) != 1 || a[0] != c.want {
				t.Fatalf("alice on %v, want %s", a, c.want)
			}
		})
	}
}

// With the one account shared already and no room by use, a new free
// user is a 429 whose error says by_use kept it off, and nobody is
// asked; with the target raised past what they'd use, it shares it.
func TestByUseFullRefusesAndSaysWhy(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	byUseTiers(t, `{"on": true}`)
	weekUsed(t, map[string]float64{"me@example.com": 60})
	// alice's calls went out on an account not signed in here: her use
	// counts, me@'s week doesn't hold them
	usedLately(t, bobOnMe(t), spread("alice", "z@example.com", 800_000, 6.5))
	s := byUser(New())
	rec := tierPost(s, "alice", "free", "hi")
	if limit, _ := limitOf(t, rec); rec.Code != 429 || limit != "seats" {
		t.Fatalf("alice: %d %q %s", rec.Code, limit, rec.Body.String())
	}
	if why := refusedWhy(s); !strings.Contains(why, "no room by their users' use (by_use: me@example.com its 7d window would be 108% used") {
		t.Fatalf("refused: %s", why)
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
	if _, bound := seatsIn(t, "codex")["alice"]; bound {
		t.Fatal("alice bound")
	}
	byUseTiers(t, `{"on": true, "week_target": 120}`)
	if rec := tierPost(s, "alice", "free", "again"); rec.Code != 200 {
		t.Fatalf("alice, target 120: %d %s", rec.Code, rec.Body.String())
	}
	if a := seatsIn(t, "codex")["alice"]; len(a) != 1 || a[0] != "me@example.com" {
		t.Fatalf("alice on %v", a)
	}
}

// An account whose week can't be told (under 5% used) takes the median
// of its provider's others: a user kept off it as it would be off them.
func TestByUseUnknownAccountTakesTheOthersMedian(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	byUseTiers(t, `{"on": true}`)
	// b@ holds 2M (0.3M routed at 15%); me@'s 1% is too little to tell
	weekUsed(t, map[string]float64{"me@example.com": 1, "b@example.com": 15})
	usedLately(t, bobOnMe(t), spread("carol", "b@example.com", 300_000, 6.5), spread("alice", "z@example.com", 800_000, 6.5))
	s := byUser(New())
	if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if a := seatsIn(t, "codex")["alice"]; len(a) != 1 || a[0] != "b@example.com" {
		t.Fatalf("alice on %v", a)
	}
}

// A user's five hours is the 90th percentile of what it sent in any five
// hours of its history, not its average; its week its total scaled to a
// week; under a day of use, nothing.
func TestUseDemand(t *testing.T) {
	now := time.Now().Truncate(time.Hour).Add(30 * time.Minute)
	var recs []usage.Record
	for d := range 4 {
		// one burst a day: 50k in one hour, then nothing
		recs = append(recs, usage.Record{Time: now.Add(-time.Duration(d)*24*time.Hour - 2*time.Hour), Provider: "codex", User: "bob", Input: 50_000, Status: 200})
	}
	recs = append(recs, usage.Record{Time: now.Add(-3 * time.Hour), Provider: "codex", User: "new", Input: 1_000, Status: 200})
	s := buildUseSnapshot(now, now.Add(-7*24*time.Hour), func(fn func(usage.Record)) {
		for _, r := range recs {
			fn(r)
		}
	})
	five, ok := s.demand("codex", userName{sharedUsers, "bob"}, nil, 5*time.Hour)
	if !ok || five != 50_000 {
		t.Fatalf("five hours %v %v", five, ok)
	}
	week, ok := s.demand("codex", userName{sharedUsers, "bob"}, nil, 7*24*time.Hour)
	if history := 3*24*time.Hour + 2*time.Hour; !ok || int(week) != int(200_000*float64(7*24*time.Hour)/float64(history)) {
		t.Fatalf("week %v %v", week, ok)
	}
	if _, ok := s.demand("codex", userName{sharedUsers, "new"}, nil, 5*time.Hour); ok {
		t.Fatal("under a day of use reckoned")
	}
	if _, ok := s.demand("claude", userName{sharedUsers, "bob"}, nil, 5*time.Hour); ok {
		t.Fatal("another provider's use reckoned")
	}
}

// by_use is off without it, takes the defaults for what it leaves out,
// and refuses a value it can't.
func TestParseByUse(t *testing.T) {
	base := `{"default": "free", "tiers": {"free": {"max_accounts": 2, "share": 2}}`
	tiers, err := parseTiers([]byte(base + "}"))
	if err != nil || tiers.ByUse != (ByUse{}) {
		t.Fatalf("without: %+v %v", tiers.ByUse, err)
	}
	tiers, err = parseTiers([]byte(base + `, "by_use": {"on": true, "unit": "tokens"}}`))
	if want := (ByUse{On: true, LookbackDays: 7, WeekTarget: 90, FiveHourTarget: 100}); err != nil || tiers.ByUse != want {
		t.Fatalf("defaults: %+v %v", tiers.ByUse, err)
	}
	tiers, err = parseTiers([]byte(base + `, "by_use": {"lookback_days": 3, "week_target": 80, "five_hour_target": 150}}`))
	if want := (ByUse{LookbackDays: 3, WeekTarget: 80, FiveHourTarget: 150}); err != nil || tiers.ByUse != want {
		t.Fatalf("set: %+v %v", tiers.ByUse, err)
	}
	for _, bad := range []string{`{"lookback_days": 0}`, `{"lookback_days": 31}`, `{"week_target": -1}`, `{"five_hour_target": 0}`, `{"unit": "cost"}`, `{"on": "yes"}`} {
		if _, err := parseTiers([]byte(base + `, "by_use": ` + bad + "}")); err == nil {
			t.Fatalf("%s taken", bad)
		}
	}
}

// A user of a tier with no limit (share 0) on an account, with no use of
// its own yet, counts nothing toward it rather than a whole account: a
// free user still shares it.
func TestByUseUnlimitedTierCountsByUseOnly(t *testing.T) {
	codexSignedIn(t)
	asked, _ := usersUpstream(t, nil)
	writeTiers(t, `{"default": "team", "tiers": {"free": {"max_accounts": 2, "share": 2}, "team": {"max_accounts": 6, "share": 0}}, "by_use": {"on": true}}`)
	weekUsed(t, map[string]float64{"me@example.com": 60})
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@me@example.com", Account: "me@example.com", At: ago(time.Minute), Used: ago(time.Minute)}}}})
	// me@ holds 2M, routed for nobody's user; alice ~11% of it
	usedLately(t, spread("", "me@example.com", 1_200_000, 6.5), spread("alice", "z@example.com", 200_000, 6.5))
	s := byUser(New())
	if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if seats := seatsIn(t, "codex"); len(seats["alice"]) != 1 {
		t.Fatalf("seats %v", seats)
	}
}

// A user on two accounts has its use spread over them: bob's ~1.29M a
// week over two accounts that each hold 1M is ~65% of each, and alice's
// ~21% fits beside him; counted whole on each, he'd fill both.
func TestByUseSpreadsAUsersUseOverItsAccounts(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	byUseTiers(t, `{"on": true}`)
	weekUsed(t, map[string]float64{"me@example.com": 60, "b@example.com": 60})
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@me@example.com", Account: "me@example.com", At: ago(time.Minute), Tier: "free", Used: ago(time.Minute),
			More: []userSeat{{Who: "codex@b@example.com", Account: "b@example.com", At: ago(time.Minute)}}}}}})
	usedLately(t, spread("bob", "me@example.com", 600_000, 6.5), spread("bob", "b@example.com", 600_000, 6.5), spread("alice", "z@example.com", 200_000, 6.5))
	s := byUser(New())
	if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if a := seatsIn(t, "codex")["alice"]; len(a) != 1 {
		t.Fatalf("alice on %v", a)
	}
}

// An account whose allowance isn't read is unknown, never room: it takes
// its provider's others' median, and a heavy user goes to the empty one.
func TestByUseUnreadAccountIsNotRoom(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	byUseTiers(t, `{"on": true}`)
	// me@'s allowance not read; b@ holds 2M (0.3M routed at 15%)
	weekUsed(t, map[string]float64{"b@example.com": 15})
	usedLately(t, bobOnMe(t), spread("", "b@example.com", 300_000, 6.5), spread("alice", "z@example.com", 800_000, 6.5))
	s := byUser(New())
	if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if a := seatsIn(t, "codex")["alice"]; len(a) != 1 || a[0] != "b@example.com" {
		t.Fatalf("alice on %v", a)
	}
}

// With lookback_days under a week, an account's week is still told: its
// calls are read from the week's start, the users' over the lookback.
func TestByUseShortLookbackStillWeighsTheWeek(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	asked, _ := usersUpstream(t, nil)
	byUseTiers(t, `{"on": true, "lookback_days": 3}`)
	weekUsed(t, map[string]float64{"me@example.com": 60, "b@example.com": 10})
	usedLatelyBack(t, 3, bobOnMe(t), spread("alice", "z@example.com", 800_000, 6.5))
	s := byUser(New())
	if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	if a := seatsIn(t, "codex")["alice"]; len(a) != 1 || a[0] != "b@example.com" {
		t.Fatalf("alice on %v", a)
	}
}

// A free user whose account is out for hours is given no spare that has
// no room by use: the 429's error says every other account is taken or
// has none, by_use naming it.
func TestByUseKeepsASpareOff(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	out, asked := limitUpstream(t)
	byUseTiers(t, `{"on": true}`)
	weekUsed(t, map[string]float64{"me@example.com": 60, "b@example.com": 60})
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"alice": bound("me@example.com", "free", ago(time.Minute)),
		"bob":   {Who: "codex@b@example.com", Account: "b@example.com", At: ago(time.Minute), Tier: "free", Used: ago(time.Minute)}}}})
	// alice's ~1.72M a week over the two accounts she'd be on: ~43% of
	// b@'s 2M, past 90% beside bob's ~65%
	usedLately(t, spread("bob", "b@example.com", 1_200_000, 6.5), spread("alice", "me@example.com", 1_600_000, 6.5))
	s := byUser(New())
	if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	out(asked(true)[0], 7200)
	if rec := tierPost(s, "alice", "free", "spent"); rec.Code != 429 {
		t.Fatalf("alice out: %d %s", rec.Code, rec.Body.String())
	}
	asked(true)
	rec := tierPost(s, "alice", "free", "again")
	if why := refusedWhy(s); rec.Code != 429 || !strings.Contains(why, "by_use: b@example.com its 7d window") {
		t.Fatalf("alice again: %d %s; %s", rec.Code, rec.Body.String(), why)
	}
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked: %v", got)
	}
	if n := len(seatsIn(t, "codex")["alice"]); n != 1 {
		t.Fatalf("alice on %d accounts", n)
	}
}

// A user's use is counted by its group of gateway keys (T-092): bob of
// a key that doesn't share its users is another user, and his heavy use
// isn't the light bob's on me@, so alice shares me@ beside him. When the
// key shares its users, the two are one bob, his use is both (~124%
// with alice's), and alice is kept off me@.
func TestByUseCountsAUsersUseByItsGroup(t *testing.T) {
	for _, shares := range []bool{false, true} {
		name := "key apart"
		if shares {
			name = "key shares"
		}
		t.Run(name, func(t *testing.T) {
			codexSignedIn(t, "b@example.com")
			asked, _ := usersUpstream(t, nil)
			byUseTiers(t, `{"on": true}`)
			if shares {
				sharing(t, "k1")
			}
			// me@'s week, 15% used, holds 2M: the light bob's ~0.32M a
			// week is ~16% of it, alice's ~0.86M ~43%
			weekUsed(t, map[string]float64{"me@example.com": 15, "b@example.com": 10})
			writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
				"bob": bound("me@example.com", "free", ago(time.Minute))}}})
			heavy := spread("bob", "z@example.com", 1_200_000, 6.5)
			for i := range heavy {
				heavy[i].CallerKeyID = "k1"
			}
			usedLately(t, spread("bob", "me@example.com", 300_000, 6.5), heavy, spread("alice", "z@example.com", 800_000, 6.5))
			s := byUser(New())
			if rec := tierPost(s, "alice", "free", "hi"); rec.Code != 200 {
				t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
			}
			asked(true)
			want := "me@example.com"
			if shares {
				want = "b@example.com"
			}
			if a := seatsIn(t, "codex")["alice"]; len(a) != 1 || a[0] != want {
				t.Fatalf("alice on %v, want %s", a, want)
			}
		})
	}
}

// A user's demand takes in its calls under every key of its group, and
// none under a key of another.
func TestUseDemandByGroup(t *testing.T) {
	now := time.Now().Truncate(time.Hour).Add(30 * time.Minute)
	var recs []usage.Record
	for d := range 4 {
		at := now.Add(-time.Duration(d)*24*time.Hour - 2*time.Hour)
		recs = append(recs,
			usage.Record{Time: at, Provider: "codex", User: "bob", Input: 10_000, Status: 200},
			usage.Record{Time: at, Provider: "codex", User: "bob", CallerKeyID: "k1", Input: 20_000, Status: 200},
			usage.Record{Time: at, Provider: "codex", User: "bob", CallerKeyID: "k2", Input: 40_000, Status: 200})
	}
	s := buildUseSnapshot(now, now.Add(-7*24*time.Hour), func(fn func(usage.Record)) {
		for _, r := range recs {
			fn(r)
		}
	})
	sharers := map[string]bool{"k1": true}
	for _, c := range []struct {
		group string
		want  float64
	}{{sharedUsers, 30_000}, {"k2", 40_000}, {"k3", 0}} {
		got, ok := s.demand("codex", userName{c.group, "bob"}, sharers, 5*time.Hour)
		if c.want == 0 {
			if ok {
				t.Fatalf("%q: reckoned %v with no calls", c.group, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Fatalf("%q: five hours %v %v, want %v", c.group, got, ok, c.want)
		}
	}
}
