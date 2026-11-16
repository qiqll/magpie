package gateway

import (
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
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
	{Agent: "copilot", Name: "GitHub Copilot", Ways: []string{wayDevice}},
	{Agent: "claude", Name: "Claude", Ways: []string{waySetupToken, wayPasteCode}},
	{Agent: "gemini", Name: "Gemini", Ways: []string{wayPasteURL}},
	{Agent: "antigravity", Name: "Antigravity", Ways: []string{wayPasteURL}},
}

// signInWays are adminSignIns whose sign-in magpie has: one a plugin
// serves now signs in there, which no area can yet.
func signInWays() []adminSignInWay {
	out := []adminSignInWay{}
	for _, w := range adminSignIns {
		if !provider.PluginServes(w.Agent) {
			out = append(out, w)
		}
	}
	return out
}

// subscriptionOf is the id of the provider agent's accounts are served
// by: a built-in subscription's is its agent's (provider.loginProvider),
// listed or not yet, as it isn't before its first account. "" for an
// agent no area signs in to.
func subscriptionOf(agent string) string {
	if slices.ContainsFunc(adminSignIns, func(w adminSignInWay) bool { return w.Agent == agent }) {
		return agent
	}
	return ""
}

// areaSignIns are the sign-ins each area started, by id: an area reads,
// pastes into and cancels its own alone.
var areaSignIns struct {
	sync.Mutex
	m map[string]string
}

func ownSignIn(c adminCall, id string) bool {
	areaSignIns.Lock()
	defer areaSignIns.Unlock()
	return id != "" && areaSignIns.m[id] == c.area.ID
}

// adminSignInState is a sign-in as the admin routes tell it.
type adminSignInState struct {
	ID      string        `json:"id"`
	Agent   string        `json:"agent"`
	State   string        `json:"state"`
	URL     string        `json:"url,omitempty"`
	Code    string        `json:"code,omitempty"`
	Paste   string        `json:"paste"` // "url", "code" or ""
	Account *adminAccount `json:"account,omitempty"`
	Message string        `json:"message,omitempty"`
}

func signInStateOf(c adminCall, st provider.SignInState) adminSignInState {
	st = st.Said()
	out := adminSignInState{ID: st.ID, Agent: st.Agent, State: st.State, URL: st.URL, Code: st.Code, Message: st.Error}
	switch {
	case st.State != "waiting":
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
		if st.Again || pid == "" {
			st.State, st.Error = "conflict", "this account is in magpie already, so it stays where it is"
			return st
		}
		if err := area.Claim(id, pid+"/"+provider.LoginID(st.Agent, st.User)); err != nil {
			st.State = "conflict"
			st.Error = "signed in, but the account couldn't be made this area's: " + err.Error()
			if !errors.Is(err, area.ErrTaken) {
				st.State = "failed"
			}
			log.Printf("admin: area %s signed in to %s, not claimed: %v", id, st.Agent, err)
			return st
		}
		log.Printf("admin: area %s signed in to a %s account", id, st.Agent)
		return st
	}
}

func (s *Server) adminSignIn(w http.ResponseWriter, r *http.Request, c adminCall) {
	var in struct{ Agent, Way string }
	if !decodeAdmin(w, r, &in) {
		return
	}
	i := slices.IndexFunc(signInWays(), func(x adminSignInWay) bool { return x.Agent == in.Agent })
	if i < 0 || !slices.Contains(c.area.SignIns, in.Agent) {
		adminNotFoundErr(w)
		return
	}
	way := signInWays()[i]
	if in.Way == "" {
		in.Way = way.Ways[0]
	}
	if !slices.Contains(way.Ways, in.Way) || in.Way == waySetupToken {
		adminError(w, http.StatusBadRequest, adminBadRequest, in.Agent+" signs in by "+strings.Join(way.Ways, " or ")+" (a setup-token is sent to "+AdminPath+"/signin/claude-token)")
		return
	}
	if len(c.area.Accounts) >= adminAccountsLimit {
		adminError(w, http.StatusConflict, adminConflict, "an area has at most 500 accounts")
		return
	}
	o := provider.SignInOptions{Area: true, Done: claimOnDone(c.area.ID)}
	if in.Agent == "codex" && in.Way == wayDevice {
		o.Site = provider.CodexDeviceSite
	}
	st, err := provider.StartSignInWith(in.Agent, o)
	if errors.Is(err, provider.ErrSignInBusy) {
		adminError(w, http.StatusConflict, adminConflict, err.Error())
		return
	}
	if err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	areaSignIns.Lock()
	if areaSignIns.m == nil {
		areaSignIns.m = map[string]string{}
	}
	areaSignIns.m[st.ID] = c.area.ID
	areaSignIns.Unlock()
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
	var in struct{ URL, Code string }
	if !decodeAdmin(w, r, &in) {
		return
	}
	if _, ok := provider.SignInStatus(id); !ok || !ownSignIn(c, id) {
		adminNotFoundErr(w)
		return
	}
	raw := strings.TrimSpace(in.URL)
	if raw == "" {
		raw = strings.TrimSpace(in.Code)
	}
	if raw == "" {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send the address the vendor's page ended on as url, or the code it showed as code")
		return
	}
	if err := provider.SubmitSignInCallbackThrough(id, raw, nil); err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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
	var in struct{ Token string }
	if !decodeAdmin(w, r, &in) {
		return
	}
	pid := subscriptionOf("claude")
	if !slices.Contains(c.area.SignIns, "claude") || pid == "" || provider.PluginServes("claude") {
		adminNotFoundErr(w)
		return
	}
	user, added, err := provider.AddClaudeSetupTokenThrough(in.Token, nil)
	if err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
		return
	}
	if !added {
		adminError(w, http.StatusConflict, adminConflict, "this token is in magpie already")
		return
	}
	ref := pid + "/" + provider.LoginID("claude", user)
	if err := area.Claim(c.area.ID, ref); err != nil {
		adminClaimErr(w, err)
		return
	}
	log.Printf("admin: area %s (%s) added a Claude setup-token", c.area.ID, c.area.Note)
	a, _ := area.Get(c.area.ID)
	for _, acc := range areaAccounts(a) {
		if acc.ID == ref {
			writeJSON(w, http.StatusCreated, acc)
			return
		}
	}
	writeJSON(w, http.StatusCreated, adminAccount{ID: ref, Provider: pid, Kind: "account", Name: user})
}
