package gateway

import (
	"cmp"
	"context"
	"errors"
	"log"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// An area admin's sign-in to a subscription a plugin serves (T-209,
// docs/subsystems/areas.md "Plugin sign-ins", magpie-api.md §11). It goes
// through the plugin host's sign-in to a new account only
// (plugin.AuthorizeNew, plugin.APIKeyNew): an account magpie has, the
// owner's or another area's, is a conflict and its tokens stay as they
// were. Only the methods read to be safe that way are offered
// (areaPlugins); the rest, and any a newer plugin adds, are not.

// The ways a plugin's sign-in finishes, beside the built-ins'.
const (
	wayAPIKey   = "api-key"   // a key, signed in with at once
	wayPasteKey = "paste-key" // a key made on the vendor's keys page, pasted
)

// areaPluginMethod is a plugin's sign-in method an area may use: its type
// and label as the plugin lists it, and how it finishes from a browser
// on another computer.
type areaPluginMethod struct {
	typ, label, way string
}

// areaPlugin is a subscription a plugin serves that an area may sign in
// to: the package read, and its methods an area may use.
type areaPlugin struct {
	pkg     string
	methods []areaPluginMethod
}

// areaPlugins are those, by magpie's provider id: the rows marked yes in
// areas.md's "Plugin sign-ins an area can use (T-221)", read from the
// plugins repo at 63c2ff93f. A method is offered while the plugin of that
// package lists one of the same type and label, at any index; one renamed,
// or another package serving the id, is offered no more until it is read
// again.
var areaPlugins = map[string]areaPlugin{
	"cursor": {"@magpie-community/opencode-cursor-auth", []areaPluginMethod{
		{"oauth", "Cursor (browser)", wayDevice},
		{"api", "Cursor API key (cursor.com/dashboard → Integrations)", wayAPIKey},
	}},
	"kiro": {"@magpie-community/opencode-kiro-auth", []areaPluginMethod{
		{"api", "Kiro API key (ksk_…)", wayAPIKey},
	}},
	"factory": {"@magpie-community/opencode-factory-auth", []areaPluginMethod{
		{"oauth", "Sign in with Factory (device code)", wayDevice},
		{"api", "Factory API key (fk-…)", wayAPIKey},
	}},
	"devin": {"@magpie-community/opencode-devin-auth", []areaPluginMethod{
		{"api", "Devin API key (cog_…)", wayAPIKey},
	}},
	"qoder": {"@magpie-community/opencode-qoder-auth", []areaPluginMethod{
		{"oauth", "Sign in with Qoder", wayDevice},
		{"oauth", "Sign in with Qoder PAT", wayDevice},
	}},
	"qoder-cn": {"@magpie-community/opencode-qoder-auth", []areaPluginMethod{
		{"oauth", "Sign in with Qoder CN", wayDevice},
		{"oauth", "Sign in with Qoder CN PAT", wayDevice},
		{"oauth", "Sign in with Qoder CN Enterprise (VPC)", wayDevice},
	}},
	"workbuddy": {"@magpie-community/opencode-workbuddy-auth", []areaPluginMethod{
		{"api", "WorkBuddy API key", wayAPIKey},
	}},
	"workbuddy-ai": {"@magpie-community/opencode-workbuddy-auth", []areaPluginMethod{
		{"api", "WorkBuddy AI API key", wayAPIKey},
	}},
	"zcode": {"@magpie-community/opencode-zcode-auth", []areaPluginMethod{
		{"api", "GLM Coding Plan API key", wayAPIKey},
	}},
	"mimo-app": {"@magpie-community/opencode-mimo-auth", []areaPluginMethod{
		{"oauth", "Xiaomi account", wayDevice},
	}},
	"commandcode-plan": {"@magpie-community/opencode-commandcode-auth", []areaPluginMethod{
		{"oauth", "Command Code (browser)", wayPasteKey},
		{"api", "API key (commandcode.ai/settings/keys)", wayAPIKey},
	}},
}

// adminPluginMethod is a plugin's method as options and catalog tell it.
type adminPluginMethod struct {
	Method         int               `json:"method"` // its index in the plugin's list now
	Way            string            `json:"way"`
	Label          string            `json:"label"`
	Labels         map[string]string `json:"labels,omitempty"`
	KeyPlaceholder string            `json:"keyPlaceholder,omitempty"`
	// Asks: it has questions, asked through signin/plugin/prompt first
	Asks bool `json:"asks"`
}

// areaPluginOf is the plugin serving agent, an id of areaPlugins (listed);
// on is false when no plugin of the package read serves it now (not
// installed, turned off, or another's).
func areaPluginOf(agent string) (pp plugin.Provider, listed, on bool) {
	ap, listed := areaPlugins[agent]
	if !listed {
		return plugin.Provider{}, false, false
	}
	pp, on = provider.PluginOf(agent)
	return pp, true, on && plugin.Name(pp.Spec) == ap.pkg
}

// areaMethods are pp's methods an area may use, as pp lists them now.
func areaMethods(agent string, pp plugin.Provider) []adminPluginMethod {
	out := []adminPluginMethod{}
	for i, m := range pp.Methods {
		for _, am := range areaPlugins[agent].methods {
			if m.Type != am.typ || m.Label != am.label {
				continue
			}
			pm := adminPluginMethod{Method: i, Way: am.way, Label: m.Label, Labels: m.Labels, Asks: m.Asked != nil}
			if am.way == wayAPIKey {
				pm.KeyPlaceholder = m.Placeholder
			}
			out = append(out, pm)
			break
		}
	}
	return out
}

// pluginSignInWays are the subscriptions plugins serve that an area may
// sign in to now: those whose plugin is on and lists a method offered.
func pluginSignInWays() []adminSignInWay {
	out := []adminSignInWay{}
	for _, id := range slices.Sorted(maps.Keys(areaPlugins)) {
		pp, _, on := areaPluginOf(id)
		if !on {
			continue
		}
		ms := areaMethods(id, pp)
		if len(ms) == 0 {
			continue
		}
		w := adminSignInWay{Agent: id, Name: cmp.Or(pp.Name, id), Plugin: true, Ways: []string{}, Methods: ms}
		for _, m := range ms {
			if !slices.Contains(w.Ways, m.Way) {
				w.Ways = append(w.Ways, m.Way)
			}
		}
		out = append(out, w)
	}
	return out
}

// pluginMethodOf is c's area's plugin subscription agent and its method
// (as way, when way isn't ""), or false with the answer written: 404 for
// one not offered to the area, 409 plugin_unavailable for one whose plugin
// is off or not installed, 409 conflict for a method whose way changed,
// and 400 for a method not offered.
func pluginMethodOf(w http.ResponseWriter, c adminCall, agent string, method int, way string) (plugin.Provider, adminPluginMethod, bool) {
	pp, listed, on := areaPluginOf(agent)
	if !listed || !slices.Contains(c.area.SignIns, agent) {
		adminNotFoundErr(w)
		return plugin.Provider{}, adminPluginMethod{}, false
	}
	if !on {
		adminError(w, http.StatusConflict, adminPluginUnavailable, "the "+agent+" plugin is off or not installed on this magpie")
		return plugin.Provider{}, adminPluginMethod{}, false
	}
	ms := areaMethods(agent, pp)
	i := slices.IndexFunc(ms, func(m adminPluginMethod) bool { return m.Method == method })
	switch {
	case i >= 0 && (way == "" || ms[i].Way == way):
		return pp, ms[i], true
	case i >= 0 || slices.ContainsFunc(ms, func(m adminPluginMethod) bool { return m.Way == way }):
		adminError(w, http.StatusConflict, adminConflict, provider.ErrPluginWayChanged.Error())
	default:
		adminError(w, http.StatusBadRequest, adminBadRequest, "this sign-in method isn't offered to areas")
	}
	return plugin.Provider{}, adminPluginMethod{}, false
}

// adminPluginUnavailable answers a plugin subscription offered to the
// area whose plugin is off or not installed: only the owner can mend it.
const adminPluginUnavailable = "plugin_unavailable"

// pluginInputKey is an answer's key, as magpie-api.md §11.3 takes it.
var pluginInputKey = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// checkInputs answers 400 for answers over §11.3's limits.
func checkInputs(w http.ResponseWriter, in map[string]string) bool {
	if len(in) > 16 {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send at most 16 inputs")
		return false
	}
	for k, v := range in {
		if !pluginInputKey.MatchString(k) || len(v) > 1024 {
			adminError(w, http.StatusBadRequest, adminBadRequest, "an input's key is 1 to 64 of A-Z, a-z, 0-9, _, . and -, and its value at most 1024 characters")
			return false
		}
	}
	return true
}

// pluginSaid is what a plugin sign-in was given, that text the plugin
// writes never says back (§11.3): Any (the key, a hidden answer) at any
// length, Long (another answer) from 4 characters.
type pluginSaid struct {
	Any, Long []string
}

func pluginGiven(m plugin.Method, inputs map[string]string, key string) pluginSaid {
	var s pluginSaid
	if k := strings.TrimSpace(key); k != "" {
		s.Any = append(s.Any, k)
	}
	for k, v := range inputs {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if m.Asked != nil && slices.Contains(m.Asked.Hidden, k) {
			s.Any = append(s.Any, v)
		} else {
			s.Long = append(s.Long, v)
		}
	}
	return s
}

// pluginWithheld takes the place of a plugin's text that says back what
// it was given.
const pluginWithheld = "the plugin's message was withheld because it repeated what you entered"

// held is text, or pluginWithheld when it says back what s holds.
func (s pluginSaid) held(text string) string {
	for _, v := range s.Any {
		if strings.Contains(text, v) {
			return pluginWithheld
		}
	}
	for _, v := range s.Long {
		if len(v) >= 4 && strings.Contains(text, v) {
			return pluginWithheld
		}
	}
	return text
}

// hiddenOf are the keys of inputs m asks unseen.
func hiddenOf(m plugin.Method, inputs map[string]string) []string {
	out := []string{}
	if m.Asked == nil {
		return out
	}
	for _, k := range slices.Sorted(maps.Keys(inputs)) {
		if slices.Contains(m.Asked.Hidden, k) {
			out = append(out, k)
		}
	}
	return out
}

// pluginFailed is what an area is told of the plugin host failing (T-229),
// its reason logged.
func pluginFailed(w http.ResponseWriter, c adminCall, agent, what string, err error) {
	log.Printf("admin: area %s's %s sign-in, %s: %v", c.area.ID, agent, what, err)
	adminError(w, http.StatusBadRequest, adminBadRequest, provider.ErrPluginFailed.Error())
}

func (s *Server) adminPluginPrompt(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		Agent  string
		Method int
		Inputs map[string]string
		Value  *string
	}
	if !decodeAdmin(w, r, &in) || !checkInputs(w, in.Inputs) {
		return
	}
	pp, pm, ok := pluginMethodOf(w, c, in.Agent, in.Method, "")
	if !ok {
		return
	}
	m := pp.Methods[pm.Method]
	inputs := maps.Clone(in.Inputs)
	if inputs == nil {
		inputs = map[string]string{}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, err := plugin.NextPrompt(ctx, pp.ID, pm.Method, inputs)
	if err != nil {
		pluginFailed(w, c, in.Agent, "its next question", err)
		return
	}
	if in.Value != nil {
		v := *in.Value
		if p == nil {
			adminError(w, http.StatusBadRequest, adminBadRequest, "this method has nothing more to ask: start the sign-in")
			return
		}
		if len(v) > 1024 {
			adminError(w, http.StatusBadRequest, adminBadRequest, "an input's value is at most 1024 characters")
			return
		}
		said := pluginGiven(m, map[string]string{p.Key: v}, "").with(pluginGiven(m, inputs, ""))
		if p.Type == "select" && !optionOf(p, v) {
			writeJSON(w, http.StatusOK, map[string]any{"error": "pick one of the options"})
			return
		}
		why, err := plugin.Validate(ctx, pp.ID, pm.Method, p.Key, v)
		if err != nil {
			pluginFailed(w, c, in.Agent, "checking an answer", err)
			return
		}
		if why != "" {
			writeJSON(w, http.StatusOK, map[string]any{"error": said.held(why)})
			return
		}
		inputs[p.Key] = v
		if len(inputs) > 16 {
			adminError(w, http.StatusBadRequest, adminBadRequest, "send at most 16 inputs")
			return
		}
		if p, err = plugin.NextPrompt(ctx, pp.ID, pm.Method, inputs); err != nil {
			pluginFailed(w, c, in.Agent, "its next question", err)
			return
		}
	}
	hidden := hiddenOf(m, inputs)
	out := maps.Clone(inputs)
	for _, k := range hidden {
		out[k] = "" // the console keeps what was typed unseen
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompt": p, "inputs": out, "hidden": hidden})
}

// optionOf: v is one of a select prompt p's options.
func optionOf(p *plugin.Prompt, v string) bool {
	for _, o := range p.Options {
		if o.Value == v {
			return true
		}
	}
	return false
}

// with is s and o, both.
func (s pluginSaid) with(o pluginSaid) pluginSaid {
	return pluginSaid{slices.Concat(s.Any, o.Any), slices.Concat(s.Long, o.Long)}
}

// startAreaPluginSignIn starts an area's plugin sign-in; a var so tests
// see what it was started with.
var startAreaPluginSignIn = provider.StartPluginSignInWith

// areaPluginSaid is what each area plugin sign-in was given, by its id,
// for the text it writes (signInStateOf).
var areaPluginSaid struct {
	sync.Mutex
	m map[string]pluginSaid
}

func pluginSaidOf(id string) pluginSaid {
	areaPluginSaid.Lock()
	defer areaPluginSaid.Unlock()
	return areaPluginSaid.m[id]
}

func (s *Server) adminPluginSignIn(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct {
		Agent, Way, Key, Opened string
		Method                  int
		Inputs                  map[string]string
		Proxy                   *proxyPick
	}
	if !decodeAdmin(w, r, &in) || !checkInputs(w, in.Inputs) {
		return
	}
	if in.Way == "" {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send the method's way, as options lists it")
		return
	}
	pp, pm, ok := pluginMethodOf(w, c, in.Agent, in.Method, in.Way)
	if !ok {
		return
	}
	m := pp.Methods[pm.Method]
	key := strings.TrimSpace(in.Key)
	var opened time.Time
	if pm.Way == wayAPIKey {
		if in.Opened != "" {
			adminError(w, http.StatusBadRequest, adminBadRequest, "a key has no opening date: leave opened out")
			return
		}
		if len(key) > 4096 {
			adminError(w, http.StatusBadRequest, adminBadRequest, "send the API key, up to 4096 characters")
			return
		}
	} else {
		if key != "" {
			adminError(w, http.StatusBadRequest, adminBadRequest, "key is sent for the api-key way, or pasted to signin/{id}/callback")
			return
		}
		var err error
		if opened, err = areaOpened(in.Opened); err != nil {
			adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
			return
		}
	}
	box := &pickBox{}
	var o provider.SignInOptions
	if in.Proxy != nil {
		url, public, ok := pickedProxy(w, c, in.Proxy)
		if !ok {
			return
		}
		if pm.Way != wayAPIKey {
			box.add(url, *in.Proxy)
			// seen by an IP's removal from now on, and judged again
			// after, as adminSignIn's
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
		}
		o.Proxy, o.PublicOnly = &url, public
	}
	if len(c.area.Accounts) >= adminAccountsLimit {
		adminError(w, http.StatusConflict, adminConflict, "an area has at most 500 accounts")
		return
	}
	said := pluginGiven(m, in.Inputs, key)
	if pm.Way == wayAPIKey {
		s.adminPluginKey(w, r, c, pp, pm, in.Inputs, key, in.Proxy, o)
		return
	}
	o.Area, o.PluginWay = true, pm.Way
	o.Done = openOnDone(mountOnDone(claimOnDone(c.area.ID), c.area.ID, box), opened)
	st, err := startAreaPluginSignIn(pp.ID, pm.Method, in.Inputs, o)
	switch {
	case errors.Is(err, provider.ErrSignInBusy), errors.Is(err, provider.ErrPluginWayChanged):
		adminError(w, http.StatusConflict, adminConflict, err.Error())
		return
	case err != nil:
		adminError(w, http.StatusBadRequest, adminBadRequest, said.held(err.Error()))
		return
	}
	log.Printf("admin: area %s (%s) started a %s sign-in", c.area.ID, c.area.Note, in.Agent)
	areaPluginSaid.Lock()
	if areaPluginSaid.m == nil {
		areaPluginSaid.m = map[string]pluginSaid{}
	}
	areaPluginSaid.m[st.ID] = said
	areaPluginSaid.Unlock()
	keepAreaSignIn(c, st.ID, box)
	writeJSON(w, http.StatusOK, signInStateOf(c, st))
}

// keepAreaSignIn records the sign-in id as c's area's, with the box of
// its proxy picks; those magpie no longer has are dropped, not kept till
// it quits.
func keepAreaSignIn(c adminCall, id string, box *pickBox) {
	areaSignIns.Lock()
	defer areaSignIns.Unlock()
	if areaSignIns.m == nil {
		areaSignIns.m = map[string]areaSignIn{}
	}
	for sid := range areaSignIns.m {
		if _, ok := provider.SignInStatus(sid); !ok {
			delete(areaSignIns.m, sid)
			areaPluginSaid.Lock()
			delete(areaPluginSaid.m, sid)
			areaPluginSaid.Unlock()
		}
	}
	areaSignIns.m[id] = areaSignIn{area: c.area.ID, pick: box}
}

// adminPluginKey signs c's area in to pp with a key, as its api method
// does, to a new account only, and claims the account once it is saved:
// one that can't be claimed (the area deleted) is signed out again.
func (s *Server) adminPluginKey(w http.ResponseWriter, r *http.Request, c adminCall, pp plugin.Provider, pm adminPluginMethod, inputs map[string]string, key string, pick *proxyPick, o provider.SignInOptions) {
	agent := provider.PluginID(pp.ID)
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	saved, user, err := provider.PluginAPIKeyNew(ctx, pp.ID, pm.Method, inputs, key, o)
	switch {
	case errors.Is(err, plugin.ErrKnown):
		adminError(w, http.StatusConflict, adminConflict, "this account is in magpie already")
		return
	case err != nil:
		adminError(w, http.StatusBadRequest, adminBadRequest, pluginGiven(pp.Methods[pm.Method], inputs, key).held(err.Error()))
		return
	}
	ref := agent + "/" + provider.LoginID(agent, user)
	if user == "" {
		err = errors.New("the account has no name in magpie")
	} else {
		err = area.ClaimUpTo(c.area.ID, ref, adminAccountsLimit)
	}
	if err != nil {
		if !errors.Is(err, area.ErrTaken) {
			// added for this area alone: not left serving the owner as
			// nobody's
			if serr := plugin.SignOut(context.Background(), saved.Provider, saved.Account); serr != nil {
				log.Printf("admin: area %s's %s key, not claimed, not signed out either: %v", c.area.ID, agent, serr)
			}
		}
		log.Printf("admin: area %s added a %s key, not claimed: %v", c.area.ID, agent, err)
		if user == "" {
			adminError(w, http.StatusBadRequest, adminBadRequest, plugin.ErrUnnamed.Error())
			return
		}
		adminClaimErr(w, err)
		return
	}
	log.Printf("admin: area %s (%s) signed in to %s with a key", c.area.ID, c.area.Note, agent)
	if pick != nil {
		if err := pick.mount(c.area.ID, ref); err != nil {
			log.Printf("admin: area %s's %s account, %s", c.area.ID, agent, mountFailed(err))
		}
	}
	a, _ := area.Get(c.area.ID)
	for _, acc := range areaAccounts(a) {
		if acc.ID == ref {
			writeJSON(w, http.StatusCreated, acc)
			return
		}
	}
	writeJSON(w, http.StatusCreated, adminAccount{ID: ref, Provider: agent, Kind: "account", Name: user})
}
