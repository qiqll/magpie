package gateway

// What the gateway answers a caller never names one of magpie's accounts or
// keys: which account served, was capped, rests or is bound to a user is
// the operator's, kept in the usage log and the routing trace, not the
// caller's. hideAccounts runs where every error leaves for a client
// (writeErrorSaid, streamError, streamFailure, each encoder's KError), so a
// message made anywhere, magpie's own or a vendor's, is said without them.

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// accountSaid and keySaid stand in a message for an account and a key.
const (
	accountSaid = "an account"
	keySaid     = "a key"
)

// anEmail is any email address: a vendor's own error may name the account
// by it, one magpie doesn't list too.
var anEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// accountNames are the names magpie's accounts and keys go by, read again
// at most every accountNamesTTL: an error is said far more often than an
// account is added.
var accountNames struct {
	sync.Mutex
	at       time.Time
	re, keys *regexp.Regexp // accounts' names as words; keys' as magpie writes them
}

const accountNamesTTL = 30 * time.Second

// unnamed are the labels a plugin's account goes by when it says nothing
// of itself (provider.pluginLabels): they name no one, and are words a
// message says for itself ("no API key was sent").
var unnamed = map[string]bool{"api key": true, "signed in": true}

// namesOfAccounts lists every account and key name (provider.AccountIDs),
// for tests to replace.
var namesOfAccounts = func() []provider.AccountRef {
	var out []provider.AccountRef
	for _, p := range provider.All() {
		out = append(out, p.AccountIDs()...)
	}
	return out
}

// accountsMatcher is the patterns matching an account's name as a whole
// word, and a key's where magpie writes one: in brackets, in quotes or
// after a provider's "/". A key's name is the user's own label, often a
// word ("key", "work") a message says for itself.
func accountsMatcher() (accounts, keys *regexp.Regexp) {
	accountNames.Lock()
	defer accountNames.Unlock()
	if !accountNames.at.IsZero() && time.Since(accountNames.at) < accountNamesTTL {
		return accountNames.re, accountNames.keys
	}
	seen := map[string]bool{}
	var accts, ks []string
	for _, r := range namesOfAccounts() {
		n := strings.TrimSpace(r.User)
		// a name too short to tell from a word, or a plugin's label for an
		// account it names nothing of (pluginLabels), is passed over
		if len([]rune(n)) < 3 || seen[strings.ToLower(n)] || unnamed[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		if r.Key {
			ks = append(ks, n)
		} else {
			accts = append(accts, n)
		}
	}
	alt := func(names []string) string {
		sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
		q := make([]string, len(names))
		for i, n := range names {
			q[i] = regexp.QuoteMeta(n)
		}
		return strings.Join(q, "|")
	}
	if len(accts) > 0 {
		accounts = regexp.MustCompile(`(?i)(^|[^\pL\pN_@.\-])(` + alt(accts) + `)($|[^\pL\pN_@\-])`)
	}
	if len(ks) > 0 {
		keys = regexp.MustCompile(`(?i)([(\"'/])(` + alt(ks) + `)([)\"',;:]|$|\s)`)
	}
	accountNames.re, accountNames.keys, accountNames.at = accounts, keys, time.Now()
	return accounts, keys
}

// forgetAccountNames has the next error read the names again.
func forgetAccountNames() {
	accountNames.Lock()
	accountNames.re, accountNames.keys, accountNames.at = nil, nil, time.Time{}
	accountNames.Unlock()
}

// hideAccounts is msg with every account and key of magpie's, and any
// email address, said as "an account" or "a key".
func hideAccounts(msg string) string {
	if msg == "" {
		return msg
	}
	accounts, keys := accountsMatcher()
	if keys != nil {
		msg = keys.ReplaceAllString(msg, "${1}"+keySaid+"${3}")
	}
	if accounts != nil {
		msg = accounts.ReplaceAllString(msg, "${1}"+accountSaid+"${3}")
	}
	return anEmail.ReplaceAllString(msg, accountSaid)
}
