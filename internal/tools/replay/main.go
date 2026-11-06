// replay weighs magpie's account routings against each other, for
// development: it is not shipped and no user runs it.
//
//	go run ./internal/tools/replay sim [-scenario staggered|file.json] [-routing smart,pace,…] [-stays auto,session] [-runs 5] [-lag 1m] [-json]
//	go run ./internal/tools/replay observe [-dir ~/.config/magpie] [-show-accounts] [-json]
//
// sim draws a scenario's conversations from a seed and routes every turn
// of them with each routing's own strategy (internal/routes), the seats
// weighed from provider.Allowance as weighRouted weighs them, resting
// accounts last, and each conversation kept where Stays says, as affine
// keeps it. It then says what each lost: turns nothing could answer, tries
// refused for a window used up (the five hours among them), conversations
// moved to another account and the cache they wrote again there, and what
// each week had left when it renewed.
//
// observe reads what a magpie recorded instead (this computer's, or a
// server's with -dir): what each window had left at its renewals
// (quota-history.json), conversations that moved account and the load
// they put on each provider (usage.jsonl), and why routing kept or moved
// each turn (routing/). Accounts are named by a hash salted for the run
// unless -show-accounts, so a report names nobody outside it.
//
// What sim leaves out: the vendor's own count of a request (a unit here is
// whatever the scenario says), "no-cache" (a first turn too small to
// keep), sinks, concurrency, fallbacks and groups. A result is a
// comparison of routings on one made-up pool, not a forecast.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sim":
		err = simCmd(os.Args[2:], os.Stdout)
	case "observe":
		err = observeCmd(os.Args[2:], os.Stdout)
	case "-h", "--help", "help":
		usage(os.Stdout)
		return
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "usage: replay sim [flags] | replay observe [flags]\npresets: %s\n", strings.Join(presetNames(), ", "))
}

func simCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sim", flag.ContinueOnError)
	scenario := fs.String("scenario", "all", "a preset ("+strings.Join(presetNames(), ", ")+"), all of them (all), or a scenario JSON file")
	routings := fs.String("routing", "smart,pace,usage,order,drain", "routings to compare, comma-separated: of "+strings.Join(simulated, ", "))
	stays := fs.String("stays", "auto,session", "Stays modes to compare: auto, session")
	runs := fs.Int("runs", 5, "seeds to run each on, averaged")
	seed := fs.Uint64("seed", 1, "the first seed")
	lag := fs.Duration("lag", 0, "how old the allowance routing weighs may be (0: live)")
	cold := fs.Duration("cold", 0, "how long a cache stays warm unread, over each scenario's (0: the scenario's)")
	rewrite := fs.Float64("rewrite", -1, "what writing a moved conversation's cache again costs, over each scenario's Rewrite (-1: the scenario's)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runs < 1 {
		return fmt.Errorf("-runs must be at least 1")
	}
	var scs []Scenario
	switch s, ok := Presets[*scenario]; {
	case *scenario == "all":
		for _, n := range presetNames() {
			scs = append(scs, Presets[n])
		}
	case ok:
		scs = []Scenario{s}
	default:
		s, err := loadScenario(*scenario)
		if err != nil {
			return err
		}
		scs = []Scenario{s}
	}
	var stayModes []string
	for _, m := range split(*stays) {
		switch m {
		case "auto":
			stayModes = append(stayModes, "")
		case "session":
			stayModes = append(stayModes, m)
		default:
			return fmt.Errorf("unknown stays %q: auto or session", m)
		}
	}
	var all []Result
	for k := range scs {
		if *cold > 0 {
			scs[k].Cold = Duration(*cold)
		}
		if *rewrite >= 0 {
			scs[k].Demand.Rewrite = *rewrite
		}
	}
	for _, sc := range scs {
		if err := sc.check(); err != nil {
			return err
		}
		res, err := Compare(sc, split(*routings), stayModes, *runs, *seed, *lag)
		if err != nil {
			return err
		}
		all = append(all, res...)
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(all)
	}
	printResults(out, scs, all)
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Result is one routing and Stays mode on one scenario, over every seed.
type Result struct {
	Scenario string  `json:"scenario"`
	Runs     int     `json:"runs"`
	Stats    []Stats `json:"stats"`
}

// Compare runs every routing and Stays mode on the same demand, seed by
// seed.
func Compare(sc Scenario, routings, stays []string, runs int, seed uint64, lag time.Duration) ([]Result, error) {
	var out []Result
	for _, r := range routings {
		for _, st := range stays {
			res := Result{Scenario: sc.Name, Runs: runs}
			for k := range runs {
				turns, convs := demand(sc, seed+uint64(k))
				s, err := Run(sc, turns, convs, Options{Routing: r, Stays: st, Lag: lag})
				if err != nil {
					return nil, err
				}
				res.Stats = append(res.Stats, s)
			}
			out = append(out, res)
		}
	}
	return out, nil
}

// mean is the average over a result's seeds of what f reads.
func (r Result) mean(f func(Stats) float64) float64 {
	sum := 0.0
	for _, s := range r.Stats {
		sum += f(s)
	}
	return sum / float64(len(r.Stats))
}

func pct(n, of float64) float64 {
	if of == 0 {
		return 0
	}
	return 100 * n / of
}

func printResults(out io.Writer, scs []Scenario, all []Result) {
	for _, sc := range scs {
		fmt.Fprintf(out, "\n%s — %s\n", sc.Name, sc.About)
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(tw, "routing\tstays\tturns\tfailed%\tfailed convs\trefused\t5h refused\tmoves/100 turns\trewrite % served\tweek left at renewal %\t")
		for _, r := range all {
			if r.Scenario != sc.Name {
				continue
			}
			s0 := r.Stats[0]
			fmt.Fprintf(tw, "%s\t%s\t%.0f\t%.2f\t%.1f\t%.1f\t%.1f\t%.2f\t%.2f\t%.1f\t\n",
				s0.Routing, s0.Stays,
				r.mean(func(s Stats) float64 { return float64(s.Turns) }),
				r.mean(func(s Stats) float64 { return pct(float64(s.Failed), float64(s.Turns)) }),
				r.mean(func(s Stats) float64 { return float64(s.FailedConvs) }),
				r.mean(func(s Stats) float64 { return float64(s.Refusals) }),
				r.mean(func(s Stats) float64 { return float64(s.CapHits) }),
				r.mean(func(s Stats) float64 { return pct(float64(s.Moves), float64(s.Turns)) }),
				r.mean(func(s Stats) float64 { return pct(s.Rewrite, s.Served) }),
				r.mean(Stats.MeanWeekLeft),
			)
		}
		tw.Flush()
	}
}

func observeCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	dir := fs.String("dir", appdir.Config(), "magpie's config folder")
	show := fs.Bool("show-accounts", false, "name accounts as they are, not by a hash")
	asJSON := fs.Bool("json", false, "print JSON")
	jsonOut := fs.String("json-out", "", "also write the JSON to this file: one run, so its accounts' hashes match the text's")
	if err := fs.Parse(args); err != nil {
		return err
	}
	o, err := observe(*dir, *show)
	if err != nil {
		return err
	}
	if *jsonOut != "" {
		b, err := json.MarshalIndent(o, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*jsonOut, append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(o)
	}
	fmt.Fprintf(out, "%s\n\nWindows (left at renewal: the last reading within an hour of it; stale: none that close)\n", o.Dir)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "provider\taccount\twindow\tspan\treadings\trenewals\tstale\tmean left %\tleft now %")
	for _, w := range o.Windows {
		span := "?"
		if w.Span > 0 {
			span = w.Span.Round(time.Minute).String()
		}
		mean := "-"
		if w.Known > 0 {
			mean = fmt.Sprintf("%.1f", w.MeanLeft())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%.1f\n", w.Provider, w.Account, w.Window, span, w.Points, w.Cycles, w.Stale, mean, w.LastLeft)
	}
	tw.Flush()
	fmt.Fprintf(out, "\nConversations that moved account (usage.jsonl; by session and model, subagents and side requests left out)\n")
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "provider\tconversations\tturns\tmoves\tmoved\tcache written on a move")
	for _, m := range o.Moves {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\n", m.Provider, m.Convs, m.Turns, m.Moves, m.Moved, m.Rewrites)
	}
	tw.Flush()
	fmt.Fprintf(out, "\nLoad (usage.jsonl; conversations an hour by UTC hour, a day's mean)\n")
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "provider\tdays\taccounts\tconversations\tturns each\tmedian gap\ttokens a turn\tbusiest hour")
	for _, l := range o.Load {
		busy := 0
		for h := range l.PerHour {
			if l.PerHour[h] > l.PerHour[busy] {
				busy = h
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%.1f\t%.0fs\t%.0f\t%02d:00 (%.1f)\n", l.Provider, l.Days, l.Accounts, l.Convs, l.Turns, l.Gap, l.Tokens, busy, l.PerHour[busy])
	}
	tw.Flush()
	fmt.Fprintf(out, "\nWhy routing kept or moved a turn (routing/, %d routes; a day keeps its latest 2000 only)\n", o.Routes)
	whys := make([]string, 0, len(o.Why))
	for w := range o.Why {
		whys = append(whys, w)
	}
	sort.Slice(whys, func(i, j int) bool {
		if o.Why[whys[i]] != o.Why[whys[j]] {
			return o.Why[whys[i]] > o.Why[whys[j]]
		}
		return whys[i] < whys[j]
	})
	for _, w := range whys {
		fmt.Fprintf(out, "  %-10s %d\n", w, o.Why[w])
	}
	if len(o.Unread)+len(o.Cut) > 0 {
		fmt.Fprintf(out, "\nNot read (what these held is unknown):\n")
		keys := make([]string, 0, len(o.Unread)+len(o.Cut))
		for k := range o.Unread {
			keys = append(keys, k)
		}
		for k := range o.Cut {
			if o.Unread[k] == 0 {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			note := ""
			if o.Cut[k] {
				note = ", stopped part way: the rest is unknown"
			}
			fmt.Fprintf(out, "  %s: %d lines%s\n", k, o.Unread[k], note)
		}
	}
	return nil
}
