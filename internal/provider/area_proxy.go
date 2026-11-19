package provider

import (
	"errors"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/renewal"
)

// An area's proxy on its account (T-198, docs/subsystems/areas.md "Proxy
// IPs"): an area's admin puts a proxy of one of its area's IPs on one of
// its area's accounts. The proxy's address is written where the owner's
// Providers editor writes an account's own proxy (AccountProxies: a key
// by its fingerprint, an account by its login in lower case), which
// ProxyChoice reads, and areas.json keeps which IP and proxy it was
// (area.AccountProxy), by its id alone. The provider marks the value as
// the area's (AreaProxies, T-224), and any other write of it takes the
// mark out, so a value the owner set, even back to the area's own, is
// the owner's: the area is told it is overridden, can't change it, and
// nothing of the area's ever takes it away. A direct (T-213) is the same
// with "direct" written there, and no IP or proxy recorded.

// ErrOverridden: the owner set this account's proxy.
var ErrOverridden = errors.New("the owner set this account's proxy")

// ErrNoAccount: the provider has no such account or key.
var ErrNoAccount = errors.New("no such account")

func init() {
	area.Released = releaseAreaProxy
	netproxy.PublicOnly = func(raw string) bool { return renewal.AreaAddedProxy(raw) || areaAttachedProxy(raw) }
}

// beforeOwnProxyWrite runs as setOwnProxy is about to take providers.json's
// lock, once the attach has made up its mind: a test writes the owner's
// value at that moment.
var beforeOwnProxyWrite func()

// beforeAttachWrite runs in AttachAreaProxy after its checks, before it
// writes: a test removes the IP there.
var beforeAttachWrite func()

// areaAttachedProxy says raw is the address of a proxy an area put on one
// of its accounts, by areas.json's record (its ProxyID): held to public
// addresses whatever ips.json says now, so one whose IP went away, or
// an ips.json that can't be read, leaves it held. Any area's record
// counts; an areas.json that can't be read holds nothing new.
func areaAttachedProxy(raw string) bool {
	areas, err := area.List()
	if err != nil {
		return false
	}
	id := renewal.ProxyID(raw)
	for _, a := range areas {
		for _, c := range a.Accounts {
			if c.Proxy != nil && !c.Proxy.Direct && c.Proxy.ID == id {
				return true
			}
		}
	}
	return false
}

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

// ownProxyOf is the account key's own proxy in provider pid, "" for none,
// and whether an area's mark says it wrote it (AreaProxies).
func ownProxyOf(pid, key string) (cur string, marked bool) {
	if p, ok := picksIn(storedFile(), pid); ok {
		return accountOwnProxy(p, key)
	}
	return "", false
}

// accountOwnProxy is ownProxyOf of the provider p as read.
func accountOwnProxy(p Provider, key string) (cur string, marked bool) {
	cur = p.AccountProxies[key]
	return cur, cur != "" && p.AreaProxies[accountKey(key)] == cur
}

// areaProxiesKept is p's AreaProxies as it is written: an entry only
// where the account's own proxy is still the value the area wrote.
func areaProxiesKept(p Provider) map[string]string {
	var out map[string]string
	for k, v := range p.AreaProxies {
		if v == "" || p.AccountProxies[k] != v {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// AreaProxyOverridden says the account's proxy as the provider has it is
// no longer the one the area put on it (rec): the owner's.
func AreaProxyOverridden(pid string, rec area.AccountProxy) bool {
	cur, marked := ownProxyOf(pid, rec.Key)
	return !areaHas(rec, cur, marked)
}

// areaHas says cur, an account's own proxy, is still the area's own: the
// value rec put there (areaValue), with the provider's mark on it
// (marked). A record of before the mark (not rec.Marked) is judged by
// its value alone, as it was then.
func areaHas(rec area.AccountProxy, cur string, marked bool) bool {
	return areaValue(rec, cur) && (marked || !rec.Marked)
}

// areaValue says cur, an account's own proxy, is the one rec put there:
// "direct" for a direct, else the proxy of rec's id.
func areaValue(rec area.AccountProxy, cur string) bool {
	if rec.Direct {
		return cur == "direct"
	}
	return cur != "" && renewal.ProxyID(cur) == rec.ID
}

// overWrites is the check setAreaProxy makes before an area's attach
// writes the account key's own proxy: the value there is none, with no
// record of the area's before (was), or still the area's own; else it is
// the owner's (ErrOverridden).
func overWrites(was *area.AccountProxy, key string) func(cur string, marked bool) error {
	return func(cur string, marked bool) error {
		if cur == "" && was == nil || was != nil && was.Key == key && areaHas(*was, cur, marked) {
			return nil
		}
		return ErrOverridden
	}
}

// attachMu holds an area's attaches, directs and detaches one at a time:
// each reads the record there before (recordOf) and writes it again, so
// two on one account never cross. Taken before ipsMu.
var attachMu sync.Mutex

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
	attachMu.Lock()
	defer attachMu.Unlock()
	// the IP and its proxy read, recorded and written under ipsMu: no
	// removal of either comes between (renewal.WithAreaProxy)
	return renewal.WithAreaProxy(areaID, ip, proxyID, func(x renewal.IP, url string) error {
		if beforeAttachWrite != nil {
			beforeAttachWrite()
		}
		was := recordOf(areaID, ref)
		rec := area.AccountProxy{IP: x.IP, ID: proxyID, Key: key, Marked: true}
		if err := area.SetProxy(areaID, ref, &rec); err != nil {
			return err
		}
		if err := setAreaProxy(pid, key, url, overWrites(was, key)); err != nil {
			area.SetProxy(areaID, ref, was)
			return err
		}
		if owner, contested, err := area.Owner(ref); err != nil || contested || owner != areaID {
			clearAreaProxy(pid, rec)
			return area.ErrNotOwned
		}
		log.Printf("areas: area %s put proxy %s of %s on %s", areaID, proxyID, x.IP, pid)
		return nil
	})
}

// AttachAreaDirect has the area areaID's account ref go through no proxy
// (T-213): "direct" is written as its own proxy, and areas.json keeps a
// direct record, no IP nor proxy in it. Checked as AttachAreaProxy
// checks, an IP aside: the account is the area's alone
// (area.ErrNotOwned) and magpie has it (ErrNoAccount), and its own proxy
// is none or the area's (ErrOverridden). The owner's own value for the
// account wins over it; the subscription's and the global proxy don't,
// as with a proxy.
func AttachAreaDirect(areaID, ref string) error {
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
	attachMu.Lock()
	defer attachMu.Unlock()
	was := recordOf(areaID, ref)
	rec := area.AccountProxy{Key: key, Direct: true, Marked: true}
	if err := area.SetProxy(areaID, ref, &rec); err != nil {
		return err
	}
	if err := setAreaProxy(pid, key, "direct", overWrites(was, key)); err != nil {
		area.SetProxy(areaID, ref, was)
		return err
	}
	if owner, contested, err := area.Owner(ref); err != nil || contested || owner != areaID {
		clearAreaProxy(pid, rec)
		return area.ErrNotOwned
	}
	log.Printf("areas: area %s put direct on %s", areaID, pid)
	return nil
}

// DetachAreaProxy takes the area's proxy, or direct, off its account
// ref: off the provider where it is still the area's, and out of
// areas.json whatever.
func DetachAreaProxy(areaID, ref string) error {
	return detachIf(areaID, ref, nil)
}

// detachIf is DetachAreaProxy of the record there now when was (nil:
// whatever) still says it: a sweep that read was before takes off no
// proxy or direct the area put on the account since.
func detachIf(areaID, ref string, was *area.AccountProxy) error {
	attachMu.Lock()
	defer attachMu.Unlock()
	rec := recordOf(areaID, ref)
	if rec == nil || was != nil && *rec != *was {
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

// clearAreaProxy takes rec's proxy, or direct, off the account rec.Key
// of provider pid when it is still the area's (areaHas).
func clearAreaProxy(pid string, rec area.AccountProxy) error {
	if cur, marked := ownProxyOf(pid, rec.Key); !areaHas(rec, cur, marked) {
		return nil
	}
	return setAreaProxy(pid, rec.Key, "", func(cur string, marked bool) error {
		if !areaHas(rec, cur, marked) {
			return errLeftAlone
		}
		return nil
	})
}

// errLeftAlone: the value is no longer the area's, and is left as it is.
var errLeftAlone = errors.New("left alone")

// setAreaProxy writes url ("" for none) as the own proxy of the account
// key of provider pid, in providers.json's lock, when allow (nil: always)
// takes the value there now and whether an area's mark is on it, read in
// the same lock: a value the owner set a moment before is never written
// over. errLeftAlone from allow is no error. url is marked as the area's
// (AreaProxies), or the mark goes with the value ("").
func setAreaProxy(pid, key, url string, allow func(cur string, marked bool) error) error {
	return writeOwnProxy(pid, key, url, true, allow)
}

// writeOwnProxy is setAreaProxy, and with byArea false an owner's write
// (a test's): an area's mark on the account goes.
func writeOwnProxy(pid, key, url string, byArea bool, allow func(cur string, marked bool) error) error {
	if beforeOwnProxyWrite != nil {
		beforeOwnProxyWrite()
	}
	if err := providersMu.Take(); err != nil {
		return err
	}
	defer providersMu.Unlock()
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == pid })
	cur, marked := "", false
	if i >= 0 {
		cur, marked = accountOwnProxy(f.Providers[i], key)
	}
	if allow != nil {
		if err := allow(cur, marked); errors.Is(err, errLeftAlone) {
			return nil
		} else if err != nil {
			return err
		}
	}
	if i < 0 {
		if url == "" {
			return nil
		}
		f.Providers = append(f.Providers, Provider{ID: pid})
		i = len(f.Providers) - 1
	}
	p := &f.Providers[i]
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
	ours := maps.Clone(p.AreaProxies)
	delete(ours, accountKey(key))
	if byArea && url != "" {
		if ours == nil {
			ours = map[string]string{}
		}
		ours[accountKey(key)] = strings.TrimSpace(url)
	}
	p.AreaProxies = ours
	return store(f)
}

// SweepAreaProxies takes every area's proxy off its accounts where the
// IP is no longer the area's, or no longer has that proxy: the owner
// removed it, took the IP back or took the proxy out of it. A direct
// goes through no IP, and stays. ips.json that can't be read is
// unknown, and nothing is taken off.
func SweepAreaProxies() error {
	ips, _, err := renewal.IPs()
	if err != nil {
		return err
	}
	if sweepBetween != nil {
		sweepBetween()
	}
	areas, err := area.List()
	if err != nil {
		return err
	}
	for _, a := range areas {
		for _, c := range a.Accounts {
			if c.Proxy == nil || c.Proxy.Direct {
				continue
			}
			if !areaIPKept(ips, a.ID, *c.Proxy) {
				if err := sweepDetach(a.ID, c.Ref, c.Proxy); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// areaIPKept says ips has the area's IP rec names with rec's proxy on it.
func areaIPKept(ips []renewal.IP, areaID string, rec area.AccountProxy) bool {
	return slices.ContainsFunc(ips, func(x renewal.IP) bool {
		_, ok := x.ProxyOf(rec.ID)
		return x.IP == rec.IP && x.Area == areaID && ok
	})
}

// sweepBetween, when set (a test), is called by a sweep between its read
// of ips.json and its read of areas.json, where a reread's move can come.
var sweepBetween func()

// sweepDetach is detachIf for a sweep that read was gone from ips.json:
// the record and ips.json are read again together, under attachMu and
// then ipsMu (renewal.WithIPs, the lock order an attach takes), and the
// proxy is taken off only while both still say it is gone. A reread's
// move (renewal.MoveAreaProxy) writes both files in one hold of ipsMu, so
// a sweep that read ips.json before it never takes the moved proxy off
// (code review M1).
func sweepDetach(areaID, ref string, was *area.AccountProxy) error {
	attachMu.Lock()
	defer attachMu.Unlock()
	return renewal.WithIPs(func(ips []renewal.IP) error {
		rec := recordOf(areaID, ref)
		if rec == nil || *rec != *was || rec.Direct || areaIPKept(ips, areaID, *rec) {
			return nil
		}
		pid, _, _ := strings.Cut(ref, "/")
		if err := clearAreaProxy(pid, *rec); err != nil {
			return err
		}
		return area.SetProxy(areaID, ref, nil)
	})
}

// InUseError is an IP or proxy of an area still on the area's accounts.
type InUseError struct{ Accounts []string }

func (e *InUseError) Error() string {
	return "the area's accounts go through it: " + strings.Join(e.Accounts, ", ")
}

// AreaIPInUse is the check renewal.RemoveAreaIP and ChangeAreaIP make
// under ipsMu before an area's IP (proxyIDs nil), or some of its proxies,
// go: an InUseError naming the area's accounts through them, from
// areas.json read then.
func AreaIPInUse(areaID string, proxyIDs []string) func(renewal.IP) error {
	return func(x renewal.IP) error {
		a, err := area.Get(areaID)
		if err != nil {
			return err
		}
		var used []string
		for _, c := range a.Accounts {
			if c.Proxy != nil && c.Proxy.IP == x.IP && (proxyIDs == nil || slices.Contains(proxyIDs, c.Proxy.ID)) {
				used = append(used, c.Ref)
			}
		}
		if len(used) > 0 {
			slices.Sort(used)
			return &InUseError{Accounts: used}
		}
		return nil
	}
}
