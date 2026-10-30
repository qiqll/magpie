package provider

// What the log says of sign-ins, one line a step, every line of one
// layout so a sign-in or an account is found by grep:
//
//	sign-in <provider> (<session>) <result>[ account <account>][ HTTP <status>][: <reason>]
//	sign-in: <provider> account <account> has to be signed in again (<why>)[ HTTP <status>]: <what found it>
//	sign-in: <provider> account <account> is signed in again: <how>
//	sign-in: <provider> account <account> refresh failed[ HTTP <status>]: <reason>
//
// The first is a sign-in's start and its end (done, failed or canceled);
// the others are an account entering and leaving "Sign-in required", as
// its row says it, and a refresh of its token that didn't go through. The
// gateway's own lines (user accounts:, gateway:) say what it did with a
// request on such an account, as "<provider> account <account> has to be
// signed in again (<why>)" too, and aren't said again here.
//
// None of them holds a token, a code or a sign-in's address: a reason
// from outside magpie that may hold one (logReason) is left out.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// logSignIn logs a step of the sign-in st: result is started, done, failed
// or canceled; status is the vendor's HTTP status that said so, 0 for none
// known; why is the reason, from outside magpie, "" for none.
func logSignIn(st SignInState, result string, status int, why string) {
	line := signInLine(st, result, status)
	if why != "" {
		line += ": " + logReason(why)
	}
	log.Print(line)
}

// signInLine is a sign-in's log line up to its reason.
func signInLine(st SignInState, result string, status int) string {
	session := st.ID
	if session == "" {
		session = "key" // a key, signed in with at once (PluginAPIKey)
	}
	line := fmt.Sprintf("sign-in %s (%s) %s", st.Agent, session, result)
	if st.User != "" {
		line += " account " + logName(st.User)
	}
	if status != 0 {
		line += fmt.Sprintf(" HTTP %d", status)
	}
	return line
}

// logReason is a reason from outside magpie (a vendor, a plugin, a CLI) as
// a log line keeps it: quoted on one line (logText), or, when it may hold
// a token, a code or an address (one of the sign-in's, its code in it),
// left out and saying so.
func logReason(why string) string {
	if logSecret.MatchString(why) {
		return "(left out: it may hold the sign-in's code)"
	}
	return logText(why)
}

// SignInSaid is a sign-in's failure reason as the page says it: as it is,
// unless it may hold a token, a code or an address (logSecret, as the log
// leaves it out) or one of secrets (the key or answer the user gave it,
// 8 characters or more, said back by its vendor or plugin); then a
// sentence saying it is left out, with the vendor's HTTP status when
// status names one.
//
// magpie's own words naming a bare scheme or a loopback address with no
// path ("paste the address … starting with http://", "proxy must look like
// http://127.0.0.1:7890") hold nothing of a sign-in's, and are said.
func SignInSaid(why string, status int, secrets ...string) string {
	said, _ := signInHeld(why, status, SignInSecrets{Given: secrets})
	return said
}

// signInHeld is why as SignInSaid says it, and key the i18n.js key of the
// sentence said in its place ({status} filled from status), "" when it is
// said as it is.
func signInHeld(why string, status int, secrets SignInSecrets) (said, key string) {
	if !logSecret.MatchString(plainAddress.ReplaceAllString(why, " ")) && !secrets.heldIn(why) {
		return why, ""
	}
	key = SignInHeld
	if status != 0 {
		key = SignInHeldStatus
	}
	return strings.ReplaceAll(key, "{status}", strconv.Itoa(status)), key
}

// SignInHeld and SignInHeldStatus are what the page says in place of a
// sign-in's failure reason that may hold its code or key: i18n.js keys,
// the second with the vendor's HTTP status.
const (
	SignInHeld       = "The answer is left out here, as it may hold the sign-in's code or key"
	SignInHeldStatus = "The vendor answered HTTP {status}; its answer is left out here, as it may hold the sign-in's code or key"
)

// SignInSecrets are what the user gave a sign-in, which its failure reason
// never says back: Given (a pasted code or address, a key, an answer
// typed) once it is 8 characters or more, Hidden (an answer typed unseen,
// or one its plugin marks secret) at any length. Each is looked for as
// it is, escaped for an address or quoted, and by its first 12 characters
// (a vendor's echo cut short).
type SignInSecrets struct {
	Given, Hidden []string
}

// With is s and o, both.
func (s SignInSecrets) With(o SignInSecrets) SignInSecrets {
	return SignInSecrets{slices.Concat(s.Given, o.Given), slices.Concat(s.Hidden, o.Hidden)}
}

func (s SignInSecrets) heldIn(why string) bool {
	for i, vs := range [][]string{s.Given, s.Hidden} {
		for _, v := range vs {
			if v = strings.TrimSpace(v); v != "" && (i == 1 || len(v) >= 8) && saysBack(why, v) {
				return true
			}
		}
	}
	return false
}

// saysBack says whether why holds v, as it is or as an address or a JSON
// or Go string would carry it, or its first 12 characters.
func saysBack(why, v string) bool {
	q := strconv.Quote(v)
	forms := []string{v, url.QueryEscape(v), q[1 : len(q)-1]}
	if len(v) >= 12 {
		forms = append(forms, v[:12])
	}
	for _, f := range forms {
		if strings.Contains(why, f) {
			return true
		}
	}
	return false
}

// PastedSecrets are what a pasted code or address may hold: the text
// itself, each part of it between #s (Claude's code#state), and each value
// of its query and fragment.
func PastedSecrets(raw string) SignInSecrets {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SignInSecrets{}
	}
	given := append([]string{raw}, strings.Split(raw, "#")...)
	if u, err := url.Parse(raw); err == nil {
		for _, part := range []string{u.RawQuery, u.Fragment} {
			// a value read before a bad pair is kept
			q, _ := url.ParseQuery(part)
			for _, vs := range q {
				given = append(given, vs...)
			}
		}
	}
	return SignInSecrets{Given: given}
}

// heldErr is a sign-in's error with what the user gave the sign-in, for
// SignInFailSaid to leave out.
type heldErr struct {
	error
	secrets SignInSecrets
}

func (e heldErr) Unwrap() error { return e.error }

// plainAddress is an address that can hold no secret: a scheme alone, or
// one to this computer by its loopback name and port, nothing after it.
var plainAddress = regexp.MustCompile(`(?i)\b(?:https?|socks5h?)://(?:(?:127\.0\.0\.1|localhost|\[::1\])(?::[0-9]{1,5})?)?/?(?:[\s,;)]|$)`)

// SignInFailSaid is err as the page may say it (SignInSaid), secrets and
// what the sign-in it ended was given left out; key and status as
// signInHeld has them, for the page to say it in the reader's language.
func SignInFailSaid(err error, secrets SignInSecrets) (said, key string, status int) {
	var h heldErr
	if errors.As(err, &h) {
		secrets = secrets.With(h.secrets)
	}
	status = httpStatusOf(err)
	said, key = signInHeld(err.Error(), status, secrets)
	return said, key, status
}

// Said is st as the page is told it: its failure reason as SignInSaid
// says it, what its sign-in was given left out, and the key of the
// sentence said in its place.
func (st SignInState) Said() SignInState {
	if st.Error != "" {
		st.Error, st.SaidKey = signInHeld(st.Error, st.status, st.secrets)
		if st.SaidKey == SignInHeldStatus {
			st.SaidStatus = st.status
		}
	}
	return st
}

// logSecret matches a reason that may hold a secret: what claudeSaidSecret
// does, and a token by its vendor's shape (a JWT, a bearer, sk-, gho_,
// Google's AIza and ya29.), a field naming a key, a state or a password,
// a JSON code, any run of 40 word characters, and a proxy's address with
// its user and password (socks5://u:p@host). A vendor's words that
// only name a token ("invalid_grant: Token has been expired or revoked")
// are kept.
var logSecret = regexp.MustCompile(claudeSaidSecret.String() +
	`|\beyJ[\w-]{8,}\.[\w-]{8,}` +
	`|(?i:\b(bearer|basic)\s+[\w.~+/=-]{8,})` +
	`|\b(sk|rk|pk)-[\w-]{8,}|\bgh[pousr]_\w{16,}|\bgithub_pat_\w{16,}` +
	`|\bAIza[\w-]{20,}|\bya29\.[\w-]{8,}|\bAKIA[0-9A-Z]{16}|\bxox[abprs]-[\w-]{8,}` +
	`|(?i:\b(state|api_?key|apikey|key|password|passwd|device_code|assertion|verifier|authorization)"?\s*[:=])` +
	`|"code"\s*:\s*"[^"]{20,}"` +
	`|[\w-]{40,}` +
	`|\b\w+://[^/\s@]+:[^/\s@]+@`)

// logName is an account's name as a log line has it: as it is, for grep,
// unless it holds a space or a control character, which could start a
// line of its own; quoted then.
func logName(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f || unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return strconv.Quote(s)
	}
	return s
}

// runLogged runs a log line kept for after a lock is let go, if any.
func runLogged(f func()) {
	if f != nil {
		f()
	}
}

// httpStatusOf is the vendor's HTTP status in a sign-in's or refresh's
// error, 0 when it carries none.
func httpStatusOf(err error) int {
	var tr *tokenRefused
	if errors.As(err, &tr) {
		return tr.status
	}
	var te *siwcTokenError
	if errors.As(err, &te) {
		return te.Status
	}
	return 0
}

// failedWith is a sign-in's end on err, its status kept for the log.
func failedWith(err error) SignInState {
	return SignInState{State: "failed", Error: err.Error(), status: httpStatusOf(err)}
}

// logLapsed logs that the account of agent and user has to be signed in
// again, why (magpie's words, as its row says them), status the HTTP status
// that said so (0 for none known) and by what found it. Called only when
// the mark is new, never under loginsMu: the log's writer can block (a
// journal's pipe).
func logLapsed(agent, user, why string, status int, by string) {
	line := fmt.Sprintf("sign-in: %s account %s has to be signed in again (%s)", agent, logName(user), why)
	if status != 0 {
		line += fmt.Sprintf(" HTTP %d", status)
	}
	log.Print(line + ": " + by)
}

// logUnlapsed logs that the account of agent and user, marked to be signed
// in again, isn't any more, how. A sign-in done says it on its own line.
func logUnlapsed(agent, user, how string) {
	log.Printf("sign-in: %s account %s is signed in again: %s", agent, logName(user), how)
	refreshFailedSaid.Delete(refreshKey(agent, user))
}

// refreshFailedSaid is the last refresh failure logged of each account,
// for the same again to be logged once in refreshFailedQuiet, not once a
// request: an account whose refresh doesn't go through is asked again by
// every request until it does.
var refreshFailedSaid sync.Map

type refreshFailure struct {
	why string
	at  time.Time
}

// refreshFailedQuiet is how long the same refresh failure of an account
// isn't logged again.
const refreshFailedQuiet = 5 * time.Minute

// digits are the numbers in a failure's words.
var digits = regexp.MustCompile(`[0-9]+`)

// rootErr is the error err wraps at its end.
func rootErr(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// refreshKey is an account's key in refreshFailedSaid.
func refreshKey(agent, user string) string { return strings.ToLower(agent + "\x00" + user) }

// logRefreshFailed logs that a refresh of the account of agent and user
// didn't go through, err saying why.
func logRefreshFailed(agent, user string, err error) {
	k := refreshKey(agent, user)
	if err == nil {
		refreshFailedSaid.Delete(k) // a failure after this is logged at once
		return
	}
	if errors.Is(err, context.Canceled) {
		return // the request that asked went away: nothing said of the sign-in
	}
	// the reason as logged, left out when it may hold a secret: the same
	// failure is the same status, error and logged words but for their
	// numbers (a dial's port, a count, a time from one try to the next)
	why, status, now := logReason(err.Error()), httpStatusOf(err), time.Now()
	same := fmt.Sprintf("%d %T %s", status, rootErr(err), digits.ReplaceAllString(why, "#"))
	if was, ok := refreshFailedSaid.Load(k); ok {
		if f := was.(refreshFailure); f.why == same && now.Sub(f.at) < refreshFailedQuiet {
			return
		}
	}
	refreshFailedSaid.Store(k, refreshFailure{same, now})
	line := fmt.Sprintf("sign-in: %s account %s refresh failed", agent, logName(user))
	if status != 0 {
		line += fmt.Sprintf(" HTTP %d", status)
	}
	log.Print(line + ": " + why)
}
