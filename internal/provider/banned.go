package provider

// An account its vendor banned, as the user marked it (2026-11-08: some
// Claude Code accounts were banned, and magpie went on sending them
// requests, renewing their sign-ins and reading their allowance). Banned is
// the user's own mark, kept on the account's entry in logins.json
// (savedLogin.Banned): nothing magpie writes takes it away, not a sign-in
// again, a refresh, rememberLogins, an import or a mover; only the user
// lifts it. magpie never sets it itself.
//
// A banned account is still the account its calls were made on: usage,
// costs and history keep it as they did. What it no longer gets is
// anything new: the gateway never routes to it, not even when no other
// account is left (where a lapsed one is still tried); it is offered for
// no switch and no gateway key; its sign-in isn't kept alive and its
// allowance isn't read.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ErrAccountBanned is a request asked of an account the user marked banned:
// it is never sent.
var ErrAccountBanned = errors.New("this account is marked banned in magpie; lift the mark in Models › Accounts to use it again")

// AccountBanned says the user marked account a banned.
func AccountBanned(a *Account) bool {
	return BannedOf([]*Account{a})[0]
}

// BannedOf is AccountBanned for each of as, logins.json read once for them
// all: routing asks it of every account of a provider at every request.
func BannedOf(as []*Account) []bool {
	out := make([]bool, len(as))
	ls := sharedLogins() // only looked at
	if !slices.ContainsFunc(ls, func(l savedLogin) bool { return l.Banned }) {
		return out // none marked: the common case
	}
	for i, a := range as {
		out[i] = bannedIn(ls, a)
	}
	return out
}

// bannedIn says account a's entry in ls is marked banned: a plugin's by
// its key in plugin-auth.json, as its lapse is matched (signInLapsedIn),
// any other by its agent and name in any case.
func bannedIn(ls []savedLogin, a *Account) bool {
	if a == nil || a.User == "" {
		return false
	}
	if a.plugin != nil {
		if a.pluginKey == "" {
			return false
		}
		agent := pluginAgent(*a.plugin)
		return slices.ContainsFunc(ls, func(l savedLogin) bool { return l.Banned && l.Agent == agent && l.Home == a.pluginKey })
	}
	return LoginBannedIn(ls, a.Agent, a.User)
}

// LoginBannedIn says the saved account of agent named user is marked
// banned in ls.
func LoginBannedIn(ls []savedLogin, agent, user string) bool {
	return slices.ContainsFunc(ls, func(l savedLogin) bool {
		return l.Banned && l.Agent == agent && strings.EqualFold(l.User, user)
	})
}

// LoginBanned says the account of agent (a provider's id: a plugin's
// accounts go by it too) named user is marked banned.
func LoginBanned(agent, user string) bool {
	ls := sharedLogins()
	if pp, ok := pluginOfAgent(agent); ok {
		agent = pluginAgent(pp)
	}
	return LoginBannedIn(ls, agent, user)
}

// BannedUser says p's account named user (one of AccountIDs') is marked
// banned: by the account's agent, or a plugin's provider id, as AccountIDs
// lists them.
func (p Provider) BannedUser(user string) bool {
	if p.Account == nil {
		return false
	}
	agent := p.Account.Agent
	if p.IsPlugin() {
		agent = p.ID
	}
	return LoginBanned(agent, user)
}

// withoutBanned is ps less those whose account is marked banned.
func withoutBanned(ps []Provider) []Provider {
	as := make([]*Account, len(ps))
	for i, p := range ps {
		as[i] = p.Account
	}
	banned := BannedOf(as)
	if !slices.Contains(banned, true) {
		return ps
	}
	var out []Provider
	for i, p := range ps {
		if !banned[i] {
			out = append(out, p)
		}
	}
	return out
}

// SetLoginBanned marks the account of agent (a provider's id) named user
// banned, or lifts the mark. The account the agent is signed in to can be
// marked too: the agent stays signed in, and magpie sends it nothing.
func SetLoginBanned(agent, user string, banned bool) error {
	if discoveryOff() {
		return errAccountDiscoveryOff
	}
	mark := func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Banned = banned
		ls[i].BannedAt = time.Time{}
		if banned {
			ls[i].BannedAt = time.Now().UTC().Truncate(time.Second)
		}
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

// keepBanned is l with the user's banned mark of was carried over: a
// sign-in again or a refresh writes the account's entry anew, and the mark
// is the user's to lift.
func keepBanned(l, was savedLogin) savedLogin {
	if was.Banned && !l.Banned {
		l.Banned, l.BannedAt = true, was.BannedAt
	}
	return l
}

// errIfBanned is ErrAccountBanned for an account marked banned, nil for
// any other: the last check before a request of an account's is sent, for
// every path that didn't leave it out already.
func errIfBanned(_ context.Context, a *Account) error {
	if a != nil && a.User != "" && AccountBanned(a) {
		return ErrAccountBanned
	}
	return nil
}
