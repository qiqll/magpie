package main

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func week(first Duration, used float64) WindowSpec {
	return WindowSpec{Span: 7 * d, Cap: 100, First: first, Used: used}
}

func pool(accts ...AccountSpec) Scenario {
	return Scenario{Name: "t", Days: 10, Accounts: accts, Demand: Demand{PerHour: []float64{0}, Turns: 1, TurnCost: 1, Gap: Duration(time.Minute), Context: 1, Rewrite: 1}}
}

func at(m time.Duration) time.Time { return start.Add(time.Hour + m) }

const s0cold = 5 * time.Minute

func TestDemandIsDrawnFromItsSeed(t *testing.T) {
	sc := Presets["staggered"]
	a, na := demand(sc, 7)
	b, nb := demand(sc, 7)
	c, _ := demand(sc, 8)
	if na != nb || !slices.Equal(a, b) {
		t.Fatal("one seed drew two demands")
	}
	if slices.Equal(a, c) {
		t.Fatal("two seeds drew one demand")
	}
	if !slices.IsSortedFunc(a, func(x, y turn) int { return x.at.Compare(y.at) }) {
		t.Fatal("turns not in time order")
	}
}

// Smart's first pick is the account whose week renews soonest, from seats
// weighed as weighRouted weighs them; Pace's is the one with the most
// left an hour.
func TestFirstTurnGoesWhereTheRoutingSays(t *testing.T) {
	sc := pool(
		AccountSpec{Name: "late", Windows: []WindowSpec{week(5*d, 0)}},
		AccountSpec{Name: "soon", Windows: []WindowSpec{week(1*d, 80)}},
	)
	turns := []turn{{at: at(0), cost: 1, last: true}}
	for routing, want := range map[string]string{"smart": "soon", "pace": "late", "order": "late"} {
		s, err := Run(sc, turns, 1, Options{Routing: routing})
		if err != nil {
			t.Fatal(err)
		}
		if s.PerAccount[want] != 1 {
			t.Errorf("%s answered on %v, want %s", routing, s.PerAccount, want)
		}
	}
}

// A try into a used up five hours is refused and rests the account until
// they renew, so the next turn goes to another first, though the reading
// routing weighs (an hour old: a vendor read by polling) still says it has
// room. Read live, an account used up is put behind by its share alone.
func TestUsedUpFiveHoursRestTheAccount(t *testing.T) {
	sc := pool(
		AccountSpec{Name: "a", Windows: []WindowSpec{{Span: 5 * h, Cap: 1, OnUse: true}, week(1*d, 0)}},
		AccountSpec{Name: "b", Windows: []WindowSpec{{Span: 5 * h, Cap: 2, OnUse: true}, week(5*d, 0)}},
	)
	turns := []turn{
		{at: at(0), conv: 0, cost: 1, last: true},
		{at: at(time.Minute), conv: 1, cost: 1, last: true},
		{at: at(2 * time.Minute), conv: 2, cost: 1, last: true},
		{at: at(6 * time.Hour), conv: 3, cost: 1, last: true},
		{at: at(6*time.Hour + time.Minute), conv: 4, cost: 1, last: true},
	}
	s, err := Run(sc, turns, 5, Options{Lag: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// conv 0 on a; conv 1 refused by a (rests), on b; conv 2 on b, a
	// resting; conv 3 after both renewed, on a; conv 4 refused by a again,
	// on b
	if s.PerAccount["a"] != 2 || s.PerAccount["b"] != 3 || s.Failed != 0 {
		t.Errorf("answered %v, failed %d; want a 2, b 3, none failed", s.PerAccount, s.Failed)
	}
	if s.Refusals != 2 || s.CapHits != 2 {
		t.Errorf("refusals %d, five-hour %d, want 2 and 2", s.Refusals, s.CapHits)
	}
}

// Stays: Auto keeps a conversation on its account while its cache is
// warm, and routes it afresh once it is cold; Stays: Session keeps it.
func TestConversationStaysWhileWarm(t *testing.T) {
	// a's week renews first, its five hours at 80%: its first turn takes
	// them to 90%, so Smart puts it behind b for whatever comes next (a
	// week at 90% alone wouldn't: Seat.WeekLow)
	sc := pool(
		AccountSpec{Name: "a", Windows: []WindowSpec{{Span: 5 * h, Cap: 10, First: 3 * h, Used: 80}, week(1*d, 0)}},
		AccountSpec{Name: "b", Windows: []WindowSpec{week(5*d, 0)}},
	)
	for _, c := range []struct {
		name  string
		gap   time.Duration
		stays string
		why   string
		moves int
	}{
		{"warm", 2 * time.Minute, "", "cache", 0},
		{"cold", 10 * time.Minute, "", "cold", 1},
		{"session", 10 * time.Minute, "session", "session", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			turns := []turn{{at: at(0), cost: 1}, {at: at(c.gap), n: 1, cost: 1, last: true}}
			s, err := Run(sc, turns, 1, Options{Stays: c.stays})
			if err != nil {
				t.Fatal(err)
			}
			if s.Why[c.why] != 1 || s.Moves != c.moves {
				t.Errorf("why %v, moves %d; want %s and %d", s.Why, s.Moves, c.why, c.moves)
			}
			// kept warm nothing is written again; cold, its one turn of
			// context is, moved or kept (Session keeps it on a cold cache)
			want := 0.0
			if c.gap > s0cold {
				want = 1
			}
			if s.Rewrite != want {
				t.Errorf("rewrite %v, want %v", s.Rewrite, want)
			}
		})
	}
}

// What a week had left is counted at each renewal, a week renewing unused
// as all of it.
func TestWeekLeftIsCountedAtRenewal(t *testing.T) {
	sc := pool(AccountSpec{Name: "a", Windows: []WindowSpec{week(1*d, 30)}})
	turns := []turn{{at: start.Add(9 * 24 * time.Hour), cost: 1, last: true}}
	s, err := Run(sc, turns, 1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.WeekRenewals != 2 || s.WeekLeft != 70+100 {
		t.Errorf("renewals %d, left %v; want 2 and 170", s.WeekRenewals, s.WeekLeft)
	}
}

// Stays: Session spreads new conversations over the accounts the fewest
// are kept on (leastHeld), where Auto puts each on routing's first.
func TestSessionSpreadsNewConversations(t *testing.T) {
	sc := pool(
		AccountSpec{Name: "a", Windows: []WindowSpec{week(1*d, 0)}},
		AccountSpec{Name: "b", Windows: []WindowSpec{week(5*d, 0)}},
	)
	turns := []turn{{at: at(0), conv: 0, cost: 1}, {at: at(time.Second), conv: 1, cost: 1}}
	for stays, want := range map[string]float64{"": 2, "session": 1} {
		s, err := Run(sc, turns, 2, Options{Stays: stays})
		if err != nil {
			t.Fatal(err)
		}
		if s.PerAccount["a"] != want {
			t.Errorf("stays %q: a answered %v, want %v", stays, s.PerAccount["a"], want)
		}
	}
}

func TestScenarioFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(path, []byte(`{"name":"f","days":2,"cold":"1h","accounts":[{"name":"a","windows":[{"span":"5h","cap":100,"onUse":true},{"span":"7d","cap":900,"first":"2d"}]}],"demand":{"perHour":[1],"turns":3,"gap":"90s","turnCost":0.5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadScenario(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.cold() != time.Hour || time.Duration(s.Accounts[0].Windows[1].First) != 48*time.Hour {
		t.Fatalf("read %+v", s)
	}
	for _, bad := range []string{
		`{"name":"x","days":0,"accounts":[],"demand":{}}`,
		`{"name":"x","days":1,"accounts":[{"name":"a","windows":[{"span":"5h","cap":0}]}],"demand":{"perHour":[1],"turns":1,"gap":"1m","turnCost":1}}`,
		`{"name":"x","days":1,"accounts":[{"name":"a","windows":[{"span":"5h","cap":1}]}],"demand":{"perHour":[1,2],"turns":1,"gap":"1m","turnCost":1}}`,
	} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadScenario(path); err == nil {
			t.Errorf("took %s", bad)
		}
	}
}

func TestEveryPresetRuns(t *testing.T) {
	for _, n := range presetNames() {
		sc := Presets[n]
		if err := sc.check(); err != nil {
			t.Fatal(err)
		}
		sc.Days = 3
		res, err := Compare(sc, []string{"smart", "pace", "usage", "order", "drain"}, []string{"", "session"}, 1, 1, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if s := r.Stats[0]; s.Turns == 0 {
				t.Errorf("%s %s/%s routed nothing", n, s.Routing, s.Stays)
			}
		}
	}
	if _, err := Compare(Presets["burst"], []string{"rotate"}, []string{""}, 1, 1, 0); err == nil {
		t.Error("an unknown routing ran")
	}
}

func TestObserve(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Weekly: 40% left just before it renewed, then a cycle whose last
	// reading was two days before its renewal (stale). Five hours: a
	// rolling window whose reset moves on with each reading, renewing
	// never.
	write("quota-history.json", `{"claude|Me@Example.com":{"Weekly":[
		{"at":"2026-11-01T10:00:00Z","left":60,"start":"2026-10-27T12:00:00Z","resetsAt":"2026-11-03T12:00:00Z"},
		{"at":"2026-11-03T11:30:00Z","left":40,"start":"2026-10-27T12:00:00Z","resetsAt":"2026-11-03T12:00:00Z"},
		{"at":"2026-11-03T13:00:00Z","left":100,"start":"2026-11-03T12:00:00Z","resetsAt":"2026-11-10T12:00:00Z"},
		{"at":"2026-11-08T12:00:00Z","left":70,"start":"2026-11-03T12:00:00Z","resetsAt":"2026-11-10T12:00:00Z"},
		{"at":"2026-11-10T13:00:00Z","left":100,"start":"2026-11-10T12:00:00Z","resetsAt":"2026-11-17T12:00:00Z"}],
	"Rolling":[
		{"at":"2026-11-03T10:00:00Z","left":90,"resetsAt":"2026-11-03T15:00:00Z"},
		{"at":"2026-11-03T10:02:00Z","left":89,"resetsAt":"2026-11-03T15:02:00Z"},
		{"at":"2026-11-03T10:04:00Z","left":88,"resetsAt":"2026-11-03T15:04:00Z"}]}}`)
	write("usage.jsonl", strings.Join([]string{
		`{"t":"2026-11-03T10:00:00Z","provider":"claude","providerAccount":"x","session":"s1","model":"opus","in":100,"out":50}`,
		`{"t":"2026-11-03T10:01:00Z","provider":"claude","providerAccount":"x","session":"s1","model":"opus","in":100,"out":50}`,
		// a side request and a subagent's turn on another account: no move
		`{"t":"2026-11-03T10:01:30Z","provider":"claude","providerAccount":"y","session":"s1","model":"haiku","kind":"title"}`,
		`{"t":"2026-11-03T10:01:40Z","provider":"claude","providerAccount":"y","session":"s1","model":"opus","subagent":"a1"}`,
		`{"t":"2026-11-03T10:20:00Z","provider":"claude","providerAccount":"y","session":"s1","model":"opus","in":100,"out":50,"cache_write":5000}`,
		`{"t":"2026-11-03T10:30:00Z","provider":"claude","providerAccount":"y","session":"s2","model":"opus","in":10}`,
		`{"t":"2026-11-03T10:31:00Z","provider":"claude","providerAccount":"z","session":"s2","model":"opus","status":429,"err":"rate limited"}`,
		`not json`,
	}, "\n"))
	write("routing/2026-11-03.jsonl", `{"affinity":{"why":"cache"}}`+"\n"+`{"affinity":{"why":"cold"}}`+"\n"+`{}`)
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	z.Write([]byte(`{"affinity":{"why":"cache"}}` + "\n"))
	z.Close()
	write("routing/2026-11-02.jsonl.gz", gz.String())
	write("routing/2026-11-01.jsonl.gz", "not gzip")

	o, err := observe(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Windows) != 2 {
		t.Fatalf("windows %+v", o.Windows)
	}
	roll, w := o.Windows[0], o.Windows[1]
	if roll.Window != "Rolling" || roll.Cycles != 0 {
		t.Errorf("a rolling window renewed: %+v", roll)
	}
	if w.Cycles != 2 || w.Known != 1 || w.Stale != 1 || w.MeanLeft() != 40 || w.LastLeft != 100 || w.Span != 7*24*time.Hour {
		t.Errorf("window %+v", w)
	}
	if strings.Contains(w.Account, "example") || !strings.HasPrefix(w.Account, "acct-") || w.Account != roll.Account {
		t.Errorf("accounts named %q and %q, want one hash", w.Account, roll.Account)
	}
	if again, _ := observe(dir, false); again.Windows[0].Account == w.Account {
		t.Errorf("two runs named the account alike (%q): the hash isn't salted", w.Account)
	}
	if len(o.Moves) != 1 || o.Moves[0] != (MoveRecord{Provider: "claude", Convs: 1, Turns: 3, Moves: 1, Moved: 1, Rewrites: 5000}) {
		t.Errorf("moves %+v", o.Moves)
	}
	if len(o.Load) != 1 {
		t.Fatalf("load %+v", o.Load)
	}
	if l := o.Load[0]; l.Convs != 2 || l.Turns != 2 || l.Accounts != 2 || l.PerHour[10] != 2 || l.Gap != 1140 || l.Tokens != 1365 {
		t.Errorf("load %+v", l)
	}
	if o.Routes != 4 || o.Why["cache"] != 2 || o.Why["cold"] != 1 {
		t.Errorf("routes %d, why %v", o.Routes, o.Why)
	}
	if o.Unread["usage.jsonl"] != 1 || !o.Cut["routing/2026-11-01.jsonl.gz"] {
		t.Errorf("unread %v, cut %v", o.Unread, o.Cut)
	}
	shown, err := observe(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if shown.Windows[0].Account != "Me@Example.com" {
		t.Errorf("-show-accounts named %q", shown.Windows[0].Account)
	}
	empty, err := observe(t.TempDir(), false)
	if err != nil || len(empty.Windows)+len(empty.Moves)+empty.Routes != 0 {
		t.Errorf("an empty folder: %+v, %v", empty, err)
	}
}

// Of several accounts resting, a turn tries only the one back soonest
// (probeOne): one refusal, not one per account.
func TestOnlyTheSoonestBackIsProbed(t *testing.T) {
	five := func(cap float64) WindowSpec { return WindowSpec{Span: 5 * h, Cap: cap, OnUse: true} }
	sc := pool(
		AccountSpec{Name: "a", Windows: []WindowSpec{five(1), week(1*d, 0)}},
		AccountSpec{Name: "b", Windows: []WindowSpec{five(1), week(5*d, 0)}},
	)
	turns := []turn{
		{at: at(0), conv: 0, cost: 1, last: true},               // on a
		{at: at(time.Minute), conv: 1, cost: 1, last: true},     // a refuses, on b
		{at: at(2 * time.Minute), conv: 2, cost: 1, last: true}, // b refuses, then a, the one resting: failed
		{at: at(3 * time.Minute), conv: 3, cost: 1, last: true}, // both rest: a, back soonest, probed alone
	}
	s, err := Run(sc, turns, 4, Options{Lag: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if s.Failed != 2 || s.Refusals != 4 {
		t.Errorf("failed %d, refusals %d; want 2 and 4", s.Failed, s.Refusals)
	}
}

// A refusal by a used up week isn't counted as the five hours'.
func TestWeekRefusalIsNoCapHit(t *testing.T) {
	sc := pool(AccountSpec{Name: "a", Windows: []WindowSpec{{Span: 5 * h, Cap: 100, OnUse: true}, week(1*d, 99)}})
	turns := []turn{{at: at(0), cost: 1}, {at: at(time.Minute), n: 1, cost: 1, last: true}}
	s, err := Run(sc, turns, 1, Options{Lag: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if s.Refusals != 1 || s.CapHits != 0 {
		t.Errorf("refusals %d, five-hour %d; want 1 and 0", s.Refusals, s.CapHits)
	}
}

// Stays: Session spreads only a conversation nobody answered: one routed
// afresh (its account resting) goes to routing's first however many are
// kept there. A conversation answered over heldFor ago, or the one asking,
// isn't counted as kept.
func TestSessionSpreadsOnlyNewConversations(t *testing.T) {
	sc := pool(
		AccountSpec{Name: "a", Windows: []WindowSpec{week(1*d, 0)}},
		AccountSpec{Name: "b", Windows: []WindowSpec{week(5*d, 0)}},
		AccountSpec{Name: "c", Windows: []WindowSpec{{Span: 5 * h, Cap: 1, OnUse: true}, week(6*d, 0)}},
	)
	turns := []turn{
		{at: at(0), conv: 0, cost: 0.1},             // new, nobody kept: a (Smart's first)
		{at: at(time.Second), conv: 1, cost: 0.1},   // new, a holds one: b
		{at: at(2 * time.Second), conv: 2, cost: 2}, // new, a and b one each: c (refused: Cap 1)...
	}
	s, err := Run(sc, turns, 3, Options{Stays: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if s.PerAccount["a"] != 0.1 || s.PerAccount["b"] != 0.1 {
		t.Fatalf("answered %v", s.PerAccount)
	}
	// conv 2 refused by c, which rests, went on to a; its next turn is
	// routed afresh ("resting" isn't c's: it never answered) — kept on a
	turns = append(turns, turn{at: at(3 * time.Second), conv: 1, n: 1, cost: 0.1, last: true})
	if s, err = Run(sc, turns, 3, Options{Stays: "session"}); err != nil {
		t.Fatal(err)
	}
	if s.Why["session"] != 1 {
		t.Errorf("why %v", s.Why)
	}
	// an hour on, the first two count as kept no longer: a new one goes
	// to routing's first, a
	late := []turn{turns[0], turns[1], {at: at(time.Hour), conv: 2, cost: 0.1, last: true}}
	if s, err = Run(sc, late, 3, Options{Stays: "session"}); err != nil {
		t.Fatal(err)
	}
	if s.PerAccount["a"] != 0.2 {
		t.Errorf("answered %v, want a third on a", s.PerAccount)
	}
}

// A conversation whose account is all but used up is routed afresh
// ("spent"), but not spread by leastHeld: it goes to routing's first, as
// affine leaves it, not to the account the fewest are kept on.
func TestRoutedAfreshIsNotSpread(t *testing.T) {
	sc := pool(
		AccountSpec{Name: "a", Windows: []WindowSpec{{Span: 5 * h, Cap: 1, OnUse: true}, week(1*d, 0)}},
		AccountSpec{Name: "b", Windows: []WindowSpec{week(3*d, 0)}},
		AccountSpec{Name: "c", Windows: []WindowSpec{week(5*d, 0)}},
	)
	turns := []turn{
		{at: at(0), conv: 0, cost: 1},                       // a, its five hours now used up
		{at: at(time.Second), conv: 1, cost: 0.1},           // new: b, routing's first now
		{at: at(2 * time.Second), conv: 0, n: 1, cost: 0.1}, // spent: to b, though c holds none
	}
	s, err := Run(sc, turns, 2, Options{Stays: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Why["spent"] != 1 || s.PerAccount["c"] != 0 {
		t.Errorf("why %v, answered %v: a conversation routed afresh was spread", s.Why, s.PerAccount)
	}
}

// What a week has left when the run ends after the last turn still
// counts at its renewal.
func TestWeeksAfterTheLastTurnAreCounted(t *testing.T) {
	sc := pool(AccountSpec{Name: "a", Windows: []WindowSpec{week(1*d, 30)}})
	sc.Days = 3
	s, err := Run(sc, []turn{{at: at(0), cost: 1, last: true}}, 1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.WeekRenewals != 1 || s.WeekLeft != 69 {
		t.Errorf("renewals %d, left %v; want 1 and 69", s.WeekRenewals, s.WeekLeft)
	}
}

func TestUnsimulatedRoutingsAreRefused(t *testing.T) {
	for _, r := range []string{"rotate", "weight", "free", "fast", "cheap", "expiring"} {
		if _, err := strategy(r); err == nil {
			t.Errorf("%s ran", r)
		}
	}
}
