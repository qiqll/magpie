package gateway

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/renewal"
	"github.com/yetone/magpie/internal/settings"
)

// More proxy-IP routes for an area (T-214, magpie-console's
// docs/magpie-api.md §16, docs/subsystems/areas.md "Proxy IPs"): the
// exit of a proxy not saved yet (ips/probe), many proxies at once
// (ips/batch), and the exits of the area's own proxies on its accounts
// read again (ips/reread). Each check any of them makes is one of the
// area's proxyChecks, the budget ips/check spends, and goes to the same
// fixed page; a proxy's login is never told, logged or put in an error.

const (
	// probeBodyMost and batchBodyMost bound the bodies the routes read
	probeBodyMost = 16 << 10
	batchBodyMost = 256 << 10
)

// proxyLooks says p can be a proxy an area adds, by its shape alone:
// one Settings takes, not "direct", of at most 2048 bytes.
func proxyLooks(p string) bool {
	return p != "" && p != "direct" && len(p) <= 2048 && settings.CheckProxy(p) == nil
}

// proxyShown is p as an area is told it: its login left out.
func proxyShown(p string) string {
	if u := netproxy.Redact(p); u != "" {
		return u
	}
	return "[proxy]"
}

func (s *Server) adminProbeProxy(w http.ResponseWriter, r *http.Request, c adminCall) {
	// spent first, whatever comes of it: judging a name public resolves it
	wait, console, ok := s.proxyChecks.begin(budgets(c), time.Now())
	if !ok {
		checksHeld(w, wait, console)
		return
	}
	defer s.proxyChecks.end(c.area.ID)
	r.Body = http.MaxBytesReader(w, r.Body, probeBodyMost)
	var in struct {
		Proxy string `json:"proxy"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	p := strings.TrimSpace(in.Proxy)
	if !proxyLooks(p) {
		adminError(w, http.StatusBadRequest, adminBadRequest, "a proxy is written scheme://host:port, its scheme http, https, socks5 or socks5h")
		return
	}
	if !checkAreaProxies(w, r, []string{p}) {
		return
	}
	// a host that is an IP matches an exit that is it
	host := ""
	if u, err := netproxy.Parse(p); err == nil {
		if a := net.ParseIP(u.Hostname()); a != nil {
			host = a.String()
		}
	}
	out := checkAreaProxy(r.Context(), p, host)
	out.Proxy = proxyShown(p)
	out.Matches = out.OK && host != "" && out.Matches
	c.logf("probed a proxy not saved: ok=%v %s", out.OK, out.Error)
	writeJSON(w, http.StatusOK, out)
}

// The statuses of a batchRow: what became of a line. A dry run's are
// ok, invalid and not_public alone.
const (
	batchAdded     = "added"
	batchMerged    = "merged"
	batchExists    = "exists"
	batchInvalid   = "invalid"
	batchNotPublic = "not_public"
	batchConflict  = "conflict"
	batchFailed    = "failed"
	batchOK        = "ok"
)

// batchRow is a line of a batch as it went (magpie-api.md's BatchRow).
type batchRow struct {
	Line    int           `json:"line"`
	Status  string        `json:"status"`
	IP      string        `json:"ip,omitempty"`
	Proxy   *adminIPProxy `json:"proxy,omitempty"`
	Message string        `json:"message,omitempty"`
	url     string        // the proxy as it is kept, with its login
	name    bool          // its host is a name: its IP is its exit
}

// adminBatchIPs adds the proxies of text, one a line in the window's
// Batch add formats (renewal.ReadBatch), each under the exit a check
// reads, one spent a line: a host that is an IP only when its exit is
// that IP (T-225), a name whatever its exit, and a name's line only when
// check is sent (a host that is an IP is checked whatever check says:
// it is never kept unread). The area's IPs and their proxies are judged and
// written at once (renewal.AddAreaProxies). A dry run reads the lines
// and judges a host that is an IP public, and does no more: it resolves
// no name, connects nowhere, writes nothing and judges no owner, so it
// tells nothing of other IPs.
func (s *Server) adminBatchIPs(w http.ResponseWriter, r *http.Request, c adminCall) {
	r.Body = http.MaxBytesReader(w, r.Body, batchBodyMost)
	var in struct {
		Text   string `json:"text"`
		Type   string `json:"type"`
		Check  bool   `json:"check"`
		DryRun bool   `json:"dryRun"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	typ := cmp.Or(strings.ToLower(strings.TrimSpace(in.Type)), "socks5")
	typ, ok := renewal.BatchTypes[typ]
	if !ok {
		adminError(w, http.StatusBadRequest, adminBadRequest, "type is http, https, socks5 or socks5h")
		return
	}
	if err := renewal.CheckBatch(in.Text); err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	lines := renewal.ReadBatch(in.Text, typ)
	if len(lines) == 0 || len(lines) > renewal.BatchMost {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send 1 to "+strconv.Itoa(renewal.BatchMost)+" proxies, one a line")
		return
	}
	rows := make([]batchRow, len(lines))
	names, literals := 0, 0
	for i, l := range lines {
		row := batchRow{Line: l.N}
		switch p := l.Proxy; {
		case l.Err != nil:
			row.Status, row.Message = batchInvalid, l.Err.Error()
		case !proxyLooks(p.URL):
			row.Status, row.Message = batchInvalid, "a proxy is written scheme://host:port, its scheme http, https, socks5 or socks5h"
		case p.IP == "":
			row.Proxy, row.url, row.name = &adminIPProxy{ID: renewal.ProxyID(p.URL), URL: proxyShown(p.URL)}, p.URL, true
			names++
		default:
			row.Proxy, row.url = &adminIPProxy{ID: renewal.ProxyID(p.URL), URL: proxyShown(p.URL)}, p.URL
			// an IP's host resolves nothing: this judges the address alone
			err := netproxy.CheckPublic(r.Context(), p.URL)
			switch {
			case errors.Is(err, netproxy.ErrNotPublic), errors.Is(err, netproxy.ErrUnresolved):
				row.Status, row.Message = batchNotPublic, "an area's proxy is at a public address"
			case err != nil:
				row.Status, row.Message = batchInvalid, err.Error()
			default:
				row.IP = net.ParseIP(p.IP).String()
				literals++
			}
		}
		rows[i] = row
	}
	if in.DryRun {
		for i := range rows {
			if rows[i].Status == "" {
				rows[i].Status = batchOK
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "added": 0})
		return
	}
	now := time.Now()
	if s.heldAnswered(w, c, now) {
		return
	}
	if literals > 0 || in.Check && names > 0 {
		wait, console, ok := s.proxyChecks.hold(budgets(c), now)
		if !ok {
			checksHeld(w, wait, console)
			return
		}
		defer s.proxyChecks.end(c.area.ID)
	}
	for i := range rows {
		row := &rows[i]
		switch {
		case row.Status != "":
		case row.name && !in.Check:
			row.Status, row.Message = batchFailed, "a host name needs check"
		case !s.proxyChecks.spend(budgets(c), time.Now()):
			row.Status, row.Message = batchFailed, "check limit reached"
		default:
			// a name is kept under its exit, an IP only once it is its exit
			got := checkAreaProxy(r.Context(), row.url, row.IP)
			switch {
			case got.Error == "not_public":
				row.Status, row.Message = batchNotPublic, "an area's proxy is at a public address"
			case !got.OK:
				row.Status, row.Message = batchFailed, "the proxy didn't carry the check: "+got.Error
			case row.name:
				row.IP = got.IP
			case !got.Matches:
				row.Status, row.Message = batchFailed, "the proxy leaves from "+got.IP+", not this IP"
			}
		}
	}
	var adds []renewal.AreaProxyAdd
	var at []int
	for i, row := range rows {
		if row.Status == "" {
			adds = append(adds, renewal.AreaProxyAdd{IP: row.IP, Proxy: row.url})
			at = append(at, i)
		}
	}
	added, merged, conflicts := 0, 0, 0
	if len(adds) > 0 {
		got, err := renewal.AddAreaProxies(c.area.ID, adds, time.Now(), func() bool {
			return s.conflicted(c, time.Now())
		})
		if err != nil {
			batchWriteErr(w, c, err)
			return
		}
		for k, i := range at {
			row := &rows[i]
			switch got[k] {
			case renewal.BatchAdded:
				row.Status = batchAdded
				added++
			case renewal.BatchMerged:
				row.Status = batchMerged
				merged++
			case renewal.BatchExists:
				row.Status = batchExists
			case renewal.BatchTaken:
				row.Status, row.Message = batchConflict, "this IP is in magpie already"
				conflicts++
			case renewal.BatchGiven:
				row.Status, row.Message = batchFailed, "the owner gave this IP: its proxies are the owner's to change"
			case renewal.BatchIPFull:
				row.Status, row.Message = batchFailed, renewal.ErrIPProxiesFull.Error()
			case renewal.BatchAreaFull:
				row.Status, row.Message = batchFailed, renewal.ErrAreaFull.Error()
			case renewal.BatchAllFull:
				row.Status, row.Message = batchFailed, "magpie keeps at most "+strconv.Itoa(renewal.MaxIPs)+" IPs"
			default:
				row.Status, row.Message = batchFailed, "too many conflicts; try again later"
			}
		}
	}
	c.logf("batch-added proxies: %d lines, %d new IPs, %d merged, %d in magpie already", len(rows), added, merged, conflicts)
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "added": added + merged})
}

// batchWriteErr answers a batch's write that failed as ipWriteErr does,
// but a whole-list check's text (CheckIPs), which can name an IP not the
// area's, is logged and not told (security review L2).
func batchWriteErr(w http.ResponseWriter, c adminCall, err error) {
	if errors.Is(err, renewal.ErrIPsUnreadable) || errors.Is(err, renewal.ErrIPNotFound) {
		ipWriteErr(w, err)
		return
	}
	log.Printf("admin: area %s's batch not written: %v", c.area.ID, err)
	adminError(w, http.StatusServiceUnavailable, adminIPsUnread, "magpie's IPs can't take this batch now: nothing was written")
}

// rereadMove is a proxy reread moved to the IP it now leaves from.
type rereadMove struct {
	Proxy string `json:"proxy"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// adminRereadIPs reads again the exit of each proxy of an IP the area
// made that is on one of its accounts, a check spent each. One whose exit
// is another IP now moves there, with its accounts (renewal.MoveAreaProxy,
// area.MoveProxy): to an IP the area made, else a new one of the area's.
// An exit magpie has as no IP the area made is failed, whose not said,
// and counted with the conflicts. An IP the owner gave is not read.
func (s *Server) adminRereadIPs(w http.ResponseWriter, r *http.Request, c adminCall) {
	r.Body = http.MaxBytesReader(w, r.Body, probeBodyMost)
	var in struct{}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		adminError(w, http.StatusBadRequest, adminBadRequest, "the body isn't the JSON this route takes: "+err.Error())
		return
	}
	now := time.Now()
	if s.heldAnswered(w, c, now) {
		return
	}
	ips, ok := readIPs(w)
	if !ok {
		return
	}
	a, err := area.Get(c.area.ID)
	if err != nil {
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
		return
	}
	type proxyOn struct{ ip, id, url string }
	var todo []proxyOn
	for _, acc := range a.Accounts {
		p := acc.Proxy
		if p == nil || p.Direct || slices.ContainsFunc(todo, func(x proxyOn) bool { return x.ip == p.IP && x.id == p.ID }) {
			continue
		}
		for _, x := range ips {
			if x.IP == p.IP && x.Area == a.ID && a.ID != "" && x.AddedBy == renewal.AddedByArea {
				if u, ok := x.ProxyOf(p.ID); ok {
					todo = append(todo, proxyOn{x.IP, p.ID, u})
				}
			}
		}
	}
	slices.SortFunc(todo, func(x, y proxyOn) int { return cmp.Or(cmp.Compare(x.ip, y.ip), cmp.Compare(x.id, y.id)) })
	out := struct {
		Moved     []rereadMove `json:"moved"`
		Failed    []string     `json:"failed"`
		Unchanged int          `json:"unchanged"`
	}{Moved: []rereadMove{}, Failed: []string{}}
	if len(todo) > 0 {
		wait, console, ok := s.proxyChecks.hold(budgets(c), now)
		if !ok {
			checksHeld(w, wait, console)
			return
		}
		defer s.proxyChecks.end(a.ID)
	}
	stop := false
	for _, x := range todo {
		if stop || !s.proxyChecks.spend(budgets(c), time.Now()) {
			out.Failed = append(out.Failed, x.id)
			continue
		}
		got := checkAreaProxy(r.Context(), x.url, x.ip)
		switch {
		case !got.OK:
			out.Failed = append(out.Failed, x.id)
			continue
		case got.IP == x.ip:
			out.Unchanged++
			continue
		}
		err := renewal.MoveAreaProxy(a.ID, x.ip, x.id, got.IP, time.Now(), func(to string) error {
			return area.MoveProxy(a.ID, x.ip, x.id, to)
		})
		switch {
		case err == nil:
			out.Moved = append(out.Moved, rereadMove{Proxy: x.id, From: x.ip, To: got.IP})
		case errors.Is(err, renewal.ErrIPTaken):
			out.Failed = append(out.Failed, x.id)
			stop = s.conflicted(c, time.Now())
		default:
			log.Printf("admin: area %s's proxy %s not moved: %v", a.ID, x.id, err)
			out.Failed = append(out.Failed, x.id)
		}
	}
	c.logf("read its proxies' exits again: %d moved, %d failed, %d unchanged", len(out.Moved), len(out.Failed), out.Unchanged)
	writeJSON(w, http.StatusOK, out)
}
