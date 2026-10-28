package gateway

// What the gateway answers a caller never names one of magpie's accounts or
// keys: which account served, was capped, rests or is bound to a user is
// the operator's, kept in the usage log and the routing trace, not the
// caller's. hideAccounts runs where every error leaves for a client
// (writeErrorSaid, streamFailure, each encoder's KError, and the vendor
// error bodies passed on as they came: hideAccountsBody), so a message
// made anywhere, magpie's own or a vendor's, is said without them.
//
// An email is hidden wherever it stands, as an account's name that has an
// @ is. Any other name (a login, a plugin's account id, a key's label) is
// the user's own word, often one a message says for itself ("default",
// "work", "models"), so it is hidden only where magpie writes a name: in
// brackets or quotes, after "@" or a list's ",", or after one of its
// providers' "<id>/".

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

// unnamed are names that say no one: the labels a plugin's account goes
// by when it says nothing of itself (provider.pluginLabels), and the words
// put in a name's place, so a message hidden twice reads the same.
var unnamed = map[string]bool{"api key": true, "signed in": true, "account": true, "an account": true, "key": true, "a key": true}

// accountNamesTTL is how long the names are kept before they are read
// again; meanwhile an email is hidden by anEmail all the same.
const accountNamesTTL = 5 * time.Second

// hiders are the patterns of one reading of the names.
type hiders struct {
	at      time.Time
	words   *regexp.Regexp  // names with an @, as whole words
	placed  *regexp.Regexp  // other names, where magpie writes a name
	keyName map[string]bool // which names are keys', by their lower case
}

var (
	hidersNow atomic.Pointer[hiders]
	hidersMu  sync.Mutex // one reading at a time; never held by a reader
)

// account names and provider ids, for tests to replace
var (
	namesOfAccounts = func() []provider.AccountRef {
		var out []provider.AccountRef
		for _, p := range provider.All() {
			out = append(out, p.AccountIDs()...)
		}
		return out
	}
	providerIDs = func() []string {
		var out []string
		for _, p := range provider.All() {
			out = append(out, p.ID)
		}
		return out
	}
)

// currentHiders is the last reading of the names: read again once it is
// old, by one caller, the others meanwhile going on with the old one.
func currentHiders() *hiders {
	h := hidersNow.Load()
	if h != nil && time.Since(h.at) < accountNamesTTL {
		return h
	}
	if h == nil {
		hidersMu.Lock() // the first: nothing to go on with
	} else if !hidersMu.TryLock() {
		return h
	}
	defer hidersMu.Unlock()
	if n := hidersNow.Load(); n != nil && time.Since(n.at) < accountNamesTTL {
		return n
	}
	n := readHiders()
	hidersNow.Store(n)
	return n
}

func readHiders() *hiders {
	h := &hiders{at: time.Now(), keyName: map[string]bool{}}
	pids := map[string]bool{}
	var ids []string
	for _, id := range providerIDs() {
		if id != "" && !pids[strings.ToLower(id)] {
			pids[strings.ToLower(id)] = true
			ids = append(ids, regexp.QuoteMeta(id))
		}
	}
	seen := map[string]bool{}
	var words, placed []string
	for _, r := range namesOfAccounts() {
		n := strings.TrimSpace(r.User)
		l := strings.ToLower(n)
		// too short to tell from a word, a label that names no one, or a
		// provider's own id: passed over
		if len([]rune(n)) < 3 || seen[l] || unnamed[l] || pids[l] {
			continue
		}
		seen[l] = true
		h.keyName[l] = r.Key
		if strings.Contains(n, "@") {
			words = append(words, n)
		} else {
			placed = append(placed, n)
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
	if len(words) > 0 {
		h.words = regexp.MustCompile(`(?i)(^|[^\pL\pN_@.\-])(` + alt(words) + `)($|[^\pL\pN_@\-])`)
	}
	if len(placed) > 0 {
		left := `[("'@,]\s*`
		if len(ids) > 0 {
			left = `(?:[("'@,]\s*|\b(?:` + strings.Join(ids, "|") + `)/)`
		}
		h.placed = regexp.MustCompile(`(?i)(^|` + left + `)(` + alt(placed) + `)($|[)"',;:.!?\s])`)
	}
	return h
}

// forgetAccountNames has the next error read the names again.
func forgetAccountNames() {
	hidersMu.Lock()
	hidersNow.Store(nil)
	hidersMu.Unlock()
}

// replaceNames says each name re finds as an account or a key, keeping
// what borders it. A border is taken by the match before it, so two names
// a separator apart ("a,b") need another pass; the words said in their
// place are no name (unnamed), so the passes end.
func (h *hiders) replaceNames(re *regexp.Regexp, msg string) string {
	for range 8 {
		next := re.ReplaceAllStringFunc(msg, func(m string) string {
			sub := re.FindStringSubmatch(m)
			said := accountSaid
			if h.keyName[strings.ToLower(sub[2])] {
				said = keySaid
			}
			return sub[1] + said + sub[3]
		})
		if next == msg {
			break
		}
		msg = next
	}
	return msg
}

// hideAccounts is msg with every account and key of magpie's, and any
// email address, said as "an account" or "a key".
func hideAccounts(msg string) string {
	if msg == "" {
		return msg
	}
	h := currentHiders()
	if h.words != nil {
		msg = h.replaceNames(h.words, msg)
	}
	if h.placed != nil {
		msg = h.replaceNames(h.placed, msg)
	}
	return anEmail.ReplaceAllString(msg, accountSaid)
}

// hideAccountsBody is a vendor's error body as hideAccounts says it. An
// email and the words put in a name's place need no escaping in JSON, so
// the body stays as valid as it came.
func hideAccountsBody(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return []byte(hideAccounts(string(b)))
}

// hideErrorEvents hides the accounts in the error events of a stream
// passed on as it came (an SSE data line whose JSON is an error), leaving
// every other line, the model's own text among them, as it is. A line
// cut across two reads is passed as it is.
func hideErrorEvents(b []byte) []byte {
	s := string(b)
	if !strings.Contains(s, `"error"`) && !strings.Contains(s, "response.failed") {
		return b
	}
	lines := strings.SplitAfter(s, "\n")
	changed := false
	for i, ln := range lines {
		data, ok := strings.CutPrefix(ln, "data:")
		if !ok || !strings.HasSuffix(ln, "\n") {
			continue
		}
		d := strings.TrimSpace(data)
		if !strings.HasPrefix(d, "{") || !(strings.Contains(d, `"type":"error"`) || strings.Contains(d, `"type": "error"`) ||
			strings.Contains(d, "response.failed") || strings.HasPrefix(d, `{"error"`)) {
			continue
		}
		if h := hideAccounts(ln); h != ln {
			lines[i], changed = h, true
		}
	}
	if !changed {
		return b
	}
	return []byte(strings.Join(lines, ""))
}
