package gui

import (
	"fmt"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Claude account's card has its Sonnet and Fable weeks at 0% when its
// reading leaves them out, in Claude Code's order, one it told kept as
// told (under a longer name too, "Fable 5"), each named for the menu bar
// as one model's week (so the Accounts page puts them under Per model);
// where a window for every model is used up each added week is held by
// it, the one renewing last; another provider's card, a failed one,
// another magpie's and one that told no week are left as they are, and
// the cards given are never written into (需求方 2026-11-13).
func TestClaudeModelWeeksAtZero(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	const span = 7 * 24 * time.Hour
	at := func(h int) *time.Time { x := now.Add(time.Duration(h) * time.Hour); return &x }
	five := func(used float64, r *time.Time) provider.QuotaWindow {
		return provider.QuotaWindow{Name: "5 hours", Used: used, Span: 5 * time.Hour, ResetsAt: r}
	}
	week := func(used float64, r *time.Time) provider.QuotaWindow {
		return provider.QuotaWindow{Name: "7 days", Used: used, Span: span, ResetsAt: r}
	}
	in := []provider.SubscriptionQuota{
		{Provider: "claude", User: "a@x.com", Windows: []provider.QuotaWindow{five(10, at(2)), week(40, at(50))}},
		{Provider: "claude", User: "b@x.com", Windows: []provider.QuotaWindow{week(40, at(50)), {Name: "7 days · Fable", Used: 30, Span: span, Model: "fable"}}},
		{Provider: "claude", User: "c@x.com", Error: "Claude Code's /usage told no allowance", Windows: []provider.QuotaWindow{}},
		{Provider: "claude", User: "d@x.com", Windows: []provider.QuotaWindow{five(3, at(2))}},
		{Provider: "codex", User: "e@x.com", Windows: []provider.QuotaWindow{week(40, nil)}},
		// spent: both used up, the week renewing last holds the added weeks
		{Provider: "claude", User: "f@x.com", Windows: []provider.QuotaWindow{five(100, at(2)), week(100, at(50))}},
		// a used-up window whose reset has passed holds nothing
		{Provider: "claude", User: "g@x.com", Windows: []provider.QuotaWindow{five(100, at(-1)), week(20, at(50))}},
		// Claude Code naming its Fable week longer
		{Provider: "claude", User: "h@x.com", Windows: []provider.QuotaWindow{week(5, at(50)), {Name: "7 days · Fable 5", Used: 12, Span: span, Model: "fable 5"}}},
		{Provider: "claude", User: "i@x.com", From: "office", Windows: []provider.QuotaWindow{week(40, at(50))}},
	}
	before := fmt.Sprint(in)
	out := withTrayKinds(claudeModelWeeks(in, now))
	if fmt.Sprint(in) != before {
		t.Fatalf("the cards given were written into: %v", in)
	}
	type said struct {
		name string
		used float64
		kind string
		held string
	}
	of := func(q provider.SubscriptionQuota) []said {
		var s []said
		for _, w := range q.Windows {
			k := ""
			if w.TrayExtra {
				k = w.TrayKind
			}
			s = append(s, said{w.Name, w.Used, k, w.HeldBy})
		}
		return s
	}
	for i, want := range [][]said{
		{{"5 hours", 10, "", ""}, {"7 days", 40, "", ""}, {"7 days · Sonnet", 0, "7d:sonnet", ""}, {"7 days · Fable", 0, "7d:fable", ""}},
		{{"7 days", 40, "", ""}, {"7 days · Sonnet", 0, "7d:sonnet", ""}, {"7 days · Fable", 30, "7d:fable", ""}},
		nil,
		{{"5 hours", 3, "", ""}},
		{{"7 days", 40, "", ""}},
		{{"5 hours", 100, "", ""}, {"7 days", 100, "", ""}, {"7 days · Sonnet", 0, "7d:sonnet", "7 days"}, {"7 days · Fable", 0, "7d:fable", "7 days"}},
		{{"5 hours", 100, "", ""}, {"7 days", 20, "", ""}, {"7 days · Sonnet", 0, "7d:sonnet", ""}, {"7 days · Fable", 0, "7d:fable", ""}},
		{{"7 days", 5, "", ""}, {"7 days · Sonnet", 0, "7d:sonnet", ""}, {"7 days · Fable 5", 12, "7d:fable 5", ""}},
		{{"7 days", 40, "", ""}},
	} {
		if got := of(out[i]); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, want %v", out[i].User, got, want)
		}
	}
}
