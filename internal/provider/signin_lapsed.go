package provider

// An account whose sign-in has to be made again before it can serve. Each
// kind of subscription keeps its own mark of it: Anthropic refusing a
// Claude account's credential (claude_auth.go, claude_dirs.go), a saved
// ChatGPT account's refresh refused (savedRefreshFailed), a ChatGPT API
// sign-in's refresh refused (siwcFresh), GitHub refusing a Copilot token
// (copilot_lapsed.go), a plugin's vendor answering 401
// (notePluginLapseOf), and a vendor revoking the credential a request
// went with (signin_revoked.go). Its row says Sign-in required for each.
// The gateway used to try such an account on every request all the
// same, first when routing put it first, so its error could be all the
// agent was told though another account of the provider would answer.
// SignInLapsed reads every kind's mark as its row reads it, for the
// gateway to leave the account out while another can take the request;
// signed in again (or the mark cleared as its kind clears it), it is back.
// Claude Code's own account is the one exception: it is read for the
// credential Claude Code holds now (claudeOwnRefused), as Token reads it,
// not its saved copy's, which can lag behind.
//
// A saved ChatGPT account's refused refresh is tried again every hour
// (KeepLoginsAlive: a refusal doesn't move Renewed, so it stays due), and
// one that goes through clears it, though routing no longer asks it.

import (
	"slices"
	"strings"
)

// SignInLapsed is why account a can't serve a request until it is signed
// in again, as its row says it; "" when it can, or a is no subscription
// account.
func SignInLapsed(a *Account) string {
	return SignInLapsedOf([]*Account{a})[0]
}

// SignInLapsedOf is SignInLapsed for each of as, logins.json read once
// for them all: routing asks it of every account of a provider at every
// request.
func SignInLapsedOf(as []*Account) []string {
	out := make([]string, len(as))
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	if !slices.ContainsFunc(ls, func(l savedLogin) bool { return l.Lapsed != "" }) && !revokedKnown() {
		return out // nothing marked: the common case, no more to read
	}
	for i, a := range as {
		out[i] = signInLapsedIn(ls, a)
	}
	return out
}

func signInLapsedIn(ls []savedLogin, a *Account) string {
	if a == nil || a.User == "" {
		return ""
	}
	if a.lapsed != nil {
		return a.lapsed(ls)
	}
	if a.plugin != nil {
		if a.pluginKey == "" {
			return "" // no account of the plugin's to match
		}
		agent := pluginAgent(*a.plugin)
		l, _ := loginIn(ls, func(l savedLogin) bool { return l.Agent == agent && l.Home == a.pluginKey })
		return l.Lapsed
	}
	switch {
	case a.Agent == "claude" && a.token == nil:
		// the account Claude Code itself is signed in to: refused for
		// the credential Claude Code holds now
		return claudeOwnRefusedIn(ls, a.User)
	case a.Agent == "claude":
		if l, ok := loginIn(ls, func(l savedLogin) bool { return l.Agent == "claude" && strings.EqualFold(l.User, a.User) }); ok {
			return claudeSignedOut(l)
		}
		return ""
	case a.token != nil || a.Agent == ChatGPTAPIID:
		// a sign-in magpie keeps and refreshes itself: a refused refresh
		// is kept on it, as a revoked sign-in is
		if l, ok := loginIn(ls, func(l savedLogin) bool { return l.Agent == a.Agent && strings.EqualFold(l.User, a.User) }); ok && l.Lapsed != "" {
			return l.Lapsed
		}
	}
	return revokedWhy(ls, a)
}

// loginIn is the saved login of ls match picks out, the last of them.
func loginIn(ls []savedLogin, match func(savedLogin) bool) (savedLogin, bool) {
	var out savedLogin
	found := false
	for _, l := range ls {
		if match(l) {
			out, found = l, true
		}
	}
	return out, found
}

// revokedKnown says some credential is known revoked.
func revokedKnown() bool {
	revoked.Lock()
	defer revoked.Unlock()
	revokedLoad()
	return len(revoked.accounts) > 0
}

// revokedWhy is why account a's sign-in was revoked, as SignInMarked
// tells it is; "" when it wasn't. A saved login that no longer holds the
// mark, or whose lapse a refresh since cleared (renewSaved), is not
// revoked, as its row reads it (signed in again in magpie, or renewed by
// the agent itself), whatever was kept of it here: the mark was only ever cleared
// by a request it serves, which routing no longer sends it.
func revokedWhy(ls []savedLogin, a *Account) string {
	if !RevokedAccount(a) {
		return ""
	}
	revoked.Lock()
	revokedLoad()
	m, ok := revoked.accounts[revokedKey(a.Agent, a.User)]
	why := revoked.marks[m]
	revoked.Unlock()
	if !ok {
		return ""
	}
	if l, saved := loginIn(ls, func(l savedLogin) bool { return l.Agent == a.Agent && strings.EqualFold(l.User, a.User) }); saved && (l.Refused != revokedPrefix+m || l.Lapsed == "") {
		return ""
	}
	if why != "" {
		return why
	}
	return revokedText(a.User)
}
