package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Scenario is a pool of subscription accounts and the demand put on it.
// Amounts are in quota units: what a request spends of a window, the same
// unit for every window of every account, so a window's Cap is how many
// units its 100% holds.
type Scenario struct {
	Name     string        `json:"name"`
	About    string        `json:"about,omitempty"`
	Days     int           `json:"days"`
	Accounts []AccountSpec `json:"accounts"`
	Demand   Demand        `json:"demand"`
	// Cold is how long a conversation's cache stays warm unread: past it,
	// Stays: Auto routes the next turn afresh (affinity's "cold").
	// Zero is 5 minutes, magpie's cacheCold.
	Cold Duration `json:"cold,omitempty"`
}

// AccountSpec is one account and its windows.
type AccountSpec struct {
	Name    string       `json:"name"`
	Windows []WindowSpec `json:"windows"`
}

// WindowSpec is one of an account's windows.
type WindowSpec struct {
	Span Duration `json:"span"`
	Cap  float64  `json:"cap"`
	// First is when it first renews, from the scenario's start; zero
	// with OnUse set starts it on its first use, as Anthropic's and
	// OpenAI's five hours do. Without OnUse a window renews every Span
	// from First (a week on its own schedule).
	First Duration `json:"first,omitempty"`
	OnUse bool     `json:"onUse,omitempty"`
	// Used is the share used at the start, 0–100.
	Used float64 `json:"used,omitempty"`
}

// Demand is the conversations started and what each turn spends.
type Demand struct {
	// PerHour is conversations started an hour, by the hour of the day
	// (24 values); one value is every hour.
	PerHour []float64 `json:"perHour"`
	// Weekend scales PerHour on Saturdays and Sundays; zero is 1.
	Weekend float64 `json:"weekend,omitempty"`
	// Turns is the mean number of turns a conversation has.
	Turns float64 `json:"turns"`
	// Gap is the mean wait between two turns; Pause is a longer break
	// taken between turns with the chance PauseOdds.
	Gap       Duration `json:"gap"`
	Pause     Duration `json:"pause,omitempty"`
	PauseOdds float64  `json:"pauseOdds,omitempty"`
	// TurnCost is the mean units a turn spends with its cache read, and
	// Context the units its conversation's prompt adds each turn: a turn
	// sent where nothing is cached spends the whole conversation so far
	// again (Rewrite times Context a turn so far).
	TurnCost float64 `json:"turnCost"`
	Context  float64 `json:"context"`
	Rewrite  float64 `json:"rewrite,omitempty"`
}

// Duration reads "5h", "7d", "90m" or a number of seconds.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n float64
		if err := json.Unmarshal(b, &n); err != nil {
			return fmt.Errorf("duration: %s", b)
		}
		*d = Duration(time.Duration(n * float64(time.Second)))
		return nil
	}
	v, err := parseDuration(s)
	*d = Duration(v)
	return err
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func parseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n float64
		if _, err := fmt.Sscanf(days, "%g", &n); err != nil {
			return 0, fmt.Errorf("duration %q", s)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

func (s Scenario) cold() time.Duration {
	if s.Cold == 0 {
		return 5 * time.Minute
	}
	return time.Duration(s.Cold)
}

func (s Scenario) check() error {
	if s.Days <= 0 {
		return fmt.Errorf("%s: days must be above 0", s.Name)
	}
	if len(s.Accounts) == 0 {
		return fmt.Errorf("%s: no accounts", s.Name)
	}
	if n := len(s.Demand.PerHour); n != 1 && n != 24 {
		return fmt.Errorf("%s: perHour has %d values, want 1 or 24", s.Name, n)
	}
	if s.Demand.Turns < 1 || s.Demand.TurnCost <= 0 || s.Demand.Gap <= 0 {
		return fmt.Errorf("%s: demand needs turns ≥ 1, turnCost > 0 and gap > 0", s.Name)
	}
	seen := map[string]bool{}
	for _, a := range s.Accounts {
		if a.Name == "" || seen[a.Name] {
			return fmt.Errorf("%s: account names must be set and apart (%q)", s.Name, a.Name)
		}
		seen[a.Name] = true
		if len(a.Windows) == 0 {
			return fmt.Errorf("%s: account %s has no window", s.Name, a.Name)
		}
		for _, w := range a.Windows {
			if w.Span <= 0 || w.Cap <= 0 {
				return fmt.Errorf("%s: account %s has a window without span or cap", s.Name, a.Name)
			}
		}
	}
	return nil
}

func loadScenario(path string) (Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	var s Scenario
	if err := json.Unmarshal(b, &s); err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", path, err)
	}
	if s.Name == "" {
		s.Name = path
	}
	return s, s.check()
}

const (
	h = Duration(time.Hour)
	d = 24 * h
)

// workday is a day's conversations an hour, heaviest in working hours.
var workday = []float64{0, 0, 0, 0, 0, 0, 0, 0.5, 1.5, 3, 3, 3, 2, 3, 3, 3, 3, 2.5, 1.5, 1, 1, 0.5, 0.5, 0}

func scaled(v []float64, k float64) []float64 {
	out := make([]float64, len(v))
	for i := range v {
		out[i] = v[i] * k
	}
	return out
}

// claude is a Max-like account: five hours started on use, inside a week
// on its own schedule that holds about nine five-hour windows' worth.
func claude(name string, firstWeek Duration, weekUsed float64) AccountSpec {
	return AccountSpec{Name: name, Windows: []WindowSpec{
		{Span: 5 * h, Cap: 100, OnUse: true},
		{Span: 7 * d, Cap: 900, First: firstWeek, Used: weekUsed},
	}}
}

// Presets are the scenarios the tool knows by name.
var Presets = map[string]Scenario{
	"staggered": {
		Name:  "staggered",
		About: "three Max-like accounts whose weeks renew on days 1, 3 and 5; working-hours demand near the pool's weekly capacity",
		Days:  21,
		Accounts: []AccountSpec{
			claude("a", 1*d, 30), claude("b", 3*d, 60), claude("c", 5*d, 10),
		},
		Demand: Demand{PerHour: workday, Weekend: 0.3, Turns: 25, Gap: Duration(90 * time.Second), Pause: Duration(20 * time.Minute), PauseOdds: 0.08, TurnCost: 0.5, Context: 0.1, Rewrite: 1},
	},
	"burst": {
		Name:  "burst",
		About: "two Max-like accounts and heavy bursts of parallel sessions, where the five hours bind before the week",
		Days:  14,
		Accounts: []AccountSpec{
			claude("a", 2*d, 0), claude("b", 4*d, 0),
		},
		Demand: Demand{PerHour: scaled(workday, 2.2), Weekend: 0.1, Turns: 30, Gap: Duration(60 * time.Second), Pause: Duration(15 * time.Minute), PauseOdds: 0.05, TurnCost: 0.15, Context: 0.03, Rewrite: 1},
	},
	"uneven": {
		Name:  "uneven",
		About: "one account 85% into a week renewing in a day beside two fresh ones renewing later: the case Smart and Pace order apart",
		Days:  14,
		Accounts: []AccountSpec{
			claude("low", 1*d, 85), claude("fresh1", 5*d, 0), claude("fresh2", 6*d, 0),
		},
		Demand: Demand{PerHour: workday, Weekend: 0.3, Turns: 25, Gap: Duration(90 * time.Second), Pause: Duration(20 * time.Minute), PauseOdds: 0.08, TurnCost: 0.5, Context: 0.1, Rewrite: 1},
	},
	"five-hour-only": {
		Name:  "five-hour-only",
		About: "an Enterprise-like account with five hours alone beside two Max-like ones",
		Days:  14,
		Accounts: []AccountSpec{
			{Name: "ent", Windows: []WindowSpec{{Span: 5 * h, Cap: 60, OnUse: true}}},
			claude("a", 2*d, 0), claude("b", 5*d, 0),
		},
		Demand: Demand{PerHour: workday, Weekend: 0.3, Turns: 25, Gap: Duration(90 * time.Second), Pause: Duration(20 * time.Minute), PauseOdds: 0.08, TurnCost: 0.5, Context: 0.1, Rewrite: 1},
	},
}

func presetNames() []string {
	names := make([]string, 0, len(Presets))
	for n := range Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
