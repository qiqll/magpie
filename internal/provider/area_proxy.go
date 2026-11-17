package provider

import (
	"errors"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/renewal"
)

// An area's proxy on its account (T-198, docs/subsystems/areas.md "Proxy
// IPs"): an area's admin puts a proxy of one of its area's IPs on one of
// its area's accounts. The proxy's address is written where the owner's
// Providers editor writes an account's own proxy (AccountProxies: a key
// by its fingerprint, an account by its login in lower case), which
// ProxyChoice reads, and areas.json keeps which IP and proxy it was
// (area.AccountProxy), by its id alone. A value there that is no longer
// the area's proxy (the owner changed or cleared it) is the owner's: the
// area is told it is overridden, can't change it, and nothing of the
// area's ever takes it away.

// ErrOverridden: the owner set this account's proxy.
var ErrOverridden = errors.New("the owner set this account's proxy")

// ErrNoAccount: the provider has no such account or key.
var ErrNoAccount = errors.New("no such account")

func init() { area.Released = releaseAreaProxy }

// areaAccountKey is the account ref of provider pid as AccountProxies
// names it: a key's fingerprint, or an account's login in lower case.
// A subscription whose agent magpie doesn't list now (not installed)
// still has its saved accounts, by the agent's id.
func areaAccountKey(pid, ref string) (string, error) {
	if p, err := Find(pid); err == nil {
		for _, r := range p.AccountIDs() {
			if r.ID != ref {
				continue
			}
			if r.Key {
				return r.ID, nil
			}
			return accountKey(r.User), nil
		}
		if p.Account == nil {
			return "", ErrNoAccount
		}
	}
	for _, l := range Logins(pid) {
		if LoginID(pid, l.User) == ref {
			return accountKey(l.User), nil
		}
	}
	return "", ErrNoAccount
}

// ownProxyOf is the account key's own proxy in provider pid, "" for none.
func ownProxyOf(pid, key string) string {
	if p, ok := picksIn(storedFile(), pid); ok {
		return p.AccountProxies[key]
	}
	return ""
}

// AreaProxyOverridden says the account's proxy as the provider has it is
// no longer the one the area put on it (rec): the owner's.
func AreaProxyOverridden(pid string, rec area.AccountProxy) bool {
	cur := ownProxyOf(pid, rec.Key)
	return cur == "" || renewal.ProxyID(cur) != rec.ID
}

// AttachAreaProxy puts the proxy proxyID of the area areaID's IP ip on its
// account ref ("<provider>/<id>"). Each is checked here, whatever the
// caller checked: the IP is the area's (renewal.ErrIPNotFound), the
// account is the area's alone (area.ErrNotOwned) and magpie has it
// (ErrNoAccount), and its own proxy is none or the area's (ErrOverridden).
// The record is kept first, so a release that comes meanwhile finds it;
// an account no longer the area's once the proxy is written has it taken
// off again.
func AttachAreaProxy(areaID, ref, ip, proxyID string) error {
	pid, id, ok := strings.Cut(ref, "/")
	if !ok {
		return ErrNoAccount
	}
	if owner, contested, err := area.Owner(ref); err != nil {
		return err
	} else if contested || owner != areaID {
		return area.ErrNotOwned
	}
	key, err := areaAccountKey(pid, id)
	if err != nil {
		return err
	}
	ips, _, err := renewal.IPs()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ips, func(x renewal.IP) bool { return x.IP == ip && x.Area == areaID && areaID != "" })
	if i < 0 {
		return renewal.ErrIPNotFound
	}
	url, ok := ips[i].ProxyOf(proxyID)
	if !ok {
		return renewal.ErrIPNotFound
	}
	was := recordOf(areaID, ref)
	if cur := ownProxyOf(pid, key); cur != "" {
		if was == nil || was.Key != key || renewal.ProxyID(cur) != was.ID {
			return ErrOverridden
		}
	} else if was != nil {
		return ErrOverridden // the owner cleared it
	}
	rec := area.AccountProxy{IP: ips[i].IP, ID: proxyID, Key: key}
	if err := area.SetProxy(areaID, ref, &rec); err != nil {
		return err
	}
	if err := setOwnProxy(pid, key, url, ""); err != nil {
		area.SetProxy(areaID, ref, was)
		return err
	}
	if owner, contested, err := area.Owner(ref); err != nil || contested || owner != areaID {
		clearAreaProxy(pid, rec)
		return area.ErrNotOwned
	}
	log.Printf("areas: area %s put proxy %s of %s on %s", areaID, proxyID, ips[i].IP, pid)
	return nil
}

// DetachAreaProxy takes the area's proxy off its account ref: off the
// provider where it is still the area's, and out of areas.json whatever.
func DetachAreaProxy(areaID, ref string) error {
	rec := recordOf(areaID, ref)
	if rec == nil {
		return nil
	}
	pid, _, _ := strings.Cut(ref, "/")
	if err := clearAreaProxy(pid, *rec); err != nil {
		return err
	}
	return area.SetProxy(areaID, ref, nil)
}

// recordOf is the proxy the area areaID put on its account ref, nil for
// none.
func recordOf(areaID, ref string) *area.AccountProxy {
	a, err := area.Get(areaID)
	if err != nil {
		return nil
	}
	for _, c := range a.Accounts {
		if c.Ref == ref {
			return c.Proxy
		}
	}
	return nil
}

// releaseAreaProxy is area.Released: an account let go of by its area
// (removed, or taken back by the owner) goes through the area's proxy no
// longer, unless the owner set it since. Its admin could otherwise watch
// the owner's own traffic on it.
func releaseAreaProxy(c area.Account) {
	if c.Proxy == nil {
		return
	}
	pid, _, _ := strings.Cut(c.Ref, "/")
	if err := clearAreaProxy(pid, *c.Proxy); err != nil {
		log.Printf("areas: %s let go of, its area's proxy not taken off: %v", pid, err)
	}
}

// clearAreaProxy takes rec's proxy off the account rec.Key of provider
// pid when it is still the area's (by its id).
func clearAreaProxy(pid string, rec area.AccountProxy) error {
	if cur := ownProxyOf(pid, rec.Key); cur == "" || renewal.ProxyID(cur) != rec.ID {
		return nil
	}
	return setOwnProxy(pid, rec.Key, "", rec.ID)
}

// setOwnProxy writes url ("" for none) as the own proxy of the account
// key of provider pid, in providers.json's lock. only, when set, clears
// it only while it is still the proxy of that id.
func setOwnProxy(pid, key, url, only string) error {
	if err := providersMu.Take(); err != nil {
		return err
	}
	defer providersMu.Unlock()
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == pid })
	if i < 0 {
		if url == "" {
			return nil
		}
		f.Providers = append(f.Providers, Provider{ID: pid})
		i = len(f.Providers) - 1
	}
	p := &f.Providers[i]
	if only != "" && renewal.ProxyID(p.AccountProxies[key]) != only {
		return nil
	}
	own := maps.Clone(p.AccountProxies)
	if own == nil {
		own = map[string]string{}
	}
	if url == "" {
		delete(own, key)
	} else {
		own[key] = url
	}
	if err := checkAccountProxies(own); err != nil {
		return err
	}
	p.AccountProxies = normalAccountProxies(own)
	return store(f)
}

// SweepAreaProxies takes every area's proxy off its accounts where the
// IP is no longer the area's, or no longer has that proxy: the owner
// removed it, took the IP back or took the proxy out of it. ips.json
// that can't be read is unknown, and nothing is taken off.
func SweepAreaProxies() error {
	ips, _, err := renewal.IPs()
	if err != nil {
		return err
	}
	areas, err := area.List()
	if err != nil {
		return err
	}
	for _, a := range areas {
		for _, c := range a.Accounts {
			if c.Proxy == nil {
				continue
			}
			kept := slices.ContainsFunc(ips, func(x renewal.IP) bool {
				_, ok := x.ProxyOf(c.Proxy.ID)
				return x.IP == c.Proxy.IP && x.Area == a.ID && ok
			})
			if !kept {
				if err := DetachAreaProxy(a.ID, c.Ref); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
