package gateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/renewal"
	"github.com/yetone/magpie/internal/settings"
)

// Proxy IPs (T-198, magpie-console's docs/magpie-api.md §9,
// docs/subsystems/areas.md "Proxy IPs"). An area's admin keeps the proxy
// exit IPs its area owns (ips.json, renewal.IP.Area), adds proxies to
// them, at public addresses alone and each kept once a check reads it
// leaves from the IP (T-225), checks those, and puts them on its
// area's accounts. The owner's IPs are no area's; one the owner gave an
// area (Models › Proxy IPs) is the area's to put on its accounts, never
// to check or change the proxies of. The console reads every area's,
// and changes nothing. A proxy is named by its id (renewal.ProxyID) and
// told with its login left out.

const (
	adminProxyNotPublic = "proxy_not_public"
	adminInUse          = "in_use"
	adminTooManyChecks  = "too_many_checks"
	adminIPsUnread      = "ips_unreadable"
	// adminProxyExit: an added proxy doesn't leave from its IP (T-225)
	adminProxyExit = "proxy_exit"
	// checksMost proxy checks an area may start in checksWindow, one at
	// a time
	checksMost   = 10
	checksWindow = 10 * time.Minute
	// consoleChecksMost proxy checks the console may start in
	// checksWindow across every area it acts for (T-222)
	consoleChecksMost = 20
)

// The area's writes, variables for the test that the store refuses an
// IP not the area's when a handler doesn't (TestAdminIPsOwnAreaOnly).
var (
	areaIPAdd    = renewal.AddAreaIP
	areaIPChange = renewal.ChangeAreaIP
	areaIPRemove = renewal.RemoveAreaIP
	areaIPAttach = provider.AttachAreaProxy
	areaDirect   = provider.AttachAreaDirect
)

// adminIP is an IP as the admin routes tell it (magpie-api.md's AreaIP).
type adminIP struct {
	IP           string          `json:"ip"`
	Area         string          `json:"area,omitempty"` // the console's alone
	Name         string          `json:"name"`
	Opened       *time.Time      `json:"opened,omitempty"`
	Renewal      *adminIPRenewal `json:"renewal,omitempty"`
	GivenByOwner bool            `json:"givenByOwner"`
	Proxies      []adminIPProxy  `json:"proxies"`
	Accounts     []string        `json:"accounts"`
}

// adminIPRenewal is an IP's period: Due as it was (true when State is
// soon or ended), State ("", "soon", "ended", as the window says) and
// DaysLeft, below 0 the days since it ended (T-214, §16.1).
type adminIPRenewal struct {
	Opened   time.Time `json:"opened"`
	Ends     time.Time `json:"ends"`
	Due      bool      `json:"due"`
	State    string    `json:"state"`
	DaysLeft int       `json:"daysLeft"`
}

type adminIPProxy struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// adminAccountProxy is the area's proxy on an account, or its direct (no
// IP nor proxy): overridden once the owner set the account's proxy since.
type adminAccountProxy struct {
	IP         string `json:"ip,omitempty"`
	ID         string `json:"id,omitempty"`
	Direct     bool   `json:"direct,omitempty"`
	Overridden bool   `json:"overridden"`
}

// ipOf is x, an IP of the area a, as told; withArea names the area (the
// console).
func ipOf(x renewal.IP, a area.Area, withArea bool, now time.Time) adminIP {
	out := adminIP{IP: x.IP, Name: x.Name, GivenByOwner: x.AddedBy != renewal.AddedByArea, Proxies: []adminIPProxy{}, Accounts: []string{}}
	if withArea {
		out.Area = a.ID
	}
	if !x.Opened.IsZero() {
		at := x.Opened
		out.Opened = &at
		if st := renewal.Of(x.Opened, now); st != nil {
			out.Renewal = &adminIPRenewal{Opened: st.Opened, Ends: st.Ends, Due: st.Due != "", State: st.Due, DaysLeft: st.DaysFrom(now)}
		}
	}
	for _, p := range x.Proxies {
		u := netproxy.Redact(p)
		if u == "" {
			u = "[proxy]"
		}
		out.Proxies = append(out.Proxies, adminIPProxy{ID: renewal.ProxyID(p), URL: u})
	}
	out.Accounts = usingIP(a, x.IP, "")
	return out
}

// usingIP are a's accounts its admin put a proxy of the IP ip on (only
// the proxy proxyID when it is set).
func usingIP(a area.Area, ip, proxyID string) []string {
	out := []string{}
	for _, c := range a.Accounts {
		if c.Proxy != nil && c.Proxy.IP == ip && (proxyID == "" || c.Proxy.ID == proxyID) {
			out = append(out, c.Ref)
		}
	}
	slices.Sort(out)
	return out
}

// readIPs is ips.json's IPs, or false with 503 answered: unknown is never
// no IPs.
func readIPs(w http.ResponseWriter) ([]renewal.IP, bool) {
	ips, _, err := renewal.IPs()
	if err != nil {
		log.Printf("admin: ips.json can't be read: %v", err)
		adminError(w, http.StatusServiceUnavailable, adminIPsUnread, "magpie can't read its ips.json now")
		return nil, false
	}
	return ips, true
}

// areaIP is the area's IP at the address ip, or false with 404 (or 503)
// answered: the owner's and another area's are no such IP.
func areaIP(w http.ResponseWriter, c adminCall, ip string) (renewal.IP, bool) {
	ips, ok := readIPs(w)
	if !ok {
		return renewal.IP{}, false
	}
	addr := ""
	if p := net.ParseIP(strings.TrimSpace(ip)); p != nil {
		addr = p.String()
	}
	for _, x := range ips {
		if addr != "" && x.IP == addr && x.Area == c.area.ID && c.area.ID != "" {
			return x, true
		}
	}
	adminError(w, http.StatusNotFound, adminNotFound, "no such IP")
	return renewal.IP{}, false
}

// ipWriteErr answers an area's IP write that failed.
func ipWriteErr(w http.ResponseWriter, err error) {
	var used *provider.InUseError
	switch {
	case errors.As(err, &used):
		inUse(w, used.Accounts)
	case errors.Is(err, errSigningThrough):
		adminError(w, http.StatusConflict, adminInUse, err.Error())
	case errors.Is(err, renewal.ErrIPNotFound):
		adminError(w, http.StatusNotFound, adminNotFound, "no such IP")
	case errors.Is(err, renewal.ErrIPTaken):
		adminError(w, http.StatusConflict, adminConflict, "this IP is in magpie already")
	case errors.Is(err, renewal.ErrAreaFull):
		adminError(w, http.StatusConflict, adminConflict, err.Error())
	case errors.Is(err, renewal.ErrIPsUnreadable):
		log.Printf("admin: ips.json can't be read: %v", err)
		adminError(w, http.StatusServiceUnavailable, adminIPsUnread, "magpie can't read its ips.json now")
	default:
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
	}
}

// checkAreaProxies says, answering 400 when not, whether each proxy can
// be one an area adds: one Settings takes, http, https, socks5 or socks5h
// with its port, at a public address (netproxy.CheckPublic).
func checkAreaProxies(w http.ResponseWriter, r *http.Request, proxies []string) bool {
	for _, p := range proxies {
		p = strings.TrimSpace(p)
		if p == "" || p == "direct" || len(p) > 2048 || settings.CheckProxy(p) != nil {
			adminError(w, http.StatusBadRequest, adminBadRequest, "a proxy is written scheme://host:port, its scheme http, https, socks5 or socks5h")
			return false
		}
		err := netproxy.CheckPublic(r.Context(), p)
		switch {
		case errors.Is(err, netproxy.ErrNotPublic), errors.Is(err, netproxy.ErrUnresolved):
			adminError(w, http.StatusBadRequest, adminProxyNotPublic, "an area's proxy is at a public address: not this computer, a private or link-local network, CGNAT, nor a name that resolves to one")
			return false
		case err != nil:
			adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
			return false
		}
	}
	return true
}

func (s *Server) adminIPs(w http.ResponseWriter, r *http.Request, c adminCall) {
	scope, ok := adminScope(w, r, c)
	if !ok {
		return
	}
	ips, ok := readIPs(w)
	if !ok {
		return
	}
	now := time.Now()
	out := []adminIP{}
	for _, a := range scope {
		for _, x := range ips {
			if a.ID != "" && x.Area == a.ID {
				out = append(out, ipOf(x, a, c.who.Admin == access.AdminConsole, now))
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// replyIP answers the area's IP ip as it is now.
func replyIP(w http.ResponseWriter, c adminCall, x renewal.IP, status int) {
	a, err := area.Get(c.area.ID)
	if err != nil {
		a = c.area
	}
	writeJSON(w, status, ipOf(x, a, false, time.Now()))
}

func (s *Server) adminAddIP(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		IP      string     `json:"ip"`
		Name    string     `json:"name"`
		Opened  *time.Time `json:"opened"`
		Proxies []string   `json:"proxies"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	ips, ok := readIPs(w)
	if !ok {
		return
	}
	// told before a check is spent; the area's own count tells nothing
	// of other IPs
	if n := len(slices.DeleteFunc(ips, func(x renewal.IP) bool { return x.Area != c.area.ID })); n >= renewal.MaxAreaIPs {
		ipWriteErr(w, renewal.ErrAreaFull)
		return
	}
	addr := net.ParseIP(strings.TrimSpace(in.IP))
	if addr == nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, "ip is an IPv4 or IPv6 address")
		return
	}
	proxies := newProxies(in.Proxies, nil)
	switch {
	case len(proxies) == 0:
		adminError(w, http.StatusBadRequest, adminBadRequest, "an area adds an IP with a proxy that leaves from it")
		return
	case len(proxies) > renewal.MaxIPProxies:
		adminError(w, http.StatusBadRequest, adminBadRequest, "an IP has at most 20 proxies")
		return
	}
	if !checkAreaProxies(w, r, proxies) || !s.confirmExits(w, r, c, addr.String(), proxies) {
		return
	}
	x := renewal.IP{IP: addr.String(), Name: in.Name, Proxies: proxies}
	if in.Opened != nil {
		x.Opened = *in.Opened
	}
	got, err := areaIPAdd(c.area.ID, x, time.Now())
	if err != nil {
		ipWriteErr(w, err)
		return
	}
	c.logf("added an IP with %d proxies", len(got.Proxies))
	replyIP(w, c, got, http.StatusCreated)
}

// newProxies are proxies trimmed, each once, those of had left out: the
// ones a request adds.
func newProxies(proxies, had []string) []string {
	var out []string
	for _, p := range proxies {
		if p = strings.TrimSpace(p); !slices.Contains(had, p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// confirmExits says each of proxies leaves from ip, read by a check of
// each, or answers why not (T-225: an area's proxy is kept only under
// the IP it leaves from, so an area can't hold an address it has no
// proxy at, nor put a proxy that leaves from elsewhere under its own).
// It holds the area's checks and spends one a proxy as it checks it,
// stopping at the first that doesn't leave from ip: 429
// too_many_checks, nothing connected to, while they can't all be spent;
// 400 proxy_exit with the check of that first, its login left out. None
// is no check.
func (s *Server) confirmExits(w http.ResponseWriter, r *http.Request, c adminCall, ip string, proxies []string) bool {
	if len(proxies) == 0 {
		return true
	}
	if len(proxies) > checksMost {
		adminError(w, http.StatusBadRequest, adminBadRequest, "an area adds at most "+strconv.Itoa(checksMost)+" proxies at once: each is checked")
		return false
	}
	now := time.Now()
	wait, console, ok := s.proxyChecks.hold(budgets(c), now)
	if !ok {
		checksHeld(w, wait, console)
		return false
	}
	defer s.proxyChecks.end(c.area.ID)
	if wait, console, ok := s.proxyChecks.room(budgets(c), len(proxies), now); !ok {
		checksHeld(w, wait, console)
		return false
	}
	for _, p := range proxies {
		// the console's total is another area's to spend meanwhile
		if !s.proxyChecks.spend(budgets(c), time.Now()) {
			wait, console, _ := s.proxyChecks.room(budgets(c), 1, time.Now())
			checksHeld(w, wait, console)
			return false
		}
		out := checkAreaProxy(r.Context(), p, ip)
		if out.OK && out.Matches {
			continue
		}
		out.Proxy = proxyShown(p)
		c.logf("an added proxy didn't leave from its IP: ok=%v %s", out.OK, out.Error)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "a proxy is added under the IP a check reads it leaves from: this one didn't leave from " + ip, "type": "invalid_request_error", "code": adminProxyExit},
			"check": out,
		})
		return false
	}
	return true
}

// inUse answers 409 in_use, naming the area's accounts on what is
// removed.
func inUse(w http.ResponseWriter, accounts []string) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":    map[string]any{"message": "the area's accounts below go through it: take its proxy off them first", "type": "invalid_request_error", "code": adminInUse},
		"accounts": accounts,
	})
}

func (s *Server) adminUpdateIP(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		IP            string     `json:"ip"`
		Name          *string    `json:"name"`
		Opened        *time.Time `json:"opened"`
		AddProxies    []string   `json:"addProxies"`
		RemoveProxies []string   `json:"removeProxies"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	x, ok := areaIP(w, c, in.IP)
	if !ok {
		return
	}
	if x.AddedBy != renewal.AddedByArea && len(in.AddProxies)+len(in.RemoveProxies) > 0 {
		adminError(w, http.StatusBadRequest, adminBadRequest, "the owner gave this IP: its proxies are the owner's to change")
		return
	}
	adds := newProxies(in.AddProxies, x.Proxies)
	if !checkAreaProxies(w, r, adds) {
		return
	}
	for _, id := range in.RemoveProxies {
		if _, ok := x.ProxyOf(id); !ok {
			adminError(w, http.StatusNotFound, adminNotFound, "no such proxy")
			return
		}
		if used := usingIP(c.area, x.IP, id); len(used) > 0 {
			inUse(w, used)
			return
		}
	}
	// told before a check is spent
	if len(x.Proxies)-len(in.RemoveProxies)+len(adds) > renewal.MaxIPProxies {
		adminError(w, http.StatusBadRequest, adminBadRequest, "an IP has at most 20 proxies")
		return
	}
	if !s.confirmExits(w, r, c, x.IP, adds) {
		return
	}
	got, err := areaIPChange(c.area.ID, x.IP, time.Now(), func(y *renewal.IP) error {
		// read again under ipsMu: an attach that passed the check above
		// holds it while it writes
		if len(in.RemoveProxies) > 0 {
			if err := ipInUse(c.area.ID, in.RemoveProxies)(*y); err != nil {
				return err
			}
		}
		if in.Name != nil {
			y.Name = *in.Name
		}
		if in.Opened != nil {
			y.Opened = *in.Opened
		}
		y.Proxies = slices.DeleteFunc(y.Proxies, func(p string) bool { return slices.Contains(in.RemoveProxies, renewal.ProxyID(p)) })
		// the checked alone: one it had when read is never added unchecked
		for _, p := range adds {
			if !slices.Contains(y.Proxies, p) {
				y.Proxies = append(y.Proxies, p)
			}
		}
		return nil
	})
	if err != nil {
		ipWriteErr(w, err)
		return
	}
	// a proxy taken out while being put on an account comes off it
	if len(in.RemoveProxies) > 0 {
		if err := provider.SweepAreaProxies(); err != nil {
			log.Printf("admin: area %s's proxies not swept: %v", c.area.ID, err)
		}
	}
	c.logf("changed an IP")
	replyIP(w, c, got, http.StatusOK)
}

func (s *Server) adminDeleteIP(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		IP string `json:"ip"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	x, ok := areaIP(w, c, in.IP)
	if !ok {
		return
	}
	if x.AddedBy != renewal.AddedByArea {
		adminError(w, http.StatusBadRequest, adminBadRequest, "the owner gave this IP: the owner takes it back")
		return
	}
	if used := usingIP(c.area, x.IP, ""); len(used) > 0 {
		inUse(w, used)
		return
	}
	if err := areaIPRemove(c.area.ID, x.IP, ipInUse(c.area.ID, nil)); err != nil {
		ipWriteErr(w, err)
		return
	}
	if err := provider.SweepAreaProxies(); err != nil {
		log.Printf("admin: area %s's proxies not swept: %v", c.area.ID, err)
	}
	c.logf("removed an IP")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminAccountProxy(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		ID    string `json:"id"`
		IP    string `json:"ip"`
		Proxy string `json:"proxy"`
		// Direct: the account goes through no proxy (T-213)
		Direct bool `json:"direct"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	if !ownsRef(c.area, in.ID) {
		adminNotFoundErr(w)
		return
	}
	if in.Direct && (in.IP != "" || in.Proxy != "") {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send direct, or an ip and its proxy, not both")
		return
	}
	if in.Direct {
		if !attachAnswered(w, areaDirect(c.area.ID, in.ID)) {
			return
		}
	} else if in.Proxy == "" {
		if err := provider.DetachAreaProxy(c.area.ID, in.ID); err != nil {
			adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
			return
		}
	} else {
		x, ok := areaIP(w, c, in.IP)
		if !ok {
			return
		}
		if _, ok := x.ProxyOf(in.Proxy); !ok {
			adminError(w, http.StatusNotFound, adminNotFound, "no such proxy")
			return
		}
		if !attachAnswered(w, areaIPAttach(c.area.ID, in.ID, x.IP, in.Proxy)) {
			return
		}
	}
	pid, _, _ := strings.Cut(in.ID, "/")
	switch {
	case in.Direct:
		c.logf("set an account of %s direct", pid)
	case in.Proxy == "":
		c.logf("took the proxy off an account of %s", pid)
	default:
		c.logf("put a proxy on an account of %s", pid)
	}
	a, err := area.Get(c.area.ID)
	if err != nil {
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
		return
	}
	for _, acc := range areaAccounts(a) {
		if acc.ID == in.ID {
			writeJSON(w, http.StatusOK, acc)
			return
		}
	}
	adminNotFoundErr(w)
}

// attachAnswered says an attach, of a proxy or direct, went, or answers
// why not.
func attachAnswered(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, renewal.ErrIPNotFound), errors.Is(err, area.ErrNotOwned), errors.Is(err, provider.ErrNoAccount):
		adminNotFoundErr(w)
	case errors.Is(err, provider.ErrOverridden):
		adminError(w, http.StatusConflict, adminConflict, "the owner set this account's proxy: it stays the owner's")
	case errors.Is(err, renewal.ErrIPsUnreadable):
		ipWriteErr(w, err)
	default:
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
	}
	return false
}

// proxyChecks holds each area's proxy checks to checksMost in
// checksWindow, one at a time, and the console's across every area to
// consoleChecksMost in the window (T-222), as many at a time as its
// areas allow. In memory: a restart clears it. Nothing that waits runs
// under mu. Each call names the counts it spends (budgets), its area's
// first; the area's is the one held while a check runs.
type proxyChecks struct {
	mu      sync.Mutex
	started map[string][]time.Time
	running map[string]bool
}

// checksLimit is how many checks id may start in the window.
func checksLimit(id string) int {
	if id == consoleBudget {
		return consoleChecksMost
	}
	return checksMost
}

// begin starts a check of ids' at now, or says how long until it may
// and whether the console's total is what holds it.
func (p *proxyChecks) begin(ids []string, now time.Time) (wait time.Duration, console, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if wait, console, ok := p.free(ids, now); !ok {
		return wait, console, false
	}
	for _, id := range ids {
		p.started[id] = append(p.started[id], now)
	}
	p.running[ids[0]] = true
	return 0, false, true
}

// hold has ids' checks for a request that makes many (a batch's
// host-name lines, a reread), at now: none other of the area's runs
// until end, and each check it makes is spent. False, with how long
// until it may, while one runs or none is left in the window (T-214: the
// batch routes share the budget).
func (p *proxyChecks) hold(ids []string, now time.Time) (wait time.Duration, console, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if wait, console, ok := p.free(ids, now); !ok {
		return wait, console, false
	}
	p.running[ids[0]] = true
	return 0, false, true
}

// spend takes one check of each of ids at now for a request that holds
// them; none, and false, when one has none left in the window.
func (p *proxyChecks) spend(ids []string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		if len(p.recent(id, now)) >= checksLimit(id) {
			return false
		}
	}
	for _, id := range ids {
		p.started[id] = append(p.started[id], now)
	}
	return true
}

// room says whether each of ids has n checks left in the window at now,
// spending none, or how long until it has and whether the console's
// total is the longer wait. More than an id may ever have is never room.
func (p *proxyChecks) room(ids []string, n int, now time.Time) (wait time.Duration, console, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok = true
	for _, id := range ids {
		ts, most := p.recent(id, now), checksLimit(id)
		w := time.Duration(0)
		switch k := len(ts) + n - most; {
		case n > most:
			w = checksWindow
		case k > 0:
			w = ts[k-1].Add(checksWindow).Sub(now)
		default:
			continue
		}
		ok = false
		if w > wait {
			wait, console = w, id == consoleBudget
		}
	}
	return wait, console, ok
}

// free says whether ids may start a check at now, the area's (ids[0])
// not running one and each with one left in the window, or how long
// until they may and whether the console's total is the longer wait.
// Called with p.mu held.
func (p *proxyChecks) free(ids []string, now time.Time) (wait time.Duration, console, ok bool) {
	ok = true
	for i, id := range ids {
		ts, most := p.recent(id, now), checksLimit(id)
		w := time.Duration(0)
		switch {
		case len(ts) >= most:
			w = ts[len(ts)-most].Add(checksWindow).Sub(now)
		case i == 0 && p.running[id]:
			w = time.Second
		default:
			continue
		}
		ok = false
		if w > wait {
			wait, console = w, id == consoleBudget
		}
	}
	return wait, console, ok
}

// recent are id's checks still in the window at now, the older let go
// of. Called with p.mu held.
func (p *proxyChecks) recent(id string, now time.Time) []time.Time {
	if p.started == nil {
		p.started, p.running = map[string][]time.Time{}, map[string]bool{}
	}
	ts := p.started[id]
	for len(ts) > 0 && now.Sub(ts[0]) >= checksWindow {
		ts = ts[1:]
	}
	if len(ts) == 0 {
		delete(p.started, id)
		return nil
	}
	p.started[id] = ts
	return ts
}

// checksHeld answers 429 too_many_checks, to try again after wait: the
// console's total across areas when console, else the area's.
func checksHeld(w http.ResponseWriter, wait time.Duration, console bool) {
	w.Header().Set("Retry-After", strconv.Itoa(max(int((wait+time.Second-1)/time.Second), 1)))
	if console {
		adminError(w, http.StatusTooManyRequests, adminTooManyChecks, "the console checks 20 proxies in 10 minutes across areas: try again later")
		return
	}
	adminError(w, http.StatusTooManyRequests, adminTooManyChecks, "this area checks 10 proxies in 10 minutes, one at a time: try again later")
}

func (p *proxyChecks) end(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, id)
}

// areaTraceURL is the page a proxy check asks through the proxy, fixed
// (an area names no target); a test points it at its own.
var areaTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

// areaCheckTimeout is how long a proxy check may take.
const areaCheckTimeout = 10 * time.Second

// areaCheckClient sends a proxy check through the proxy its context
// names (netproxy.With), on a connection of its own, guarded as every
// transport Dispatch makes is, and follows no redirect.
var areaCheckClient = sync.OnceValue(func() *http.Client {
	t := &http.Transport{Proxy: netproxy.Func, DisableKeepAlives: true, TLSClientConfig: upstreamTLS, TLSHandshakeTimeout: areaCheckTimeout}
	return &http.Client{
		Transport:     netproxy.Dispatch(t),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
})

// adminProxyCheck is a check's answer: never the error's text, nor the
// address magpie's own requests leave from.
type adminProxyCheck struct {
	Proxy   string `json:"proxy"`
	OK      bool   `json:"ok"`
	IP      string `json:"ip,omitempty"`
	Loc     string `json:"loc,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	Matches bool   `json:"matches"`
	Error   string `json:"error"`
}

func (s *Server) adminCheckProxy(w http.ResponseWriter, r *http.Request, c adminCall) {
	wait, console, ok := s.proxyChecks.begin(budgets(c), time.Now())
	if !ok {
		checksHeld(w, wait, console)
		return
	}
	defer s.proxyChecks.end(c.area.ID)
	var in struct {
		IP    string `json:"ip"`
		Proxy string `json:"proxy"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	x, ok := areaIP(w, c, in.IP)
	if !ok {
		return
	}
	p, ok := x.ProxyOf(in.Proxy)
	if !ok {
		adminError(w, http.StatusNotFound, adminNotFound, "no such proxy")
		return
	}
	if x.AddedBy != renewal.AddedByArea {
		adminError(w, http.StatusBadRequest, adminBadRequest, "only proxies this area added can be checked")
		return
	}
	out := checkAreaProxy(r.Context(), p, x.IP)
	out.Proxy = in.Proxy
	c.logf("checked proxy %s: ok=%v %s", in.Proxy, out.OK, out.Error)
	writeJSON(w, http.StatusOK, out)
}

// checkAreaProxy asks the trace page through the proxy p, whose IP is
// ip, and says what came back in the categories an area is told.
func checkAreaProxy(ctx context.Context, p, ip string) adminProxyCheck {
	var out adminProxyCheck
	ctx, cancel := context.WithTimeout(ctx, areaCheckTimeout)
	defer cancel()
	if err := netproxy.CheckPublic(ctx, p); err != nil {
		out.Error = proxyErrorKind(err)
		return out
	}
	start := time.Now()
	// held to public addresses at its dial whether or not ips.json names
	// it: a probe's and a batch's proxies aren't saved yet
	req, err := http.NewRequestWithContext(netproxy.With(netproxy.WithPublicOnly(ctx), p), http.MethodGet, areaTraceURL, nil)
	if err != nil {
		out.Error = "refused"
		return out
	}
	res, err := areaCheckClient().Do(req)
	if err != nil {
		out.Error = proxyErrorKind(err)
		return out
	}
	defer res.Body.Close()
	out.Ms = time.Since(start).Milliseconds()
	switch {
	case res.StatusCode == http.StatusProxyAuthRequired:
		out.Error = "proxy_auth"
		return out
	case res.StatusCode != http.StatusOK:
		out.Error = "http_status"
		return out
	}
	sc := bufio.NewScanner(io.LimitReader(res.Body, 16<<10))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "ip":
			if a := net.ParseIP(strings.TrimSpace(v)); a != nil {
				out.IP = a.String()
			}
		case "loc":
			if v = strings.TrimSpace(v); len(v) <= 3 {
				out.Loc = v
			}
		}
	}
	if out.IP == "" {
		out.Error = "http_status"
		return out
	}
	out.OK, out.Matches = true, out.IP == ip
	return out
}

// proxyErrorKind is the category of a proxy check's failure: refused,
// timeout, tls, proxy_auth, not_public or dns. A failure none of them
// names is refused: the proxy didn't carry the request.
func proxyErrorKind(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var recErr tls.RecordHeaderError
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var netErr net.Error
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, netproxy.ErrNotPublic):
		return "not_public"
	case errors.Is(err, netproxy.ErrUnresolved), errors.As(err, &dnsErr):
		return "dns"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case strings.Contains(text, "proxy authentication required"), strings.Contains(text, "authentication failed"):
		return "proxy_auth"
	case errors.As(err, &certErr), errors.As(err, &recErr), errors.As(err, &unknownCA), errors.As(err, &hostErr),
		strings.Contains(text, "tls:"), strings.Contains(text, "x509:"):
		return "tls"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	}
	return "refused"
}
