package gui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// usersShown is how many users the Gateway page is given at a time: a
// service's users can be many, and a word narrows them.
const usersShown = 200

// providerShown is how the page names a provider and its icon.
type providerShown struct{ name, icon string }

// userAccountJSON is one account and how many users are bound to it.
type userAccountJSON struct {
	Provider     string `json:"provider"`
	ProviderName string `json:"providerName"`
	Icon         string `json:"icon,omitempty"`
	Account      string `json:"account"`
	Seat         string `json:"seat"`   // who it is, lower-cased: a user's account names it the same
	Users        int    `json:"users"`  // users bound to it, as their first or a spare
	Spares       int    `json:"spares"` // of them, those it is a spare of
	seatOutJSON
	// Gone: the account is no longer the provider's; its users are
	// bound afresh on their next request. ProviderGone: the provider
	// itself is no longer magpie's
	Gone         bool `json:"gone,omitempty"`
	ProviderGone bool `json:"providerGone,omitempty"`
}

// seatOutJSON is why an account can't answer now, and until when: Out
// as gateway.SeatOut's Why, "" while it can. OutKnown is false when that
// can't be told: the magpie serving the gateway didn't say what rests,
// or the provider has no such account.
type seatOutJSON struct {
	Out      string     `json:"out,omitempty"`
	OutUntil *time.Time `json:"outUntil,omitempty"`
	OutModel string     `json:"outModel,omitempty"`
	OutKnown bool       `json:"outKnown"`
}

// userSpareJSON is one of a user's accounts after its first.
type userSpareJSON struct {
	Account string    `json:"account"`
	Seat    string    `json:"seat"`
	At      time.Time `json:"at"`
}

// userJSON is one user's binding. Whether each of its accounts is out is
// its account's, in accounts, by Seat.
type userJSON struct {
	User         string          `json:"user"`
	Provider     string          `json:"provider"`
	ProviderName string          `json:"providerName"`
	Icon         string          `json:"icon,omitempty"`
	Account      string          `json:"account"` // its first
	Seat         string          `json:"seat"`
	Spares       []userSpareJSON `json:"spares,omitempty"`
	Key          string          `json:"key,omitempty"`     // the gateway key's id, "" without one
	KeyName      string          `json:"keyName,omitempty"` // its name, while the keys could be read
	KeyGone      bool            `json:"keyGone,omitempty"` // the key is removed: the keys were read and it isn't among them
	At           time.Time       `json:"at"`
	// Tier: the tier its last request named; "" from a magpie before
	// tiers, which the page shows as the default tier
	Tier string `json:"tier,omitempty"`
}

// gatewayUserRoutes serves the Gateway page's User bindings: each
// provider's accounts with how many of the caller's users are bound to
// them and whether each is out, and the users with their first and spare
// accounts, narrowed by q's words. Read-only: a binding is moved or
// forgotten in user-accounts.json. A search (q) leaves the accounts, and
// how the gateway binds, out, which don't depend on it: the page keeps
// the ones it has, and nobody's sign-ins or rests are read again for each
// word typed.
func gatewayUserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/gateway/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		rows, err := gateway.UserBindings()
		if err != nil {
			// why, for the page to say it in the reader's language
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("user-accounts.json can't be read: %v", err), "why": err.Error()})
			return
		}
		// the keys' names; a list that can't be read is unknown, and no key
		// is said removed for it
		keyNames := map[string]string{}
		keys, kerr := access.List()
		for _, k := range keys {
			keyNames[k.ID] = k.Name
		}
		keyGone := func(id string) bool {
			_, ok := keyNames[id]
			return id != "" && kerr == nil && !ok
		}
		q := r.URL.Query().Get("q")
		providers := map[string]providerShown{}
		byID := map[string]provider.Provider{}
		// the accounts and keys each provider has now, nil for one that
		// can't be told: a binding to none of them is gone
		seats := map[string]map[string]bool{}
		for _, p := range provider.All() {
			providers[p.ID] = providerShown{cmp.Or(p.Name, p.ID), p.Icon}
			byID[p.ID] = p
			if q != "" {
				continue
			}
			if s, known := gateway.UserSeats(p); known {
				seats[p.ID] = s
			}
		}
		// a provider not among them is gone, unless accounts aren't
		// discovered at all, when a subscription's isn't listed either
		providerGone := func(id string) bool {
			_, ok := providers[id]
			return !ok && !settings.AccountDiscoveryOff()
		}
		nameOf := func(id string) providerShown { return cmp.Or(providers[id], providerShown{name: id}) }
		slices.SortFunc(rows, func(a, b gateway.UserRow) int {
			return cmp.Or(strings.Compare(nameOf(a.Provider).name, nameOf(b.Provider).name), strings.Compare(a.Provider, b.Provider),
				strings.Compare(a.Account, b.Account), strings.Compare(a.User, b.User), strings.Compare(a.Key, b.Key))
		})
		out := map[string]any{"total": len(rows)}
		if q == "" {
			bind := servingBinding()
			out["mode"] = bind.mode
			if bind.tiers != nil {
				out["tiers"] = bind.tiers
			}
			out["accounts"] = userAccounts(rows, byID, seats, bind.rests, nameOf, providerGone)
		}
		words := strings.Fields(strings.ToLower(q))
		users := []userJSON{}
		matched := 0
		for _, u := range rows {
			p := nameOf(u.Provider)
			hay := []string{u.User, u.Account, p.name, u.Provider, keyNames[u.Key]}
			for _, sp := range u.Spares {
				hay = append(hay, sp.Account)
			}
			low := strings.ToLower(strings.Join(hay, " "))
			if slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(low, w) }) {
				continue
			}
			matched++
			if len(users) == usersShown {
				continue
			}
			uj := userJSON{User: u.User, Provider: u.Provider, ProviderName: p.name, Icon: p.icon, Account: u.Account, Seat: strings.ToLower(u.Who), Key: u.Key, KeyName: keyNames[u.Key], KeyGone: keyGone(u.Key), At: u.At, Tier: u.Tier}
			for _, sp := range u.Spares {
				uj.Spares = append(uj.Spares, userSpareJSON{Account: sp.Account, Seat: strings.ToLower(sp.Who), At: sp.At})
			}
			users = append(users, uj)
		}
		out["matched"], out["users"] = matched, users
		writeJSON(w, out)
	})
}

// userAccounts is every account a user is bound to, as its first or a
// spare, with its users counted and whether it is out: each read once.
func userAccounts(rows []gateway.UserRow, byID map[string]provider.Provider, seats map[string]map[string]bool, rests map[string]gateway.Rest,
	nameOf func(string) providerShown, providerGone func(string) bool) []userAccountJSON {
	accounts := []userAccountJSON{}
	at := map[string]int{}
	count := func(pid, who, account string, spare bool) {
		seat := strings.ToLower(who)
		k := pid + "\x00" + seat
		i, ok := at[k]
		if !ok {
			p := nameOf(pid)
			i, at[k] = len(accounts), len(accounts)
			a := userAccountJSON{Provider: pid, ProviderName: p.name, Icon: p.icon, Account: account, Seat: seat, ProviderGone: providerGone(pid)}
			a.Gone = a.ProviderGone || seats[pid] != nil && !seats[pid][seat]
			if pp, ok := byID[pid]; ok && !a.Gone {
				so, found := gateway.UserSeatOut(pp, who, rests)
				// unknown rests, or a cap with no reading, tell only what
				// holds it: the rest is unknown, not "it can"
				a.OutKnown = found && (rests != nil && !so.CapUnknown || so.Why != "")
				a.Out, a.OutModel = so.Why, so.Model
				if !so.Until.IsZero() {
					until := so.Until.UTC()
					a.OutUntil = &until
				}
			}
			accounts = append(accounts, a)
		}
		accounts[i].Users++
		if spare {
			accounts[i].Spares++
		}
	}
	for _, u := range rows {
		count(u.Provider, u.Who, u.Account, false)
		for _, sp := range u.Spares {
			count(u.Provider, sp.Who, sp.Account, true)
		}
	}
	slices.SortStableFunc(accounts, func(a, b userAccountJSON) int {
		return cmp.Or(strings.Compare(a.ProviderName, b.ProviderName), strings.Compare(a.Provider, b.Provider), strings.Compare(a.Account, b.Account))
	})
	return accounts
}

// gatewayBinding is how the gateway binds requests and what rests in it.
type gatewayBinding struct {
	mode string // "session" or "user"; "" when the magpie serving doesn't say (an older one)
	// tiers: bound by user, the tiers it holds to, each tier's most
	// accounts a user may have; nil when not known or bound by session
	tiers *gateway.Tiers
	rests map[string]gateway.Rest
}

// tiersOf is the tiers user-tiers.json holds as this process reads it,
// bound by user; nil bound by session, or when it can't be read.
func tiersOf(b gateway.Binding) *gateway.Tiers {
	if b.Mode != gateway.BindUser {
		return nil
	}
	t, err := gateway.UserTiers()
	if err != nil {
		return nil
	}
	return &t
}

// servingBinding is gatewayBinding of the gateway as it serves now: this
// process's own when it serves it; else the magpie serving's as it says,
// rests nil (unknown) when it doesn't tell them; else, while something
// holds the port and doesn't answer in time (a magpie starting, or busy),
// nothing known; else what one started here would read, with nothing
// resting, as no gateway runs.
func servingBinding() gatewayBinding {
	if gw := served.Load(); gw != nil {
		b := gw.Binding()
		return gatewayBinding{b.Mode, tiersOf(b), gateway.Rests()}
	}
	if o := gateway.ServedBy(); o.Running {
		rests, _ := gateway.OwnerRests()
		return gatewayBinding{o.Bind, o.Tiers, rests}
	}
	if gateway.Taken() {
		return gatewayBinding{}
	}
	b, _ := gateway.BindingFromEnv()
	return gatewayBinding{b.Mode, tiersOf(b), map[string]gateway.Rest{}}
}
