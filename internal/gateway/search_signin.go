package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A plugin's search API may sign in as the same plugin's provider
// (provider.SearchVendor.Auth) rather than take a key: a search is then
// one of that provider's accounts, those on in the order its requests take
// them (accountsOn), each asked as a key is (askSearchAPIKeys). An account
// the API refuses (429, out of credit, turned away) rests for search alone
// (restSearchKey, by the account's id, never its token): the provider's
// own rests, which its models' requests read, are never touched, so a
// search's rate limit leaves the account's models as they were. Bound by
// user, a search made for a user takes the accounts of the provider the
// user is on alone (usersSearchAccounts), as a plan's search service
// takes the user's own key (usersSearchKey): never another user's.

// runPluginSearchAs asks a plugin's search API as one account in the host
// (a test's has no host).
var runPluginSearchAs = plugin.RunSearchAs

// searchAccountID is how an account is known among a search API's rests:
// beside its keys, by its key in the plugin host's sign-ins.
func searchAccountID(p provider.Provider) string { return "account " + p.PluginAccountKey() }

func (s *Server) askSearchSignedIn(ctx context.Context, a provider.SearchAPI, v provider.SearchVendor, query string) ([]foundPage, error) {
	name, p := provider.SearchSignIn(v)
	var cs []candidate
	if p != nil {
		cs = accountsOn(*p, "")
	}
	if len(cs) == 0 {
		err := fmt.Errorf("not signed in to %s: sign in to it on Providers", name)
		if p != nil {
			err = fmt.Errorf("no account of %s is on", name)
		}
		s.logSearch(ctx, usage.Record{Requested: a.Name()}, query, time.Now(), 0, err)
		return nil, err
	}
	if s.bind.byUser() {
		var err error
		if cs, err = usersSearchAccounts(ctx, name, cs); err != nil {
			s.logSearch(ctx, usage.Record{Requested: a.Name()}, query, time.Now(), 0, err)
			return nil, err
		}
	}
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = searchAccountID(c.p)
	}
	// the accounts in the provider's own order, one refused lately last
	inOrder := provider.SearchAPI{Vendor: a.Vendor, Pick: provider.SearchPickOrder}
	var errs []error
	for _, i := range searchKeyOrder(inOrder, ids, time.Now()) {
		q := cs[i].p
		ctx, _ := withSearchWire(q.Via(ctx), "")
		ctx, cancel := context.WithTimeout(ctx, searchTimeout/4)
		start := time.Now()
		pages, err := s.askPluginSearchAs(ctx, v, q, query)
		cancel()
		rec := usage.Record{Requested: a.Name(), ProviderKeyID: searchKeyID(a.Vendor, ids[i]), ProviderKeyName: q.Account.User}
		s.logSearch(ctx, rec, query, start, len(pages), err)
		if err == nil {
			wakeSearchKey(a.Vendor, ids[i])
			return pages, nil
		}
		var r searchRefused
		refused := errors.As(err, &r)
		if refused {
			d, why := searchRestFor(a.Vendor, r.status, r.retryAfter, time.Now())
			restSearchKey(a.Vendor, ids[i], d, why, time.Now())
		}
		if len(cs) > 1 {
			err = fmt.Errorf("account %s: %w", q.Account.User, err)
		}
		errs = append(errs, err)
		// signed out since it was listed: the next account may still be in
		if !refused && !strings.Contains(err.Error(), "not signed in") {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// usersSearchAccounts is, of cs, the accounts the user ctx is asked for
// (forUser) is on, as its group shares them (sharedOf), in cs' order. A
// search for no user, for a user on none of them, or with
// user-accounts.json never read is refused: whose accounts they are is
// then unknown. A user is not bound afresh for a search.
func usersSearchAccounts(ctx context.Context, name string, cs []candidate) ([]candidate, error) {
	f, ok := ctx.Value(askedForKey{}).(askedFor)
	if !ok || f.user == "" {
		return nil, fmt.Errorf("%s: bound by user, a search names its user (%s)", name, UserHeader)
	}
	if _, _, err := askedUserTier(ctx, f); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var seats []userSeat
	userBinds.Lock()
	// read once and changed since into what can't be read (stale), the
	// seats are the last read, as usersSearchKey takes them
	if loadUsers() == nil {
		b, _ := sharedOf(f.key, cs[0].p.ID, f.user)
		seats = b.seats()
	}
	userBinds.Unlock()
	var own []candidate
	for _, c := range cs {
		if seatIn(seats, c.who()) {
			own = append(own, c)
		}
	}
	if len(own) == 0 {
		return nil, fmt.Errorf("%s: none of its accounts that are on is %s's", name, f.user)
	}
	return own, nil
}

// SearchAccountsOn is how many of p's accounts a search that signs in as
// it may take (accountsOn), for the GUI's row.
func SearchAccountsOn(p provider.Provider) int { return len(accountsOn(p, "")) }

// askPluginSearchAs is askPluginSearch as one account of the provider the
// API signs in to.
func (s *Server) askPluginSearchAs(ctx context.Context, v provider.SearchVendor, p provider.Provider, query string) ([]foundPage, error) {
	n := searchCount(ctx)
	got, err := runPluginSearchAs(ctx, v.Plugin, v.PluginID, query, n, p.PluginAccountKey())
	notePluginSearch(ctx, v.Plugin, pluginSearchAsked(v, query, n, "", "", p.Account.User), got, err)
	if err != nil {
		var r plugin.SearchRefused
		if errors.As(err, &r) {
			if keyRefusal(r.Status) {
				return nil, searchRefused{r.Status, r.Msg, r.RetryAfter}
			}
			return nil, errors.New(r.Msg)
		}
		if ctx.Err() != nil {
			return nil, errors.New("no answer in time")
		}
		return nil, err
	}
	return pluginPages(got, n), nil
}
