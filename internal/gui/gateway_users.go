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

// userJSON is one user's binding of a provider, every gateway key's of
// one group it is bound under as one (UserRow's Group): the gateway
// counts a name sent with any key of the group as one user (the owner's
// option A2, 2026-10-30) and binds it to one set of accounts (option A,
// 2026-11-03, T-075), and the page shows it so. The keys that share
// their users and callers without a key are one group; any other key is
// a group of its own (option B, T-089), its user of the same name
// another row. Its accounts are every key's, each once, as the gateway reads them
// (gateway's sharedOf): the first key's first, the key used last (the
// earliest bound when alike), then the rest in that order. Whether each
// is out is its account's, in accounts, by Seat. Keys is each key's
// binding as user-accounts.json keeps it, alike under each once the
// gateway wrote it; the page reads each key's name and tier there, Key,
// KeyName, KeyGone and Tier being its first key's alone.
type userJSON struct {
	User         string          `json:"user"`
	Provider     string          `json:"provider"`
	ProviderName string          `json:"providerName"`
	Icon         string          `json:"icon,omitempty"`
	Account      string          `json:"account"` // its first key's first
	Seat         string          `json:"seat"`
	Spares       []userSpareJSON `json:"spares,omitempty"`  // its other accounts, At the earliest a key was given it
	Key          string          `json:"key,omitempty"`     // its first key's id, "" without one
	KeyName      string          `json:"keyName,omitempty"` // its name, while the keys could be read
	KeyGone      bool            `json:"keyGone,omitempty"` // the key is removed: the keys were read and it isn't among them
	At           time.Time       `json:"at"`                // when it was first bound, under any key
	// Tier: the tier its first key's last request named; "" from a
	// magpie before tiers, which the page shows as the default tier.
	// Each key's is in Keys
	Tier string `json:"tier,omitempty"`
	// Used: when it last sent the provider a request, under any key, to
	// within an hour; left out for one bound before it was kept
	Used time.Time     `json:"used,omitzero"`
	Keys []userKeyJSON `json:"keys"`
}

// userKeyJSON is a user's binding under one gateway key: its accounts,
// first then spares, by Seat, and its own time, tier and last use.
type userKeyJSON struct {
	Key     string    `json:"key,omitempty"`
	KeyName string    `json:"keyName,omitempty"`
	KeyGone bool      `json:"keyGone,omitempty"`
	Seats   []string  `json:"seats"`
	At      time.Time `json:"at"`
	Tier    string    `json:"tier,omitempty"`
	Used    time.Time `json:"used,omitzero"`
}

// userMerged is a user's bindings of a provider, one a key of its group,
// the one used last first, of those alike the earliest bound (a time left
// out of a file edited by hand last), as the gateway reads them.
type userMerged []gateway.UserRow

// mergedUsers is rows by user, provider and group of keys, in the order
// each first comes.
func mergedUsers(rows []gateway.UserRow) []userMerged {
	var out []userMerged
	at := map[[3]string]int{}
	for _, r := range rows {
		k := [3]string{r.Provider, r.User, r.Group}
		if i, ok := at[k]; ok {
			out[i] = append(out[i], r)
			continue
		}
		at[k] = len(out)
		out = append(out, userMerged{r})
	}
	for _, m := range out {
		slices.SortStableFunc(m, func(a, b gateway.UserRow) int {
			if c := b.Used.Compare(a.Used); c != 0 {
				return c
			}
			if a.At.IsZero() != b.At.IsZero() {
				if a.At.IsZero() {
					return 1
				}
				return -1
			}
			return cmp.Or(a.At.Compare(b.At), strings.Compare(a.Key, b.Key))
		})
	}
	return out
}

// userSeat is one of a user's accounts under a key: who, as candidate.who().
type userSeat struct {
	who, account string
	at           time.Time
}

// seatsOf is r's accounts, its first then its spares.
func seatsOf(r gateway.UserRow) []userSeat {
	seats := []userSeat{{r.Who, r.Account, r.At}}
	for _, sp := range r.Spares {
		seats = append(seats, userSeat{sp.Who, sp.Account, sp.At})
	}
	return seats
}

// shown is m as the page shows it, its keys named as keyNames has them.
func (m userMerged) shown(p providerShown, keyNames map[string]string, keyGone func(string) bool) userJSON {
	f := m[0]
	u := userJSON{User: f.User, Provider: f.Provider, ProviderName: p.name, Icon: p.icon, Account: f.Account, Seat: strings.ToLower(f.Who),
		Key: f.Key, KeyName: keyNames[f.Key], KeyGone: keyGone(f.Key), At: f.At, Tier: f.Tier, Keys: []userKeyJSON{}}
	spare := map[string]int{u.Seat: -1} // seat → its index in Spares, -1 the first
	for _, r := range m {
		k := userKeyJSON{Key: r.Key, KeyName: keyNames[r.Key], KeyGone: keyGone(r.Key), At: r.At, Tier: r.Tier, Used: r.Used}
		for _, s := range seatsOf(r) {
			seat := strings.ToLower(s.who)
			k.Seats = append(k.Seats, seat)
			i, ok := spare[seat]
			switch {
			case !ok:
				spare[seat] = len(u.Spares)
				u.Spares = append(u.Spares, userSpareJSON{Account: s.account, Seat: seat, At: s.at})
			case i >= 0 && !s.at.IsZero() && (u.Spares[i].At.IsZero() || s.at.Before(u.Spares[i].At)):
				u.Spares[i].At = s.at
			}
		}
		if r.Used.After(u.Used) {
			u.Used = r.Used
		}
		u.Keys = append(u.Keys, k)
	}
	return u
}

// gatewayUserRoutes serves the Gateway page's User bindings: each
// provider's accounts with how many of the caller's users are bound to
// them and whether each is out, and the users with their first and spare
// accounts, narrowed by q's words; and a user's account let go of by hand
// (unbind). A search (q) leaves the accounts, and
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
		merged := mergedUsers(rows)
		slices.SortFunc(merged, func(a, b userMerged) int {
			return cmp.Or(strings.Compare(nameOf(a[0].Provider).name, nameOf(b[0].Provider).name), strings.Compare(a[0].Provider, b[0].Provider),
				strings.Compare(a[0].Account, b[0].Account), strings.Compare(a[0].User, b[0].User))
		})
		out := map[string]any{"total": len(merged)}
		if q == "" {
			bind := servingBinding()
			out["mode"] = bind.mode
			if bind.tiers != nil {
				out["tiers"] = bind.tiers
			}
			if bind.idleHours > 0 {
				out["idleHours"] = bind.idleHours
			}
			out["accounts"] = userAccounts(merged, byID, seats, bind.rests, nameOf, providerGone)
		}
		words := strings.Fields(strings.ToLower(q))
		users := []userJSON{}
		matched := 0
		for _, m := range merged {
			p := nameOf(m[0].Provider)
			hay := []string{m[0].User, p.name, m[0].Provider}
			for _, r := range m {
				hay = append(hay, keyNames[r.Key])
				for _, s := range seatsOf(r) {
					hay = append(hay, s.account)
				}
			}
			low := strings.ToLower(strings.Join(hay, " "))
			if slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(low, w) }) {
				continue
			}
			matched++
			if len(users) == usersShown {
				continue
			}
			users = append(users, m.shown(p, keyNames, keyGone))
		}
		out["matched"], out["users"] = matched, users
		writeJSON(w, out)
	})
	// one of a user's accounts let go of, as the page names it: its
	// provider, user, seat and a gateway key it is bound under ("" for
	// none), under every key of that key's group the user is bound under
	// (gateway.Unbind): another group's user of the name is another;
	// only while the gateway binds by user, as the page offers it: bound
	// by session the bindings are kept as they are. unbound is false when
	// the user was let go of it meanwhile; a file that doesn't read, or
	// can't be written, is said why, for the page to say it in the
	// reader's language
	mux.HandleFunc("POST /api/gateway/users/unbind", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Key      string `json:"key"`
			Provider string `json:"provider"`
			User     string `json:"user"`
			Seat     string `json:"seat"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in) != nil || in.Provider == "" || in.User == "" || in.Seat == "" {
			http.Error(w, "nothing to unbind", http.StatusBadRequest)
			return
		}
		if servingBinding().mode != gateway.BindUser {
			http.Error(w, "the gateway doesn't bind by user now (start option --bind=user), so nothing is unbound", http.StatusConflict)
			return
		}
		found, err := gateway.Unbind(in.Key, in.Provider, in.User, in.Seat)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("user-accounts.json can't be changed: %v", err), "why": err.Error()})
			return
		}
		writeJSON(w, map[string]bool{"unbound": found})
	})
}

// userAccounts is every account a user is bound to, as its first or a
// spare, with its users counted and whether it is out: each read once. A
// user bound to it under several gateway keys of a group is counted once, and as a
// spare only when it is the first under none of them.
func userAccounts(users []userMerged, byID map[string]provider.Provider, seats map[string]map[string]bool, rests map[string]gateway.Rest,
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
	for _, m := range users {
		var own []userSeat
		spare := map[string]bool{} // seat → a spare under every key it is under
		for _, r := range m {
			for i, s := range seatsOf(r) {
				seat := strings.ToLower(s.who)
				was, ok := spare[seat]
				if !ok {
					own = append(own, s)
				}
				spare[seat] = (was || !ok) && i > 0
			}
		}
		for _, s := range own {
			count(m[0].Provider, s.who, s.account, spare[strings.ToLower(s.who)])
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
	// idleHours: bound by user, how long a user sends nothing before its
	// accounts are let go; 0 when the magpie serving doesn't say (an
	// older one, which lets nobody go)
	idleHours int
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

// idleOf is this magpie's idle hours bound as mode says: none by session.
func idleOf(mode string) int {
	if mode == gateway.BindUser {
		return gateway.UserIdleHours
	}
	return 0
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
		return gatewayBinding{b.Mode, tiersOf(b), gateway.Rests(), idleOf(b.Mode)}
	}
	if o := gateway.ServedBy(); o.Running {
		rests, _ := gateway.OwnerRests()
		idle := 0
		if o.Bind == gateway.BindUser {
			idle = o.IdleHours
		}
		return gatewayBinding{o.Bind, o.Tiers, rests, idle}
	}
	if gateway.Taken() {
		return gatewayBinding{}
	}
	b, _ := gateway.BindingFromEnv()
	return gatewayBinding{b.Mode, tiersOf(b), map[string]gateway.Rest{}, idleOf(b.Mode)}
}
