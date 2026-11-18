package gateway

import (
	"errors"
	"log"
	"net/http"
	"slices"
	"sync"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/renewal"
)

// A proxy an area picks for a sign-in, a setup-token or a key (T-212,
// docs/subsystems/areas.md "Area sign-ins"): one of its own IPs' proxies,
// or direct. The sign-in's requests go through it (SignInOptions.Proxy,
// so an area-added proxy is held to public addresses on every connection
// as its account's traffic is), and the account it leaves the area's has
// it put on as POST accounts/proxy puts it: the owner's own value for the
// account wins, and one magpie had before (the owner's, another area's)
// is never touched.

// proxyPick is a request's "proxy": {"ip","id"} or {"direct":true}.
type proxyPick struct {
	IP     string `json:"ip"`
	ID     string `json:"id"`
	Direct bool   `json:"direct"`
}

// pickedProxy is the proxy p names, as netproxy.With takes it ("direct"
// for direct), with p's IP as ips.json writes it, and whether the area
// added it (held to public addresses); or false with the answer
// written: 400 for a pick that names neither or both, 404 for an IP or
// proxy that isn't c's area's (another area's alike).
func pickedProxy(w http.ResponseWriter, c adminCall, p *proxyPick) (url string, public, ok bool) {
	if p.Direct {
		if p.IP != "" || p.ID != "" {
			adminError(w, http.StatusBadRequest, adminBadRequest, "send proxy as direct, or as an ip and its proxy's id, not both")
			return "", false, false
		}
		return "direct", false, true
	}
	if p.IP == "" || p.ID == "" {
		adminError(w, http.StatusBadRequest, adminBadRequest, `send proxy as {"ip","id"}, one of this area's IPs and a proxy of it, or {"direct":true}`)
		return "", false, false
	}
	x, ok := areaIP(w, c, p.IP)
	if !ok {
		return "", false, false
	}
	url, ok = x.ProxyOf(p.ID)
	if !ok {
		adminError(w, http.StatusNotFound, adminNotFound, "no such proxy")
		return "", false, false
	}
	p.IP = x.IP
	return url, x.AddedBy == renewal.AddedByArea, true
}

// mount puts p on the area areaID's account ref, as accounts/proxy does.
func (p proxyPick) mount(areaID, ref string) error {
	if p.Direct {
		return areaDirect(areaID, ref)
	}
	return areaIPAttach(areaID, ref, p.IP, p.ID)
}

// mountFailed is why a pick wasn't put on an account added all the same,
// after "added, but " or "signed in, but ".
func mountFailed(err error) string {
	if errors.Is(err, provider.ErrOverridden) {
		return "the owner set this account's proxy: it stays the owner's"
	}
	return "its proxy wasn't put on it: " + err.Error()
}

// pickBox is what an area's sign-in was given to go through: the pick it
// started with, and each picked as an address was pasted, by the proxy
// it names. Done puts on the one the sign-in went
// through (SignInState.Proxy), so a paste refused, or one that crossed
// another, never leaves its pick on the account.
type pickBox struct {
	mu    sync.Mutex
	picks map[string]proxyPick
}

// add has url be p's.
func (b *pickBox) add(url string, p proxyPick) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.picks == nil {
		b.picks = map[string]proxyPick{}
	}
	b.picks[url] = p
}

// of is the pick of url, false for none.
func (b *pickBox) of(url string) (proxyPick, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.picks[url]
	return p, ok
}

// through says one of the box's picks is a proxy of ip (any of its
// proxies when ids is nil, else one of ids).
func (b *pickBox) through(ip string, ids []string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.picks {
		if !p.Direct && p.IP == ip && (ids == nil || slices.Contains(ids, p.ID)) {
			return true
		}
	}
	return false
}

// mountOnDone is done, and then the pick of the proxy the sign-in went
// through put on the account a sign-in of area id left the area's: none
// on one that ended otherwise (a conflict is another's account), nor
// through a proxy that wasn't picked.
func mountOnDone(done func(provider.SignInState) provider.SignInState, id string, box *pickBox) func(provider.SignInState) provider.SignInState {
	return func(st provider.SignInState) provider.SignInState {
		st = done(st)
		p, ok := box.of(st.Proxy)
		if !ok || st.State != "done" {
			return st
		}
		ref := subscriptionOf(st.Agent) + "/" + provider.LoginID(st.Agent, st.User)
		if err := p.mount(id, ref); err != nil {
			log.Printf("admin: area %s's %s account signed in, its proxy not put on: %v", id, st.Agent, err)
			st.Error = "signed in, but " + mountFailed(err)
		}
		return st
	}
}

// errSigningThrough refuses taking out an area's IP or proxy that one of
// its sign-ins under way goes through: its requests would reach a proxy
// no longer held to public addresses (Claude Code's own sign-in is given
// the proxy as ips.json says it).
var errSigningThrough = errors.New("a sign-in of this area goes through it: let it end, or cancel it, first")

// signingThrough says a sign-in of area areaID under way, or starting, was
// given a proxy of its IP ip (one of ids, nil for any).
func signingThrough(areaID, ip string, ids []string) bool {
	areaSignIns.Lock()
	defer areaSignIns.Unlock()
	for box, a := range areaSignIns.pending {
		if a == areaID && box.through(ip, ids) {
			return true
		}
	}
	for sid, x := range areaSignIns.m {
		if x.area != areaID || x.pick == nil || !x.pick.through(ip, ids) {
			continue
		}
		if st, ok := provider.SignInStatus(sid); ok && (st.State == "waiting" || st.State == "installing") {
			return true
		}
	}
	return false
}

// ipInUse is the check an area's removal of its IP (ids nil) or some of
// its proxies makes under ipsMu: no account of the area goes through
// them (provider.AreaIPInUse), and no sign-in of it under way.
func ipInUse(areaID string, ids []string) func(renewal.IP) error {
	return func(x renewal.IP) error {
		if err := provider.AreaIPInUse(areaID, ids)(x); err != nil {
			return err
		}
		if signingThrough(areaID, x.IP, ids) {
			return errSigningThrough
		}
		return nil
	}
}
