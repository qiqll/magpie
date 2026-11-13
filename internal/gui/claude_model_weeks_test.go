package gui

import (
	"fmt"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Claude account's card has its Sonnet and Fable weeks at 0% when its
// reading leaves them out, one it told kept as told, each named for the
// menu bar as one model's week (so the Accounts page puts them under Per
// model); another provider's card, a failed one and one that told no week
// are left as they are, and the cards given are never written into
// (需求方 2026-11-13).
func TestClaudeModelWeeksAtZero(t *testing.T) {
	const span = 7 * 24 * time.Hour
	week := provider.QuotaWindow{Name: "7 days", Used: 40, Span: span}
	in := []provider.SubscriptionQuota{
		{Provider: "claude", User: "a@x.com", Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 10, Span: 5 * time.Hour}, week}},
		{Provider: "claude", User: "b@x.com", Windows: []provider.QuotaWindow{week, {Name: "7 days · Sonnet", Used: 55, Span: span, Model: "sonnet"}}},
		{Provider: "claude", User: "c@x.com", Error: "Claude Code's /usage told no allowance", Windows: []provider.QuotaWindow{}},
		{Provider: "claude", User: "d@x.com", Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 3, Span: 5 * time.Hour}}},
		{Provider: "codex", User: "e@x.com", Windows: []provider.QuotaWindow{week}},
	}
	before := fmt.Sprint(in)
	out := withTrayKinds(claudeModelWeeks(in))
	if fmt.Sprint(in) != before {
		t.Fatalf("the cards given were written into: %v", in)
	}
	type said struct {
		name string
		used float64
		kind string
	}
	of := func(q provider.SubscriptionQuota) []said {
		var s []said
		for _, w := range q.Windows {
			k := ""
			if w.TrayExtra {
				k = w.TrayKind
			}
			s = append(s, said{w.Name, w.Used, k})
		}
		return s
	}
	for i, want := range [][]said{
		{{"5 hours", 10, ""}, {"7 days", 40, ""}, {"7 days · Sonnet", 0, "7d:sonnet"}, {"7 days · Fable", 0, "7d:fable"}},
		{{"7 days", 40, ""}, {"7 days · Sonnet", 55, "7d:sonnet"}, {"7 days · Fable", 0, "7d:fable"}},
		nil,
		{{"5 hours", 3, ""}},
		{{"7 days", 40, ""}},
	} {
		if got := of(out[i]); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, want %v", out[i].User, got, want)
		}
	}
}
