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
	"strings"
	"sync"

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
// would have them (bindUser); for an ask that goes to the vendor without
// a request through serve (Codex's web.run on the ChatGPT backend, a Kimi
// Code plan's search service). bound is false for an ask made for no
// user: nothing to narrow. With none of the user's to ask, err says why,
// and none of p's other accounts is asked in their place.
func (s *Server) usersOwn(ctx context.Context, p provider.Provider, model string, from provider.Protocol) (own []candidate, bound bool, err error) {
	f, ok := ctx.Value(askedForKey{}).(askedFor)
	if !ok {
		return nil, false, nil
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
	cands, pl := s.plan(p, model, from)
	cands, _, ub, refused := bindUser(f.key, f.user, tier, p, cands, pl)
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

// noUser refuses with a 400, bound by user, a request without a user, or
// with one UserHeader can't take, or with a tier (UserTierHeader) that
// isn't one of user-tiers.json's: each request goes to its user's
// accounts, so one without can't be placed. A group's request and one
// with AccountHeader need it too, though neither is bound. Tiers that
// can't be read are left to the binding to refuse.
func (s *Server) noUser(w http.ResponseWriter, r *http.Request, from provider.Protocol) bool {
	if !s.bind.byUser() {
		return false
	}
	user, bad := userOf(r.Header)
	if user != "" {
		if _, bad, _ := tierOf(r.Header); bad != "" {
			writeError(w, from, http.StatusBadRequest, bad)
			return true
		}
		return false
	}
	if bad == "" {
		bad = fmt.Sprintf("magpie binds accounts by user (--bind=user): name the request's user in %s", strings.Join(userHeaders(), " or "))
	}
	writeError(w, from, http.StatusBadRequest, bad)
	return true
}
