package provider

import (
	"regexp"
	"slices"
	"strings"
)

// errNotSignedIn is the Error of an account's card with no sign-in to read it
// by: a Copilot, Google or plugin account whose token is gone.
const errNotSignedIn = "not signed in"

// signedOutCard is a built-in account's card read as signed out until it
// signs in again: a revoked sign-in, a refused Copilot or ChatGPT one, a
// saved one to add again. Not "sign-in has expired" alone: Codex's own
// says that of a token Codex renews as it runs (codexTokenAsKept).
var signedOutCard = regexp.MustCompile(`(?i)sign in again|add the account again`)

// WithUnusable is qs with each account's card that can't be used now told
// so (Unusable), for the Usage page's Overview to leave it out (需求方
// 2026-11-23: 当前周期已经无法使用的账号（被封、未在登录状态）就不要显示了，
// 会占用地方). That is an account the user marked banned or disabled, or
// resting after authentication_failed (banned.go), and one whose card was
// read as signed out: not signed in, a built-in's to sign in again
// (signedOutCard), a plugin's as magpie marks its lapse (signInGone). A
// marked one isn't read (usageLogins), but its card read before the mark
// stands in the usage cache until the next read. The Providers page still
// lists each with its Sign in again or its mark.
//
// The marks are read from logins.json as kept (sharedLogins), with no
// agent's sign-in read: the page asks this every 1.5s while a read is under
// way. A name is unusable only when every saved entry of it is: two Team
// seats of one email are two entries, and one banned leaves the other's
// card in sight (43a65950). qs, which may be the usage cache, is left as
// it is.
func WithUnusable(qs []SubscriptionQuota) []SubscriptionQuota {
	var ls []savedLogin
	listed := false
	plugins := map[string]string{} // a card's provider: its accounts' agent
	var out []SubscriptionQuota
	for i, q := range qs {
		// a remote magpie's card is that one's to say
		if q.User == "" || q.From != "" {
			continue
		}
		agent, ok := plugins[q.Provider]
		if !ok {
			agent = q.Provider
			if pp, plugin := pluginOfAgent(q.Provider); plugin {
				agent = pluginAgent(pp)
			}
			plugins[q.Provider] = agent
		}
		plugin := agent != q.Provider
		off := q.Error == errNotSignedIn || signedOutCard.MatchString(q.Error) || plugin && signInGone.MatchString(q.Error)
		if !off {
			if !listed {
				ls, listed = sharedLogins(), true // only looked at
			}
			off = markedAll(ls, agent, q.User)
		}
		if !off {
			continue
		}
		if out == nil {
			out = slices.Clone(qs)
		}
		out[i].Unusable = true
	}
	if out == nil {
		return qs
	}
	return out
}

// markedAll says agent's account named user has saved entries in ls, and
// every one is marked banned or disabled, or rests after
// authentication_failed.
func markedAll(ls []savedLogin, agent, user string) bool {
	seen := false
	for _, l := range ls {
		if l.Agent != agent || !strings.EqualFold(l.User, user) {
			continue
		}
		if !l.unusable() {
			return false
		}
		seen = true
	}
	return seen
}
