package provider

import (
	"context"
	"errors"
	"log"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// StartPluginSignIn begins signing in to a plugin's provider (OpenCode's
// id) with its OAuth method, the method's questions answered: the page to
// open, then either the plugin notices on its own or the code the page
// shows is pasted back (SubmitSignInCallback). SignInStatus follows it,
// as it does a built-in sign-in; its Agent is the provider's magpie id.
// The account goes beside those signed in already, or replaces the one it
// is. A built-in moved onto its plugin installs the CLI it runs through
// first, as the built-in's sign-in did.
func StartPluginSignIn(id string, method int, inputs map[string]string) (SignInState, error) {
	return StartPluginSignInWith(id, method, inputs, SignInOptions{})
}

// StartPluginSignInWith is StartPluginSignIn through the proxy o picks,
// else the one the provider's requests go through (#1663).
func StartPluginSignInWith(id string, method int, inputs map[string]string, o SignInOptions) (SignInState, error) {
	s := &signInFlow{done: make(chan struct{}), ofPlugin: true}
	s.st = SignInState{ID: randomToken(9), Agent: PluginID(id), State: "waiting"}
	if err := reserveSignIn(s, o); err != nil {
		return SignInState{}, err
	}
	s.secrets = PluginInputSecrets(id, method, inputs)
	s.pluginWay, s.conflicted = o.PluginWay, o.Conflicted
	logSignIn(SignInState{ID: s.st.ID, Agent: s.st.Agent, User: strings.TrimSpace(o.User)}, "started", 0, "")
	// one that couldn't start ends here, logged as failed by finish
	failed := func(err error) (SignInState, error) {
		s.finish(failedWith(err))
		return SignInState{}, err
	}
	if settings.AccountDiscoveryOff() {
		return failed(errAccountDiscoveryOff)
	}
	if err := s.pickProxy(PluginID(id), o); err != nil {
		return failed(err)
	}
	s.st.Proxy = s.proxy
	cli, install := pluginCLI(id)
	// an area's sign-in installs nothing on this computer: the methods
	// offered to it (admin_plugin_signin.go) run no vendor CLI
	install = install && !s.area
	var installing context.Context
	if install {
		s.st.State, s.st.Installing = "installing", cli.Name
		installing, s.stop = context.WithCancel(context.Background())
	} else if err := s.pluginBegin(id, method, inputs); err != nil {
		return failed(err)
	}
	replaceSignIns(s)
	if install {
		go func() {
			err := installCLI(installing, cli)
			if err == nil {
				forgetAccountCaches()
				err = s.pluginBegin(id, method, inputs)
			}
			if err != nil {
				s.finish(failedWith(err))
			}
		}()
	}
	return s.status(), nil
}

// pluginRunsCLI are the plugins (by OpenCode's id) that sign in with the
// vendor's CLI themselves, as the built-in did: the community Grok plugin
// runs `grok login`. One installed beside the built-in, not moved onto,
// said "install Grok Build first: curl … | bash", which a magpie in Docker
// has no shell to run; its CLI is installed first, as a moved one's is.
var pluginRunsCLI = map[string]bool{"grok": true}

// pluginCLI is the vendor CLI a plugin's sign-in (OpenCode's id) runs
// through and this machine lacks: a moved built-in's, or one in
// pluginRunsCLI.
func pluginCLI(id string) (agentCLI, bool) {
	if PluginServes(PluginID(id)) {
		return missingCLI(PluginID(id))
	}
	if pluginRunsCLI[id] {
		return missingCLI(id)
	}
	return agentCLI{}, false
}

// PluginCLIMissing is the name of the vendor CLI a plugin's sign-in runs
// through and this machine lacks (Grok Build for Grok's), "" when there is
// none to install. The command line's sign-in installs it first, as
// StartPluginSignIn does; without it, `magpie plugin login grok` in Docker
// said "install Grok Build first: curl … | bash", which the image has no
// curl to run (#2223).
func PluginCLIMissing(id string) string {
	c, ok := pluginCLI(id)
	if !ok {
		return ""
	}
	return c.Name
}

// InstallPluginCLI installs the CLI PluginCLIMissing names, with its
// vendor's installer or, where that can't run, as it installs it.
func InstallPluginCLI(ctx context.Context, id string) error {
	c, ok := pluginCLI(id)
	if !ok {
		return nil
	}
	if err := installCLI(ctx, c); err != nil {
		return err
	}
	forgetAccountCaches()
	return nil
}

// pluginBegin asks the plugin for the page to open and waits for the
// sign-in to finish there, or for the code the page shows.
func (s *signInFlow) pluginBegin(id string, method int, inputs map[string]string) error {
	ctx, cancel := context.WithTimeout(s.via(context.Background()), time.Minute)
	var a plugin.Authorization
	var err error
	if s.area {
		// to a new account only: one magpie has is ErrKnown, untouched
		a, err = plugin.AuthorizeNew(ctx, id, method, inputs)
	} else {
		a, err = plugin.Authorize(ctx, id, method, inputs, plugin.NewAccount)
	}
	cancel()
	if err != nil {
		return s.areaSaid(err)
	}
	if s.area && a.Opened {
		plugin.Abandon(a.Session)
		return errPluginHere
	}
	wait, stop := context.WithTimeout(context.Background(), signInTimeout)
	s.mu.Lock()
	if s.st.State != "waiting" && s.st.State != "installing" {
		// canceled while the CLI was installed
		s.mu.Unlock()
		stop()
		return nil
	}
	s.st.State, s.st.Installing = "waiting", ""
	s.st.URL, s.st.Instructions, s.st.Code, s.st.Opened = a.URL, a.Instructions, codeIn(a.Instructions), a.Opened
	s.stop = stop
	if a.Method == "code" {
		s.st.PasteCode, s.plugin = true, a.Session
	} else if PluginPastesCallback(id, a.URL) {
		// a browser that can't reach the plugin's port here ends on a page
		// that won't load: its address finishes it
		s.st.PasteCallback, s.pluginPorts = true, loopbackPorts(a.URL)
	} else if m, keys, ok := pluginKeyMethod(id); ok && m != method {
		// its page posts where nothing can be pasted: a key made on the
		// keys page finishes it
		s.st.PasteKey, s.st.KeysURL, s.pluginKey = true, keys, &pluginKeyWay{id: id, method: m}
	}
	if s.pluginWay != "" {
		if way := PluginWayOf(s.st); way != s.pluginWay {
			s.mu.Unlock()
			stop()
			plugin.Abandon(a.Session)
			if way == "" {
				return errPluginHere
			}
			return ErrPluginWayChanged
		}
	}
	s.mu.Unlock()
	if a.Method == "code" {
		go func() {
			<-wait.Done()
			if errors.Is(wait.Err(), context.DeadlineExceeded) {
				s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
			}
		}()
	} else {
		go func() {
			saved, err := plugin.Finish(wait, a.Session, "")
			s.pluginDone(saved, err)
		}()
	}
	return nil
}

// deviceCode is the code a device sign-in's instructions ask to check on
// the vendor's page ("Confirm the code ABCD-EFGH on Factory's page").
var deviceCode = regexp.MustCompile(`(?i:\bcode):?\s+([A-Z0-9]+(?:-[A-Z0-9]+)+|[A-Z0-9]{6,})\b`)

// codeIn is that code, shown to copy as the built-in's was.
func codeIn(instructions string) string {
	if m := deviceCode.FindStringSubmatch(instructions); m != nil {
		return m[1]
	}
	return ""
}

// pluginCode finishes a plugin's sign-in with the code its page showed.
func (s *signInFlow) pluginCode(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("paste the code the sign-in page showed")
	}
	s.mu.Lock()
	session := s.plugin
	s.plugin = ""
	waiting := s.st.State == "waiting"
	// one picked as the code was pasted (repick) goes to the plugin in
	// place of the one it was authorized through
	var through *string
	if s.repicked {
		p := s.proxy
		through = &p
	}
	s.mu.Unlock()
	if session == "" || !waiting {
		return errors.New("this sign-in is over; start it again")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	saved, err := plugin.FinishThrough(ctx, session, code, through)
	err = s.areaSaid(err)
	s.pluginDone(saved, err)
	return err
}

func (s *signInFlow) pluginDone(saved plugin.Saved, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		s.finish(SignInState{State: "canceled"})
	case errors.Is(err, context.DeadlineExceeded):
		s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
	case errors.Is(err, plugin.ErrAreaOwned):
		// the owner's, to an account an area owns (T-228): the area's
		// stays as it was, and what was saved of it was signed out again.
		// A failure with why, as the window and command line show one.
		st := s.status()
		logSignIn(SignInState{ID: st.ID, Agent: st.Agent}, "conflict", 0, "")
		s.finish(SignInState{State: "failed", Error: plugin.ErrAreaOwned.Error()})
	case errors.Is(err, plugin.ErrKnown):
		// an area's, to an account magpie has: that one stays as it was
		st := s.status()
		logSignIn(SignInState{ID: st.ID, Agent: st.Agent}, "conflict", 0, "")
		if s.finish(SignInState{State: "conflict", Error: plugin.ErrKnown.Error() + ", so it stays where it is"}) && s.conflicted != nil {
			s.conflicted()
		}
	case err != nil:
		s.finish(failedWith(s.areaSaid(err)))
	case s.area && PluginID(saved.Provider) != s.st.Agent:
		// the plugin kept it as another provider's (one package serves
		// Qoder and Qoder CN): no account of the agent the area claims, so
		// what was kept is signed out again rather than left nobody's
		if serr := plugin.SignOut(context.Background(), saved.Provider, saved.Account); serr != nil {
			log.Printf("sign-in %s (%s): kept as %s's, not signed out again: %v", s.st.Agent, s.st.ID, saved.Provider, serr)
		}
		s.finish(failedWith(ErrPluginWayChanged))
	default:
		clearPluginLapse(saved)
		user := pluginUser(saved)
		s.finish(SignInState{State: "done", User: user, Using: pluginUsing(saved, user)})
	}
}

// PluginWayOf is how a plugin's sign-in under way finishes from a browser
// on another computer, as an area is offered it (T-209): "paste-code"
// (the code its page shows), "paste-url" (the address its page ends on),
// "paste-key" (a key made on KeysURL), "device" (the plugin waits for
// its page by itself), or "" for one with no page at all, which works on
// this computer.
func PluginWayOf(st SignInState) string {
	switch {
	case st.PasteCode:
		return "paste-code"
	case st.PasteCallback:
		return "paste-url"
	case st.PasteKey:
		return "paste-key"
	case st.URL != "":
		return "device"
	}
	return ""
}

// ErrPluginWayChanged is an area's plugin sign-in that came out another
// way than it was offered by: the plugin changed since its methods were
// listed.
var ErrPluginWayChanged = errors.New("the plugin's sign-in ways changed; reload options")

// errPluginHere is an area's plugin sign-in that works on this computer
// (a page the plugin opened here, or none): an area's user can't reach it.
var errPluginHere = errors.New("this sign-in works on magpie's own computer, which an area can't reach")

// ErrPluginFailed is what an area is told of its plugin sign-in failing
// for a reason the plugin gave (T-229): the plugin's own words may say
// back what was typed, or what the vendor said of the host, and go to
// magpie's log alone.
var ErrPluginFailed = errors.New("the plugin couldn't finish this sign-in; magpie's owner can read why in magpie's log")

// areaSaid is err as an area's plugin sign-in says it: ErrPluginFailed
// for any but magpie's own (a known or unnamed account, a cancel, a time
// out), with the plugin's words logged, what the sign-in was given left
// out. Any other sign-in's err is as it is.
func (s *signInFlow) areaSaid(err error) error {
	if err == nil || !s.area || errors.Is(err, ErrPluginFailed) || errors.Is(err, plugin.ErrKnown) || errors.Is(err, plugin.ErrUnnamed) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	st := s.status()
	why := err.Error()
	if st.secrets.heldIn(why) {
		why = "(left out: it repeats what was entered)"
	}
	logSignIn(SignInState{ID: st.ID, Agent: st.Agent}, "plugin failed", httpStatusOf(err), why)
	return ErrPluginFailed
}

// PluginInputSecrets are the answers to a plugin's sign-in method (id,
// OpenCode's) that its failure never says back: each typed, at any length
// one typed unseen or marked secret (plugin.Asked); one picked from a list
// is no secret. A method that doesn't list its questions has each answer
// taken as typed.
func PluginInputSecrets(id string, method int, inputs map[string]string) SignInSecrets {
	var asked plugin.Asked
	if pp, ok := PluginOf(PluginID(id)); ok && method >= 0 && method < len(pp.Methods) && pp.Methods[method].Asked != nil {
		asked = *pp.Methods[method].Asked
	}
	var out SignInSecrets
	for _, k := range slices.Sorted(maps.Keys(inputs)) {
		switch {
		case slices.Contains(asked.Hidden, k):
			out.Hidden = append(out.Hidden, inputs[k])
		case !slices.Contains(asked.Selects, k):
			out.Given = append(out.Given, inputs[k])
		}
	}
	return out
}

// PluginAPIKey signs in to a plugin's provider with a key, as its "api"
// method does, beside the accounts signed in already; it gives the
// provider's magpie id.
func PluginAPIKey(ctx context.Context, id string, method int, inputs map[string]string, key string) (string, error) {
	// logged as a sign-in with no session ("key"); the key never is
	at := SignInState{Agent: PluginID(id)}
	logSignIn(at, "started", 0, "")
	// with subscription accounts off, a key is refused as a browser
	// sign-in is (StartPluginSignInWith), but for a public plugin's: one
	// saved would be signed in and listed nowhere
	if settings.AccountDiscoveryOff() {
		if pp, ok := PluginOf(PluginID(id)); !ok || !publicPlugin(pp) {
			logSignIn(at, "failed", 0, errAccountDiscoveryOff.Error())
			return "", errAccountDiscoveryOff
		}
	}
	if strings.TrimSpace(key) == "" {
		pp, ok := PluginOf(PluginID(id))
		if !ok || method < 0 || method >= len(pp.Methods) || pp.Methods[method].Type != "api" || !pp.Methods[method].Keyless {
			err := errors.New("enter the key")
			logSignIn(at, "failed", 0, err.Error())
			return "", err
		}
	}
	// through the proxy the provider's requests go through (#1663)
	saved, err := plugin.APIKey(Via(ctx, PluginID(id)), id, method, inputs, strings.TrimSpace(key), plugin.NewAccount)
	if errors.Is(err, plugin.ErrAreaOwned) {
		// an area owns the account the key is (T-228): it stays the area's
		logSignIn(at, "conflict", 0, "")
		return "", err
	}
	if err != nil {
		logSignIn(at, "failed", httpStatusOf(err), keyFailure(err, key, inputs))
		return "", err
	}
	id = PluginSignedIn(saved)
	at.User = pluginUser(saved)
	logSignIn(at, "done", 0, "")
	return id, nil
}

// PluginAPIKeyNew is PluginAPIKey to a new account only, as an area adds
// one (T-209): through the proxy o picks (held to public addresses when
// o says), else the provider's. A key magpie has, or an account the
// plugin names that magpie has, is plugin.ErrKnown and stays as it was;
// one the plugin names no account of is plugin.ErrUnnamed. A plugin's
// own reason is logged, and is ErrPluginFailed to the caller (T-229). It
// gives where the account was saved, and its name as the accounts list
// has it.
func PluginAPIKeyNew(ctx context.Context, id string, method int, inputs map[string]string, key string, o SignInOptions) (plugin.Saved, string, error) {
	at := SignInState{Agent: PluginID(id)}
	logSignIn(at, "started", 0, "")
	if settings.AccountDiscoveryOff() {
		logSignIn(at, "failed", 0, errAccountDiscoveryOff.Error())
		return plugin.Saved{}, "", errAccountDiscoveryOff
	}
	if strings.TrimSpace(key) == "" {
		pp, ok := PluginOf(PluginID(id))
		if !ok || method < 0 || method >= len(pp.Methods) || pp.Methods[method].Type != "api" || !pp.Methods[method].Keyless {
			err := errors.New("enter the key")
			logSignIn(at, "failed", 0, err.Error())
			return plugin.Saved{}, "", err
		}
	}
	if o.Proxy != nil {
		if o.PublicOnly {
			ctx = netproxy.WithPublicOnly(ctx)
		}
		ctx = netproxy.With(ctx, *o.Proxy)
	} else {
		ctx = Via(ctx, PluginID(id))
	}
	saved, err := plugin.APIKeyNew(ctx, id, method, inputs, strings.TrimSpace(key))
	switch {
	case errors.Is(err, plugin.ErrKnown):
		logSignIn(at, "conflict", 0, "")
		return plugin.Saved{}, "", err
	case errors.Is(err, plugin.ErrUnnamed):
		logSignIn(at, "failed", 0, err.Error())
		return plugin.Saved{}, "", err
	case err != nil:
		logSignIn(at, "failed", httpStatusOf(err), keyFailure(err, key, inputs))
		return plugin.Saved{}, "", ErrPluginFailed
	}
	PluginSignedIn(saved)
	at.User = pluginUser(saved)
	logSignIn(at, "done", 0, "")
	return saved, at.User, nil
}

// keyFailure is why a sign-in with key and inputs failed, err, as the log
// has it: a plugin's error may repeat the key or another answer it was
// given (a secret key, a token in an address), and is left out then.
func keyFailure(err error, key string, inputs map[string]string) string {
	why := err.Error()
	if k := strings.TrimSpace(key); k != "" && strings.Contains(why, k) {
		return "(left out: it repeats what was entered)"
	}
	// an answer of a few letters (a region's "us") is no secret
	for _, v := range slices.Sorted(maps.Values(inputs)) {
		if v = strings.TrimSpace(v); len(v) >= 4 && strings.Contains(why, v) {
			return "(left out: it repeats what was entered)"
		}
	}
	return why
}

// PluginSignedIn finishes a sign-in to a plugin's provider that magpie
// doesn't follow, a key or the command line's own, once the plugin has
// saved the account: as for one it follows (pluginDone), the account's
// lapsed mark goes and, removed from magpie, it comes back. It gives the
// provider's magpie id.
func PluginSignedIn(saved plugin.Saved) string {
	clearPluginLapse(saved)
	id := PluginID(saved.Provider)
	_ = ShowAccount(id)
	return id
}

// pluginUsing is whether the account signed in to is the one in use, the
// first, as a built-in's sign-in says.
func pluginUsing(saved plugin.Saved, user string) bool {
	for _, l := range Logins(PluginID(saved.Provider)) {
		if l.Active {
			return user != "" && strings.EqualFold(l.User, user)
		}
	}
	return false
}

// pluginUser is the account signed in to, as the accounts list names it.
func pluginUser(saved plugin.Saved) string {
	for _, pp := range plugin.Cached() {
		if pp.ID == saved.Provider {
			return pluginNames(pp)[saved.Account]
		}
	}
	return ""
}
