package gui

import (
	"slices"
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

// claudeModelWeeks is qs with each Claude subscription account's Sonnet and
// Fable weeks at 0% where its reading leaves them out (需求方 2026-11-13: with
// no Sonnet or Fable request yet the card says their use is 0, not
// nothing). Claude Code's /usage prints those lines only once Anthropic
// counts them, as it leaves the five hours out before a session (provider's
// claudeWithUnstarted, which fills that one in for routing too). These are
// for the cards alone, routing reads the account as it was read: a card
// whose reading told no week, or failed, is left as it is.
func claudeModelWeeks(qs []provider.SubscriptionQuota) []provider.SubscriptionQuota {
	out := qs
	for i, q := range qs {
		if q.Provider != "claude" || q.Error != "" || !slices.ContainsFunc(q.Windows, func(w provider.QuotaWindow) bool { return w.Name == "7 days" }) {
			continue
		}
		ws := q.Windows
		for _, k := range claudeShownWeeks {
			if !slices.ContainsFunc(ws, func(w provider.QuotaWindow) bool { return w.Name == k.name }) {
				ws = append(slices.Clip(ws), provider.QuotaWindow{Name: k.name, Span: 7 * 24 * time.Hour, Model: k.model})
			}
		}
		if len(ws) == len(q.Windows) {
			continue
		}
		if &out[0] == &qs[0] {
			out = slices.Clone(qs)
		}
		out[i].Windows = ws
	}
	return out
}
