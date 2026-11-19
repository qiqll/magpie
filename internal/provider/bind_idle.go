package provider

import (
	"fmt"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// SetAccountBindIdle keeps hours as how long a user bound to the account
// ref of the subscription id may send the provider nothing before the
// account is let go of it (AccountBindIdle, T-242), 0 for Settings'
// (settings.BindIdle). Hours outside settings.BindIdleFewest to
// settings.BindIdleMost are refused. Written on the record as it is
// stored then (editStored), so another setting set meanwhile stays.
func SetAccountBindIdle(id, ref string, hours int) error {
	if hours != 0 && (hours < settings.BindIdleFewest || hours > settings.BindIdleMost) {
		return fmt.Errorf("an account lets go of an idle user after %d to %d hours, not %d", settings.BindIdleFewest, settings.BindIdleMost, hours)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts", p.Name)
	}
	k := accountKey(ref)
	if r, ok := p.accountRef(ref); ok {
		k = accountKey(r)
	} else if hours != 0 {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	return editStored(p.ID, func(q *Provider) bool {
		m := normalBindIdle(q.AccountBindIdle)
		if was, had := m[k]; (hours == 0 && !had) || (had && was == hours) {
			return false
		}
		if hours == 0 {
			delete(m, k)
		} else {
			if m == nil {
				m = map[string]int{}
			}
			m[k] = hours
		}
		q.AccountBindIdle = normalBindIdle(m)
		return true
	})
}

// AccountBindIdles is how long a user bound to each account of the
// provider id may be idle, by the account's name in lower case, for the
// accounts that have their own (AccountBindIdle); none for the rest,
// which follow Settings'. Read once a sweep (gateway sweepIdle).
func AccountBindIdles(id string) map[string]time.Duration {
	s, ok := storedPicks(id)
	if !ok || len(s.AccountBindIdle) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(s.AccountBindIdle))
	for u, h := range normalBindIdle(s.AccountBindIdle) {
		out[u] = time.Duration(h) * time.Hour
	}
	return out
}

// normalBindIdle is m as it is kept: keys in lower case, hours held to
// settings.BindIdleFewest to settings.BindIdleMost, accounts with none
// (0 or less) left out; nil for none.
func normalBindIdle(m map[string]int) map[string]int {
	var out map[string]int
	for u, h := range m {
		if u = accountKey(u); u == "" || h <= 0 {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[u] = min(max(h, settings.BindIdleFewest), settings.BindIdleMost)
	}
	return out
}
