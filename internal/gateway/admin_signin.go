package gateway

import (
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/renewal"
)

// An area admin's sign-in to a subscription (docs/subsystems/areas.md),
// from a browser that isn't on this computer: each way here finishes with
// nothing coming back to this computer's ports, a device code typed on
// the vendor's page, or the address or code the page ends on pasted
// back. The account is the area's the moment the sign-in is done
// (provider.SignInOptions.Done), whether or not the console asks again,
// and never the agent's own here (provider.SignInOptions.Area).

// adminSignInWay is a subscription an area may sign in to, and how.
type adminSignInWay struct {
	Agent string   `json:"agent"`
	Name  string   `json:"name"`
	Ways  []string `json:"ways"`
	// Sites are where it signs in, for one with more than one: "ghe.com"
	// is an enterprise's <name>.ghe.com, sent as the sign-in's site
	Sites []string `json:"sites,omitempty"`
	// Plugin is a subscription a plugin serves (T-209), signed in to at
	// signin/plugin by one of Methods
	Plugin  bool                `json:"plugin,omitempty"`
	Methods []adminPluginMethod `json:"methods,omitempty"`
}

// The ways a sign-in finishes.
const (
	wayDevice     = "device"      // a code typed on the vendor's page
	wayPasteURL   = "paste-url"   // the address the vendor's page ends on, pasted
	wayPasteCode  = "paste-code"  // the code the vendor's page shows, pasted
	waySetupToken = "setup-token" // a Claude setup-token, pasted
)

// adminSignIns are the subscriptions an area may sign in to.
var adminSignIns = []adminSignInWay{
	{Agent: "codex", Name: "ChatGPT (Codex)", Ways: []string{wayDevice, wayPasteURL}},
	{Agent: provider.ChatGPTAPIID, Name: "Sign in with ChatGPT (API)", Ways: []string{wayPasteURL}},
	{Agent: "copilot", Name: "GitHub Copilot", Ways: []string{wayDevice}, Sites: []string{"github.com", "ghe.com"}},
	{Agent: "claude", Name: "Claude", Ways: []string{waySetupToken, wayPasteCode}},
	{Agent: "gemini", Name: "Gemini", Ways: []string{wayPasteURL}},
	{Agent: "antigravity", Name: "Antigravity", Ways: []string{wayPasteURL}},
}

// signInWays are the subscriptions an area may sign in to now:
// adminSignIns whose sign-in magpie has, and those plugins serve
// (pluginSignInWays).
func signInWays() []adminSignInWay {
	out := []adminSignInWay{}
	for _, w := range adminSignIns {
		if !provider.PluginServes(w.Agent) {
			out = append(out, w)
		}
	}
	return append(out, pluginSignInWays()...)
}

// copilotSite is the <name>.ghe.com an area's Copilot sign-in was sent,
// lower case with a leading https:// and a trailing / taken off; ok is
// false for anything else, github.com included (which is no site sent).
// The host is judged by provider.CopilotHost, the one reader of where
// Copilot signs in, after a check that leaves nothing but a bare host
// name to it: a GitHub Enterprise Server's own host, an IP, a port or a
// path is none, so a GitHub token is never sent to a host the console
// names.
func copilotSite(s string) (string, bool) {
	h := strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(s), "https://"), "/")
	if h == "" || strings.Trim(h, "abcdefghijklmnopqrstuvwxyz0123456789.-") != "" {
		return "", false
	}
	host, err := provider.CopilotHost(h)
	return h, err == nil && host == h
}

// startAreaSignIn starts an area's sign-in; a var so tests see what it
// was started with.
var startAreaSignIn = provider.StartSignInWith

// subscriptionOf is the id of the provider agent's accounts are served
// by: a built-in subscription's is its agent's (provider.loginProvider),
// listed or not yet, as it isn't before its first account. "" for an
// agent no area signs in to.
func subscriptionOf(agent string) string {
	if slices.ContainsFunc(adminSignIns, func(w adminSignInWay) bool { return w.Agent == agent }) {
		return agent
	}
	// a plugin's: its accounts are under its provider id
	if _, ok := areaPlugins[agent]; ok {
		return agent
	}
	return ""
}

// areaSignIns are the sign-ins each area started, by id: an area reads,
// pastes into and cancels its own alone.
var areaSignIns struct {
	sync.Mutex
	m map[string]areaSignIn
	// pending are the picks of sign-ins starting, not listed yet, by
	// area: an IP's removal sees them too (signingThrough)
	pending map[*pickBox]string
}

// areaSignIn is a sign-in an area started: whose, and the proxy its
// account is to have (T-212).
type areaSignIn struct {
	area string
	pick *pickBox
}

func ownSignIn(c adminCall, id string) bool {
	areaSignIns.Lock()
	defer areaSignIns.Unlock()
	return id != "" && areaSignIns.m[id].area == c.area.ID
}

// pickOf is the pick box of the sign-in id, nil for none listed.
func pickOf(id string) *pickBox {
	areaSignIns.Lock()
	defer areaSignIns.Unlock()
	return areaSignIns.m[id].pick
}

// adminSignInState is a sign-in as the admin routes tell it.
type adminSignInState struct {
	ID      string        `json:"id"`
	Agent   string        `json:"agent"`
	State   string        `json:"state"`
	URL     string        `json:"url,omitempty"`
	Code    string        `json:"code,omitempty"`
	Paste   string        `json:"paste"` // "url", "code", "key" or ""
	Account *adminAccount `json:"account,omitempty"`
	Message string        `json:"message,omitempty"`
	// KeysURL is where a key is made, for paste "key"; Instructions a
	// plugin's words for its page (T-209)
	KeysURL      string `json:"keysUrl,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

func signInStateOf(c adminCall, st provider.SignInState) adminSignInState {
	st = st.Said()
	out := adminSignInState{ID: st.ID, Agent: st.Agent, State: st.State, URL: st.URL, Code: st.Code, Message: st.Error}
	_, ofPlugin := areaPlugins[st.Agent]
	if ofPlugin {
		// a plugin's words never say back what it was given (§11.3)
		said := pluginSaidOf(st.ID)
		out.Message, out.Instructions = said.held(out.Message), said.held(st.Instructions)
		if st.PasteKey {
			out.KeysURL = st.KeysURL
		}
	}
	switch {
	case st.State != "waiting":
	case ofPlugin:
		switch provider.PluginWayOf(st) {
		case wayPasteCode:
			out.Paste = "code"
		case wayPasteURL:
			out.Paste = "url"
		case wayPasteKey:
			out.Paste = "key"
		}
	case st.PasteCode || st.Agent == "claude":
		out.Paste = "code"
	case st.PasteCallback || st.Code == "":
		out.Paste = "url"
	}
	if st.State == "done" {
		if pid := subscriptionOf(st.Agent); pid != "" {
			ref := pid + "/" + provider.LoginID(st.Agent, st.User)
			if a, err := area.Get(c.area.ID); err == nil && a.Has(ref) {
				for _, acc := range areaAccounts(a) {
					if acc.ID == ref {
						out.Account = &acc
					}
				}
			}
		}
	}
	return out
}

// claimOnDone gives the account a sign-in of area id signed in to to the
// area, as it is done: one magpie had before it (Again), the owner's or
// another area's, stays whose it was, and reads as a conflict.
func claimOnDone(id string) func(provider.SignInState) provider.SignInState {
	return func(st provider.SignInState) provider.SignInState {
		pid := subscriptionOf(st.Agent)
		ref := pid + "/" + provider.LoginID(st.Agent, st.User)
		if st.Again {
			// the area's own, signed in again: its sign-in renewed
			if a, err := area.Get(id); err == nil && ownsRef(a, ref) {
				return st
			}
			st.State, st.Error = "conflict", "this account is in magpie already, so it stays where it is"
			return st
		}
		if pid == "" {
			st.State, st.Error = "failed", "no area signs in to "+st.Agent
			return st
		}
		if err := area.ClaimUpTo(id, ref, adminAccountsLimit); err != nil {
			st.State = "conflict"
			st.Error = "signed in, but the account couldn't be made this area's: " + err.Error()
			if !errors.Is(err, area.ErrTaken) {
				// the area deleted, or areas.json unread: an account
				// added for it alone is taken out again, rather than
				// left serving the owner as nobody's
				st.State = "failed"
				if ferr := provider.ForgetLogin(st.Agent, st.User); ferr != nil {
					log.Printf("admin: area %s's %s account, not claimed, not removed either: %v", id, st.Agent, ferr)
				}
			}
			log.Printf("admin: area %s signed in to %s, not claimed: %v", id, st.Agent, err)
			return st
		}
		log.Printf("admin: area %s signed in to a %s account", id, st.Agent)
		return st
	}
}

// areaOpened is when a subscription account's paid period was opened,
// sent as YYYY-MM-DD in this computer's zone (T-213), as
// provider.SetAccountOpened takes it: zero for none sent, or why it
// can't be one (renewal.CheckOpened).
func areaOpened(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	at, err := time.ParseInLocation(time.DateOnly, s, time.Local)
	if err != nil {
		return time.Time{}, errors.New("opened is a date, YYYY-MM-DD")
	}
	if err := renewal.CheckOpened(at, time.Now()); err != nil {
		return time.Time{}, err
	}
	return at, nil
}

// openOnDone is done, and then at (none when zero) kept as the opening
// date of the account a sign-in left the area's.
func openOnDone(done func(provider.SignInState) provider.SignInState, at time.Time) func(provider.SignInState) provider.SignInState {
	return func(st provider.SignInState) provider.SignInState {
		st = done(st)
		if at.IsZero() || st.State != "done" {
			return st
		}
		if err := provider.SetAccountOpened(subscriptionOf(st.Agent), st.User, at); err != nil {
			log.Printf("admin: an area's %s account signed in, its opening date not kept: %v", st.Agent, err)
			msg := "signed in, but its opening date wasn't kept: " + err.Error()
			if st.Error != "" {
				// what an earlier step said (its proxy) is kept beside it
				msg = st.Error + "; " + msg
			}
			st.Error = msg
		}
		return st
	}
}

func (s *Server) adminSignIn(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		Agent, Way, Site, Opened string
		Proxy                    *proxyPick
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	i := slices.IndexFunc(signInWays(), func(x adminSignInWay) bool { return x.Agent == in.Agent })
	if i < 0 || !slices.Contains(c.area.SignIns, in.Agent) {
		adminNotFoundErr(w)
		return
	}
	way := signInWays()[i]
	if way.Plugin {
		adminError(w, http.StatusBadRequest, adminBadRequest, way.Name+" is a plugin's: sign in to it at "+AdminPath+"/signin/plugin")
		return
	}
	if in.Way == "" {
		in.Way = way.Ways[0]
	}
	if !slices.Contains(way.Ways, in.Way) || in.Way == waySetupToken {
		adminError(w, http.StatusBadRequest, adminBadRequest, in.Agent+" signs in by "+strings.Join(way.Ways, " or ")+" (a setup-token is sent to "+AdminPath+"/signin/claude-token)")
		return
	}
	site := ""
	if in.Site != "" {
		ok := false
		if site, ok = copilotSite(in.Site); !ok || len(way.Sites) == 0 {
			msg := in.Agent + " takes no site"
			if len(way.Sites) > 0 {
				msg = "site is an enterprise's <name>.ghe.com; leave it out for github.com"
			}
			adminError(w, http.StatusBadRequest, adminBadRequest, msg)
			return
		}
	}
	opened, err := areaOpened(in.Opened)
	if err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	box := &pickBox{}
	o := provider.SignInOptions{Site: site, Area: true, Done: openOnDone(mountOnDone(claimOnDone(c.area.ID), c.area.ID, box), opened)}
	if in.Proxy != nil {
		url, public, ok := pickedProxy(w, c, in.Proxy)
		if !ok {
			return
		}
		box.add(url, *in.Proxy)
		// seen by an IP's removal from now on, and judged again after,
		// so a removal and this start never pass each other (ipInUse
		// reads under ipsMu, as pickedProxy does)
		areaSignIns.Lock()
		if areaSignIns.pending == nil {
			areaSignIns.pending = map[*pickBox]string{}
		}
		areaSignIns.pending[box] = c.area.ID
		areaSignIns.Unlock()
		defer func() {
			areaSignIns.Lock()
			delete(areaSignIns.pending, box)
			areaSignIns.Unlock()
		}()
		if _, _, ok := pickedProxy(w, c, in.Proxy); !ok {
			return
		}
		o.Proxy, o.PublicOnly = &url, public
	}
	if len(c.area.Accounts) >= adminAccountsLimit {
		adminError(w, http.StatusConflict, adminConflict, "an area has at most 500 accounts")
		return
	}
	if in.Agent == "codex" && in.Way == wayDevice {
		o.Site = provider.CodexDeviceSite
	}
	st, err := startAreaSignIn(in.Agent, o)
	if errors.Is(err, provider.ErrSignInBusy) {
		adminError(w, http.StatusConflict, adminConflict, err.Error())
		return
	}
	if err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	keepAreaSignIn(c, st.ID, box)
	writeJSON(w, http.StatusOK, signInStateOf(c, st))
}

func (s *Server) adminSignInState(w http.ResponseWriter, r *http.Request, c adminCall) {
	id := r.PathValue("id")
	st, ok := provider.SignInStatus(id)
	if !ok || !ownSignIn(c, id) {
		adminNotFoundErr(w)
		return
	}
	writeJSON(w, http.StatusOK, signInStateOf(c, st))
}

func (s *Server) adminSignInCallback(w http.ResponseWriter, r *http.Request, c adminCall) {
	id := r.PathValue("id")
	var in struct {
		URL, Code, Key string
		Proxy          *proxyPick
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	st, ok := provider.SignInStatus(id)
	if !ok || !ownSignIn(c, id) {
		adminNotFoundErr(w)
		return
	}
	raw := strings.TrimSpace(in.URL)
	if raw == "" {
		raw = strings.TrimSpace(in.Code)
	}
	if raw == "" {
		// a key made on a plugin's keys page (paste "key")
		raw = strings.TrimSpace(in.Key)
	}
	if raw == "" {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send the address the vendor's page ended on as url, the code it showed as code, or the key made as key")
		return
	}
	// a proxy picked as it is pasted (T-212): the address is traded for
	// the account through it, and the account has it once done
	var through *string
	box := pickOf(id)
	if box == nil {
		box = &pickBox{}
	}
	if in.Proxy != nil {
		url, public, ok := pickedProxy(w, c, in.Proxy)
		if !ok {
			return
		}
		// the proxy it goes through now, picked or not: the same one is
		// taken by any sign-in, another by one not fixed
		if st.ProxyFixed && url != st.Proxy {
			adminError(w, http.StatusBadRequest, adminBadRequest, errSignInProxyFixed)
			return
		}
		// seen by an IP's removal before it is judged again
		box.add(url, *in.Proxy)
		if _, _, ok := pickedProxy(w, c, in.Proxy); !ok {
			return
		}
		if public {
			provider.HoldSignInPublic(id, url)
		}
		through = &url
	}
	if err := provider.SubmitSignInCallbackThrough(id, raw, through); err != nil {
		msg := err.Error()
		if errors.Is(err, provider.ErrProxyFixed) {
			msg = errSignInProxyFixed
		}
		adminError(w, http.StatusBadRequest, adminBadRequest, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// errSignInProxyFixed answers another proxy picked as a sign-in's address
// is pasted, for one whose proxy was handed out as it started (a device
// code, Claude Code's own sign-in): magpie-api.md §14.
const errSignInProxyFixed = "this sign-in's proxy was fixed when it started"

func (s *Server) adminSignInCancel(w http.ResponseWriter, r *http.Request, c adminCall) {
	id := r.PathValue("id")
	if _, ok := provider.SignInStatus(id); !ok || !ownSignIn(c, id) {
		adminNotFoundErr(w)
		return
	}
	provider.CancelSignIn(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminClaudeToken(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		Token, Opened string
		Proxy         *proxyPick
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	pid := subscriptionOf("claude")
	if !slices.Contains(c.area.SignIns, "claude") || pid == "" || provider.PluginServes("claude") {
		adminNotFoundErr(w)
		return
	}
	opened, err := areaOpened(in.Opened)
	if err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	// a setup-token is added with no request out: the pick is only put on
	if in.Proxy != nil {
		if _, _, ok := pickedProxy(w, c, in.Proxy); !ok {
			return
		}
	}
	if len(c.area.Accounts) >= adminAccountsLimit {
		adminError(w, http.StatusConflict, adminConflict, "an area has at most 500 accounts")
		return
	}
	user, err := provider.ClaudeTokenUser(in.Token)
	if err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	// owned first, as a key is: the account is the area's from the moment
	// it is in magpie, and one magpie has (the owner's, another area's)
	// is never written over
	ref := pid + "/" + provider.LoginID("claude", user)
	if err := area.ClaimUpTo(c.area.ID, ref, adminAccountsLimit); err != nil {
		adminClaimErr(w, err)
		return
	}
	_, added, err := provider.AddClaudeSetupTokenThrough(in.Token, nil)
	if err != nil || !added {
		area.Release(ref)
		if err == nil || errors.Is(err, provider.ErrClaudeTokenName) {
			adminError(w, http.StatusConflict, adminConflict, "this account is in magpie already")
			return
		}
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	log.Printf("admin: area %s (%s) added a Claude setup-token", c.area.ID, c.area.Note)
	if !opened.IsZero() {
		if err := provider.SetAccountOpened(pid, user, opened); err != nil {
			log.Printf("admin: area %s's Claude account, its opening date not kept: %v", c.area.ID, err)
		}
	}
	if in.Proxy != nil {
		if err := in.Proxy.mount(c.area.ID, ref); err != nil {
			log.Printf("admin: area %s's Claude account, %s", c.area.ID, mountFailed(err))
		}
	}
	a, _ := area.Get(c.area.ID)
	for _, acc := range areaAccounts(a) {
		if acc.ID == ref {
			writeJSON(w, http.StatusCreated, acc)
			return
		}
	}
	writeJSON(w, http.StatusCreated, adminAccount{ID: ref, Provider: pid, Kind: "account", Name: user})
}

// CancelAreaSignIns cancels the sign-ins area id started: one turned off
// or deleted adds no account.
func CancelAreaSignIns(id string) {
	var ids []string
	areaSignIns.Lock()
	for sid, a := range areaSignIns.m {
		if a.area == id {
			ids = append(ids, sid)
			delete(areaSignIns.m, sid)
		}
	}
	areaSignIns.Unlock()
	for _, sid := range ids {
		provider.CancelSignIn(sid)
	}
}
