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
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
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
		line += " account " + st.User
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
	if claudeSaidSecret.MatchString(why) {
		return "(left out: it may hold the sign-in's code)"
	}
	return logText(why)
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
	line := fmt.Sprintf("sign-in: %s account %s has to be signed in again (%s)", agent, user, why)
	if status != 0 {
		line += fmt.Sprintf(" HTTP %d", status)
	}
	log.Print(line + ": " + by)
}

// logUnlapsed logs that the account of agent and user, marked to be signed
// in again, isn't any more, how. A sign-in done says it on its own line.
func logUnlapsed(agent, user, how string) {
	log.Printf("sign-in: %s account %s is signed in again: %s", agent, user, how)
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

// logRefreshFailed logs that a refresh of the account of agent and user
// didn't go through, err saying why.
func logRefreshFailed(agent, user string, err error) {
	if err == nil {
		return
	}
	why, status := err.Error(), httpStatusOf(err)
	k, now := strings.ToLower(agent+"\x00"+user), time.Now()
	if was, ok := refreshFailedSaid.Load(k); ok {
		if f := was.(refreshFailure); f.why == why && now.Sub(f.at) < refreshFailedQuiet {
			return
		}
	}
	refreshFailedSaid.Store(k, refreshFailure{why, now})
	line := fmt.Sprintf("sign-in: %s account %s refresh failed", agent, user)
	if status != 0 {
		line += fmt.Sprintf(" HTTP %d", status)
	}
	log.Print(line + ": " + logReason(why))
}
