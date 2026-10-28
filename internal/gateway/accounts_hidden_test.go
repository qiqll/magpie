package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// What a caller is told names none of magpie's accounts or keys: an
// account by its name or email as a word, a key's name where magpie
// writes one, any email a vendor's own error has. A word that is also a
// key's name, and a plugin's "API key" label, are left as they are.
func TestHideAccounts(t *testing.T) {
	old := namesOfAccounts
	t.Cleanup(func() { namesOfAccounts = old; forgetAccountNames() })
	namesOfAccounts = func() []provider.AccountRef {
		return []provider.AccountRef{
			{User: "me@example.com"}, {User: "team@example.com · Team"}, {User: "alice-work"},
			{User: "key", Key: true}, {User: "Work key", Key: true}, {User: "API key"},
		}
	}
	forgetAccountNames()
	for in, want := range map[string]string{
		"Codex (me@example.com) is at 92%":             "Codex (an account) is at 92%",
		"codex/team@example.com · Team is held":        "codex/an account is held",
		"turn alice-work on again":                     "turn an account on again",
		"openai (Work key): 401":                       "openai (a key): 401",
		`no account "Work key" serves it`:              `no account "a key" serves it`,
		"no API key was sent":                          "no API key was sent",
		"no key was sent, try a key":                   "no key was sent, try a key",
		"the vendor says stranger@corp.io is disabled": "the vendor says an account is disabled",
		"alice-workshop stays":                         "alice-workshop stays",
	} {
		if got := hideAccounts(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	if got := hideAccounts("x"); strings.Contains(got, "account") {
		t.Fatalf("%q", got)
	}
}
