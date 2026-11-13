package gui

import (
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// claudeShownWeeks are the per-model weeks a Claude account's card shows
// even before Anthropic counts them: their names as provider's claudeKinds
// has them, and the models each counts.
var claudeShownWeeks = []struct{ name, model string }{
	{"7 days · Sonnet", "sonnet"},
	{"7 days · Fable", "fable"},
}

// claudeWindowOrder is how Claude Code names its windows, as provider's
// claudeKinds orders them; another (Extra usage) goes after.
var claudeWindowOrder = []string{"5 hours", "7 days", "7 days · Opus", "7 days · Sonnet", "7 days · Fable"}

// claudeModelWeeks is qs with each Claude subscription account's Sonnet and
// Fable weeks at 0% where its reading leaves them out (需求方 2026-11-13: with
// no Sonnet or Fable request yet the card says their use is 0, not
// nothing). Claude Code's /usage prints those lines only once Anthropic
// counts them, as it leaves the five hours out before a session (provider's
// claudeWithUnstarted, which fills that one in for routing too). These are
// for the cards alone, routing reads the account as it was read: a card
// whose reading told no week, or failed, or another magpie's (shown as it
// sent it) is left as it is. Where a window for every model is used up,
// each added week is held by it (HeldBy, the one that renews last), as
// WithHeldWindows holds a told one: nothing of it can be spent till then,
// so the card counts as spent, not as 100% left (review M1). Called with
// the windows' holds read (after WithHeldWindows), at now.
func claudeModelWeeks(qs []provider.SubscriptionQuota, now time.Time) []provider.SubscriptionQuota {
	out := qs
	for i, q := range qs {
		if q.Provider != "claude" || q.Error != "" || q.From != "" || !slices.ContainsFunc(q.Windows, func(w provider.QuotaWindow) bool { return w.Name == "7 days" }) {
			continue
		}
		held := claudeHeldBy(q.Windows, now)
		ws := q.Windows
		for _, k := range claudeShownWeeks {
			// one told under a longer name ("Fable 5") is told (review M2)
			if slices.ContainsFunc(ws, func(w provider.QuotaWindow) bool {
				return strings.HasPrefix(strings.ToLower(w.Model), k.model) || strings.HasPrefix(strings.ToLower(w.Name), strings.ToLower(k.name))
			}) {
				continue
			}
			ws = append(slices.Clip(ws), provider.QuotaWindow{Name: k.name, Span: 7 * 24 * time.Hour, Model: k.model, HeldBy: held})
		}
		if len(ws) == len(q.Windows) {
			continue
		}
		// in Claude Code's order, as a reading has them (review L3)
		rank := func(w provider.QuotaWindow) int {
			if j := slices.Index(claudeWindowOrder, w.Name); j >= 0 {
				return j
			}
			return len(claudeWindowOrder)
		}
		slices.SortStableFunc(ws, func(a, b provider.QuotaWindow) int { return rank(a) - rank(b) })
		if &out[0] == &qs[0] {
			out = slices.Clone(qs)
		}
		out[i].Windows = ws
	}
	return out
}

// claudeHeldBy is the name of the window of ws for every model that is
// used up at now, the one that renews last (its reset not known, last of
// all), as provider's stopsAll reads one; "" for none.
func claudeHeldBy(ws []provider.QuotaWindow, now time.Time) string {
	name, last, unknown := "", time.Time{}, false
	for _, w := range ws {
		if w.Aside || w.Text || w.Unlimited || w.Model != "" || w.Pool != "" || w.Used < 100 || w.ResetsAt != nil && !w.ResetsAt.After(now) {
			continue
		}
		switch {
		case w.ResetsAt == nil:
			if !unknown {
				name, unknown = w.Name, true
			}
		case !unknown && (name == "" || w.ResetsAt.After(last)):
			name, last = w.Name, *w.ResetsAt
		}
	}
	return name
}
