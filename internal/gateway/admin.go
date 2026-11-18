package gateway

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// The admin routes (docs/subsystems/areas.md): an area's admin key lists,
// adds and removes the accounts its area owns and reads what they spent;
// the console's key reads every area and makes, changes and deletes
// them. Neither reaches anything else (readOnlyGuard refuses an admin key
// every other route), and neither routes a request: traffic is the
// owner's, and an area's accounts serve it as any other.
//
// Each handler judges the caller again (admin), never by where it came
// from: lanGuard lets a request from this computer through with no key,
// or a wrong one, as the owner's, and a console beside magpie is one.

// AdminPath is where the admin routes are.
const AdminPath = "/v1/magpie/admin"

// The error codes the admin routes answer with.
const (
	AdminKeyCode       = "gateway_key_admin_only"
	adminKeyRequired   = "admin_key_required"
	adminIPNotAllowed  = "admin_ip_not_allowed"
	adminConsoleOnly   = "admin_console_only"
	adminAreaOnly      = "admin_area_only"
	adminAreasUnread   = "areas_unreadable"
	adminNotFound      = "not_found"
	adminConflict      = "conflict"
	adminBadRequest    = "bad_request"
	AdminAllowIPsEnv   = "MAGPIE_ADMIN_ALLOW_IPS"
	adminBodyMost      = 64 << 10
	adminAccountsLimit = 500
)

// adminRoutes are the routes of each admin role, by the pattern the
// request is routed to: an admin key is refused every other
// (readOnlyGuard), and a route of the other role by its handler.
var adminRoutes = map[string]map[string]bool{
	access.AdminArea: {
		"GET " + AdminPath + "/me":                    true,
		"GET " + AdminPath + "/options":               true,
		"GET " + AdminPath + "/accounts":              true,
		"POST " + AdminPath + "/accounts/key":         true,
		"POST " + AdminPath + "/accounts/keys":        true,
		"POST " + AdminPath + "/accounts/remove":      true,
		"GET " + AdminPath + "/usage":                 true,
		"GET " + AdminPath + "/usage/requests":        true,
		"GET " + AdminPath + "/usage/stats":           true,
		"POST " + AdminPath + "/signin":               true,
		"GET " + AdminPath + "/signin/{id}":           true,
		"POST " + AdminPath + "/signin/{id}/callback": true,
		"POST " + AdminPath + "/signin/{id}/cancel":   true,
		"POST " + AdminPath + "/signin/claude-token":  true,
		"POST " + AdminPath + "/signin/plugin":        true,
		"POST " + AdminPath + "/signin/plugin/prompt": true,
		"GET " + AdminPath + "/ips":                   true,
		"POST " + AdminPath + "/ips":                  true,
		"POST " + AdminPath + "/ips/update":           true,
		"POST " + AdminPath + "/ips/delete":           true,
		"POST " + AdminPath + "/ips/check":            true,
		"POST " + AdminPath + "/ips/probe":            true,
		"POST " + AdminPath + "/ips/batch":            true,
		"POST " + AdminPath + "/ips/reread":           true,
		"POST " + AdminPath + "/accounts/proxy":       true,
		"POST " + AdminPath + "/accounts/import":      true,
	},
	access.AdminConsole: {
		"GET " + AdminPath + "/me":                        true,
		"GET " + AdminPath + "/catalog":                   true,
		"GET " + AdminPath + "/areas":                     true,
		"POST " + AdminPath + "/areas":                    true,
		"GET " + AdminPath + "/areas/{id}":                true,
		"GET " + AdminPath + "/areas/{id}/accounts":       true,
		"GET " + AdminPath + "/areas/{id}/usage":          true,
		"GET " + AdminPath + "/usage/requests":            true,
		"GET " + AdminPath + "/areas/{id}/usage/requests": true,
		"GET " + AdminPath + "/usage/stats":               true,
		"GET " + AdminPath + "/areas/{id}/usage/stats":    true,
		"POST " + AdminPath + "/areas/{id}/update":        true,
		"POST " + AdminPath + "/areas/{id}/rotate-key":    true,
		"POST " + AdminPath + "/areas/{id}/delete":        true,
		"GET " + AdminPath + "/ips":                       true,
		"GET " + AdminPath + "/areas/{id}/ips":            true,
	},
}

// adminRoute: pattern is one of role's routes.
func adminRoute(role, pattern string) bool { return adminRoutes[role][pattern] }

// refuseAdminKey answers an admin key a route that isn't an admin one.
func refuseAdminKey(w http.ResponseWriter, r *http.Request, who access.Identity) {
	log.Printf("refused %s %s: gateway key %q is an admin key", r.Method, r.URL.Path, who.KeyName)
	adminError(w, http.StatusForbidden, AdminKeyCode, "this is a magpie admin key: it reaches "+AdminPath+"/* alone, no model, MCP server, stats or other route")
}

func (s *Server) adminRoutesOn(mux *routeMux) {
	mux.HandleFunc("GET "+AdminPath+"/me", s.admin("", s.adminMe))
	mux.HandleFunc("GET "+AdminPath+"/options", s.admin(access.AdminArea, s.adminOptions))
	mux.HandleFunc("GET "+AdminPath+"/accounts", s.admin(access.AdminArea, s.adminAccounts))
	mux.HandleFunc("POST "+AdminPath+"/accounts/key", s.admin(access.AdminArea, s.probeHeld(s.adminAddKey)))
	mux.HandleFunc("POST "+AdminPath+"/accounts/keys", s.admin(access.AdminArea, s.adminAddKeys))
	mux.HandleFunc("POST "+AdminPath+"/accounts/remove", s.admin(access.AdminArea, s.adminRemove))
	mux.HandleFunc("GET "+AdminPath+"/usage", s.admin(access.AdminArea, s.adminUsage))
	mux.HandleFunc("GET "+AdminPath+"/usage/requests", s.admin("", s.adminUsageRequests))
	mux.HandleFunc("GET "+AdminPath+"/usage/stats", s.admin("", s.adminUsageStats))
	mux.HandleFunc("POST "+AdminPath+"/signin", s.admin(access.AdminArea, s.adminSignIn))
	mux.HandleFunc("GET "+AdminPath+"/signin/{id}", s.admin(access.AdminArea, s.adminSignInState))
	mux.HandleFunc("POST "+AdminPath+"/signin/{id}/callback", s.admin(access.AdminArea, s.adminSignInCallback))
	mux.HandleFunc("POST "+AdminPath+"/signin/{id}/cancel", s.admin(access.AdminArea, s.adminSignInCancel))
	mux.HandleFunc("POST "+AdminPath+"/signin/claude-token", s.admin(access.AdminArea, s.probeHeld(s.adminClaudeToken)))
	mux.HandleFunc("POST "+AdminPath+"/signin/plugin", s.admin(access.AdminArea, s.adminPluginSignIn))
	mux.HandleFunc("POST "+AdminPath+"/signin/plugin/prompt", s.admin(access.AdminArea, s.adminPluginPrompt))
	mux.HandleFunc("GET "+AdminPath+"/ips", s.admin("", s.adminIPs))
	mux.HandleFunc("POST "+AdminPath+"/ips", s.admin(access.AdminArea, s.probeHeld(s.adminAddIP)))
	mux.HandleFunc("POST "+AdminPath+"/ips/update", s.admin(access.AdminArea, s.adminUpdateIP))
	mux.HandleFunc("POST "+AdminPath+"/ips/delete", s.admin(access.AdminArea, s.adminDeleteIP))
	mux.HandleFunc("POST "+AdminPath+"/ips/check", s.admin(access.AdminArea, s.adminCheckProxy))
	mux.HandleFunc("POST "+AdminPath+"/ips/probe", s.admin(access.AdminArea, s.adminProbeProxy))
	mux.HandleFunc("POST "+AdminPath+"/ips/batch", s.admin(access.AdminArea, s.adminBatchIPs))
	mux.HandleFunc("POST "+AdminPath+"/ips/reread", s.admin(access.AdminArea, s.adminRereadIPs))
	mux.HandleFunc("POST "+AdminPath+"/accounts/proxy", s.admin(access.AdminArea, s.adminAccountProxy))
	mux.HandleFunc("POST "+AdminPath+"/accounts/import", s.adminUpTo(access.AdminArea, adminImportMost, s.adminImport))

	mux.HandleFunc("GET "+AdminPath+"/catalog", s.admin(access.AdminConsole, s.adminCatalog))
	mux.HandleFunc("GET "+AdminPath+"/areas", s.admin(access.AdminConsole, s.adminAreas))
	mux.HandleFunc("POST "+AdminPath+"/areas", s.admin(access.AdminConsole, s.adminCreateArea))
	mux.HandleFunc("GET "+AdminPath+"/areas/{id}", s.admin(access.AdminConsole, s.adminArea))
	mux.HandleFunc("GET "+AdminPath+"/areas/{id}/accounts", s.admin(access.AdminConsole, s.adminAreaAccounts))
	mux.HandleFunc("GET "+AdminPath+"/areas/{id}/usage", s.admin(access.AdminConsole, s.adminAreaUsage))
	mux.HandleFunc("GET "+AdminPath+"/areas/{id}/usage/requests", s.admin(access.AdminConsole, s.adminUsageRequests))
	mux.HandleFunc("GET "+AdminPath+"/areas/{id}/usage/stats", s.admin(access.AdminConsole, s.adminUsageStats))
	mux.HandleFunc("POST "+AdminPath+"/areas/{id}/update", s.admin(access.AdminConsole, s.adminUpdateArea))
	mux.HandleFunc("POST "+AdminPath+"/areas/{id}/rotate-key", s.admin(access.AdminConsole, s.adminRotateArea))
	mux.HandleFunc("POST "+AdminPath+"/areas/{id}/delete", s.admin(access.AdminConsole, s.adminDeleteArea))
	mux.HandleFunc("GET "+AdminPath+"/areas/{id}/ips", s.admin(access.AdminConsole, s.adminIPs))
}

// adminCall is a judged admin request: its key, and an area admin's area.
type adminCall struct {
	who  access.Identity
	area area.Area
}

// admin judges an admin route's caller before h runs: from an address
// the admin allow list lets in, with an enabled admin key of role (""
// for either), and an area admin's area there and on.
func (s *Server) admin(role string, h func(http.ResponseWriter, *http.Request, adminCall)) http.HandlerFunc {
	return s.adminUpTo(role, adminBodyMost, h)
}

// adminUpTo is admin with the body held to most bytes: the import's 8 MB
// (adminImportMost), every other route's 64 KB.
func (s *Server) adminUpTo(role string, most int64, h func(http.ResponseWriter, *http.Request, adminCall)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if addr, ok := adminAllowed(r); !ok {
			log.Printf("refused %s %s from %s: not on the admin allow list", r.Method, r.URL.Path, addr)
			adminError(w, http.StatusForbidden, adminIPNotAllowed, addr+" is not on magpie's admin allow list (Settings, or "+AdminAllowIPsEnv+"): the admin routes answer this computer and the addresses listed there alone")
			return
		}
		if r.URL.Query().Has("key") {
			adminError(w, http.StatusBadRequest, adminBadRequest, "send the admin key as Authorization: Bearer <key>, never in the address")
			return
		}
		who := access.Caller(r.Context())
		if who.Admin == "" || !access.Current(who) {
			adminError(w, http.StatusUnauthorized, adminKeyRequired, "send an enabled magpie admin key as Authorization: Bearer <key>")
			return
		}
		if role != "" && who.Admin != role {
			if role == access.AdminConsole {
				adminError(w, http.StatusForbidden, adminConsoleOnly, "this route is the console's: an area admin's key reaches its own area's routes alone")
			} else {
				adminError(w, http.StatusForbidden, adminAreaOnly, "this route is an area admin's: the console's key reads areas and manages them, and adds or removes no account")
			}
			return
		}
		c := adminCall{who: who}
		if who.Admin == access.AdminArea {
			a, err := area.Get(who.Area)
			switch {
			case errors.Is(err, area.ErrNotFound):
				adminError(w, http.StatusUnauthorized, adminKeyRequired, "this admin key's area was deleted")
				return
			case err != nil:
				adminError(w, http.StatusServiceUnavailable, adminAreasUnread, "magpie can't read its areas.json now: "+err.Error())
				return
			case a.Off:
				adminError(w, http.StatusUnauthorized, adminKeyRequired, "this admin key's area is turned off")
				return
			}
			c.area = a
		}
		r.Body = http.MaxBytesReader(w, r.Body, most)
		h(w, r, c)
	}
}

func adminError(w http.ResponseWriter, status int, code, msg string) {
	typ := "invalid_request_error"
	switch status {
	case http.StatusUnauthorized:
		typ = "authentication_error"
	case http.StatusForbidden:
		typ = "permission_error"
	case http.StatusNotFound:
		typ = "not_found_error"
	case http.StatusServiceUnavailable:
		typ = "api_error"
	case http.StatusTooManyRequests:
		typ = "rate_limit_error"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code}})
}

func adminNotFoundErr(w http.ResponseWriter) {
	adminError(w, http.StatusNotFound, adminNotFound, "no such account, area or sign-in")
}

// decodeAdmin reads r's JSON body into v, answering 400 when it can't.
func decodeAdmin(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, "the body isn't the JSON this route takes: "+err.Error())
		return false
	}
	return true
}

func (s *Server) adminMe(w http.ResponseWriter, r *http.Request, c adminCall) {
	out := map[string]any{"role": c.who.Admin, "keyName": c.who.KeyName, "version": Version}
	if c.who.Admin == access.AdminArea {
		out["area"] = map[string]string{"id": c.area.ID, "note": c.area.Note}
		out["accounts"] = len(c.area.Accounts)
	}
	writeJSON(w, http.StatusOK, out)
}

// adminProvider is a provider an area's admin may add keys of.
type adminProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Protocols are those a key of it may be set to (T-213): "" for any,
	// and each API it has an address for (keyProtocols)
	Protocols []string `json:"protocols"`
	// Plans are the plans a key of it may be for, of a preset that sells
	// more than one on keys of their own (provider.KeyPlans)
	Plans []adminPlan `json:"plans,omitempty"`
}

// adminPlan is a plan a provider's key may be for.
type adminPlan struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// keyProviders are the providers that take API keys, by id: those with
// no signed-in account.
func keyProviders() []adminProvider {
	out := []adminProvider{}
	for _, p := range provider.All() {
		if p.Account == nil && !p.IsPlugin() {
			ap := adminProvider{ID: p.ID, Name: p.Name, Protocols: keyProtocols(p)}
			for _, r := range provider.KeyPlans(p) {
				ap.Plans = append(ap.Plans, adminPlan{ID: r.ID, Name: r.Name})
			}
			out = append(out, ap)
		}
	}
	return out
}

func (s *Server) adminOptions(w http.ResponseWriter, r *http.Request, c adminCall) {
	providers := []adminProvider{}
	for _, p := range keyProviders() {
		if slices.Contains(c.area.Providers, p.ID) {
			providers = append(providers, p)
		}
	}
	signIns := []adminSignInWay{}
	for _, si := range signInWays() {
		if slices.Contains(c.area.SignIns, si.Agent) {
			signIns = append(signIns, si)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers, "signIns": signIns})
}

func (s *Server) adminCatalog(w http.ResponseWriter, r *http.Request, c adminCall) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": keyProviders(), "signIns": signInWays()})
}

// adminAccount is an account as the admin routes tell it: never a key's
// value, nor a token.
type adminAccount struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	ProviderName string `json:"providerName"`
	Kind         string `json:"kind"` // "key" | "account"
	Name         string `json:"name"`
	Masked       string `json:"masked,omitempty"`
	Plan         string `json:"plan,omitempty"`
	On           bool   `json:"on"`
	SignedOut    bool   `json:"signedOut,omitempty"`
	// Opened is when a subscription account's paid period was opened
	// (provider.AccountOpenedOf), YYYY-MM-DD in this computer's zone;
	// none when unset, and never a key's (T-213)
	Opened string `json:"opened,omitempty"`
	Gone   bool   `json:"gone,omitempty"` // removed from magpie by its owner
	// Contested: another area has it too (areas.json written by hand):
	// neither removes it nor is told what it spent until the owner settles it
	Contested bool      `json:"contested,omitempty"`
	Added     time.Time `json:"added"`
	// Proxy is the area's proxy its admin put on it (T-198), none when
	// it has none
	Proxy *adminAccountProxy `json:"proxy,omitempty"`
	// previous are the fingerprints a key had before its value was
	// replaced in place (area.Account.Previous): its calls under them are
	// still the area's. Never told.
	previous []string
}

// ownsRef: ref is a's, and no other area's too (contested: areas.json
// written by hand), which no admin manages.
func ownsRef(a area.Area, ref string) bool {
	if !a.Has(ref) {
		return false
	}
	owner, contested, err := area.Owner(ref)
	return err == nil && !contested && owner == a.ID
}

// areaAccounts are a's accounts as they stand in magpie now.
func areaAccounts(a area.Area) []adminAccount {
	out := []adminAccount{}
	for _, owned := range a.Accounts {
		pid, ref, _ := strings.Cut(owned.Ref, "/")
		acc := adminAccount{ID: owned.Ref, Provider: pid, Name: ref, Added: owned.Added, Gone: true, Contested: !ownsRef(a, owned.Ref)}
		if rec := owned.Proxy; rec != nil {
			acc.Proxy = &adminAccountProxy{IP: rec.IP, ID: rec.ID, Direct: rec.Direct, Overridden: provider.AreaProxyOverridden(pid, *rec)}
		}
		p, err := provider.Find(pid)
		if err == nil {
			acc.ProviderName = p.Name
			describe(&acc, *p, ref)
		}
		// a value replaced and then added again (the owner's, another
		// area's) is live under that fingerprint: its calls since can't be
		// told apart, so none of them is this area's
		for _, was := range owned.Previous {
			wpid, fp, ok := strings.Cut(was, "/")
			if !ok || wpid != pid || fp == "" || err == nil && slices.ContainsFunc(p.KeyList(), func(k provider.KeyInfo) bool { return k.ID == fp }) {
				continue
			}
			acc.previous = append(acc.previous, fp)
		}
		out = append(out, acc)
	}
	return out
}

// describe fills acc from p's account or key ref, and clears Gone when p
// still has it.
func describe(acc *adminAccount, p provider.Provider, ref string) {
	if p.Account == nil {
		for _, k := range p.KeyList() {
			if k.ID == ref {
				acc.Kind, acc.Name, acc.Masked, acc.On, acc.Gone = "key", k.Name, k.Masked, k.On, false
				if acc.Name == "" {
					acc.Name = k.Masked
				}
			}
		}
		if acc.Gone {
			acc.Kind = "key"
		}
		return
	}
	acc.Kind = "account"
	agentID := loginAgent(p)
	for _, r := range p.AccountIDs() {
		if r.ID != ref {
			continue
		}
		acc.Name, acc.Plan, acc.Gone = r.User, r.Plan, false
		if at := provider.AccountOpenedOf(p.ID, r.User); !at.IsZero() {
			acc.Opened = at.In(time.Local).Format(time.DateOnly)
		}
		for _, l := range provider.Logins(agentID) {
			if strings.EqualFold(l.User, r.User) {
				acc.On = (l.On || l.Active) && !l.Banned && !l.Disabled
				acc.SignedOut = l.Lapsed != ""
			}
		}
	}
}

// loginAgent is the agent p's accounts are saved under.
func loginAgent(p provider.Provider) string {
	if p.IsPlugin() {
		return p.ID
	}
	return p.Account.Agent
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request, c adminCall) {
	writeJSON(w, http.StatusOK, areaAccounts(c.area))
}

func (s *Server) adminAddKey(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		keyFields
		Key string `json:"key"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	p, proto, plan, ok := keyTarget(w, c, in.keyFields)
	if !ok {
		return
	}
	in.Key, in.Name = strings.TrimSpace(in.Key), strings.TrimSpace(in.Name)
	if in.Key == "" || len(in.Key) > 4096 {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send the API key, up to 4096 characters")
		return
	}
	if len(c.area.Accounts) >= adminAccountsLimit {
		adminError(w, http.StatusConflict, adminConflict, "an area has at most 500 accounts")
		return
	}
	acc, status, code, msg := addAreaKey(c, p, in.Name, in.Key, proto, plan, in.Proxy)
	if status != http.StatusCreated {
		if code == adminFull {
			code = adminConflict
		}
		adminError(w, status, code, msg)
		return
	}
	log.Printf("admin: area %s (%s) added a key of %s", c.area.ID, c.area.Note, p.ID)
	writeJSON(w, http.StatusCreated, acc)
}

func adminClaimErr(w http.ResponseWriter, err error) {
	_, status, code, msg := claimFailure(err)
	adminError(w, status, code, msg)
}

// claimFailure is the answer to a claim that failed with err: the status,
// code and message, as adminClaimErr writes them.
func claimFailure(err error) (adminAccount, int, string, string) {
	switch {
	case errors.Is(err, area.ErrFull):
		return adminAccount{}, http.StatusConflict, adminConflict, err.Error()
	case errors.Is(err, area.ErrTaken):
		return adminAccount{}, http.StatusConflict, adminConflict, "this account is in magpie already"
	case errors.Is(err, area.ErrNotFound):
		return adminAccount{}, http.StatusUnauthorized, adminKeyRequired, "this admin key's area was deleted"
	}
	return adminAccount{}, http.StatusServiceUnavailable, adminAreasUnread, err.Error()
}

// Reseat runs a change that can take a provider's last account or key
// away, moving the agents on its models to others (agent.Reseat, which
// sets it: the agent package imports this one).
var Reseat = func(change func() error) error { return change() }

func (s *Server) adminRemove(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct{ ID string }
	if !decodeAdmin(w, r, &in) {
		return
	}
	if !ownsRef(c.area, in.ID) {
		adminNotFoundErr(w)
		return
	}
	pid, ref, _ := strings.Cut(in.ID, "/")
	if p, err := provider.Find(pid); err == nil {
		acc := adminAccount{Gone: true}
		describe(&acc, *p, ref)
		if !acc.Gone {
			var change func() error
			if p.Account == nil {
				change = func() error { return provider.RemoveKey(p.ID, ref) }
			} else {
				agentID, user := loginAgent(*p), acc.Name
				change = func() error { return provider.ForgetLogin(agentID, user) }
			}
			if err := Reseat(change); err != nil {
				adminError(w, http.StatusConflict, adminConflict, err.Error())
				return
			}
		}
	}
	if err := area.Release(in.ID); err != nil {
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
		return
	}
	log.Printf("admin: area %s (%s) removed %s", c.area.ID, c.area.Note, pid)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminUsage(w http.ResponseWriter, r *http.Request, c adminCall) {
	p, loc, ok := usageArgs(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, areaUsage(c.area, p, loc, usage.Clock()))
}

// usageArgs reads ?period= (today, 7d or 30d) and ?tz=.
func usageArgs(w http.ResponseWriter, r *http.Request) (usage.Period, *time.Location, bool) {
	q := r.URL.Query()
	var p usage.Period
	switch q.Get("period") {
	case "", "today":
		p = usage.Today
	case "7d":
		p = usage.Week
	case "30d":
		p = usage.Month
	default:
		adminError(w, http.StatusBadRequest, adminBadRequest, "period is today, 7d or 30d")
		return "", nil, false
	}
	loc := time.Local
	if tz := q.Get("tz"); tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			adminError(w, http.StatusBadRequest, adminBadRequest, "tz is an IANA time zone, such as Asia/Shanghai")
			return "", nil, false
		}
		loc = l
	}
	return p, loc, true
}

// adminTotals are what some calls spent.
type adminTotals struct {
	Requests   int     `json:"requests"`
	Failed     int     `json:"failed"`
	Input      int     `json:"inputTokens"`
	Output     int     `json:"outputTokens"`
	CacheRead  int     `json:"cacheReadTokens"`
	CacheWrite int     `json:"cacheWriteTokens"`
	Cost       float64 `json:"cost"`
	Unpriced   int     `json:"unpriced"`
}

func (t *adminTotals) add(r usage.Record, cost float64, priced bool) {
	if r.Answered() {
		t.Requests++
	} else if r.LostRequest() {
		t.Failed++
	}
	t.Input += r.Input
	t.Output += r.Output
	t.CacheRead += r.CacheRead
	t.CacheWrite += r.CacheWrite
	if priced {
		t.Cost += cost
	} else if r.Input+r.Output+r.CacheRead+r.CacheWrite > 0 {
		t.Unpriced++
	}
}

type adminShare struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	adminTotals
}

// modelShare and dayShare are a model's and a day's, named as
// magpie-console's docs/magpie-api.md has them.
type modelShare struct {
	Model string `json:"model"`
	adminTotals
}

type dayShare struct {
	Day string `json:"day"`
	adminTotals
}

// AreaUsage is what an area's accounts spent over a period.
type AreaUsage struct {
	Period    string       `json:"period"`
	Zone      string       `json:"zone"`
	Since     time.Time    `json:"since"`
	Totals    adminTotals  `json:"totals"`
	ByModel   []modelShare `json:"byModel"`
	ByAccount []adminShare `json:"byAccount"`
	ByDay     []dayShare   `json:"byDay"`
}

var periodNames = map[usage.Period]string{usage.Today: "today", usage.Week: "7d", usage.Month: "30d"}

// areaMatch is how the ledger names one of an area's accounts: a key by
// its fingerprint, or one it had before its value was replaced in place
// (previous), an account by Record.Account(), counted from added.
type areaMatch struct {
	ref, name string
	previous  []string
	key       bool
	added     time.Time
}

// names says whether r is m's call, whenever it was made.
func (m areaMatch) names(r usage.Record) bool {
	if m.key {
		return r.ProviderKeyID != "" && (r.ProviderKeyID == m.name || slices.Contains(m.previous, r.ProviderKeyID))
	}
	return m.name != "" && strings.EqualFold(r.Account(), m.name)
}

// areaMatches are accs' uncontested accounts by provider.
func areaMatches(accs []adminAccount) map[string][]areaMatch {
	byProvider := map[string][]areaMatch{}
	for _, acc := range accs {
		if acc.Contested {
			continue
		}
		pid, ref, _ := strings.Cut(acc.ID, "/")
		m := areaMatch{ref: acc.ID, key: acc.Kind == "key", added: acc.Added, name: acc.Name}
		if m.key {
			m.name, m.previous = ref, acc.previous // the fingerprints the ledger keeps
		}
		byProvider[pid] = append(byProvider[pid], m)
	}
	return byProvider
}

// areaUsage is what a's accounts answered over p, each from the time the
// area got it: a key's calls by its fingerprint, an account's by the name
// the ledger keeps (Record.Account) under its provider. It is the stats'
// tally (usageStats), so /usage and /usage/stats can't disagree.
func areaUsage(a area.Area, p usage.Period, loc *time.Location, now time.Time) AreaUsage {
	q, t, names := usageStats(a, p, loc, now)
	out := AreaUsage{Period: periodNames[p], Zone: loc.String(), Since: q.since, Totals: t.totals.totals(), ByModel: []modelShare{}, ByAccount: []adminShare{}, ByDay: []dayShare{}}
	for _, g := range t.groups[0] {
		out.ByModel = append(out.ByModel, modelShare{g.key["model"], g.totals.totals()})
	}
	for _, g := range t.groups[1] {
		id := g.key["account"]
		out.ByAccount = append(out.ByAccount, adminShare{ID: id, Name: names[id], adminTotals: g.totals.totals()})
	}
	for i, m := range t.series {
		if m.Attempts > 0 {
			out.ByDay = append(out.ByDay, dayShare{q.buckets[i].Format(time.DateOnly), m.totals()})
		}
	}
	most := func(x, y adminTotals) int {
		if x.Cost != y.Cost {
			if x.Cost > y.Cost {
				return -1
			}
			return 1
		}
		return y.Requests - x.Requests
	}
	slices.SortFunc(out.ByModel, func(x, y modelShare) int { return most(x.adminTotals, y.adminTotals) })
	slices.SortFunc(out.ByAccount, func(x, y adminShare) int { return most(x.adminTotals, y.adminTotals) })
	return out
}

// adminArea is an area as the console reads it.
type adminArea struct {
	ID        string      `json:"id"`
	Note      string      `json:"note"`
	Off       bool        `json:"off"`
	Created   time.Time   `json:"created"`
	Providers []string    `json:"providers"`
	SignIns   []string    `json:"signIns"`
	Accounts  int         `json:"accounts"`
	AdminKey  string      `json:"adminKeyName,omitempty"`
	Today     adminTotals `json:"today"`
}

// areasToday is what each area's accounts answered today in loc, by
// area id: areaUsage(a, usage.Today, loc, now).Totals for each, from one
// visit of the ledger however many areas there are. accs[i] are
// areas[i]'s accounts.
func areasToday(areas []area.Area, accs [][]adminAccount, loc *time.Location, now time.Time) map[string]adminTotals {
	out := make(map[string]adminTotals, len(areas))
	type areaOwn struct {
		area int
		areaMatch
	}
	byProvider := map[string][]areaOwn{}
	for i := range areas {
		out[areas[i].ID] = adminTotals{}
		for pid, ms := range areaMatches(accs[i]) {
			for _, m := range ms {
				byProvider[pid] = append(byProvider[pid], areaOwn{i, m})
			}
		}
	}
	if len(byProvider) == 0 {
		return out // no account, nothing spent: never the whole ledger
	}
	since := usage.Today.Since(now.In(loc))
	totals := make([]adminTotals, len(areas))
	// owners are the accounts a provider, key and account name are, worked
	// out once for the calls that share them; counted[i] is the call area i
	// last counted, so an area with two accounts naming one call counts it
	// once
	type hit struct {
		area  int
		added time.Time
	}
	owners := map[[3]string][]hit{}
	counted := make([]int, len(areas))
	n := 0
	cost := usage.NewCoster()
	visitAll(since, func(r usage.Record) {
		if r.Time.Before(since) || !areaRecord(r) {
			return
		}
		ms := byProvider[r.Provider]
		if len(ms) == 0 {
			return
		}
		k := [3]string{r.Provider, r.ProviderKeyID, r.Account()}
		hits, ok := owners[k]
		if !ok {
			for _, m := range ms {
				if m.names(r) {
					hits = append(hits, hit{m.area, m.added})
				}
			}
			owners[k] = hits
		}
		n++
		var c float64
		var priced, costed bool
		for _, h := range hits {
			if r.Time.Before(h.added) || counted[h.area] == n {
				continue
			}
			counted[h.area] = n
			if !costed {
				c, priced = cost(r)
				costed = true
			}
			totals[h.area].add(r, c, priced)
		}
	})
	for i, a := range areas {
		out[a.ID] = totals[i]
	}
	return out
}

func areaOf(a area.Area, now time.Time) adminArea {
	return areasOf([]area.Area{a}, [][]adminAccount{areaAccounts(a)}, now)[0]
}

// areasOf are areas as the console reads them, accs[i] being areas[i]'s
// accounts.
func areasOf(areas []area.Area, accs [][]adminAccount, now time.Time) []adminArea {
	today := areasToday(areas, accs, time.Local, now)
	out := make([]adminArea, 0, len(areas))
	for _, a := range areas {
		v := adminArea{ID: a.ID, Note: a.Note, Off: a.Off, Created: a.Created, Providers: a.Providers, SignIns: a.SignIns, Accounts: len(a.Accounts), Today: today[a.ID]}
		if v.Providers == nil {
			v.Providers = []string{}
		}
		if v.SignIns == nil {
			v.SignIns = []string{}
		}
		if k, ok, _ := access.AreaKey(a.ID); ok {
			v.AdminKey = k.Name
		}
		out = append(out, v)
	}
	return out
}

// consoleArea is the area the path names, answering 404 or 503 when
// there is none to be had.
func consoleArea(w http.ResponseWriter, r *http.Request) (area.Area, bool) {
	a, err := area.Get(r.PathValue("id"))
	switch {
	case errors.Is(err, area.ErrNotFound):
		adminNotFoundErr(w)
		return a, false
	case err != nil:
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
		return a, false
	}
	return a, true
}

func (s *Server) adminAreas(w http.ResponseWriter, r *http.Request, c adminCall) {
	areas, err := area.List()
	if err != nil {
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
		return
	}
	accs := make([][]adminAccount, len(areas))
	for i, a := range areas {
		accs[i] = areaAccounts(a)
	}
	writeJSON(w, http.StatusOK, areasOf(areas, accs, time.Now()))
}

func (s *Server) adminArea(w http.ResponseWriter, r *http.Request, c adminCall) {
	if a, ok := consoleArea(w, r); ok {
		writeJSON(w, http.StatusOK, areaOf(a, time.Now()))
	}
}

func (s *Server) adminAreaAccounts(w http.ResponseWriter, r *http.Request, c adminCall) {
	if a, ok := consoleArea(w, r); ok {
		writeJSON(w, http.StatusOK, areaAccounts(a))
	}
}

func (s *Server) adminAreaUsage(w http.ResponseWriter, r *http.Request, c adminCall) {
	a, ok := consoleArea(w, r)
	if !ok {
		return
	}
	if p, loc, ok := usageArgs(w, r); ok {
		writeJSON(w, http.StatusOK, areaUsage(a, p, loc, usage.Clock()))
	}
}

// areaErr answers an area change that failed.
func areaErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, area.ErrNotFound):
		adminNotFoundErr(w)
	case errors.Is(err, area.ErrNoteInUse), errors.Is(err, area.ErrNotEmpty):
		adminError(w, http.StatusConflict, adminConflict, err.Error())
	case errors.Is(err, area.ErrUnreadable):
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
	default:
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
	}
}

// checkOffer refuses providers and sign-ins magpie doesn't offer an area.
func checkOffer(providers, signIns *[]string) error {
	if providers != nil {
		for _, id := range *providers {
			if !slices.ContainsFunc(keyProviders(), func(p adminProvider) bool { return p.ID == id }) {
				return errors.New("no provider taking API keys has the id " + id)
			}
		}
	}
	if signIns != nil {
		for _, id := range *signIns {
			if !slices.ContainsFunc(signInWays(), func(s adminSignInWay) bool { return s.Agent == id }) {
				return errors.New("no subscription an area can sign in to has the id " + id)
			}
		}
	}
	return nil
}

func (s *Server) adminCreateArea(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		Note               string
		Providers, SignIns []string
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	if err := checkOffer(&in.Providers, &in.SignIns); err != nil {
		areaErr(w, err)
		return
	}
	a, err := area.Create(in.Note, in.Providers, in.SignIns)
	if err != nil {
		areaErr(w, err)
		return
	}
	_, secret, err := access.AddAdmin(access.AdminArea, a.ID, a.Note+" admin")
	if err != nil {
		// no area without its admin key
		if derr := area.Delete(a.ID); derr != nil {
			log.Printf("admin: area %s made, its admin key not (%v), and the area not deleted: %v", a.ID, err, derr)
		}
		adminError(w, http.StatusServiceUnavailable, "admin_key_unsaved", "the area's admin key couldn't be saved: "+err.Error())
		return
	}
	log.Printf("admin: the console made area %s (%s)", a.ID, a.Note)
	writeJSON(w, http.StatusCreated, map[string]any{"area": areaOf(a, time.Now()), "adminKey": secret})
}

func (s *Server) adminUpdateArea(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		Note               *string
		Off                *bool
		Providers, SignIns *[]string
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	if err := checkOffer(in.Providers, in.SignIns); err != nil {
		areaErr(w, err)
		return
	}
	a, err := area.Update(r.PathValue("id"), area.Spec{Note: in.Note, Off: in.Off, Providers: in.Providers, SignIns: in.SignIns})
	if err != nil {
		areaErr(w, err)
		return
	}
	if a.Off {
		CancelAreaSignIns(a.ID)
	}
	writeJSON(w, http.StatusOK, areaOf(a, time.Now()))
}

func (s *Server) adminRotateArea(w http.ResponseWriter, r *http.Request, c adminCall) {
	a, ok := consoleArea(w, r)
	if !ok {
		return
	}
	var secret string
	k, has, err := access.AreaKey(a.ID)
	if err == nil && has {
		secret, err = access.Update("rotate-key", access.Change{Key: k.ID})
	} else if err == nil {
		_, secret, err = access.AddAdmin(access.AdminArea, a.ID, a.Note+" admin")
	}
	if err != nil {
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"adminKey": secret})
}

func (s *Server) adminDeleteArea(w http.ResponseWriter, r *http.Request, c adminCall) {
	id := r.PathValue("id")
	if err := area.Delete(id); err != nil {
		areaErr(w, err)
		return
	}
	CancelAreaSignIns(id)
	if err := access.RemoveArea(id); err != nil {
		// the area is gone, so its key reaches nothing (admin): only
		// left in the list until removed by hand
		log.Printf("admin: area %s deleted, its admin key not: %v", id, err)
	}
	log.Printf("admin: the console deleted area %s", id)
	w.WriteHeader(http.StatusNoContent)
}

// AdminAllowIPs is the admin allow list in force, as written, and whether
// MAGPIE_ADMIN_ALLOW_IPS set it. Empty: this computer alone.
func AdminAllowIPs() (list []string, fromEnv bool) {
	if v := strings.TrimSpace(os.Getenv(AdminAllowIPsEnv)); v != "" {
		return settings.SplitAllowIPs(v), true
	}
	s, err := settings.SharedChecked()
	lastAdminAllow.Lock()
	defer lastAdminAllow.Unlock()
	if err == nil {
		lastAdminAllow.list = s.AdminAllowIPs
	}
	// unread: the list read last, none (this computer alone) before one
	return slices.Clone(lastAdminAllow.list), false
}

var lastAdminAllow struct {
	sync.Mutex
	list []string
}

// adminAllowed: r came from this computer, or an address on the admin
// allow list, judged by the connection's own address. One that reaches
// loopback through a proxy or tunnel here (a forwarding header) is
// someone else's, from wherever that reaches: it is judged by the address
// the proxy names only when MAGPIE_TRUST_PROXY says the proxy here sets
// it, and refused otherwise. One through magpie web's page port is
// refused, trusted proxy or not: a raw forward to that port arrives from
// loopback carrying whatever forwarding header its client wrote, so no
// address it names is vouched for (oneport.go). A forwarding header from
// another computer is never taken for its address.
func adminAllowed(r *http.Request) (string, bool) {
	a, ok := peerAddr(r)
	if !ok {
		return r.RemoteAddr, false
	}
	if a.IsLoopback() || ownAddr(a) {
		if viaPagePort(r) {
			return a.String() + " (through magpie web's page port)", false
		}
		forwarded := slices.ContainsFunc(forwardHeaders, func(h string) bool { return r.Header.Get(h) != "" })
		if !forwarded {
			return a.String(), true
		}
		if !trustProxy() {
			return a.String() + " (through a proxy here)", false
		}
		f, ok := forwardedAddr(r)
		if !ok {
			return a.String() + " (through a proxy here)", false
		}
		a = f
		if a.IsLoopback() {
			return a.String(), true
		}
	}
	list, _ := AdminAllowIPs()
	for _, e := range list {
		if p, err := settings.ParseAllowIP(e); err == nil && p.Contains(a) {
			return a.String(), true
		}
	}
	return a.String(), false
}

// forwardedAddr is the client's address a trusted proxy here names: the
// last hop of X-Forwarded-For, the one it added, else X-Real-IP or
// Cf-Connecting-IP.
func forwardedAddr(r *http.Request) (netip.Addr, bool) {
	v := ""
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		parts := strings.Split(f, ",")
		v = parts[len(parts)-1]
	} else if v = r.Header.Get("X-Real-IP"); v == "" {
		v = r.Header.Get("Cf-Connecting-IP")
	}
	a, err := netip.ParseAddr(strings.TrimSpace(v))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// AreaCatalog is what an area may be given: the providers that take API
// keys and the subscriptions it may sign in to (the window's Areas).
func AreaCatalog() map[string]any {
	return map[string]any{"providers": keyProviders(), "signIns": signInWays()}
}

// AreaViews are areas as the window shows them: as the console reads
// them, with their accounts.
func AreaViews(areas []area.Area) []any {
	accs := make([][]adminAccount, len(areas))
	for i, a := range areas {
		accs[i] = areaAccounts(a)
	}
	out := make([]any, 0, len(areas))
	for i, v := range areasOf(areas, accs, time.Now()) {
		out = append(out, struct {
			adminArea
			List []adminAccount `json:"list"`
		}{v, accs[i]})
	}
	return out
}

// UnownedAccounts are magpie's accounts and keys no area owns, which the
// owner may give one.
func UnownedAccounts() []any {
	out := []any{}
	for _, p := range provider.All() {
		for _, r := range p.AccountIDs() {
			ref := p.ID + "/" + r.ID
			if owner, contested, err := area.Owner(ref); err != nil || owner != "" || contested {
				continue
			}
			acc := adminAccount{ID: ref, Provider: p.ID, ProviderName: p.Name, Gone: true}
			describe(&acc, p, r.ID)
			if !acc.Gone {
				out = append(out, acc)
			}
		}
	}
	return out
}
