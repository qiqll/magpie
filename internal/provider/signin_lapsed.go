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

import "strings"

// SignInLapsed is why account a can't serve a request until it is signed
// in again, as its row says it; "" when it can, or a is no subscription
// account.
func SignInLapsed(a *Account) string {
	if a == nil || a.User == "" {
		return ""
	}
	if a.lapsed != nil {
		return a.lapsed()
	}
	if a.plugin != nil {
		agent := pluginAgent(*a.plugin)
		l, _ := savedLoginOf(func(l savedLogin) bool { return l.Agent == agent && l.Home == a.pluginKey })
		return l.Lapsed
	}
	switch {
	case a.Agent == "claude" && a.token == nil:
		// the account Claude Code itself is signed in to: refused for
		// the credential Claude Code holds now
		return claudeOwnRefused(a.User)
	case a.Agent == "claude":
		if l, ok := savedLoginOf(func(l savedLogin) bool { return l.Agent == "claude" && strings.EqualFold(l.User, a.User) }); ok {
			return claudeSignedOut(l)
		}
		return ""
	case a.token != nil || a.Agent == ChatGPTAPIID:
		// a sign-in magpie keeps and refreshes itself: a refused refresh
		// is kept on it, as a revoked sign-in is
		if l, ok := savedLoginOf(func(l savedLogin) bool { return l.Agent == a.Agent && strings.EqualFold(l.User, a.User) }); ok && l.Lapsed != "" {
			return l.Lapsed
		}
	}
	return revokedWhy(a)
}

// savedLoginOf is the saved login match picks out, the last of them.
func savedLoginOf(match func(savedLogin) bool) (savedLogin, bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	var out savedLogin
	found := false
	for _, l := range readLogins() {
		if match(l) {
			out, found = l, true
		}
	}
	return out, found
}

// revokedWhy is why account a's sign-in was revoked, as SignInMarked
// tells it is; "" when it wasn't.
func revokedWhy(a *Account) string {
	if !RevokedAccount(a) {
		return ""
	}
	revoked.Lock()
	defer revoked.Unlock()
	revokedLoad()
	m, ok := revoked.accounts[revokedKey(a.Agent, a.User)]
	if !ok {
		return ""
	}
	if why := revoked.marks[m]; why != "" {
		return why
	}
	return revokedText(a.User)
}
