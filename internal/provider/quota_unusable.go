package provider

import (
	"slices"
	"strings"
)

// errNotSignedIn is the Error of an account's card with no sign-in to read it
// by: a Copilot, Google or plugin account whose token is gone.
const errNotSignedIn = "not signed in"

// WithUnusable is qs with each account's card that can't be used now told
// so (Unusable), for the Usage page's Overview to leave it out (需求方
// 2026-11-23: 当前周期已经无法使用的账号（被封、未在登录状态）就不要显示了，
// 会占用地方). That is an account the user marked banned or disabled, one
// resting after authentication_failed (banned.go), and one signed out until
// it signs in again (Login.Lapsed, or a card read as not signed in). A
// marked one isn't read (usageLogins), but its card read before the mark
// stands in the usage cache until the next read. The Providers page still
// lists each with its Sign in again or its mark.
//
// The accounts are listed once for all cards (Logins("")); qs, which may be
// the usage cache, is left as it is.
func WithUnusable(qs []SubscriptionQuota) []SubscriptionQuota {
	var ls []Login
	listed := false
	var out []SubscriptionQuota
	for i, q := range qs {
		// a remote magpie's card is that one's to say
		if q.User == "" || q.From != "" {
			continue
		}
		off := q.Error == errNotSignedIn
		if !off {
			if !listed {
				ls, listed = Logins(""), true
			}
			off = slices.ContainsFunc(ls, func(l Login) bool {
				return l.Agent == q.Provider && strings.EqualFold(l.User, q.User) && (l.Unusable() || l.Lapsed != "")
			})
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
