package gateway

import (
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/yetone/magpie/internal/provider"
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
// for direct), with p's IP as ips.json writes it; or false with the
// answer written: 400 for a pick that names neither or both, 404 for an
// IP or proxy that isn't c's area's (another area's alike).
func pickedProxy(w http.ResponseWriter, c adminCall, p *proxyPick) (string, bool) {
	if p.Direct {
		if p.IP != "" || p.ID != "" {
			adminError(w, http.StatusBadRequest, adminBadRequest, "send proxy as direct, or as an ip and its proxy's id, not both")
			return "", false
		}
		return "direct", true
	}
	if p.IP == "" || p.ID == "" {
		adminError(w, http.StatusBadRequest, adminBadRequest, `send proxy as {"ip","id"}, one of this area's IPs and a proxy of it, or {"direct":true}`)
		return "", false
	}
	x, ok := areaIP(w, c, p.IP)
	if !ok {
		return "", false
	}
	url, ok := x.ProxyOf(p.ID)
	if !ok {
		adminError(w, http.StatusNotFound, adminNotFound, "no such proxy")
		return "", false
	}
	p.IP = x.IP
	return url, true
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

// pickBox is the pick an area's sign-in has once it is done: the one it
// started with, or one picked as its address was pasted. url is the
// pick's proxy as the sign-in goes through it, "" for none picked.
type pickBox struct {
	mu   sync.Mutex
	pick *proxyPick
	url  string
}

func (b *pickBox) get() (*proxyPick, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pick, b.url
}

// set has the sign-in end with p through url, and answers what it had.
func (b *pickBox) set(p *proxyPick, url string) (*proxyPick, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	was, wasURL := b.pick, b.url
	b.pick, b.url = p, url
	return was, wasURL
}

// mountOnDone is done, and then the box's pick put on the account a
// sign-in of area id left the area's: none on one that ended otherwise
// (a conflict is another's account).
func mountOnDone(done func(provider.SignInState) provider.SignInState, id string, box *pickBox) func(provider.SignInState) provider.SignInState {
	return func(st provider.SignInState) provider.SignInState {
		st = done(st)
		p, _ := box.get()
		if p == nil || st.State != "done" {
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
