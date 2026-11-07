package gui

import (
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// accountsKeyJSON is a gateway key as Models › Accounts filters by it:
// the subscription accounts a request sending it may be served by, as
// "<provider>/<account>" in lower case (the account's name, as the page
// knows its rows), or All for a key held to none.
type accountsKeyJSON struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	All      bool     `json:"all,omitempty"`
	Accounts []string `json:"accounts"`
}

// reachesProvider says a key's models reach provider pid at all: none
// listed, a pattern of pid's or a glob in its provider part, or a routing
// group with a member of pid's. The accounts it names hold it further
// (AllowsAccount).
func reachesProvider(who access.Identity, pid string) bool {
	if len(who.Models) == 0 {
		return true
	}
	for _, m := range who.Models {
		if _, ok := provider.GroupIDOf(m); ok {
			if _, ms, ok := provider.FindGroup(m); ok {
				for _, x := range ms {
					if strings.EqualFold(x.Provider.ID, pid) {
						return true
					}
				}
			}
			continue
		}
		head, _, _ := strings.Cut(m, "/")
		if strings.EqualFold(head, pid) || strings.Contains(head, "*") {
			return true
		}
	}
	return false
}

// accountsKeysOf are keys' accounts among those of providers ps.
func accountsKeysOf(keys []access.Identity, ps []provider.Provider) []accountsKeyJSON {
	out := make([]accountsKeyJSON, 0, len(keys))
	for _, who := range keys {
		k := accountsKeyJSON{ID: who.KeyID, Name: who.KeyName, All: !who.Restricted(), Accounts: []string{}}
		if !k.All {
			for _, p := range ps {
				// a provider turned off is listed on the page too: a key
				// reaches its accounts once it is on again
				if p.Account == nil || !reachesProvider(who, p.ID) {
					continue
				}
				for _, a := range p.AccountIDs() {
					if !a.Key && who.AllowsAccount(p.ID, a.ID) {
						k.Accounts = append(k.Accounts, strings.ToLower(p.ID+"/"+strings.TrimSpace(a.User)))
					}
				}
			}
		}
		out = append(out, k)
	}
	return out
}

func accountsKeyRoutes(mux *http.ServeMux) {
	// the gateway keys that work now, each with the accounts it may use,
	// for Models › Accounts' key filter
	mux.HandleFunc("GET /api/accounts/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		keys, err := access.Identities()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]any{"keys": accountsKeysOf(keys, provider.All())})
	})
}
