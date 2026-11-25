package gateway

// One seat more on an account of low use (T-279): an account of a
// provider with a low use of its own in Settings (settings.ProviderLowUse,
// Settings › Growth policy) whose week its vendor says is used, on
// average, less than that share a day takes one user of lowUseTier more
// than the tier's share (2 to an account: 3), once, never two more. The
// week's daily use is its share used over the days it has run: its start
// (its renewal less its 7 days) a day or more ago, so a window not a day
// old, one on credits, an account with no week and one whose week isn't
// read take none more. It holds only for a new seat (a first account or
// a spare), as by_use does: nobody bound is moved, so a week that renews
// or rises past it takes the seat from nobody, and a seat a user lets go
// of (idle, sweepIdle) is taken again only while the account's use is
// still low then.

import (
	"log"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// lowUseTier is the tier an account of low use takes one more user of.
const lowUseTier = "free"

// lowUseWeek is the window whose daily use is told.
const lowUseWeek = 7 * 24 * time.Hour

// providerLowUseOf is provider pid's low use, Settings', in percent a day,
// 0 for none: read before userBinds is held. A settings.json that doesn't
// read is none: an account takes no seat more than its share on what
// can't be read.
var providerLowUseOf = func(pid string) int {
	s, err := settings.SharedChecked()
	if err != nil {
		return 0
	}
	return s.ProviderLowUse[pid]
}

// lowUse is the accounts of a provider of low use (lowUseOf): lower-cased
// who → its week's daily use, in percent; below is the provider's low use.
type lowUse struct {
	below int
	daily map[string]float64
}

// lowUseOf is each account of provider pid among cands whose week is used
// less than its low use a day; none with no low use set. It reads
// settings.json and the allowances routing keeps, never with userBinds
// held.
func lowUseOf(pid string, cands []candidate, now time.Time) lowUse {
	below := providerLowUseOf(pid)
	if below <= 0 {
		return lowUse{}
	}
	out := map[string]float64{}
	for _, c := range cands {
		if c.p.ID != pid || c.p.Account == nil {
			continue
		}
		who := strings.ToLower(c.who())
		if _, seen := out[who]; seen {
			continue
		}
		a, ok := c.allowance()
		if !ok {
			continue
		}
		daily, told := -1.0, true
		for _, l := range a {
			if l.Model != "" || l.Span != lowUseWeek {
				continue
			}
			ran := now.Sub(l.Resets.Add(-l.Span))
			// on credits its share used doesn't tell its use, and a week
			// renewed or not a day old has no day's use yet
			if l.Credits || !l.Resets.After(now) || ran < 24*time.Hour {
				told = false
				break
			}
			// of two weeks (each a window of every model), the busier
			daily = max(daily, l.Used/(ran.Hours()/24))
		}
		if told && daily >= 0 && daily < float64(below) {
			out[who] = daily
		}
	}
	return lowUse{below, out}
}

// withLowUse marks each account of uses in low (lowUseOf) as of low use.
func (uses seatUses) withLowUse(low lowUse) {
	for who := range low.daily {
		u := uses[who]
		u.low = true
		uses[who] = u
	}
}

// oneMore is share with one user more for an account of low use: a share
// of 2 or more, never 1 (an account its user's alone) or 0 (any number).
func oneMore(share int) int {
	if share >= 2 {
		return share + 1
	}
	return share
}

// noteLowUseSeat logs that user, of tier, is bound to who of provider
// pid as the one user more its low use takes, when it is (uses).
func noteLowUseSeat(pid, who, user string, tier Tier, uses seatUses, low lowUse) {
	u := uses[strings.ToLower(who)]
	if !u.low || u.seated(tier.Share) {
		return
	}
	log.Printf("gateway: %s: %s bound to %s as one %s user more than its share, its week used %.1f%% a day, under %d%% (T-279)", pid, user, who, tier.Name, low.daily[strings.ToLower(who)], low.below)
}
