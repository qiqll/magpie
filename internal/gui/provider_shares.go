package gui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// shareTierJSON is a tier of user-tiers.json as Settings › Growth
// policy's shares by provider name it (T-243): its name, its aliases and
// its share in the file.
type shareTierJSON struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Share   int      `json:"share"`
}

// shareProviderJSON is a provider a share can be set for.
type shareProviderJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// shareTiers is user-tiers.json's tiers as the gateway reads them, in
// their order, none while it can't be read.
func shareTiers() []shareTierJSON {
	t, err := gateway.UserTiers()
	if err != nil {
		return []shareTierJSON{}
	}
	out := make([]shareTierJSON, 0, len(t.Tiers))
	for _, x := range t.Tiers {
		var as []string
		for a, n := range t.Aliases {
			if n == x.Name {
				as = append(as, a)
			}
		}
		slices.Sort(as)
		out = append(out, shareTierJSON{Name: x.Name, Aliases: as, Share: x.Share})
	}
	return out
}

// shareProviders is every provider in Providers, by id.
func shareProviders() []shareProviderJSON {
	out := []shareProviderJSON{}
	for _, p := range provider.All() {
		out = append(out, shareProviderJSON{ID: p.ID, Name: p.Name})
	}
	return out
}

// providerShares is in, the shares by tier the user set for provider
// pid, as Settings keeps them: each tier by its name in user-tiers.json
// (one of its aliases or another case taken), 0 to settings.ShareMost
// users; or why it can't be taken. A provider not in Providers takes
// none but an empty set, which clears it.
func providerShares(pid string, in map[string]int) (map[string]int, error) {
	if strings.TrimSpace(pid) == "" {
		return nil, fmt.Errorf("no provider named")
	}
	if len(in) == 0 {
		return nil, nil
	}
	if !slices.ContainsFunc(provider.All(), func(p provider.Provider) bool { return p.ID == pid }) {
		return nil, fmt.Errorf("%q isn't in Providers", pid)
	}
	t, err := gateway.UserTiers()
	if err != nil {
		return nil, fmt.Errorf("the tiers aren't known, as user-tiers.json doesn't read: %v", err)
	}
	out := map[string]int{}
	for name, n := range in {
		x, ok := t.Named(name)
		if !ok {
			return nil, fmt.Errorf("%q is none of user-tiers.json's tiers", name)
		}
		if _, twice := out[x.Name]; twice {
			return nil, fmt.Errorf("tier %s is named twice", x.Name)
		}
		if n < 0 || n > settings.ShareMost {
			return nil, fmt.Errorf("tier %s: an account is shared by 1 to %d users, or 0 for any number, not %d", x.Name, settings.ShareMost, n)
		}
		out[x.Name] = n
	}
	return out, nil
}
