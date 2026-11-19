package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/renewal"
)

// T-212: an area picks one of its own IPs' proxies, or direct, for a
// sign-in, a setup-token or a key. The sign-in's requests go through it,
// and the account it adds has it put on as accounts/proxy would.

// vendorBehind is an area proxy that is the vendor too: every request
// sent through it is answered here and counted, so a request that
// reaches the vendor without it fails (*.invalid has no address).
type vendorBehind struct {
	srv   *httptest.Server
	mu    sync.Mutex
	seen  []string // paths asked through the proxy
	nonce string   // the nonce a Sign in with ChatGPT's id_token says
	exits map[string]*httptest.Server
	serve func(exit string) http.Handler
}

// at is the proxy that leaves from ip, the vendor too, started once: its
// trace page says ip (T-225: an area's proxy is added once it does).
func (v *vendorBehind) at(t *testing.T, ip string) *httptest.Server {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.exits[ip]; ok {
		return s
	}
	s := httptest.NewServer(v.serve(ip))
	t.Cleanup(s.Close)
	_, port, _ := net.SplitHostPort(s.Listener.Addr().String())
	fakePort(t, port)
	v.exits[ip] = s
	return s
}

const vendorHost = "auth.openai.invalid"

// startVendorBehind starts it, with http.DefaultTransport and
// http.DefaultClient routed as netproxy.Install routes them in magpie: a
// request made through a proxy (netproxy.With) goes through it, and a
// dial to an area-added one is guarded. Put back as the test ends.
func startVendorBehind(t *testing.T) *vendorBehind {
	t.Helper()
	base := http.DefaultTransport.(*http.Transport)
	proxy, dial, client := base.Proxy, base.DialContext, http.DefaultClient.Transport
	base.Proxy = netproxy.Func
	http.DefaultClient.Transport = netproxy.Dispatch(base)
	t.Cleanup(func() {
		base.Proxy, base.DialContext, http.DefaultClient.Transport = proxy, dial, client
		base.CloseIdleConnections()
	})
	v := &vendorBehind{exits: map[string]*httptest.Server{}}
	v.serve = func(exit string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v.mu.Lock()
			v.seen = append(v.seen, r.Host+r.URL.Path)
			nonce := v.nonce
			v.mu.Unlock()
			if r.URL.Path == "/cdn-cgi/trace" && exit != "" {
				w.Write([]byte("fl=1\nip=" + exit + "\nloc=JP\n"))
				return
			}
			v.vendor(w, r, nonce)
		})
	}
	v.srv = httptest.NewServer(v.serve(""))
	t.Cleanup(v.srv.Close)
	return v
}

// closeConns closes the connections made to it so far, at every exit.
func (v *vendorBehind) closeConns() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.srv.CloseClientConnections()
	for _, s := range v.exits {
		s.CloseClientConnections()
	}
}

// vendor answers r as the vendor would.
func (v *vendorBehind) vendor(w http.ResponseWriter, r *http.Request, nonce string) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/accounts/oauth/token":
		r.ParseForm()
		email := r.PostForm.Get("code") + "@example.com"
		claims, _ := json.Marshal(map[string]any{"iss": "http://" + vendorHost, "aud": r.PostForm.Get("client_id"), "sub": "user-" + email,
			"email": email, "nonce": nonce, "exp": float64(time.Now().Add(time.Hour).Unix()),
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "refresh_token": "rt-1", "token_type": "Bearer",
			"expires_in": 3600, "scope": "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct",
			"id_token": "h." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"})
	case "/api/accounts/deviceauth/usercode":
		json.NewEncoder(w).Encode(map[string]any{"device_auth_id": "dev-1", "user_code": "ABCD-1234", "interval": "5"})
	default:
		w.WriteHeader(http.StatusForbidden)
	}
}

// asked is how many requests for path came through the proxy.
func (v *vendorBehind) asked(path string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for _, s := range v.seen {
		if s == vendorHost+path {
			n++
		}
	}
	return n
}

// paths are the requests made through the proxy so far.
func (v *vendorBehind) paths() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.seen...)
}

// addVendorIP is an IP the area's admin adds with one proxy, at a public
// name that reaches v, and that proxy's id.
func (w *ipsWorld) addVendorIP(t *testing.T, secret, ip string, v *vendorBehind) string {
	t.Helper()
	_, port, _ := net.SplitHostPort(v.at(t, ip).Listener.Addr().String())
	proxy := "http://user:pw@" + proxyHost(ip) + ":" + port
	if c, body := w.post(t, secret, "/ips", map[string]any{"ip": ip, "name": "东京", "proxies": []string{proxy}}); c != http.StatusCreated {
		t.Fatalf("adding %s: %d %s", ip, c, body)
	}
	return renewal.ProxyID(proxy)
}

// startSIWC starts an area's Sign in with ChatGPT with body, and the
// address its page would end on with code.
func (w *ipsWorld) startSIWC(t *testing.T, secret, body string, v *vendorBehind, code string) (adminSignInState, string) {
	t.Helper()
	c, out := w.do("POST", AdminPath+"/signin", lanPeer, secret, body)
	if c != http.StatusOK {
		t.Fatal("start:", c, out)
	}
	var st adminSignInState
	json.Unmarshal([]byte(out), &st)
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	u, _ := url.Parse(st.URL)
	q := u.Query()
	v.mu.Lock()
	v.nonce = q.Get("nonce")
	v.mu.Unlock()
	back := url.Values{"code": {code}, "state": {q.Get("state")}, "client_id": {"oaiapp_1"}}
	return st, q.Get("redirect_uri") + "?" + back.Encode()
}

// ended is the sign-in id as it reads once it isn't waiting.
func (w *ipsWorld) ended(t *testing.T, secret, id string) adminSignInState {
	t.Helper()
	var st adminSignInState
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, body := w.do("GET", AdminPath+"/signin/"+id, lanPeer, secret, "")
		json.Unmarshal([]byte(body), &st)
		if st.State != "waiting" || time.Now().After(deadline) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (w *ipsWorld) paste(t *testing.T, secret, id string, in map[string]any) (int, string) {
	t.Helper()
	return w.post(t, secret, "/signin/"+id+"/callback", in)
}

// An area's sign-in through a proxy of its own IP trades the address for
// the account through that proxy, and the account has it, the area's
// (not overridden). A pick of another area's IP or proxy, or the owner's,
// is 404 and starts nothing (magpie-api.md §14, Q6). Another area
// signing in to that account through a proxy of its own gets a conflict,
// and the account keeps the first area's proxy: nothing of the second's
// is written on an account that isn't its.
func TestAdminSignInThroughAreaProxy(t *testing.T) {
	w := newIPsWorld(t)
	openSignIns(t, w.do, w.console, w.aID, provider.ChatGPTAPIID)
	openSignIns(t, w.do, w.console, w.bID, provider.ChatGPTAPIID)
	v := startVendorBehind(t)
	t.Cleanup(provider.SIWCForTest("http://"+vendorHost, "http://"+vendorHost+"/v1"))
	pxA := w.addVendorIP(t, w.a, "203.0.113.7", v)
	pxB := w.addVendorIP(t, w.b, "203.0.113.8", v)
	owners := renewal.ProxyID("http://owner:secret@192.168.1.2:7890")

	listed := func() int {
		areaSignIns.Lock()
		defer areaSignIns.Unlock()
		return len(areaSignIns.m)
	}
	before := listed()
	for _, tt := range []struct {
		pick any
		code int
	}{
		{map[string]any{"ip": "203.0.113.8", "id": pxB}, http.StatusNotFound},     // B's IP and proxy
		{map[string]any{"ip": "203.0.113.7", "id": pxB}, http.StatusNotFound},     // A's IP, B's proxy
		{map[string]any{"ip": "198.51.100.1", "id": owners}, http.StatusNotFound}, // the owner's
		{map[string]any{"ip": "203.0.113.7", "id": "px_0000000000"}, http.StatusNotFound},
		{map[string]any{}, http.StatusBadRequest},
		{map[string]any{"ip": "203.0.113.7"}, http.StatusBadRequest},
		{map[string]any{"direct": true, "ip": "203.0.113.7", "id": pxA}, http.StatusBadRequest},
	} {
		c, body := w.post(t, w.a, "/signin", map[string]any{"agent": provider.ChatGPTAPIID, "proxy": tt.pick})
		if c != tt.code || strings.Contains(body, "pw") {
			t.Fatalf("pick %v: %d %s", tt.pick, c, body)
		}
	}
	if listed() != before {
		t.Fatal("a refused pick started a sign-in")
	}

	b, _ := json.Marshal(map[string]any{"agent": provider.ChatGPTAPIID, "proxy": map[string]any{"ip": "203.0.113.7", "id": pxA}})
	st, end := w.startSIWC(t, w.a, string(b), v, "first")
	if c, body := w.paste(t, w.a, st.ID, map[string]any{"url": end}); c != http.StatusNoContent {
		t.Fatal("paste:", c, body)
	}
	st = w.ended(t, w.a, st.ID)
	ref := provider.ChatGPTAPIID + "/" + provider.LoginID(provider.ChatGPTAPIID, "first@example.com")
	if st.State != "done" || st.Account == nil || st.Account.ID != ref || st.Message != "" {
		t.Fatalf("ended: %+v", st)
	}
	if v.asked("/api/accounts/oauth/token") != 1 {
		t.Fatalf("the code wasn't traded through the area's proxy: %v", v.paths())
	}
	if p := st.Account.Proxy; p == nil || p.IP != "203.0.113.7" || p.ID != pxA || p.Direct || p.Overridden {
		t.Fatalf("the account's proxy: %+v", p)
	}

	// B signs in to the same account through its own proxy
	b, _ = json.Marshal(map[string]any{"agent": provider.ChatGPTAPIID, "proxy": map[string]any{"ip": "203.0.113.8", "id": pxB}})
	stB, endB := w.startSIWC(t, w.b, string(b), v, "first")
	// the paste's own answer to a sign-in that ended a conflict is
	// provider's (400); how it ended is read from the sign-in
	w.paste(t, w.b, stB.ID, map[string]any{"url": endB})
	if v.asked("/api/accounts/oauth/token") != 2 {
		t.Fatalf("B's code wasn't traded through B's proxy: %v", v.paths())
	}
	if stB = w.ended(t, w.b, stB.ID); stB.State != "conflict" || stB.Account != nil {
		t.Fatalf("B ended: %+v", stB)
	}
	if owner, _, _ := area.Owner(ref); owner != w.aID {
		t.Fatal("owner:", owner)
	}
	_, body := w.do("GET", AdminPath+"/accounts", lanPeer, w.a, "")
	var accs []adminAccount
	json.Unmarshal([]byte(body), &accs)
	for _, acc := range accs {
		if acc.ID == ref && (acc.Proxy == nil || acc.Proxy.ID != pxA || acc.Proxy.Overridden) {
			t.Fatalf("B's sign-in wrote over A's account's proxy: %s", body)
		}
	}
}

// A proxy picked as the address is pasted: the address is traded through
// it and the account has it. One that isn't the area's is 404, and the
// sign-in still waits for a paste.
func TestAdminSignInCallbackPicksAreaProxy(t *testing.T) {
	w := newIPsWorld(t)
	openSignIns(t, w.do, w.console, w.aID, provider.ChatGPTAPIID)
	v := startVendorBehind(t)
	t.Cleanup(provider.SIWCForTest("http://"+vendorHost, "http://"+vendorHost+"/v1"))
	pxA := w.addVendorIP(t, w.a, "203.0.113.7", v)
	pxB := w.addVendorIP(t, w.b, "203.0.113.8", v)

	st, end := w.startSIWC(t, w.a, `{"agent":"chatgpt-api"}`, v, "pasted")
	if c, body := w.paste(t, w.a, st.ID, map[string]any{"url": end, "proxy": map[string]any{"ip": "203.0.113.8", "id": pxB}}); c != http.StatusNotFound || strings.Contains(body, "pasted") {
		t.Fatal("B's proxy:", c, body)
	}
	if now, _ := provider.SignInStatus(st.ID); now.State != "waiting" || v.asked("/api/accounts/oauth/token") != 0 {
		t.Fatalf("a refused pick went on: %+v %v", now, v.paths())
	}
	if c, body := w.paste(t, w.a, st.ID, map[string]any{"url": end, "proxy": map[string]any{"ip": "203.0.113.7", "id": pxA}}); c != http.StatusNoContent {
		t.Fatal("A's proxy:", c, body)
	}
	st = w.ended(t, w.a, st.ID)
	if st.State != "done" || st.Account == nil || st.Account.Proxy == nil || st.Account.Proxy.ID != pxA || st.Account.Proxy.Overridden {
		t.Fatalf("ended: %+v %+v", st, st.Account)
	}
	if v.asked("/api/accounts/oauth/token") != 1 {
		t.Fatalf("the code wasn't traded through the picked proxy: %v", v.paths())
	}
}

// A device sign-in asks for its code through the area's proxy as it
// starts, so another proxy picked as something is pasted is 400 (§14).
// The area-added proxy is still held to public addresses: once its name
// answers this computer, a sign-in through it is refused before any
// request reaches it.
func TestAdminDeviceSignInProxyFixed(t *testing.T) {
	w := newIPsWorld(t)
	openSignIns(t, w.do, w.console, w.aID, "codex")
	v := startVendorBehind(t)
	t.Cleanup(provider.CodexDeviceForTest("http://" + vendorHost))
	pxA := w.addVendorIP(t, w.a, "203.0.113.7", v)
	pick := map[string]any{"ip": "203.0.113.7", "id": pxA}

	c, body := w.post(t, w.a, "/signin", map[string]any{"agent": "codex", "way": "device", "proxy": pick})
	if c != http.StatusOK {
		t.Fatal("start:", c, body)
	}
	var st adminSignInState
	json.Unmarshal([]byte(body), &st)
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	if st.Code != "ABCD-1234" || v.asked("/api/accounts/deviceauth/usercode") != 1 {
		t.Fatalf("the code wasn't asked through the area's proxy: %s %v", body, v.paths())
	}
	if c, body := w.paste(t, w.a, st.ID, map[string]any{"code": "x", "proxy": map[string]any{"direct": true}}); c != http.StatusBadRequest || !strings.Contains(body, errSignInProxyFixed) {
		t.Fatal("another proxy:", c, body)
	}
	if c, _ := w.post(t, w.a, "/signin/"+st.ID+"/cancel", map[string]any{}); c != http.StatusNoContent {
		t.Fatal("cancel:", c)
	}
	w.ended(t, w.a, st.ID)

	// its name now answers this computer; a connection made before, to
	// the public address, is gone
	w.answer.Store("127.0.0.1")
	v.closeConns()
	if c, body := w.post(t, w.a, "/signin", map[string]any{"agent": "codex", "way": "device", "proxy": pick}); c != http.StatusBadRequest {
		t.Fatal("through a proxy that now answers this computer:", c, body)
	}
	if n := v.asked("/api/accounts/deviceauth/usercode"); n != 1 {
		t.Fatalf("the proxy was reached at a home address: %d", n)
	}
}

// A key, many keys and a setup-token take a pick too: each added has it
// put on; another area's is 404 and adds nothing.
func TestAdminKeyAndTokenTakeAreaProxy(t *testing.T) {
	w := newIPsWorld(t)
	openSignIns(t, w.do, w.console, w.aID, "claude")
	pxA := w.addIP(t, w.a, "203.0.113.7")
	pxB := w.addIP(t, w.b, "203.0.113.8")
	keys := func() int { p, _ := provider.Find("plan"); return len(p.KeyList()) }
	n := keys()

	if c, body := w.post(t, w.a, "/accounts/key", map[string]any{"provider": "plan", "key": "sk-picked-b-0000000", "proxy": map[string]any{"ip": "203.0.113.8", "id": pxB}}); c != http.StatusNotFound || keys() != n {
		t.Fatal("B's proxy:", c, body)
	}
	if c, body := w.post(t, w.a, "/accounts/keys", map[string]any{"provider": "plan", "keys": []string{"sk-picked-b-0000001"}, "proxy": map[string]any{"ip": "203.0.113.7", "id": pxB}}); c != http.StatusNotFound || keys() != n {
		t.Fatal("B's proxy on A's IP, many:", c, body)
	}
	c, body := w.post(t, w.a, "/accounts/key", map[string]any{"provider": "plan", "key": "sk-picked-a-0000000", "proxy": map[string]any{"ip": " 203.0.113.7 ", "id": pxA}})
	var acc adminAccount
	json.Unmarshal([]byte(body), &acc)
	if c != http.StatusCreated || acc.Proxy == nil || acc.Proxy.ID != pxA || acc.Proxy.IP != "203.0.113.7" || acc.Proxy.Overridden {
		t.Fatal("A's proxy:", c, body)
	}
	c, body = w.post(t, w.a, "/accounts/keys", map[string]any{"provider": "plan", "keys": []string{"sk-picked-a-0000001", "sk-picked-a-0000002"}, "proxy": map[string]any{"direct": true}})
	var out struct{ Results []keyRow }
	json.Unmarshal([]byte(body), &out)
	if c != http.StatusOK || len(out.Results) != 2 {
		t.Fatal("many, direct:", c, body)
	}
	for _, row := range out.Results {
		if row.Status != keyAdded || row.Account == nil || row.Account.Proxy == nil || !row.Account.Proxy.Direct || row.Message != "" {
			t.Fatal("many, direct:", body)
		}
	}

	tok := "sk-ant-oat01-" + strings.Repeat("a", 40)
	if c, body := w.post(t, w.a, "/signin/claude-token", map[string]any{"token": tok, "proxy": map[string]any{"ip": "203.0.113.8", "id": pxB}}); c != http.StatusNotFound || len(provider.Logins("claude")) != 0 {
		t.Fatal("token, B's proxy:", c, body)
	}
	c, body = w.post(t, w.a, "/signin/claude-token", map[string]any{"token": tok, "proxy": map[string]any{"ip": "203.0.113.7", "id": pxA}})
	acc = adminAccount{}
	json.Unmarshal([]byte(body), &acc)
	if c != http.StatusCreated || acc.Proxy == nil || acc.Proxy.ID != pxA || acc.Proxy.Overridden || strings.Contains(body, tok) {
		t.Fatal("token, A's proxy:", c, body)
	}
}

// Review H1: an area takes out no IP or proxy a sign-in of its under way
// goes through (409 in_use), and once it is gone anyway (the owner's
// window, or ips.json by hand) the sign-in still holds it to public
// addresses: a name now answering this computer is refused before any
// request reaches it.
func TestAdminSignInHoldsItsProxyPublic(t *testing.T) {
	w := newIPsWorld(t)
	openSignIns(t, w.do, w.console, w.aID, provider.ChatGPTAPIID)
	v := startVendorBehind(t)
	t.Cleanup(provider.SIWCForTest("http://"+vendorHost, "http://"+vendorHost+"/v1"))
	pxA := w.addVendorIP(t, w.a, "203.0.113.7", v)

	b, _ := json.Marshal(map[string]any{"agent": provider.ChatGPTAPIID, "proxy": map[string]any{"ip": "203.0.113.7", "id": pxA}})
	st, end := w.startSIWC(t, w.a, string(b), v, "held")
	if c, body := w.post(t, w.a, "/ips/delete", map[string]any{"ip": "203.0.113.7"}); c != http.StatusConflict || !strings.Contains(body, adminInUse) {
		t.Fatal("deleting the IP a sign-in goes through:", c, body)
	}
	if c, body := w.post(t, w.a, "/ips/update", map[string]any{"ip": "203.0.113.7", "removeProxies": []string{pxA}}); c != http.StatusConflict || !strings.Contains(body, adminInUse) {
		t.Fatal("removing the proxy a sign-in goes through:", c, body)
	}

	// gone all the same, and its name pointed home
	if err := renewal.RemoveAreaIP(w.aID, "203.0.113.7", func(renewal.IP) error { return nil }); err != nil {
		t.Fatal(err)
	}
	w.answer.Store("127.0.0.1")
	v.closeConns()
	c, body := w.paste(t, w.a, st.ID, map[string]any{"url": end})
	if c != http.StatusBadRequest || !strings.Contains(body, netproxy.ErrNotPublic.Error()) || v.asked("/api/accounts/oauth/token") != 0 {
		t.Fatalf("traded through a proxy at a home address: %d %s %v", c, body, v.paths())
	}

	// a sign-in over takes nothing: the IP goes
	provider.CancelSignIn(st.ID)
	w.answer.Store(ipsPublic)
	w.addVendorIP(t, w.a, "203.0.113.9", v)
	if c, body := w.post(t, w.a, "/ips/delete", map[string]any{"ip": "203.0.113.9"}); c != http.StatusNoContent {
		t.Fatal("deleting an IP no sign-in goes through:", c, body)
	}
}

// Review M1/M2: a paste refused, with a proxy picked, leaves the pick the
// sign-in started with: the account has the proxy it was signed in
// through. An IP the owner gave the area is one it may pick.
func TestAdminSignInRefusedPasteKeepsItsPick(t *testing.T) {
	w := newIPsWorld(t)
	openSignIns(t, w.do, w.console, w.aID, provider.ChatGPTAPIID)
	v := startVendorBehind(t)
	t.Cleanup(provider.SIWCForTest("http://"+vendorHost, "http://"+vendorHost+"/v1"))
	pxA := w.addVendorIP(t, w.a, "203.0.113.7", v)
	px2 := w.addVendorIP(t, w.a, "203.0.113.9", v)

	b, _ := json.Marshal(map[string]any{"agent": provider.ChatGPTAPIID, "proxy": map[string]any{"ip": "203.0.113.7", "id": pxA}})
	st, end := w.startSIWC(t, w.a, string(b), v, "kept")
	u, _ := url.Parse(end)
	q := u.Query()
	q.Set("state", "not-its-state")
	u.RawQuery = q.Encode()
	if c, body := w.paste(t, w.a, st.ID, map[string]any{"url": u.String(), "proxy": map[string]any{"ip": "203.0.113.9", "id": px2}}); c != http.StatusBadRequest {
		t.Fatal("a wrong state:", c, body)
	}
	if c, body := w.paste(t, w.a, st.ID, map[string]any{"url": end}); c != http.StatusNoContent {
		t.Fatal("paste:", c, body)
	}
	if st = w.ended(t, w.a, st.ID); st.State != "done" || st.Account == nil || st.Account.Proxy == nil || st.Account.Proxy.ID != pxA {
		t.Fatalf("ended: %+v %+v", st, st.Account)
	}

	if err := renewal.GiveIP("198.51.100.1", w.aID); err != nil {
		t.Fatal(err)
	}
	owners := renewal.ProxyID("http://owner:secret@192.168.1.2:7890")
	c, body := w.post(t, w.a, "/accounts/key", map[string]any{"provider": "plan", "key": "sk-given-0000000", "proxy": map[string]any{"ip": "198.51.100.1", "id": owners}})
	var acc adminAccount
	json.Unmarshal([]byte(body), &acc)
	if c != http.StatusCreated || acc.Proxy == nil || acc.Proxy.IP != "198.51.100.1" || acc.Proxy.ID != owners || strings.Contains(body, "secret") {
		t.Fatal("an IP the owner gave:", c, body)
	}
}
