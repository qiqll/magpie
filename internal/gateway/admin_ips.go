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
// them, at public addresses alone, checks those, and puts them on its
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
	// checksMost proxy checks an area may start in checksWindow, one at
	// a time
	checksMost   = 10
	checksWindow = 10 * time.Minute
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
	if len(in.Proxies) > renewal.MaxIPProxies {
		adminError(w, http.StatusBadRequest, adminBadRequest, "an IP has at most 20 proxies")
		return
	}
	if !checkAreaProxies(w, r, in.Proxies) {
		return
	}
	x := renewal.IP{IP: in.IP, Name: in.Name, Proxies: in.Proxies}
	if in.Opened != nil {
		x.Opened = *in.Opened
	}
	got, err := areaIPAdd(c.area.ID, x, time.Now())
	if err != nil {
		ipWriteErr(w, err)
		return
	}
	log.Printf("admin: area %s (%s) added an IP with %d proxies", c.area.ID, c.area.Note, len(got.Proxies))
	replyIP(w, c, got, http.StatusCreated)
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
	if !checkAreaProxies(w, r, in.AddProxies) {
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
	got, err := areaIPChange(c.area.ID, x.IP, time.Now(), func(y *renewal.IP) error {
		// read again under ipsMu: an attach that passed the check above
		// holds it while it writes
		if len(in.RemoveProxies) > 0 {
			if err := provider.AreaIPInUse(c.area.ID, in.RemoveProxies)(*y); err != nil {
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
		y.Proxies = append(y.Proxies, in.AddProxies...)
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
	if err := areaIPRemove(c.area.ID, x.IP, provider.AreaIPInUse(c.area.ID, nil)); err != nil {
		ipWriteErr(w, err)
		return
	}
	if err := provider.SweepAreaProxies(); err != nil {
		log.Printf("admin: area %s's proxies not swept: %v", c.area.ID, err)
	}
	log.Printf("admin: area %s (%s) removed an IP", c.area.ID, c.area.Note)
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
// checksWindow, one at a time. In memory: a restart clears it. Nothing
// that waits runs under mu.
type proxyChecks struct {
	mu      sync.Mutex
	started map[string][]time.Time
	running map[string]bool
}

// begin starts a check of id's at now, or says how long until it may.
func (p *proxyChecks) begin(id string, now time.Time) (wait time.Duration, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if wait, ok := p.free(id, now); !ok {
		return wait, false
	}
	p.started[id] = append(p.started[id], now)
	p.running[id] = true
	return 0, true
}

// hold has id's checks for a request that makes many (a batch's host-name
// lines, a reread), at now: none other runs until end, and each check it
// makes is spent. False, with how long until it may, while one runs or
// none is left in the window (T-214: the batch routes share the budget).
func (p *proxyChecks) hold(id string, now time.Time) (wait time.Duration, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if wait, ok := p.free(id, now); !ok {
		return wait, false
	}
	p.running[id] = true
	return 0, true
}

// spend takes one of id's checks at now for a request that holds them;
// false when none is left in the window.
func (p *proxyChecks) spend(id string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recent(id, now)
	if len(p.started[id]) >= checksMost {
		return false
	}
	p.started[id] = append(p.started[id], now)
	return true
}

// free says whether id may start a check at now, none running and one
// left in the window, or how long until it may. Called with p.mu held.
func (p *proxyChecks) free(id string, now time.Time) (time.Duration, bool) {
	ts := p.recent(id, now)
	if p.running[id] {
		return time.Second, false
	}
	if len(ts) >= checksMost {
		return ts[len(ts)-checksMost].Add(checksWindow).Sub(now), false
	}
	return 0, true
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
	p.started[id] = ts
	return ts
}

// checksHeld answers 429 too_many_checks, to try again after wait.
func checksHeld(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(int((wait+time.Second-1)/time.Second), 1)))
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
	wait, ok := s.proxyChecks.begin(c.area.ID, time.Now())
	if !ok {
		checksHeld(w, wait)
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
	log.Printf("admin: area %s checked proxy %s: ok=%v %s", c.area.ID, in.Proxy, out.OK, out.Error)
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
