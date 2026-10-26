package gateway

// How the gateway keeps a caller's requests on accounts: by conversation
// ("session", the default: each conversation stays where its affinity
// says, as magpie always has), or by the caller's own user ("user": each
// user UserHeader names stays on accounts of its own, bindUser). Read
// once, as the gateway starts; `magpie serve --bind=user` and the like
// set it in BindEnv and BindMaxEnv.

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// BindEnv is "session" or "user"; BindMaxEnv the most accounts of each
// provider a user is bound to by "user".
const (
	BindEnv    = "MAGPIE_BIND"
	BindMaxEnv = "MAGPIE_BIND_MAX"
)

// The modes BindEnv takes.
const (
	BindSession = "session"
	BindUser    = "user"
)

// bindMaxDefault is BindMaxEnv unset: a user's first account and one
// spare.
const bindMaxDefault = 2

// Binding is how the gateway binds a caller's requests to accounts.
type Binding struct {
	Mode string // BindSession or BindUser
	Max  int    // the most accounts of a provider a user is bound to
}

// byUser says requests are bound by their user.
func (b Binding) byUser() bool { return b.Mode == BindUser }

// BindingOf is the binding mode and max say, as BindEnv and BindMaxEnv
// take them; "" is the default. An error says why one can't be taken,
// beside the default binding.
func BindingOf(mode, max string) (Binding, error) {
	b, def := Binding{Mode: BindSession, Max: bindMaxDefault}, Binding{Mode: BindSession, Max: bindMaxDefault}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", BindSession:
	case BindUser:
		b.Mode = BindUser
	default:
		return def, fmt.Errorf("--bind (%s) is %s or %s, not %q", BindEnv, BindSession, BindUser, mode)
	}
	if m := strings.TrimSpace(max); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil || n < 1 {
			return def, fmt.Errorf("--bind-max (%s) is a whole number of accounts, 1 or more, not %q", BindMaxEnv, max)
		}
		b.Max = n
	}
	return b, nil
}

// BindingFromEnv is BindingOf what BindEnv and BindMaxEnv hold.
func BindingFromEnv() (Binding, error) {
	return BindingOf(os.Getenv(BindEnv), os.Getenv(BindMaxEnv))
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

// noUser refuses with a 400, bound by user, a request without a user, or
// with one UserHeader can't take: each request goes to its user's
// accounts, so one without can't be placed. A group's request and one
// with AccountHeader need it too, though neither is bound.
func (s *Server) noUser(w http.ResponseWriter, r *http.Request, from provider.Protocol) bool {
	if !s.bind.byUser() {
		return false
	}
	user, bad := userOf(r.Header)
	if user != "" {
		return false
	}
	if bad == "" {
		bad = fmt.Sprintf("magpie binds accounts by user (--bind=user): name the request's user in %s", strings.Join(userHeaders(), " or "))
	}
	writeError(w, from, http.StatusBadRequest, bad)
	return true
}
