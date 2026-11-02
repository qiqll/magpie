package gateway

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A request refused while its user is bound (--bind=user), before any
// account is asked, is seen by the operator three ways (T-062): a
// Rejected row in the usage log with why (the call's error, turnedAway
// in serve, refusedRow on a route that isn't a turn), a line in magpie's
// log (logUserRefused), and its kind in UserLimitHeader when it is a
// limit. The caller is told no more than before (userRefusal.told).

// refusedLogEvery is how often a user's refusals by one provider of one
// kind are logged: the first at once, the others in the window counted
// into the next line written.
const refusedLogEvery = 5 * time.Minute

// refusedLogMax is how many user, provider and kind lines are kept in
// memory before those whose window is over are let go.
const refusedLogMax = 4096

// refusedLogged is when each user, provider and kind (refusedKey) was
// last logged, and how many refusals since weren't.
var refusedLogged = struct {
	sync.Mutex
	m map[string]*refusedLine
}{m: map[string]*refusedLine{}}

type refusedLine struct {
	at     time.Time // the last line's
	merged int       // refusals since at, not logged
}

// refusedNow is the clock refusals are logged by, held in tests.
var refusedNow = time.Now

func refusedLogKey(user, pid, kind string) string { return user + "\x00" + pid + "\x00" + kind }

// refusalKind is what refused is, for the log: its UserLimitHeader when
// it is a limit, else "unread" (user-accounts.json or user-tiers.json
// doesn't read), "none-free" (none of the provider's accounts can answer
// now), "request" (a 400: the header, the tier, the model), "key" (a
// 403: the gateway key may not use its accounts) or "unavailable".
func refusalKind(refused userRefusal) string {
	switch {
	case refused.limit != "":
		return refused.limit
	case refused.unknown:
		return "unread"
	case refused.none:
		return "none-free"
	}
	switch refused.status {
	case http.StatusBadRequest:
		return "request"
	case http.StatusForbidden:
		return "key"
	}
	return "unavailable"
}

// logUserRefused logs refused, why a request of the user u to provider
// pid (u's own when it has one) goes to none of its accounts: one line a
// user, provider and kind every refusedLogEvery, the refusals between
// counted into the next. The user and the account are as logName has
// them, the reason quoted, so neither can start a line of its own.
func logUserRefused(u *UserBound, pid string, refused userRefusal) {
	var user, account string
	if u != nil {
		user, account, pid = u.User, u.Account, cmp.Or(u.pid, pid)
	}
	noteRefused(user, pid, refusalKind(refused), refused.status, account, refused.msg)
}

// noteRefused logs a refusal of user's request to provider pid, of kind,
// with status and why (msg), account the one it is bound to: at once
// when none of the same user, provider and kind was logged within
// refusedLogEvery, else counted into the next line.
func noteRefused(user, pid, kind string, status int, account, msg string) {
	now := refusedNow()
	k := refusedLogKey(user, pid, kind)
	refusedLogged.Lock()
	l := refusedLogged.m[k]
	if l != nil && now.Sub(l.at) < refusedLogEvery && !now.Before(l.at) {
		l.merged++
		refusedLogged.Unlock()
		return
	}
	var merged int
	var since time.Time
	if l != nil {
		merged, since = l.merged, l.at
	}
	if len(refusedLogged.m) >= refusedLogMax {
		// those whose window is over: their next refusal is logged at
		// once, the count not logged of them lost
		for key, o := range refusedLogged.m {
			if now.Sub(o.at) >= refusedLogEvery {
				delete(refusedLogged.m, key)
			}
		}
	}
	refusedLogged.m[k] = &refusedLine{at: now}
	refusedLogged.Unlock()
	line := fmt.Sprintf("gateway: user %s refused (%s, %d) by %s", provider.LogName(cmp.Or(user, "(none)")), kind, status, cmp.Or(pid, "no provider"))
	if account != "" {
		line += " bound to " + provider.LogName(account)
	}
	line += ": " + strconv.Quote(msg)
	if merged > 0 {
		line += fmt.Sprintf(" (and %d more like it since %s, not logged)", merged, since.UTC().Format(time.RFC3339))
	}
	log.Print(line)
}

// refusedRow writes the usage log's row of a request r that isn't a turn
// (call, a drawing, an embedding, a video, a System One decision) its
// user's binding refused before any account was asked (err, a
// userRefusedError not tried): Rejected, so no tokens, no cost and no
// key's limit count it, with its status and why.
func refusedRow(r *http.Request, call *Call, err error) {
	var ur *userRefusedError
	if r == nil || !errors.As(err, &ur) || ur.tried {
		return
	}
	pid := ur.pid
	if ur.u != nil {
		pid = cmp.Or(ur.u.pid, pid)
	}
	at := call.Time
	if at.IsZero() {
		at = time.Now()
	}
	rec := usage.Record{Time: at, Agent: call.Agent, Via: call.Via, Provider: pid, Model: call.Model, Requested: call.Model, Kind: call.Kind,
		Millis: time.Since(at).Milliseconds(), Status: call.Status, Rejected: true, Session: sessionOf(r.Header)}
	failedWith(&rec, call.Status, call.Error, "")
	appendUsage(r, rec)
}
