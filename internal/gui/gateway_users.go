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

// userAccountJSON is one account and how many users are bound to it.
type userAccountJSON struct {
	Provider     string `json:"provider"`
	ProviderName string `json:"providerName"`
	Icon         string `json:"icon,omitempty"`
	Account      string `json:"account"`
	Users        int    `json:"users"`
	// Gone: the account is no longer the provider's; its users are
	// bound afresh on their next request. ProviderGone: the provider
	// itself is no longer magpie's
	Gone         bool `json:"gone,omitempty"`
	ProviderGone bool `json:"providerGone,omitempty"`
}

// userJSON is one user's binding.
type userJSON struct {
	User         string    `json:"user"`
	Provider     string    `json:"provider"`
	ProviderName string    `json:"providerName"`
	Icon         string    `json:"icon,omitempty"`
	Account      string    `json:"account"`
	Key          string    `json:"key,omitempty"`     // the gateway key's id, "" without one
	KeyName      string    `json:"keyName,omitempty"` // its name, while the keys could be read
	KeyGone      bool      `json:"keyGone,omitempty"` // the key is removed: the keys were read and it isn't among them
	At           time.Time `json:"at"`
}

// gatewayUserRoutes serves the Gateway page's User bindings: each
// provider's accounts with how many of the caller's users are bound to
// them, and the users, narrowed by q's words. Read-only: a binding is
// moved or forgotten in user-accounts.json. A search (q) leaves the
// accounts out, which don't depend on it: the page keeps the ones it has,
// and nobody's sign-ins are read again for each word typed.
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
		type shown struct{ name, icon string }
		providers := map[string]shown{}
		// the accounts and keys each provider has now, nil for one that
		// can't be told: a binding to none of them is gone
		seats := map[string]map[string]bool{}
		for _, p := range provider.All() {
			providers[p.ID] = shown{cmp.Or(p.Name, p.ID), p.Icon}
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
		nameOf := func(id string) shown { return cmp.Or(providers[id], shown{name: id}) }
		slices.SortFunc(rows, func(a, b gateway.UserRow) int {
			return cmp.Or(strings.Compare(nameOf(a.Provider).name, nameOf(b.Provider).name), strings.Compare(a.Provider, b.Provider),
				strings.Compare(a.Account, b.Account), strings.Compare(a.User, b.User), strings.Compare(a.Key, b.Key))
		})
		var accounts []userAccountJSON
		at := map[string]int{}
		for _, u := range rows {
			if q != "" {
				break
			}
			k := u.Provider + "\x00" + strings.ToLower(u.Who)
			i, ok := at[k]
			if !ok {
				p := nameOf(u.Provider)
				i, at[k] = len(accounts), len(accounts)
				a := userAccountJSON{Provider: u.Provider, ProviderName: p.name, Icon: p.icon, Account: u.Account, ProviderGone: providerGone(u.Provider)}
				a.Gone = a.ProviderGone || seats[u.Provider] != nil && !seats[u.Provider][strings.ToLower(u.Who)]
				accounts = append(accounts, a)
			}
			accounts[i].Users++
		}
		words := strings.Fields(strings.ToLower(q))
		users := []userJSON{}
		matched := 0
		for _, u := range rows {
			p := nameOf(u.Provider)
			hay := strings.ToLower(strings.Join([]string{u.User, u.Account, p.name, u.Provider, keyNames[u.Key]}, " "))
			if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }) {
				matched++
				if len(users) < usersShown {
					users = append(users, userJSON{User: u.User, Provider: u.Provider, ProviderName: p.name, Icon: p.icon, Account: u.Account, Key: u.Key, KeyName: keyNames[u.Key], KeyGone: keyGone(u.Key), At: u.At})
				}
			}
		}
		out := map[string]any{"total": len(rows), "matched": matched, "users": users}
		if q == "" {
			if accounts == nil {
				accounts = []userAccountJSON{}
			}
			out["accounts"] = accounts
		}
		writeJSON(w, out)
	})
}
