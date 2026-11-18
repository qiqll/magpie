package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/renewal"
)

// The console acts for an area (magpie-api.md §17, T-215): it adds and
// removes the area's keys through /areas/{id}/…, with the area's own
// checks, and each write is in the log as the console's for that area
// with the actor it names. An area's key is refused these routes whatever
// the id, the console the area's own unprefixed ones, an area that isn't
// is 404, and one turned off is acted for still.
func TestAdminConsoleActsForArea(t *testing.T) {
	do, console, _ := adminWorld(t)
	studyID, study := makeArea(t, do, console, "学习", "plan")
	workID, work := makeArea(t, do, console, "工作", "plan", "other")
	logs := catchLog(t)
	key := func(secret, id, prov, value string, hdr ...string) (int, string) {
		b, _ := json.Marshal(map[string]string{"provider": prov, "name": "k", "key": value})
		return do("POST", AdminPath+"/areas/"+id+"/accounts/key", lanPeer, secret, string(b), hdr...)
	}
	c, body := key(console, studyID, "plan", "sk-console-for-study-01", ActorHeader, "alice@ops")
	if c != http.StatusCreated || strings.Contains(body, "sk-console-for-study-01") {
		t.Fatal("the console's key for study:", c, body)
	}
	var acc adminAccount
	json.Unmarshal([]byte(body), &acc)
	if owner, _, _ := area.Owner(acc.ID); owner != studyID {
		t.Fatal("owner:", owner)
	}
	c, body = do("GET", AdminPath+"/accounts", lanPeer, study, "")
	if c != http.StatusOK || !strings.Contains(body, acc.ID) {
		t.Fatal("study doesn't list it:", c, body)
	}
	line := waitLogged(t, logs, "admin: console for area "+studyID+" (学习) added a key of plan", "actor=alice@ops")
	if strings.Contains(line, "sk-console") {
		t.Fatal("the key is logged:", line)
	}

	// the area's checks: a provider it wasn't given, the same key again
	if c, body := key(console, studyID, "other", "sk-console-other-0002"); c != http.StatusNotFound {
		t.Fatal("a provider study wasn't given:", c, body)
	}
	if c, body := key(console, workID, "plan", "sk-console-for-study-01"); c != http.StatusConflict || strings.Contains(body, "学习") {
		t.Fatal("study's key for work:", c, body)
	}
	if c, body := key(console, "ar_nosuch", "plan", "sk-console-nosuch-03"); c != http.StatusNotFound || !strings.Contains(body, adminNotFound) {
		t.Fatal("no such area:", c, body)
	}
	for _, bad := range []string{"", "a b", strings.Repeat("a", 65), "名字"} {
		if c, body := key(console, studyID, "plan", "sk-console-bad-actor-04", ActorHeader, bad); c != http.StatusBadRequest {
			t.Fatalf("actor %q: %d %s", bad, c, body)
		}
	}

	// an area's key, for its own area or another or none: 403 before the id
	for _, id := range []string{studyID, workID, "ar_nosuch"} {
		if c, body := key(study, id, "plan", "sk-area-acting-000005"); c != http.StatusForbidden || !strings.Contains(body, adminConsoleOnly) {
			t.Fatalf("study's key for %s: %d %s", id, c, body)
		}
	}
	if c, body := do("GET", AdminPath+"/areas/"+studyID+"/options", lanPeer, work, ""); c != http.StatusForbidden || !strings.Contains(body, adminConsoleOnly) {
		t.Fatal("work's key reads study's options:", c, body)
	}
	// the console on an area's own route: 403
	b, _ := json.Marshal(map[string]string{"provider": "plan", "key": "sk-console-unprefixed-6"})
	if c, body := do("POST", AdminPath+"/accounts/key", lanPeer, console, string(b)); c != http.StatusForbidden || !strings.Contains(body, adminAreaOnly) {
		t.Fatal("the console's unprefixed add:", c, body)
	}

	// its options are the area's
	c, body = do("GET", AdminPath+"/areas/"+workID+"/options", lanPeer, console, "")
	var opts struct{ Providers []adminProvider }
	json.Unmarshal([]byte(body), &opts)
	if c != http.StatusOK || len(opts.Providers) != 2 {
		t.Fatal("work's options:", c, body)
	}

	// another area's account is no such account; its own is removed
	rm := func(id, ref string, hdr ...string) (int, string) {
		b, _ := json.Marshal(map[string]string{"id": ref})
		return do("POST", AdminPath+"/areas/"+id+"/accounts/remove", lanPeer, console, string(b), hdr...)
	}
	if c, body := rm(workID, acc.ID); c != http.StatusNotFound {
		t.Fatal("study's account through work:", c, body)
	}
	if owner, _, _ := area.Owner(acc.ID); owner != studyID {
		t.Fatal("removed through another area:", owner)
	}
	// turned off, the area's key is refused, the console acts for it
	if c, body := do("POST", AdminPath+"/areas/"+studyID+"/update", lanPeer, console, `{"off":true}`); c != http.StatusOK {
		t.Fatal("off:", c, body)
	}
	if c, _ := do("GET", AdminPath+"/accounts", lanPeer, study, ""); c != http.StatusUnauthorized {
		t.Fatal("study's key while off:", c)
	}
	if c, body := rm(studyID, acc.ID); c != http.StatusNoContent {
		t.Fatal("the console removes study's:", c, body)
	}
	waitLogged(t, logs, "admin: console for area "+studyID+" (学习) removed plan", "actor=-")
	// a write its handler logged nothing for has its line too
	rm(workID, "plan/nosuch")
	waitLogged(t, logs, "admin: console for area "+workID+" (工作) accounts/remove: 404", "actor=-")
}

// The console's add routes count toward the area's conflict hold, as
// the area's do: past 20 409s the area is held, from either key, and
// another area is not (no limit of its own to go round).
func TestAdminConsoleSharesAreaLimits(t *testing.T) {
	do, console, _ := adminWorld(t)
	studyID, study := makeArea(t, do, console, "学习", "plan")
	workID, _ := makeArea(t, do, console, "工作", "plan")
	if err := provider.AddKey("plan", "owner's", "sk-owners-probe-0001", ""); err != nil {
		t.Fatal(err)
	}
	add := func(path, secret string) int {
		c, _ := do("POST", AdminPath+path, lanPeer, secret, `{"provider":"plan","key":"sk-owners-probe-0001"}`)
		return c
	}
	for i := 1; i <= conflictMost; i++ {
		path, secret := "/areas/"+studyID+"/accounts/key", console
		if i%2 == 0 {
			path, secret = "/accounts/key", study
		}
		if c := add(path, secret); c != http.StatusConflict {
			t.Fatalf("409 number %d: %d", i, c)
		}
	}
	if c := add("/areas/"+studyID+"/accounts/key", console); c != http.StatusTooManyRequests {
		t.Fatal("the console for study, held:", c)
	}
	if c := add("/accounts/key", study); c != http.StatusTooManyRequests {
		t.Fatal("study's key, held:", c)
	}
	if c := add("/areas/"+workID+"/accounts/key", console); c != http.StatusConflict {
		t.Fatal("work is held:", c)
	}
}

// A sign-in the console starts for an area is the console's: the area's
// key reads, pastes into and cancels it as no such sign-in, the console
// finishes it, and the account is the area's. One the area started is
// no such sign-in to the console.
func TestAdminConsoleSignInIsItsOwn(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, key := makeArea(t, do, console, "A")
	openSignIns(t, do, console, id, provider.ChatGPTAPIID, "codex")

	var mu sync.Mutex
	nonce := ""
	var oai *httptest.Server
	oai = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/oauth/token" {
			http.NotFound(w, r)
			return
		}
		r.ParseForm()
		mu.Lock()
		n := nonce
		mu.Unlock()
		claims, _ := json.Marshal(map[string]any{"iss": oai.URL, "aud": r.PostForm.Get("client_id"), "sub": "user-1",
			"email": "console@example.com", "nonce": n, "exp": float64(time.Now().Add(time.Hour).Unix()),
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "refresh_token": "rt-1", "token_type": "Bearer",
			"expires_in": 3600, "scope": "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct",
			"id_token": "h." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"})
	}))
	t.Cleanup(oai.Close)
	t.Cleanup(provider.SIWCForTest(oai.URL, oai.URL+"/v1"))

	logs := catchLog(t)
	base := AdminPath + "/areas/" + id
	c, body := do("POST", base+"/signin", lanPeer, console, `{"agent":"chatgpt-api","way":"paste-url"}`, ActorHeader, "bob")
	if c != http.StatusOK {
		t.Fatal("start:", c, body)
	}
	var st adminSignInState
	json.Unmarshal([]byte(body), &st)
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	if st.State != "waiting" || st.Paste != "url" {
		t.Fatal("started:", body)
	}
	// the area's key: no such sign-in, read, pasted into or cancelled
	if c, _ := do("GET", AdminPath+"/signin/"+st.ID, lanPeer, key, ""); c != http.StatusNotFound {
		t.Fatal("the area reads the console's:", c)
	}
	if c, _ := do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, `{"url":"http://127.0.0.1:1/?code=x"}`); c != http.StatusNotFound {
		t.Fatal("the area pastes into the console's:", c)
	}
	if c, _ := do("POST", AdminPath+"/signin/"+st.ID+"/cancel", lanPeer, key, ""); c != http.StatusNotFound {
		t.Fatal("the area cancels the console's:", c)
	}
	// nor the console for another area
	otherID, _ := makeArea(t, do, console, "B")
	if c, _ := do("GET", AdminPath+"/areas/"+otherID+"/signin/"+st.ID, lanPeer, console, ""); c != http.StatusNotFound {
		t.Fatal("the console reads it through another area:", c)
	}

	// turned off, the area's key adds nothing, while the console acts for
	// it still: its sign-in goes on, and is finished while off
	if c, body := do("POST", AdminPath+"/areas/"+id+"/update", lanPeer, console, `{"off":true}`); c != http.StatusOK {
		t.Fatal("off:", c, body)
	}
	if got, ok := provider.SignInStatus(st.ID); !ok || got.State != "waiting" {
		t.Fatal("the console's sign-in after off:", got, ok)
	}

	u, _ := url.Parse(st.URL)
	q := u.Query()
	mu.Lock()
	nonce = q.Get("nonce")
	mu.Unlock()
	back := url.Values{"code": {"the-code"}, "state": {q.Get("state")}, "client_id": {"oaiapp_1"}}
	b, _ := json.Marshal(map[string]string{"url": q.Get("redirect_uri") + "?" + back.Encode()})
	if c, body := do("POST", base+"/signin/"+st.ID+"/callback", lanPeer, console, string(b)); c != http.StatusNoContent {
		t.Fatal("the console pastes:", c, body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for st.State == "waiting" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		_, body = do("GET", base+"/signin/"+st.ID, lanPeer, console, "")
		json.Unmarshal([]byte(body), &st)
	}
	ref := provider.ChatGPTAPIID + "/" + provider.LoginID(provider.ChatGPTAPIID, "console@example.com")
	if st.State != "done" || st.Account == nil || st.Account.ID != ref || strings.Contains(body, "at-1") {
		t.Fatal("ended:", body)
	}
	if owner, _, _ := area.Owner(ref); owner != id {
		t.Fatal("owner:", owner)
	}
	// the account it adds is logged as the console's, with its actor
	waitLogged(t, logs, "admin: console for area "+id+" (A) started a chatgpt-api sign-in", "actor=bob")
	waitLogged(t, logs, "admin: console for area "+id+" (A) signed in to a chatgpt-api account", "actor=bob")

	// one the area starts is no such sign-in to the console
	if c, body := do("POST", AdminPath+"/areas/"+id+"/update", lanPeer, console, `{"off":false}`); c != http.StatusOK {
		t.Fatal("on:", c, body)
	}
	c, body = do("POST", AdminPath+"/signin", lanPeer, key, `{"agent":"chatgpt-api","way":"paste-url"}`)
	var own adminSignInState
	json.Unmarshal([]byte(body), &own)
	if c != http.StatusOK || own.ID == "" {
		t.Fatal("the area's own:", c, body)
	}
	t.Cleanup(func() { provider.CancelSignIn(own.ID) })
	if c, _ := do("GET", base+"/signin/"+own.ID, lanPeer, console, ""); c != http.StatusNotFound {
		t.Fatal("the console reads the area's:", c)
	}
	if c, _ := do("POST", base+"/signin/"+own.ID+"/cancel", lanPeer, console, ""); c != http.StatusNotFound {
		t.Fatal("the console cancels the area's:", c)
	}
	if c, body := do("GET", AdminPath+"/signin/"+own.ID, lanPeer, key, ""); c != http.StatusOK || !strings.Contains(body, `"waiting"`) {
		t.Fatal("the area reads its own:", c, body)
	}
	// turned off, the area's own sign-in is cancelled
	if c, body := do("POST", AdminPath+"/areas/"+id+"/update", lanPeer, console, `{"off":true}`); c != http.StatusOK {
		t.Fatal("off:", c, body)
	}
	if got, ok := provider.SignInStatus(own.ID); ok && got.State == "waiting" {
		t.Fatal("the area's own sign-in after off:", got)
	}
}

// The console adds an IP for an area, puts its proxy on the area's
// account and removes it, as the area would: the IP is the area's,
// added by the area (public only), and another area's proxy or account
// is no such thing through it.
func TestAdminConsoleIPsForArea(t *testing.T) {
	w := newIPsWorld(t)
	pxB := w.addIP(t, w.b, "203.0.113.8")
	base := "/areas/" + w.aID
	proxy := "http://user:pw@" + proxyHost("203.0.113.7") + ":" + w.proxy.port()
	c, body := w.post(t, w.console, base+"/ips", map[string]any{"ip": "203.0.113.7", "proxies": []string{proxy}})
	if c != http.StatusCreated || strings.Contains(body, "pw@") {
		t.Fatal("the console adds A's IP:", c, body)
	}
	pxA := renewal.ProxyID(proxy)
	c, body = w.do("GET", AdminPath+"/ips", lanPeer, w.a, "")
	if got := ipsOf(t, body); c != http.StatusOK || len(got) != 1 || got[0].IP != "203.0.113.7" || got[0].GivenByOwner {
		t.Fatal("A's IPs:", c, body)
	}
	// a private proxy is refused, as the area's is
	if c, body := w.post(t, w.console, base+"/ips", map[string]any{"ip": "203.0.113.9", "proxies": []string{"http://192.168.1.2:7890"}}); c != http.StatusBadRequest {
		t.Fatal("a private proxy:", c, body)
	}
	for _, in := range []map[string]any{
		{"id": w.aKey, "ip": "203.0.113.8", "proxy": pxB}, // B's proxy
		{"id": w.bKey, "ip": "203.0.113.7", "proxy": pxA}, // B's account
	} {
		if c, body := w.post(t, w.console, base+"/accounts/proxy", in); c != http.StatusNotFound {
			t.Fatal("through A:", in, c, body)
		}
	}
	if c, body := w.post(t, w.console, base+"/accounts/proxy", map[string]any{"id": w.aKey, "ip": "203.0.113.7", "proxy": pxA}); c != http.StatusOK && c != http.StatusNoContent {
		t.Fatal("A's proxy on A's account:", c, body)
	}
	if c, body := w.post(t, w.console, base+"/accounts/proxy", map[string]any{"id": w.aKey, "direct": true}); c != http.StatusOK && c != http.StatusNoContent {
		t.Fatal("A's account direct:", c, body)
	}
	if c, body := w.post(t, w.console, "/areas/"+w.bID+"/ips/delete", map[string]any{"ip": "203.0.113.7"}); c != http.StatusNotFound {
		t.Fatal("A's IP through B:", c, body)
	}
	if c, body := w.post(t, w.console, base+"/ips/delete", map[string]any{"ip": "203.0.113.7"}); c != http.StatusNoContent && c != http.StatusOK {
		t.Fatal("A's IP removed:", c, body)
	}
}

// A sign-in recorded after its area was turned off (its key's) or
// deleted (anyone's) is let go at once: the cancel that came first never
// saw it.
func TestAdminSignInKeptAfterOffIsLetGo(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, _ := makeArea(t, do, console, "A")
	if c, body := do("POST", AdminPath+"/areas/"+id+"/update", lanPeer, console, `{"off":true}`); c != http.StatusOK {
		t.Fatal("off:", c, body)
	}
	kept := func(sid string) bool {
		areaSignIns.Lock()
		defer areaSignIns.Unlock()
		_, ok := areaSignIns.m[sid]
		return ok
	}
	a, _ := area.Get(id)
	// each read before the next is kept, which drops those magpie hasn't
	keepAreaSignIn(adminCall{area: a}, "si-key", nil)
	if kept("si-key") {
		t.Fatal("off: the key's kept")
	}
	keepAreaSignIn(adminCall{area: a, console: true}, "si-console", nil)
	if !kept("si-console") {
		t.Fatal("off: the console's let go")
	}
	if c, body := do("POST", AdminPath+"/areas/"+id+"/delete", lanPeer, console, ""); c != http.StatusNoContent {
		t.Fatal("delete:", c, body)
	}
	keepAreaSignIn(adminCall{area: a, console: true}, "si-console-2", nil)
	if kept("si-console-2") {
		t.Fatal("deleted: the console's kept")
	}
}
