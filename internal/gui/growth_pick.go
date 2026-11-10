package gui

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// pickedPolicyError is a save of the growth policies refused because it
// deletes the policy Policy (its name) that the subscriptions or accounts
// Subs picked: ids until namePickedSubs names them, a subscription's
// account as "subscription · account".
type pickedPolicyError struct {
	Policy string
	Subs   []string
}

func (e *pickedPolicyError) Error() string {
	return fmt.Sprintf("%s picked the growth policy %q: pick another in its editor, or on its account's row in Models › Accounts, before deleting this one", strings.Join(e.Subs, ", "), e.Policy)
}

// growthPickedKept refuses a save of the user's growth policies (next)
// that deletes one of was a subscription picked (T-131), or an account of
// one (T-133): its accounts would go to the default, or to their
// subscription's, with nobody having said so. The subscription picks
// another in its editor first, an account on its row. providers.json that can't be read is
// refused too: whether one is picked is unknown. It runs inside
// settings.EditGrowth, under the settings file's lock, so it reads
// providers.json alone, nothing that reads the settings (provider.Find
// does): the subscriptions are named once the lock is let go
// (namePickedSubs).
func growthPickedKept(was, next []settings.GrowthPolicy) error {
	var gone []settings.GrowthPolicy
	for _, p := range was {
		if !slices.ContainsFunc(next, func(n settings.GrowthPolicy) bool { return n.ID == p.ID }) {
			gone = append(gone, p)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	picks, err := provider.GrowthPicks()
	if err != nil {
		return fmt.Errorf("which subscriptions picked a growth policy can't be read, so none is deleted: %w", err)
	}
	for _, p := range gone {
		var subs []string
		for _, pick := range picks {
			if pick.Policy != p.ID {
				continue
			}
			who := pick.Provider
			if pick.Account != "" {
				who += pickedAccountSep + pick.Account
			}
			subs = append(subs, who)
		}
		if len(subs) > 0 {
			return &pickedPolicyError{Policy: p.Name, Subs: subs}
		}
	}
	return nil
}

// pickedAccountSep parts a subscription from its account in a
// pickedPolicyError's Subs.
const pickedAccountSep = " · "

// namePickedSubs is err with the subscriptions a pickedPolicyError names
// by their names, read outside the settings file's lock; err as it is
// otherwise.
func namePickedSubs(err error) error {
	var pe *pickedPolicyError
	if !errors.As(err, &pe) {
		return err
	}
	named := &pickedPolicyError{Policy: pe.Policy}
	for _, who := range pe.Subs {
		sub, account, _ := strings.Cut(who, pickedAccountSep)
		name := sub
		if q, err := provider.Find(sub); err == nil {
			name = cmp.Or(q.Name, sub)
			if account != "" {
				account = cmp.Or(q.AccountName(account), account)
			}
		}
		if account != "" {
			name += pickedAccountSep + account
		}
		named.Subs = append(named.Subs, name)
	}
	return named
}
