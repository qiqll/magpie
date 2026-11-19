package provider

import (
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/plugin"
)

// The owner's sign-in to a plugin's provider folds into an account of the
// same name (the host's settle), and an area's included it took the
// area's account: its tokens replaced by the owner's, and the owner's
// sign-in managed by the area's admin from then on (T-228). The host is
// told which plugin-auth.json keys an area owns, and leaves them alone.
func init() { plugin.AreaOwned = areaPluginKeys }

// areaPluginKeys is the plugin-auth.json keys of the accounts an area
// owns, each found as an area's sign-in claimed it (claimOnDone,
// areaPluginKey): "<PluginID>/<LoginID of its name in the accounts
// list>". ok is false when areas.json can't be read, or when an area owns
// accounts and the plugins can't be read: then every account is kept
// from the owner's sign-in, never none. A plugin an area owns a ref of
// that no account's name gives now (a name its duplicate's " (slot)"
// changed) has every account kept.
func areaPluginKeys() ([]string, bool) {
	areas, err := area.List()
	if err != nil {
		return nil, false
	}
	owned := map[string]bool{}
	for _, a := range areas {
		for _, c := range a.Accounts {
			owned[c.Ref] = true
		}
	}
	if len(owned) == 0 {
		return nil, true
	}
	pps := plugin.Cached()
	if len(pps) == 0 {
		return nil, false
	}
	var keys []string
	for _, pp := range pps {
		agent := PluginID(pp.ID)
		names := pluginNames(pp)
		found := map[string]bool{}
		var mine []string
		for k, user := range names {
			if ref := agent + "/" + LoginID(agent, user); user != "" && owned[ref] {
				mine = append(mine, k)
				found[ref] = true
			}
		}
		for ref := range owned {
			if strings.HasPrefix(ref, agent+"/") && !found[ref] {
				mine = slices.Collect(maps.Keys(names))
				break
			}
		}
		keys = append(keys, mine...)
	}
	return keys, true
}
