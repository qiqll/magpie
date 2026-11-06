package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

var t0 = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// placed writes routes as the gateway's own Route, one day's file, and
// reads them back as observe does: a renamed field in the gateway's JSON
// fails here.
func placed(t *testing.T, routes ...gateway.Route) *Placements {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "routing"), 0o700); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, r := range routes {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "routing", "2026-11-03.jsonl"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := observe(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return o.Placements
}

// seat is an account weighed with a week renewing in three days and five
// hours in two; fullest names which of them Used is of.
func wseat(id string, used float64, fullest string) gateway.Weighed {
	week, five := t0.Add(72*time.Hour), t0.Add(2*time.Hour)
	at := week
	if fullest == "5h" {
		at = five
	}
	return gateway.Weighed{ID: id, Who: id + "@example.com", Kind: "account", Known: true, Used: used, Renews: []time.Time{week, five}, UsedRenews: &at}
}

func ok(id string) gateway.Try { return gateway.Try{ID: id, Status: 200} }
func fail(id string, status int, why string) gateway.Try {
	return gateway.Try{ID: id, Status: status, Fail: why}
}

type rt struct {
	at      time.Duration
	session string
	why     string
	user    string
	order   []gateway.Weighed
	tries   []gateway.Try
	edit    func(*gateway.Route)
}

func (r rt) route() gateway.Route {
	g := gateway.Route{Time: t0.Add(r.at), Session: r.session, Model: "opus", Provider: "claude", Order: r.order, Tries: r.tries}
	if r.why != "-" {
		g.Affinity = &gateway.Affinity{Why: r.why}
	}
	if r.user != "" {
		g.User = &gateway.UserBound{User: r.user, Spares: []string{"b"}}
	}
	if r.edit != nil {
		r.edit(&g)
	}
	return g
}

func routesOf(rs ...rt) []gateway.Route {
	out := make([]gateway.Route, len(rs))
	for i, r := range rs {
		out[i] = r.route()
	}
	return out
}

func bucket(p *Placements, pool string, headroom float64) Bucket {
	for _, pl := range p.Pools {
		if pl.Name != pool {
			continue
		}
		for _, b := range pl.Buckets {
			if headroom >= b.From && headroom < b.To {
				return b
			}
		}
	}
	return Bucket{}
}

// A bound user's conversation put on an account with 12% of its five hours
// left while the user's other had 70% is refused there two requests on
// and moved: a forced stint in the bound pool's lowest bucket, with a
// roomier account beside it. A model switch, a title, a subagent's and a
// pinned request move nothing. An unbound conversation put on the roomy
// one ends there; an account at 95% isn't counted ready beside it.
func TestPlacements(t *testing.T) {
	sub := func(g *gateway.Route) { g.Subagent = &gateway.SubagentPick{} }
	p := placed(t, routesOf(
		rt{at: 0, session: "s1", why: "first", user: "u1", order: []gateway.Weighed{wseat("a", 88, "5h"), wseat("b", 30, "week")}, tries: []gateway.Try{ok("a")}},
		rt{at: time.Minute, session: "s1", why: "cache", user: "u1", order: []gateway.Weighed{wseat("a", 95, "5h"), wseat("b", 30, "week")}, tries: []gateway.Try{ok("a")}},
		rt{at: 70 * time.Second, session: "s1", why: "first", user: "u1", order: []gateway.Weighed{wseat("c", 0, "week")}, tries: []gateway.Try{ok("c")}, edit: func(g *gateway.Route) { g.Kind = "title" }},
		rt{at: 80 * time.Second, session: "s1", why: "first", user: "u1", order: []gateway.Weighed{wseat("c", 0, "week")}, tries: []gateway.Try{ok("c")}, edit: sub},
		rt{at: 90 * time.Second, session: "s1", why: "first", user: "u1", order: []gateway.Weighed{wseat("c", 0, "week")}, tries: []gateway.Try{ok("c")}, edit: func(g *gateway.Route) { g.Pinned = "c" }},
		rt{at: 2 * time.Minute, session: "s1", why: "cache", user: "u1", order: []gateway.Weighed{wseat("a", 99, "5h"), wseat("b", 30, "week")}, tries: []gateway.Try{fail("a", 429, "rate"), ok("b")}},
		rt{at: 3 * time.Minute, session: "s1", why: "cache", user: "u1", order: []gateway.Weighed{wseat("b", 32, "week"), wseat("a", 99, "5h")}, tries: []gateway.Try{ok("b")}, edit: func(g *gateway.Route) { g.Model = "sonnet" }},
		rt{at: 4 * time.Minute, session: "s2", why: "first", order: []gateway.Weighed{wseat("b", 32, "week"), {ID: "a", Known: true, Used: 99, Rest: &gateway.Rest{}}, wseat("d", 95, "week")}, tries: []gateway.Try{ok("b")}},
		rt{at: 5 * time.Minute, session: "s2", why: "cache", order: []gateway.Weighed{wseat("b", 40, "week")}, tries: []gateway.Try{ok("b")}},
	)...)
	if p.Convs != 2 || p.Stints != 3 || p.Started["first"] != 2 || p.Started["moved"] != 1 {
		t.Fatalf("placements %+v", p)
	}
	low := bucket(p, "bound", 12)
	if low.Stints != 1 || low.First != 1 || low.Forced != 1 || low.Ended["refused"] != 1 || low.FiveHours != 1 || low.Week != 0 || low.Roomier != 1 || low.Requests != 2 {
		t.Errorf("bound 10-25 %+v", low)
	}
	if b := bucket(p, "bound", 70); b.Stints != 1 || b.Ended["end"] != 1 || b.Week != 1 || b.Roomier != 0 {
		t.Errorf("bound 50-75 %+v", b)
	}
	if b := bucket(p, "unbound", 68); b.Stints != 1 || b.First != 1 || b.Ended["end"] != 1 {
		t.Errorf("unbound 50-75 %+v", b)
	}
	// s1's stint on b rose 30 → 32, s2's 32 → 40
	if p.RiseStints != 2 || p.Rise[0] != 2 || p.Rise[2] != 8 {
		t.Errorf("rise %v of %d", p.Rise, p.RiseStints)
	}
	// s1 began with two ready; s2 with one (a rests, d at 95%)
	if p.Ready != [2]float64{1, 2} || p.Users != 1 || p.Bound != [2]float64{2, 2} {
		t.Errorf("ready %v, users %d, bound %v", p.Ready, p.Users, p.Bound)
	}
}

// How a stint ends when it isn't the conversation's last, nor a refusal
// that moved it.
func TestPlacementEnds(t *testing.T) {
	a := func(used float64) []gateway.Weighed {
		return []gateway.Weighed{wseat("a", used, "week"), wseat("b", 10, "week")}
	}
	for _, c := range []struct {
		name  string
		rs    []rt
		ended string
	}{
		{"a refusal answered after is no refusal", []rt{
			{at: 0, session: "s", why: "first", order: a(20), tries: []gateway.Try{ok("a")}},
			{at: time.Minute, session: "s", why: "cache", order: a(21), tries: []gateway.Try{fail("a", 429, "rate")}},
			{at: 2 * time.Minute, session: "s", why: "cache", order: a(21), tries: []gateway.Try{ok("a")}},
			{at: time.Hour, session: "s", why: "cold", order: a(22), tries: []gateway.Try{ok("b")}},
		}, "cold"},
		{"refused to the end is stranded", []rt{
			{at: 0, session: "s", why: "first", order: a(20), tries: []gateway.Try{ok("a")}},
			{at: time.Minute, session: "s", why: "cache", order: a(99), tries: []gateway.Try{fail("a", 429, "quota")}},
		}, "stranded"},
		{"a 5xx failover is failed", []rt{
			{at: 0, session: "s", why: "first", order: a(20), tries: []gateway.Try{ok("a")}},
			{at: time.Minute, session: "s", why: "cache", order: a(21), tries: []gateway.Try{fail("a", 500, "other"), ok("b")}},
		}, "failed:other"},
		{"a forgotten conversation is evicted", []rt{
			{at: 0, session: "s", why: "first", order: a(20), tries: []gateway.Try{ok("a")}},
			{at: time.Minute, session: "s", why: "first", order: a(21), tries: []gateway.Try{ok("a")}},
		}, "first"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := placed(t, routesOf(c.rs...)...)
			if b := bucket(p, "unbound", 80); b.Ended[c.ended] != 1 {
				t.Errorf("ended %v, want %s", b.Ended, c.ended)
			}
			if c.ended == "first" && p.Started["evicted"] != 1 {
				t.Errorf("started %v, want one evicted", p.Started)
			}
		})
	}
}

// A week renewing within five hours is the week, not the five hours; use
// past 100 is the lowest bucket; Stays: Off is unkept; a stint whose
// fullest window's renewal moved saw a renewal and tells no rise.
func TestPlacementEdges(t *testing.T) {
	soon := t0.Add(3 * time.Hour)
	week := gateway.Weighed{ID: "a", Known: true, Used: 50, Renews: []time.Time{soon, t0.Add(4 * time.Hour)}, UsedRenews: &soon}
	later := t0.Add(4 * time.Hour)
	moved := gateway.Weighed{ID: "a", Known: true, Used: 55, Renews: []time.Time{t0.Add(170 * time.Hour), later}, UsedRenews: &later}
	p := placed(t, routesOf(
		rt{at: 0, session: "w", why: "first", order: []gateway.Weighed{week}, tries: []gateway.Try{ok("a")}},
		rt{at: time.Minute, session: "w", why: "cache", order: []gateway.Weighed{moved}, tries: []gateway.Try{ok("a")}},
		rt{at: 0, session: "o", why: "first", order: []gateway.Weighed{wseat("x", 105, "week")}, tries: []gateway.Try{ok("x")}},
		rt{at: 0, session: "f", why: "-", order: []gateway.Weighed{wseat("y", 10, "week")}, tries: []gateway.Try{ok("y")}},
	)...)
	if b := bucket(p, "unbound", 50); b.Week != 1 || b.FiveHours != 0 {
		t.Errorf("a week renewing in 3h: %+v", b)
	}
	if b := bucket(p, "unbound", 0); b.Stints != 1 {
		t.Errorf("used past 100: %+v", b)
	}
	if p.Started["unkept"] != 1 {
		t.Errorf("started %v", p.Started)
	}
	if p.RiseStints != 2 { // o and f; w renewed
		t.Errorf("rise of %d stints, want 2", p.RiseStints)
	}
}

// A day packed to .jsonl.gz beside its .jsonl is read once.
func TestPackedDayReadOnce(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "routing"), 0o700)
	line, _ := json.Marshal(rt{session: "s", why: "first", order: []gateway.Weighed{wseat("a", 10, "week")}, tries: []gateway.Try{ok("a")}}.route())
	os.WriteFile(filepath.Join(dir, "routing", "2026-11-03.jsonl"), append(line, '\n'), 0o600)
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	z.Write(append(line, '\n'))
	z.Close()
	os.WriteFile(filepath.Join(dir, "routing", "2026-11-03.jsonl.gz"), gz.Bytes(), 0o600)
	o, err := observe(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if o.Routes != 1 {
		t.Errorf("read %d routes, want 1", o.Routes)
	}
}

// A bound user's conversation on a whose binding then changes to b alone
// (a is capped and left out of the order) moves as "one · user gone ·
// binding changed · left unbound, left:capped"; a move off an account that rests,
// beside another, as "several · ... · resting"; one whose binding no
// longer names the account it was on, as "unbound". Requests nothing
// answered are told by their last try's fail, or "no try" and the
// user's binding why.
func TestMoveCauses(t *testing.T) {
	bind := func(why, first string, spares ...string) func(*gateway.Route) {
		return func(g *gateway.Route) {
			g.User = &gateway.UserBound{User: "u1", Account: first, Spares: spares, Why: why}
		}
	}
	capped := wseat("a", 100, "week")
	capped.Capped = 100
	resting := wseat("b", 20, "week")
	resting.Rest = &gateway.Rest{}
	p := placed(t, routesOf(
		rt{at: 0, session: "s1", why: "first", order: []gateway.Weighed{wseat("a", 50, "week"), wseat("b", 10, "week")}, tries: []gateway.Try{ok("a")}, edit: bind("bound", "a@example.com", "b@example.com")},
		rt{at: time.Minute, session: "s1", why: "-", order: []gateway.Weighed{wseat("b", 10, "week")}, tries: []gateway.Try{ok("b")}, edit: func(g *gateway.Route) {
			bind("gone", "b@example.com")(g)
			g.Left = []gateway.Weighed{capped}
		}},
		rt{at: 2 * time.Minute, session: "s1", why: "resting", order: []gateway.Weighed{wseat("c", 10, "week"), resting}, tries: []gateway.Try{ok("c")}, edit: bind("added", "b@example.com", "c@example.com")},
		rt{at: 3 * time.Minute, session: "s1", why: "cache", order: []gateway.Weighed{wseat("d", 10, "week")}, tries: []gateway.Try{ok("d")}, edit: bind("kept", "d@example.com")},
		rt{at: 4 * time.Minute, session: "s1", why: "cache", order: []gateway.Weighed{wseat("d", 99, "week")}, tries: []gateway.Try{fail("d", 429, "quota")}, edit: bind("kept", "d@example.com")},
		rt{at: 5 * time.Minute, session: "s1", why: "cache", order: []gateway.Weighed{wseat("d", 99, "week")}, edit: bind("full", "d@example.com")},
	)...)
	want := map[string]int{
		"one · user gone · binding changed · left unbound, left:capped": 1,
		"several · user added · binding changed · left resting":         1,
		"one · user kept · binding changed · left unbound":              1,
	}
	for k, n := range want {
		if p.MoveCauses[k] != n {
			t.Errorf("move causes %v, want %q", p.MoveCauses, k)
		}
	}
	if len(p.MoveCauses) != len(want) {
		t.Errorf("move causes %v", p.MoveCauses)
	}
	if p.UnansweredWhy["quota"] != 1 || p.UnansweredWhy["no try (user full)"] != 1 {
		t.Errorf("unanswered %v", p.UnansweredWhy)
	}
}

// u1, the heavy user, has a conversation put on a (60% used, capped at
// 80: 20 left) while b had 90 left: a tight pick. a reaches its cap
// three requests on and the conversation moves to b: a quota end. u1's
// other conversation has b alone ready: no pick, and b renews in it. u2, light, is put on c
// with d as roomy beside it (a loose pick), then the key holds c and it
// moves: an access end, not a quota one.
func TestUserPlacements(t *testing.T) {
	as := func(user string, tokens int, first string, spares ...string) func(*gateway.Route) {
		return func(g *gateway.Route) {
			g.User = &gateway.UserBound{User: user, Account: first, Spares: spares, Why: "kept"}
			g.Tokens = tokens
		}
	}
	u1 := as("u1@example.com", 100, "a@example.com", "b@example.com")
	capped := wseat("a", 80, "week")
	capped.Capped = 80
	held := wseat("c", 10, "week")
	held.Held = true
	two := func(a, b float64) []gateway.Weighed {
		return []gateway.Weighed{wseat("a", a, "week"), wseat("b", b, "week")}
	}
	b := func(used float64) []gateway.Weighed { return []gateway.Weighed{wseat("b", used, "week")} }
	p := placed(t, routesOf(
		rt{at: 0, session: "s1", why: "first", order: two(60, 10), tries: []gateway.Try{ok("a")}, edit: u1},
		rt{at: time.Minute, session: "s1", why: "cache", order: two(70, 10), tries: []gateway.Try{ok("a")}, edit: u1},
		rt{at: 2 * time.Minute, session: "s1", why: "cache", order: two(75, 10), tries: []gateway.Try{ok("a")}, edit: u1},
		rt{at: 3 * time.Minute, session: "s1", why: "-", order: b(10), tries: []gateway.Try{ok("b")}, edit: func(g *gateway.Route) { u1(g); g.Left = []gateway.Weighed{capped} }},
		rt{at: 4 * time.Minute, session: "s1", why: "cache", order: b(12), tries: []gateway.Try{ok("b")}, edit: u1},
		rt{at: 5 * time.Minute, session: "s2", why: "first", order: b(12), tries: []gateway.Try{ok("b")}, edit: func(g *gateway.Route) { u1(g); g.Left = []gateway.Weighed{capped} }},
		rt{at: 6 * time.Minute, session: "s2", why: "cache", order: b(13), tries: []gateway.Try{ok("b")}, edit: u1},
		rt{at: 7 * time.Minute, session: "s2", why: "cache", order: b(3), tries: []gateway.Try{ok("b")}, edit: u1}, // b renewed: s2 tells no cost
		rt{at: 0, session: "s3", why: "first", order: []gateway.Weighed{wseat("c", 10, "week"), wseat("d", 10, "week")}, tries: []gateway.Try{ok("c")}, edit: as("u2", 5, "c@example.com", "d@example.com")},
		rt{at: time.Minute, session: "s3", why: "-", order: []gateway.Weighed{wseat("d", 10, "week")}, tries: []gateway.Try{ok("d")}, edit: func(g *gateway.Route) {
			as("u2", 5, "c@example.com", "d@example.com")(g)
			g.Left = []gateway.Weighed{held}
		}},
	)...)
	// a's stint is told to its cap: 20 left, not 40
	if b := bucket(p, "bound", 20); b.Stints != 1 || b.Roomier != 1 {
		t.Errorf("bound 10-25 %+v", b)
	}
	u := p.ByUser
	if u == nil || len(u.Users) != 2 {
		t.Fatalf("by user %+v", u)
	}
	h, l := u.Users[0], u.Users[1]
	if h.Tier != "heavy" || h.Requests != 8 || h.Tokens != 800 || h.Picks != 1 || h.Tight != 1 || h.TightQuota != 1 || h.Ends["quota"] != 1 || h.Ends["end"] != 2 || h.Served != 3 {
		t.Errorf("heavy %+v", h)
	}
	// 15 + 2 points over 5 requests; s2 saw a renewal
	if near := func(x, y float64) bool { return x-y < 0.01 && y-x < 0.01 }; !near(h.PerRequest, 17.0/5) || !near(h.Had, 20/(17.0/5)) || !near(h.Could, 90/(17.0/5)) {
		t.Errorf("heavy per request %v, had %v, could %v", h.PerRequest, h.Had, h.Could)
	}
	if strings.Contains(h.User, "u1") || !strings.HasPrefix(h.User, "acct-") {
		t.Errorf("user named %q, want a hash", h.User)
	}
	if l.Tier != "light" || l.Ends["access"] != 1 || l.Ends["quota"] != 0 || l.Picks != 1 || l.Tight != 0 || l.LooseQuota != 0 {
		t.Errorf("light %+v", l)
	}
	if len(u.Tiers) != 2 || u.Tiers[0].Requests != 8 || u.Tiers[1].Requests != 2 || u.Tiers[0].TightQuota != 1 {
		t.Errorf("tiers %+v", u.Tiers)
	}
}
