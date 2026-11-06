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
	fmt.Fprintf(out, "\nWhy routing kept or moved a turn (routing/, %d routes)\n", o.Routes)
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
	printPlacements(out, o.Placements)
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

func printPlacements(out io.Writer, p *Placements) {
	if p == nil || p.Stints == 0 {
		return
	}
	fmt.Fprintf(out, "\nWhere conversations were put (routing/): %d conversations, %d stints on one account\n", p.Convs, p.Stints)
	starts := make([]string, 0, len(p.Started))
	for k := range p.Started {
		starts = append(starts, k)
	}
	sort.Strings(starts)
	var parts []string
	for _, k := range starts {
		parts = append(parts, fmt.Sprintf("%s %d", k, p.Started[k]))
	}
	fmt.Fprintf(out, "  stints began: %s\n", strings.Join(parts, ", "))
	fmt.Fprintf(out, "  accounts ready for a new conversation: median %.0f, 90th %.0f; users bound %d, accounts each median %.0f, 90th %.0f\n",
		p.Ready[0], p.Ready[1], p.Users, p.Bound[0], p.Bound[1])
	fmt.Fprintf(out, "  a stint that ended with its conversation raised its account's fullest window by %.0f / %.0f / %.0f points (50th/75th/90th, %d stints)\n",
		p.Rise[0], p.Rise[1], p.Rise[2], p.RiseStints)
	fmt.Fprintf(out, "  requests no try answered: %d; with nobody weighed: %d\n", p.Unanswered, p.Untold)
	fmt.Fprintf(out, "  caps: %s\n", p.Caps)
	printUsers(out, p.ByUser)
	printCounts(out, "  why nothing answered them:", p.UnansweredWhy, 12)
	printCounts(out, "  moves to another account, by the moving request's route (accounts in its order · user binding why · binding since the request before · where the account left was):", p.MoveCauses, 20)
	for _, pl := range p.Pools {
		n := pl.Unknown.Stints
		for _, b := range pl.Buckets {
			n += b.Stints
		}
		if n == 0 {
			continue
		}
		about := "conversations of users not bound to accounts"
		if pl.Name == "bound" {
			about = "conversations of bound users: \"another\" is another of the user's own accounts"
		}
		fmt.Fprintf(out, "\n  %s (%d stints)\n", about, n)
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  left when put there %\tstints\tnew convs\tforced off\tforced %\tfullest 5h\tfullest week\tanother had 30+ more\tmedian requests\thow they ended")
		row := func(name string, b Bucket) {
			if b.Stints == 0 {
				return
			}
			ends := make([]string, 0, len(b.Ended))
			for k := range b.Ended {
				ends = append(ends, k)
			}
			sort.Slice(ends, func(i, j int) bool {
				if b.Ended[ends[i]] != b.Ended[ends[j]] {
					return b.Ended[ends[i]] > b.Ended[ends[j]]
				}
				return ends[i] < ends[j]
			})
			var parts []string
			for _, k := range ends {
				parts = append(parts, fmt.Sprintf("%s %d", k, b.Ended[k]))
			}
			fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%.0f\t%d\t%d\t%d\t%.0f\t%s\n", name, b.Stints, b.First, b.Forced, pct(float64(b.Forced), float64(b.Stints)), b.FiveHours, b.Week, b.Roomier, b.Requests, strings.Join(parts, ", "))
		}
		for _, b := range pl.Buckets {
			row(fmt.Sprintf("%.0f-%.0f", b.From, min(b.To, 100)), b)
		}
		row("not told", pl.Unknown)
		tw.Flush()
	}
}

// printCounts prints counts, the most first, at most n of them.
func printCounts(out io.Writer, title string, counts map[string]int, n int) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	fmt.Fprintln(out, title)
	rest := 0
	for i, k := range keys {
		if i >= n {
			rest += counts[k]
			continue
		}
		fmt.Fprintf(out, "    %5d  %s\n", counts[k], k)
	}
	if rest > 0 {
		fmt.Fprintf(out, "    %5d  (others)\n", rest)
	}
}

func printUsers(out io.Writer, u *UserPlacements) {
	if u == nil || len(u.Users)+len(u.Tiers) == 0 && u.Unbound == nil {
		return
	}
	fmt.Fprintln(out, "\n  by user (a user's conversations under every key are one user's; heavy: the busiest that asked 80% of requests)")
	fmt.Fprintln(out, "  pick: routing chose its account, ready, among 2+ ready; tight: another had 30+ points more before its cap; sooner: the account picked renewed before that one (Smart's reason), later: it didn't")
	fmt.Fprintln(out, "  ends: end/quota/rate/access/failed/routine/unknown; served/had/could: a tight pick that ended on quota, in requests (answered / the picked account had room for / the roomier had room for)")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  user\ttier\trequests\tstints\tends\tpicks\ttight (sooner/later)\tsooner→quota\tlater→quota\tloose→quota\tpoints/request\tserved\thad\tcould")
	row := func(r UserUse) {
		e := r.Ends
		fmt.Fprintf(tw, "  %s\t%s\t%d\t%d\t%d/%d/%d/%d/%d/%d/%d\t%d\t%d (%d/%d)\t%d (%.0f%%)\t%d (%.0f%%)\t%d (%.0f%%)\t%.3f\t%.0f\t%.0f\t%.0f\n",
			r.User, r.Tier, r.Requests, r.Stints, e["end"], e["quota"], e["rate"], e["access"], e["failed"], e["routine"], e["unknown"],
			r.Picks, r.Tight, r.Sooner, r.Later,
			r.SoonerQuota, pct(float64(r.SoonerQuota), float64(r.Sooner)),
			r.LaterQuota, pct(float64(r.LaterQuota), float64(r.Later)),
			r.LooseQuota, pct(float64(r.LooseQuota), float64(r.Picks-r.Tight)),
			r.PerRequest, r.Served, r.Had, r.Could)
	}
	for _, r := range u.Users {
		row(r)
	}
	for _, r := range u.Tiers {
		row(r)
	}
	if u.Unbound != nil {
		row(*u.Unbound)
	}
	tw.Flush()
}
