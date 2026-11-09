package provider

// An account the user keeps magpie off, by one of two marks of their own,
// kept on the account's entry in logins.json:
//   - Banned: its vendor banned it (2026-11-08: some Claude Code accounts
//     were banned, and magpie went on sending them requests, renewing their
//     sign-ins and reading their allowance).
//   - Disabled: the user turned it off on purpose (需求方 2026-11-08), the
//     vendor having done nothing.
//
// The two differ only in their name and what they say; every path reads
// them through one predicate (AccountUnusable, UnusableOf), so none checks
// one and forgets the other. Each is the user's own value: nothing magpie
// writes takes it away, not a sign-in again, a refresh, rememberLogins, an
// import or a mover (keepMarks); only the user lifts it, and magpie never
// sets it itself. An account with both stays out until both are lifted.
//
// Such an account is still the account its calls were made on: usage,
// costs and history keep it as they did. What it no longer gets is
// anything new: the gateway never routes to it, not even when no other
// account is left (where a lapsed one is still tried); it is offered for
// no switch and no gateway key; its sign-in isn't kept alive and its
// allowance isn't read.
//
// Disabled beside On and Paused (logins_on.go): an account not On stands
// out of the gateway's rotation only, still kept alive and read, and the
// one the agent is signed in to can't be turned off, only Paused while
// another is on. Disabled stops everything for any account, the one
// signed in to too (the agent stays signed in, magpie sends it nothing),
// and leaves On and Paused as they were, to hold again once it is enabled.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ErrAccountBanned and ErrAccountDisabled are a request asked of an account
// the user marked so: it is never sent.
var (
	ErrAccountBanned   = errors.New("this account is marked banned in magpie; lift the mark in Models › Accounts to use it again")
	ErrAccountDisabled = errors.New("this account is disabled in magpie; enable it in Models › Accounts to use it again")
)

// unusable says the user marked l banned or disabled.
func (l savedLogin) unusable() bool { return l.Banned || l.Disabled }

// Unusable says the user marked the account banned or disabled.
func (l Login) Unusable() bool { return l.Banned || l.Disabled }

// AccountUnusable says the user marked account a banned or disabled.
func AccountUnusable(a *Account) bool {
	return UnusableOf([]*Account{a})[0]
}

// UnusableOf is AccountUnusable for each of as, logins.json read once for
// them all: routing asks it of every account of a provider at every request.
func UnusableOf(as []*Account) []bool {
	out := make([]bool, len(as))
	ls := sharedLogins() // only looked at
	if !slices.ContainsFunc(ls, savedLogin.unusable) {
		return out // none marked: the common case
	}
	for i, a := range as {
		out[i] = markOf(ls, a).unusable()
	}
	return out
}

// markOf is account a's entry in ls when it is marked banned or disabled,
// else none: a plugin's by its key in plugin-auth.json, as its lapse is
// matched (signInLapsedIn), any other by its agent and name in any case.
func markOf(ls []savedLogin, a *Account) savedLogin {
	if a == nil || a.User == "" {
		return savedLogin{}
	}
	if a.plugin != nil {
		if a.pluginKey == "" {
			return savedLogin{}
		}
		agent := pluginAgent(*a.plugin)
		l, _ := loginIn(ls, func(l savedLogin) bool { return l.unusable() && l.Agent == agent && l.Home == a.pluginKey })
		return l
	}
	return loginMarked(ls, a.Agent, a.User)
}

// loginMarked is the saved account of agent named user in ls when it is
// marked banned or disabled, else none.
func loginMarked(ls []savedLogin, agent, user string) savedLogin {
	l, _ := loginIn(ls, func(l savedLogin) bool { return l.unusable() && l.Agent == agent && strings.EqualFold(l.User, user) })
	return l
}

// LoginUnusable says the account of agent (a provider's id: a plugin's
// accounts go by it too) named user is marked banned or disabled.
func LoginUnusable(agent, user string) bool {
	ls := sharedLogins()
	if pp, ok := pluginOfAgent(agent); ok {
		agent = pluginAgent(pp)
	}
	return loginMarked(ls, agent, user).unusable()
}

// UnusableUser says p's account named user (one of AccountIDs') is marked
// banned or disabled: by the account's agent, or a plugin's provider id,
// as AccountIDs lists them.
func (p Provider) UnusableUser(user string) bool {
	if p.Account == nil {
		return false
	}
	agent := p.Account.Agent
	if p.IsPlugin() {
		agent = p.ID
	}
	return LoginUnusable(agent, user)
}

// withoutUnusable is ps less those whose account is marked banned or
// disabled.
func withoutUnusable(ps []Provider) []Provider {
	as := make([]*Account, len(ps))
	for i, p := range ps {
		as[i] = p.Account
	}
	off := UnusableOf(as)
	if !slices.Contains(off, true) {
		return ps
	}
	var out []Provider
	for i, p := range ps {
		if !off[i] {
			out = append(out, p)
		}
	}
	return out
}

// SetLoginBanned marks the account of agent (a provider's id) named user
// banned, or lifts the mark. The account the agent is signed in to can be
// marked too: the agent stays signed in, and magpie sends it nothing.
func SetLoginBanned(agent, user string, banned bool) error {
	return setLoginMark(agent, user, func(l *savedLogin) {
		l.Banned, l.BannedAt = banned, markedAt(banned)
	})
}

// SetLoginDisabled turns the account of agent named user off in magpie, or
// on again, as SetLoginBanned marks it.
func SetLoginDisabled(agent, user string, disabled bool) error {
	return setLoginMark(agent, user, func(l *savedLogin) {
		l.Disabled, l.DisabledAt = disabled, markedAt(disabled)
	})
}

// markedAt is now for a mark set, zero for one lifted.
func markedAt(set bool) time.Time {
	if !set {
		return time.Time{}
	}
	return time.Now().UTC().Truncate(time.Second)
}

// setLoginMark changes one saved entry of the account of agent named user
// by set.
func setLoginMark(agent, user string, set func(*savedLogin)) error {
	if discoveryOff() {
		return errAccountDiscoveryOff
	}
	mark := func(ls []savedLogin, i int) ([]savedLogin, error) {
		set(&ls[i])
		return ls, nil
	}
	if pp, ok := pluginOfAgent(agent); ok {
		return editListedLogin(pluginAgent(pp), user, pluginSide(pp), mark)
	}
	switch agent {
	case "copilot":
		return editListedLogin(agent, user, copilotSide(), mark)
	case ChatGPTAPIID:
		return editListedLogin(agent, user, siwcSide(), mark)
	case "gemini", "antigravity":
		return editListedLogin(agent, user, googleSide(agent), mark)
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	// one entry, the one shown before one hidden, as editSideLogin picks:
	// two seats named alike are two entries, and only the one picked is
	// marked (43a65950: matching every entry of a name took both)
	i := pickSaved(ls, func(l savedLogin) bool { return l.Agent == agent && strings.EqualFold(l.User, user) })
	if i >= 0 {
		ls, _ = mark(ls, i)
	} else {
		// the agent's own sign-in not saved yet: saved now, with the mark
		live, ok := liveLogin(agent)
		if !ok || !strings.EqualFold(live.User, user) {
			return fmt.Errorf("no saved %s account %q", agent, user)
		}
		live.Seen = time.Now().UTC().Truncate(time.Second)
		ls = append(ls, live)
		ls, _ = mark(ls, len(ls)-1)
	}
	return writeLogins(ls)
}

// keepMarks is l with the user's banned and disabled marks of was carried
// over: a sign-in again or a refresh writes the account's entry anew, and
// the marks are the user's to lift.
func keepMarks(l, was savedLogin) savedLogin {
	if was.Banned && !l.Banned {
		l.Banned, l.BannedAt = true, was.BannedAt
	}
	if was.Disabled && !l.Disabled {
		l.Disabled, l.DisabledAt = true, was.DisabledAt
	}
	return l
}

// unusableErr is the refusal of a request asked of l's account, nil for
// one with neither mark: banned said first.
func unusableErr(l savedLogin) error {
	switch {
	case l.Banned:
		return ErrAccountBanned
	case l.Disabled:
		return ErrAccountDisabled
	}
	return nil
}

// errIfUnusable is ErrAccountBanned or ErrAccountDisabled for an account
// marked so, nil for any other: the last check before a request of an
// account's is sent, for every path that didn't leave it out already.
func errIfUnusable(_ context.Context, a *Account) error {
	if a == nil || a.User == "" {
		return nil
	}
	ls := sharedLogins()
	if !slices.ContainsFunc(ls, savedLogin.unusable) {
		return nil
	}
	return unusableErr(markOf(ls, a))
}

// LoginRefusal is the refusal of a request asked of the account of agent
// named user, nil when it has neither mark.
func LoginRefusal(agent, user string) error {
	ls := sharedLogins()
	if pp, ok := pluginOfAgent(agent); ok {
		agent = pluginAgent(pp)
	}
	return unusableErr(loginMarked(ls, agent, user))
}
