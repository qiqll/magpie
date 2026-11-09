package gui

import (
	"cmp"
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

// keyReach is how one key reaches a provider: by its own models (direct),
// and through each routing group it may use that has a member of the
// provider's, held there to the group's accounts too (gateway heldByGroup).
type keyReach struct {
	direct bool
	via    []access.Identity
}

// allows says the provider's account ref may serve the key along one of
// its ways: the key's accounts on a direct one, the key's and the group's
// (access.HeldToGroup) through a group.
func (r keyReach) allows(who access.Identity, pid, ref string) bool {
	if r.direct && who.AllowsAccount(pid, ref) {
		return true
	}
	for _, held := range r.via {
		if held.AllowsAccount(pid, ref) {
			return true
		}
	}
	return false
}

// accountsCatalog is what reachOf reads, once a request: the catalog's
// models by provider, and the routing groups with their members.
type accountsCatalog struct {
	models  map[string][]string // provider id in lower case → "<provider>/<model>" ids
	groups  []provider.Group
	members map[string][]provider.Member // group id → members ready now
}

func readAccountsCatalog() accountsCatalog {
	c := accountsCatalog{models: map[string][]string{}, members: map[string][]provider.Member{}}
	for _, e := range provider.Catalog() {
		if e.Group != "" || e.Provider.ID == "" {
			continue
		}
		pid := strings.ToLower(e.Provider.ID)
		c.models[pid] = append(c.models[pid], e.ID, e.Provider.ID+"/"+e.Model)
	}
	find := provider.GroupFinder()
	for _, g := range provider.Groups() {
		if g.ID == "" {
			continue
		}
		if _, ms, ok := find(provider.GroupPrefix + g.ID); ok {
			c.groups = append(c.groups, g)
			c.members[g.ID] = ms
		}
	}
	return c
}

// reachOf is how the key reaches provider pid. Directly: one of the
// provider's catalog models is among its models (access.Allows, globs
// included: "*/claude-*" reaches no provider without such a model); a
// provider not in the catalog (turned off) by its id against each
// pattern's provider part. Through a group: a group the key may use
// ("group/<id>", "group/*") with a member of the provider's.
func (c accountsCatalog) reachOf(who access.Identity, pid string) keyReach {
	var r keyReach
	if ms, ok := c.models[strings.ToLower(pid)]; ok {
		r.direct = who.Allows(ms...)
	} else {
		for _, m := range who.Models {
			if _, group := provider.GroupIDOf(m); group {
				continue
			}
			head, _, _ := strings.Cut(m, "/")
			if access.ModelAllowed([]string{head}, pid) {
				r.direct = true
				break
			}
		}
	}
	for _, g := range c.groups {
		if !who.Allows(provider.GroupPrefix + g.ID) {
			continue
		}
		for _, m := range c.members[g.ID] {
			if strings.EqualFold(m.Provider.ID, pid) {
				r.via = append(r.via, who.HeldToGroup(cmp.Or(g.Name, g.ID), g.Accounts))
				break
			}
		}
	}
	return r
}

// accountsKeysOf are keys' accounts among those of providers ps.
func accountsKeysOf(keys []access.Identity, ps []provider.Provider, c accountsCatalog) []accountsKeyJSON {
	out := make([]accountsKeyJSON, 0, len(keys))
	for _, who := range keys {
		k := accountsKeyJSON{ID: who.KeyID, Name: who.KeyName, All: !who.Restricted(), Accounts: []string{}}
		if !k.All {
			for _, p := range ps {
				// a provider turned off is listed on the page too: a key
				// reaches its accounts once it is on again
				if p.Account == nil {
					continue
				}
				r := c.reachOf(who, p.ID)
				if !r.direct && len(r.via) == 0 {
					continue
				}
				for _, a := range p.AccountIDs() {
					// one marked banned is used by no key (provider/banned.go)
					if !a.Key && r.allows(who, p.ID, a.ID) && !p.BannedUser(a.User) {
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
		writeJSON(w, map[string]any{"keys": accountsKeysOf(keys, provider.All(), readAccountsCatalog())})
	})
}
