package gateway

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
)

// An area's keys (docs/subsystems/areas.md, T-213): accounts/key adds one,
// accounts/keys up to adminKeysMost at once, each claimed and written on
// its own, and none rolled back when a later one fails. Both take the
// protocol and plan the window's key row does, and no opened date: that
// is a subscription account's.

// adminKeysMost is the most keys one accounts/keys takes.
const adminKeysMost = 100

// keyFields are what both key routes take beside the keys.
type keyFields struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	// Protocol is the one protocol the key works with, "" any (the
	// window's protocol picker)
	Protocol string `json:"protocol"`
	// Plan is the preset's plan the key is for, "" the provider's own
	Plan string `json:"plan"`
	// Opened, sent at all, is refused: a key has no opening date
	Opened json.RawMessage `json:"opened"`
	// Proxy is put on each key added, as accounts/proxy puts it (T-212)
	Proxy *proxyPick `json:"proxy"`
}

// keyProtocols are the protocols a key of p may be set to: any, and each
// API p has an address for, as the window's protocol picker offers.
func keyProtocols(p provider.Provider) []string {
	out := []string{""}
	for _, x := range []struct {
		proto provider.Protocol
		url   string
	}{{provider.Anthropic, p.Anthropic}, {provider.Chat, p.Chat}, {provider.Responses, p.Responses}, {provider.Gemini, p.Gemini}} {
		if x.url != "" {
			out = append(out, string(x.proto))
		}
	}
	return out
}

// keyTarget is the provider in's keys go to, with their protocol and
// plan, or false with the answer written: 404 for a provider the area
// wasn't given (or one that takes no keys), 400 for an opened date, a
// protocol it has no address for or a plan its preset doesn't sell, and
// a proxy picked as pickedProxy answers it.
func keyTarget(w http.ResponseWriter, c adminCall, in keyFields) (provider.Provider, provider.Protocol, string, bool) {
	if !slices.Contains(c.area.Providers, in.Provider) {
		adminNotFoundErr(w)
		return provider.Provider{}, "", "", false
	}
	p, err := provider.Find(in.Provider)
	if err != nil || p.Account != nil || p.IsPlugin() {
		adminNotFoundErr(w)
		return provider.Provider{}, "", "", false
	}
	if in.Opened != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, "a key has no opening date: opened is a subscription account's")
		return provider.Provider{}, "", "", false
	}
	if !slices.Contains(keyProtocols(*p), in.Protocol) {
		adminError(w, http.StatusBadRequest, adminBadRequest, p.Name+" takes a key's protocol as one of "+strings.Join(quoteEach(keyProtocols(*p)), ", "))
		return provider.Provider{}, "", "", false
	}
	plan := strings.TrimSpace(in.Plan)
	if plan != "" && !slices.ContainsFunc(provider.KeyPlans(*p), func(r provider.Region) bool { return r.ID == plan }) {
		adminError(w, http.StatusBadRequest, adminBadRequest, p.Name+" sells no plan "+strconv.Quote(plan)+" on keys of its own")
		return provider.Provider{}, "", "", false
	}
	if in.Proxy != nil {
		if _, _, ok := pickedProxy(w, c, in.Proxy); !ok {
			return provider.Provider{}, "", "", false
		}
	}
	return *p, provider.Protocol(in.Protocol), plan, true
}

func quoteEach(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strconv.Quote(s)
	}
	return out
}

// addAreaKey claims the key for c's area, then adds it to p, and puts
// pick (nil: none) on it: the account as told and 201, or the status,
// code and message to answer. A key magpie has (the owner's, another
// area's) is 409 and stays as it was. A key added whose pick couldn't be
// put on stays added, 201 with the message saying so.
func addAreaKey(c adminCall, p provider.Provider, name, key string, proto provider.Protocol, plan string, pick *proxyPick) (adminAccount, int, string, string) {
	fp := provider.KeyID(key)
	ref := p.ID + "/" + fp
	if slices.ContainsFunc(p.KeyList(), func(k provider.KeyInfo) bool { return k.ID == fp }) {
		return adminAccount{}, http.StatusConflict, adminConflict, "this key is in magpie already"
	}
	// the key a batch came with, and its area, read again before each
	// key is claimed (T-260): nothing is written once either changed
	if err := stillActs(c); err != nil {
		return claimFailure(err)
	}
	// owned first, so the key is the area's from the moment it is there;
	// the area's 500 judged in the same lock
	if err := area.ClaimUpTo(c.area.ID, ref, adminAccountsLimit); err != nil {
		switch {
		case errors.Is(err, area.ErrFull):
			return adminAccount{}, http.StatusConflict, adminFull, err.Error()
		case errors.Is(err, area.ErrTaken):
			return adminAccount{}, http.StatusConflict, adminConflict, "this account is in magpie already"
		case errors.Is(err, area.ErrNotFound):
			return adminAccount{}, http.StatusUnauthorized, adminKeyRequired, "this admin key's area was deleted"
		default:
			return adminAccount{}, http.StatusServiceUnavailable, adminAreasUnread, err.Error()
		}
	}
	if err := provider.AddKeyOnPlan(p.ID, name, key, proto, plan); err != nil {
		area.Release(ref)
		// added by the owner or another area since p was read: a conflict,
		// counted as one, never a failure that says so
		if q, ferr := provider.Find(p.ID); ferr == nil && q.HasKey(key) {
			return adminAccount{}, http.StatusConflict, adminConflict, "this key is in magpie already"
		}
		return adminAccount{}, http.StatusBadRequest, adminBadRequest, err.Error()
	}
	msg := ""
	if pick != nil {
		if err := pick.mount(c.area.ID, ref); err != nil {
			log.Printf("admin: area %s's key of %s, %s", c.area.ID, p.ID, mountFailed(err))
			msg = "added, but " + mountFailed(err)
		}
	}
	acc := adminAccount{ID: ref, Provider: p.ID, ProviderName: p.Name, Kind: "key", Name: fp, Gone: true}
	if q, err := provider.Find(p.ID); err == nil {
		describe(&acc, *q, fp)
	}
	if a, err := area.Get(c.area.ID); err == nil {
		if i := slices.IndexFunc(a.Accounts, func(o area.Account) bool { return o.Ref == ref }); i >= 0 {
			acc.Added = a.Accounts[i].Added
			if rec := a.Accounts[i].Proxy; rec != nil {
				acc.Proxy = &adminAccountProxy{IP: rec.IP, ID: rec.ID, Direct: rec.Direct, Overridden: provider.AreaProxyOverridden(p.ID, *rec)}
			}
		}
	}
	return acc, http.StatusCreated, "", msg
}

// adminFull is addAreaKey's code for an area at its 500 accounts, a 409
// conflict as the routes answer it.
const adminFull = adminConflict + ":full"

// keyRow is how one key of an accounts/keys went, in the order sent:
// never the key itself.
type keyRow struct {
	Index   int           `json:"index"` // from 1
	Status  string        `json:"status"`
	Account *adminAccount `json:"account,omitempty"`
	Masked  string        `json:"masked"`
	Message string        `json:"message,omitempty"`
}

// The statuses of a keyRow.
const (
	keyAdded     = "added"
	keyConflict  = "conflict"
	keyFailed    = "failed"
	keyDuplicate = "duplicate"
)

// heldAnswered answers 429 while c's area is held for its conflicts
// (conflictProbes), and says so.
func (s *Server) heldAnswered(w http.ResponseWriter, c adminCall, now time.Time) bool {
	wait, console := s.conflictHeld(c, now)
	if wait <= 0 {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(max(int((wait+time.Second-1)/time.Second), 1)))
	if console {
		adminError(w, http.StatusTooManyRequests, adminTooManyConflicts, "the console was told an account is in magpie already too often across areas: try again later")
		return true
	}
	adminError(w, http.StatusTooManyRequests, adminTooManyConflicts, "this area was told an account is in magpie already too often: try again later")
	return true
}

// adminAddKeys adds each key sent as accounts/key adds one, one at a
// time, and tells how each went. One added stays whatever comes after.
// An area held for its conflicts is 429 at the door; a request that
// reaches the line, or the area's 500 accounts, partway fails the rest,
// each saying why.
func (s *Server) adminAddKeys(w http.ResponseWriter, r *http.Request, c adminCall) {
	if s.heldAnswered(w, c, time.Now()) {
		return
	}
	var in struct {
		keyFields
		Keys []string `json:"keys"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	p, proto, plan, ok := keyTarget(w, c, in.keyFields)
	if !ok {
		return
	}
	keys := []string{}
	for _, k := range in.Keys {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 || len(keys) > adminKeysMost {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send 1 to "+strconv.Itoa(adminKeysMost)+" keys")
		return
	}
	name := strings.TrimSpace(in.Name)
	out := struct {
		Results  []keyRow `json:"results"`
		Added    int      `json:"added"`
		Conflict int      `json:"conflict"`
		Failed   int      `json:"failed"`
	}{Results: []keyRow{}}
	owned := len(c.area.Accounts)
	seen := map[string]bool{}
	stop := "" // why the rest fail, once one did for all
	for i, k := range keys {
		row := keyRow{Index: i + 1, Masked: provider.Mask(k)}
		fp := provider.KeyID(k)
		switch {
		case seen[fp]:
			row.Status = keyDuplicate
		case stop != "":
			row.Status, row.Message = keyFailed, stop
		case len(k) > 4096:
			row.Status, row.Message = keyFailed, "a key is up to 4096 characters"
		case owned >= adminAccountsLimit:
			row.Status, row.Message = keyFailed, area.ErrFull.Error()
		default:
			n := name
			if n != "" && i > 0 {
				n += " #" + strconv.Itoa(i+1)
			}
			acc, status, code, msg := addAreaKey(c, p, n, k, proto, plan, in.Proxy)
			switch {
			case code == adminFull:
				row.Status, row.Message = keyFailed, msg
				stop = msg
			case status == http.StatusCreated:
				row.Status, row.Account, row.Message = keyAdded, &acc, msg
				owned++
			case status == http.StatusConflict:
				row.Status, row.Message = keyConflict, msg
				if s.conflicted(c, time.Now()) {
					stop = "too many conflicts; try again later"
				}
			case status == http.StatusUnauthorized, status == http.StatusServiceUnavailable:
				row.Status, row.Message = keyFailed, msg
				stop = msg
			default:
				row.Status, row.Message = keyFailed, msg
			}
		}
		seen[fp] = true
		switch row.Status {
		case keyAdded:
			out.Added++
		case keyConflict:
			out.Conflict++
		case keyFailed:
			out.Failed++
		}
		out.Results = append(out.Results, row)
	}
	c.logf("added %d keys of %s (%d in magpie already, %d failed, %d sent twice)", out.Added, p.ID, out.Conflict, out.Failed, len(keys)-out.Added-out.Conflict-out.Failed)
	writeJSON(w, http.StatusOK, out)
}
