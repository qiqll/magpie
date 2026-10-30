package gateway

// How the gateway keeps a caller's requests on accounts: by conversation
// ("session", the default: each conversation stays where its affinity
// says, as magpie always has), or by the caller's own user ("user": each
// user UserHeader names stays on accounts of its own, bindUser). Read
// once, as the gateway starts; `magpie serve --bind=user` and the like
// set it in BindEnv. How many accounts a user may have is its tier's
// (user_tiers.go).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// BindEnv is "session" or "user".
const BindEnv = "MAGPIE_BIND"

// bindMaxEnv is what said the most accounts of a provider a user had
// before user-tiers.json did. A service installed then may carry it
// still: it is read only to say it is ignored (bindMaxIgnored).
const bindMaxEnv = "MAGPIE_BIND_MAX"

var bindMaxSaid sync.Map // a bindMaxEnv left set, logged once a value

// The modes BindEnv takes.
const (
	BindSession = "session"
	BindUser    = "user"
)

// Binding is how the gateway binds a caller's requests to accounts.
type Binding struct {
	Mode string // BindSession or BindUser
}

// byUser says requests are bound by their user.
func (b Binding) byUser() bool { return b.Mode == BindUser }

// BindingOf is the binding mode says, as BindEnv takes it; "" is the
// default. An error says why it can't be taken, beside the default
// binding.
func BindingOf(mode string) (Binding, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", BindSession:
		return Binding{Mode: BindSession}, nil
	case BindUser:
		return Binding{Mode: BindUser}, nil
	}
	return Binding{Mode: BindSession}, fmt.Errorf("--bind (%s) is %s or %s, not %q", BindEnv, BindSession, BindUser, mode)
}

// BindingFromEnv is BindingOf what BindEnv holds. A bindMaxEnv left set
// is logged, once, as ignored: never a reason not to start.
func BindingFromEnv() (Binding, error) {
	bindMaxIgnored()
	return BindingOf(os.Getenv(BindEnv))
}

// bindMaxIgnored logs, once a value, a bindMaxEnv still set (a service
// installed with --bind-max): user-tiers.json's max_accounts says how
// many accounts a user has now.
func bindMaxIgnored() {
	v := strings.TrimSpace(os.Getenv(bindMaxEnv))
	if v == "" {
		return
	}
	if _, said := bindMaxSaid.LoadOrStore(v, true); !said {
		log.Printf("gateway: %s=%s is ignored: each user's tier in %s says how many accounts it has (max_accounts)", bindMaxEnv, v, tiersPath())
	}
}

// Binding is how s binds requests, as it was started.
func (s *Server) Binding() Binding { return s.bind }

// userNeeded refuses, bound by user, a request that picks a model or an
// account without a user (noUser). Bound by session it is h.
func (s *Server) userNeeded(from provider.Protocol, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.noUser(w, r, from) {
			return
		}
		h(w, r)
	}
}

// askedForKey holds, bound by user, the user (and its tier) of the
// request magpie's own asks are made for — a web search, an image looked
// at — so each is bound to that user's accounts, as the request is
// (bindUser): never drawn from every account of the provider.
type askedForKey struct{}

type askedFor struct{ key, user, tier string }

// forUser is ctx for magpie's own asks made for r: carrying r's user and
// tier, bound by user. Bound by session, or for a request without a user,
// it is ctx.
func (s *Server) forUser(ctx context.Context, r *http.Request) context.Context {
	if !s.bind.byUser() {
		return ctx
	}
	user, _ := userOf(r.Header)
	if user == "" {
		return ctx
	}
	return context.WithValue(ctx, askedForKey{}, askedFor{userKey(r), user, strings.TrimSpace(r.Header.Get(UserTierHeader))})
}

// askAsUser names, on h, the headers of magpie's own ask, the user ctx
// is asked for (forUser), and its tier.
func askAsUser(ctx context.Context, h http.Header) {
	f, ok := ctx.Value(askedForKey{}).(askedFor)
	if !ok {
		return
	}
	h.Set(UserHeader, f.user)
	if f.tier != "" {
		h.Set(UserTierHeader, f.tier)
	}
}

// askedAs is ctx asked for the user as is (forUser): for an ask made
// later, from a request that carries none of its own (a Claude Code
// run's web search, asked back through its MCP callback).
func askedAs(ctx, as context.Context) context.Context {
	if f, ok := as.Value(askedForKey{}).(askedFor); ok {
		return context.WithValue(ctx, askedForKey{}, f)
	}
	return ctx
}

// askedUser is the user ctx is asked for (forUser), with its gateway
// key's id, whose users are its own; "" for none.
func askedUser(ctx context.Context) string {
	f, ok := ctx.Value(askedForKey{}).(askedFor)
	if !ok {
		return ""
	}
	return f.key + "\x00" + f.user
}

// usersOwn is, for magpie's own ask of p's model made for a user
// (forUser), the accounts and keys of p that user is on, its first first,
// bound now when it is on none, as a request of that user to the model
// would have them (ownOf: a spare added too when each of its accounts is
// out a while, as for a turn); for an ask that goes to the vendor without
// a request through serve (Codex's web.run on the ChatGPT backend, a Kimi
// Code plan's search service). bound is false bound by session: nothing
// to narrow. Bound by user, an ask made for no user is refused as noUser
// refuses its request, never asked of every account of p. With none of
// the user's to ask, err says why, and none of p's other accounts is
// asked in their place.
func (s *Server) usersOwn(ctx context.Context, p provider.Provider, model string, from provider.Protocol) (own []candidate, bound bool, err error) {
	if !s.bind.byUser() {
		return nil, false, nil
	}
	f, ok := ctx.Value(askedForKey{}).(askedFor)
	if !ok {
		return nil, true, errors.New(noUserSaid())
	}
	h := http.Header{}
	askAsUser(ctx, h)
	tier, bad, err := tierOf(h)
	if bad != "" {
		return nil, true, errors.New(bad)
	}
	if err != nil {
		return nil, true, fmt.Errorf("%s: magpie can't read user-tiers.json, so it doesn't know how many accounts %q may have; nothing was sent", UserTierHeader, f.user)
	}
	pool, poolPl := s.plan(p, model, from)
	cands, _, ub, refused := ownOf(f.key, f.user, tier, tierNamed(h), p, pool, poolPl)
	if refused.status == http.StatusTooManyRequests {
		// told as a limit, never the sign-in (lapsedRefusal) nor the
		// user's accounts and their rests (setRefusal, boundRefusal)
		return nil, true, errors.New(userLimitSaid)
	}
	if refused.status != 0 {
		return nil, true, errors.New(refused.msg)
	}
	// "none": no account of p can answer now, and the user is bound to
	// none of them yet; those cands are p's, not the user's
	if ub != nil && ub.Why == "none" || len(cands) == 0 {
		return nil, true, fmt.Errorf("%s: none of %s's accounts %q is on can answer now; no other is asked in its place", UserHeader, p.Name, f.user)
	}
	return cands, true, nil
}

// ownOf narrows pool, p's candidates for a request of user (of the
// gateway key keyID, at tier, or its binding's when the request named
// none: named, as bindUser) planned as poolPl, index for index, to the
// accounts the user is on, as serve narrows a turn of its: bindUser's,
// then growUser's spare when none of them can answer (each to be signed
// in again, or out a while) and its tier lets it have one more. The
// refusal is theirs: a 503 while user-accounts.json can't be read, else
// a 429 that names no sign-in when none is left.
func ownOf(keyID, user string, tier Tier, named bool, p provider.Provider, pool []candidate, poolPl planned) ([]candidate, planned, *UserBound, userRefusal) {
	cands, pl, ub, refused := bindUser(keyID, user, tier, named, p, pool, poolPl)
	if refused.status != 0 {
		return nil, pl, ub, refused
	}
	cands, pl, ub, _, refused = growUser(ub, pool, poolPl, cands, pl)
	return cands, pl, ub, refused
}

// userRefusedError is a request refused by its user's binding on a route
// that isn't a turn (usersOn): answered as serve answers a turn's
// (userRefused, answerUserRefused), a 429 told as a limit alone.
type userRefusedError struct {
	u       *UserBound
	refused userRefusal
}

func (e *userRefusedError) Error() string {
	if e.refused.status == http.StatusTooManyRequests {
		return userLimitSaid
	}
	return e.refused.msg
}

// userRefusedStatus is the status a userRefusedError is answered with, 0
// for another error.
func userRefusedStatus(err error) int {
	var ur *userRefusedError
	if !errors.As(err, &ur) {
		return 0
	}
	return ur.refused.status
}

// refusedFor is the userRefusedError of a refusal of status that says msg.
func refusedFor(status int, msg string) error {
	return &userRefusedError{refused: userRefusal{status: status, msg: msg}}
}

// answerUserRefused answers err, when it is a userRefusedError, as serve
// answers a turn its user's binding refuses (userRefused), and says so.
func (s *Server) answerUserRefused(w http.ResponseWriter, from provider.Protocol, call *Call, err error) bool {
	var ur *userRefusedError
	if !errors.As(err, &ur) {
		return false
	}
	s.userRefused(w, from, call, ur.u, ur.refused)
	return true
}

// usersOn narrows cs, p's candidates for a request r that isn't a turn
// (a drawing, a video, an embedding or rerank, audio, a System One
// decision), planned as pl index for index, to the accounts r's user is
// on, bound by user, as a turn of that user's is (ownOf): never another
// user's. A request without a user is refused with a 400, as noUser
// refuses it, whoever let it through: never given the whole pool. The
// userTry, nil when nothing is bound — bound by session, a provider with
// no key or account — goes with the tries (userTry.lapsed). With none of
// the user's left the error, a userRefusedError, says why, and none of
// the others' is asked in their place.
func (s *Server) usersOn(r *http.Request, p provider.Provider, cs []candidate, pl planned) ([]candidate, planned, *userTry, error) {
	if !s.bind.byUser() || p.Account == nil && p.Key == "" {
		return cs, pl, nil, nil
	}
	user, bad := userOf(r.Header)
	if bad != "" {
		return nil, pl, nil, refusedFor(http.StatusBadRequest, bad)
	}
	if user == "" {
		return nil, pl, nil, refusedFor(http.StatusBadRequest, noUserSaid())
	}
	tier, bad, err := tierOf(r.Header)
	if bad != "" {
		return nil, pl, nil, refusedFor(http.StatusBadRequest, bad)
	}
	if err != nil {
		return nil, pl, nil, &userRefusedError{refused: userRefusal{status: http.StatusServiceUnavailable, unknown: true,
			msg: fmt.Sprintf("%s: magpie can't read user-tiers.json, so it doesn't know how many accounts %q may have; nothing was sent. Its log says why", UserTierHeader, user)}}
	}
	own, opl, ub, refused := ownOf(userKey(r), user, tier, tierNamed(r.Header), p, cs, pl)
	if refused.status != 0 {
		return nil, opl, nil, &userRefusedError{u: ub, refused: refused}
	}
	// "none": no account of p can answer now, and the user is bound to
	// none of them yet; those are p's, others' among them, not the user's
	// (a turn is routed among them, a probe: this isn't). Back when the
	// first of them is
	if ub != nil && ub.Why == "none" || len(own) == 0 {
		return nil, opl, nil, &userRefusedError{u: ub, refused: userRefusal{status: http.StatusServiceUnavailable, until: firstBack(cs),
			msg: fmt.Sprintf("%s: none of %s's accounts %q is on can take this request now; no other is tried in its place", UserHeader, p.Name, user)}}
	}
	return own, opl, &userTry{ub: ub, pool: cs, poolPl: pl}, nil
}

// firstBack is when the first of cs resting is back, lapsedRetry from now
// with none resting (each to be signed in again).
func firstBack(cs []candidate) time.Time {
	var at time.Time
	for _, c := range cs {
		if rest, ok := restingOf(c); ok && (at.IsZero() || rest.Until.Before(at)) {
			at = rest.Until
		}
	}
	if at.IsZero() || time.Until(at) <= 0 {
		return time.Now().Add(lapsedRetry)
	}
	return at
}

// userTry is a user's request on a route that isn't a turn as it goes
// over the user's accounts (usersOn): what growUser needs to add a spare
// when the last of them turns out to have to be signed in again, as a
// turn's tries do (serve).
type userTry struct {
	ub     *UserBound
	pool   []candidate // p's candidates before the user's narrowing
	poolPl planned
	failed []string // the user's accounts that failed as lapsed so far
	grew   bool     // a spare was added, once a request
}

// lapsed says c, one of the user's accounts, failed with status and body
// as one whose sign-in has to be made again (failAuth): out for a spare
// later in the request, though its mark may not be written yet. As the
// last asked (last), it gives the spare growUser adds, the user's tier
// letting it have one more, to ask next; else the refusal the caller is
// answered (userTryRefusal, a userRefusedError: a limit that names no
// sign-in, never the vendor's words). On a nil u, or another failure,
// lapsed is false.
func (u *userTry) lapsed(c candidate, status int, body []byte, last bool) (next []candidate, refused error, lapsed bool) {
	if u == nil || failureOf(c, status, body) != failAuth {
		return nil, nil, false
	}
	noteTryLapsed(c)
	u.failed = append(u.failed, c.who())
	if !last {
		return nil, nil, true
	}
	var why userRefusal
	if !u.grew {
		var grew bool
		if next, _, u.ub, grew, why = growUser(u.ub, u.pool, u.poolPl, nil, planned{left: u.ub.left}, u.failed...); grew {
			u.grew = true
			return next, nil, true
		}
	}
	return nil, &userRefusedError{u: u.ub, refused: userTryRefusal(u.ub, why)}, true
}

// usersSeated narrows cs, p's candidates for r's count of tokens, bound
// by user, to the accounts r's user is on already, not to be signed in
// again, reading the binding alone: a count, which any caller may send
// for any user and which costs nothing, never binds a user, adds a spare
// or stamps its use, so it can't take seats from others. bound is false
// as usersOn's nil userTry; bound to none (or the file never read), none
// is left, and the count is estimated.
func (s *Server) usersSeated(r *http.Request, p provider.Provider, cs []candidate, pl planned) (_ []candidate, bound bool) {
	if !s.bind.byUser() || p.Account == nil && p.Key == "" {
		return cs, false
	}
	user, _ := userOf(r.Header)
	if user == "" {
		return cs, false
	}
	// read before the lock: logins.json isn't read while userBinds is held
	lapsedWho := lapsedOf(p.ID, cs, pl)
	userBinds.Lock()
	loadUsers()
	seats := userBinds.m[userKey(r)][p.ID][user].seats()
	userBinds.Unlock()
	return slices.DeleteFunc(slices.Clone(cs), func(c candidate) bool {
		return c.p.ID != p.ID || !seatIn(seats, c.who()) || lapsedWho[strings.ToLower(c.who())] != ""
	}), true
}

// usersOwnRoute says r is a request of a user's, bound by user: one whose
// accounts usersOn narrows.
func (s *Server) usersOwnRoute(r *http.Request) bool {
	if !s.bind.byUser() {
		return false
	}
	user, bad := userOf(r.Header)
	return user != "" || bad != ""
}

// keepIn is cs with those keep leaves, pl's order index for index with
// them.
func keepIn(cs []candidate, pl planned, keep func(candidate) bool) ([]candidate, planned) {
	out, order := cs[:0:0], pl.order[:0:0]
	for i, c := range cs {
		if !keep(c) {
			continue
		}
		out = append(out, c)
		if i < len(pl.order) {
			order = append(order, pl.order[i])
		}
	}
	pl.order = order
	return out, pl
}

// noUser refuses with a 400, bound by user, a request without a user, or
// with one UserHeader can't take, or with a tier (UserTierHeader) that
// isn't one of user-tiers.json's: each request goes to its user's
// accounts, so one without can't be placed. A group's request and one
// with AccountHeader need it too, though neither is bound. Tiers that
// can't be read are left to the binding to refuse.
func (s *Server) noUser(w http.ResponseWriter, r *http.Request, from provider.Protocol) bool {
	if bad := s.userMissing(r); bad != "" {
		writeError(w, from, http.StatusBadRequest, bad)
		return true
	}
	return false
}

// userMissing is why noUser refuses r, "" when it doesn't: bound by user,
// no user, one UserHeader can't take, or a tier that isn't one of
// user-tiers.json's. serve asks it too, of whoever sent the request.
func (s *Server) userMissing(r *http.Request) string {
	if !s.bind.byUser() {
		return ""
	}
	user, bad := userOf(r.Header)
	if user != "" {
		_, bad, _ := tierOf(r.Header)
		return bad
	}
	if bad == "" {
		bad = noUserSaid()
	}
	return bad
}

// noUserSaid is why, bound by user, a request or one of magpie's own asks
// that names no user is refused.
func noUserSaid() string {
	return fmt.Sprintf("magpie binds accounts by user (--bind=user): name the request's user in %s", strings.Join(userHeaders(), " or "))
}
