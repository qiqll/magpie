package provider

import (
	"fmt"
	"time"

	"github.com/yetone/magpie/internal/renewal"
)

// SetAccountOpened keeps at as when the paid period of the account ref of
// the subscription id was opened (AccountOpened, T-154), zero to take it
// off. A time renewal.CheckOpened refuses is refused. Written on the record
// as it is stored then (editStored), so another setting set meanwhile
// stays; nothing else changes.
func SetAccountOpened(id, ref string, at time.Time) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	if !at.IsZero() {
		if err := renewal.CheckOpened(at, time.Now()); err != nil {
			return err
		}
		at = at.UTC().Truncate(time.Second)
	}
	k := accountKey(r)
	return editStored(p.ID, func(q *Provider) bool {
		m := normalAccountAdded(q.AccountOpened)
		if was, had := m[k]; (at.IsZero() && !had) || (had && was.Equal(at)) {
			return false
		}
		if at.IsZero() {
			delete(m, k)
		} else {
			if m == nil {
				m = map[string]time.Time{}
			}
			m[k] = at
		}
		q.AccountOpened = normalAccountAdded(m)
		return true
	})
}

// AccountOpenedOf is when the paid period of the account user of the
// subscription id was opened (AccountOpened), zero for none.
func AccountOpenedOf(id, user string) time.Time {
	if s, ok := storedPicks(id); ok {
		return s.AccountOpened[accountKey(user)]
	}
	return time.Time{}
}
