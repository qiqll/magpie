package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
)

// openSignIns gives area id the subscriptions its admin may sign in to.
func openSignIns(t *testing.T, do adminDo, console, id string, agents ...string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"signIns": agents})
	if c, body := do("POST", AdminPath+"/areas/"+id+"/update", lanPeer, console, string(b)); c != http.StatusOK {
		t.Fatalf("opening %v: %d %s", agents, c, body)
	}
}

// An area signs in to Sign in with ChatGPT (chatgpt-api) from a browser
// that isn't on this computer: the address OpenAI's page ends on, pasted
// to the sign-in's own callback, finishes it, and the account is the
// area's. An address for another port is refused, and nothing is kept.
func TestAdminSignInChatGPTAPIPasted(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, key := makeArea(t, do, console, "A")
	openSignIns(t, do, console, id, provider.ChatGPTAPIID)

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
			"email": "area@example.com", "nonce": n, "exp": float64(time.Now().Add(time.Hour).Unix()),
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "refresh_token": "rt-1", "token_type": "Bearer",
			"expires_in": 3600, "scope": "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct",
			"id_token": "h." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"})
	}))
	t.Cleanup(oai.Close)
	t.Cleanup(provider.SIWCForTest(oai.URL, oai.URL+"/v1"))

	c, body := do("POST", AdminPath+"/signin", lanPeer, key, `{"agent":"chatgpt-api","way":"paste-url"}`)
	if c != http.StatusOK {
		t.Fatal("start:", c, body)
	}
	var st adminSignInState
	json.Unmarshal([]byte(body), &st)
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	if st.State != "waiting" || st.Paste != "url" || !strings.HasPrefix(st.URL, oai.URL+"/api/accounts/authorize?") {
		t.Fatal("started:", body)
	}
	u, _ := url.Parse(st.URL)
	q := u.Query()
	mu.Lock()
	nonce = q.Get("nonce")
	mu.Unlock()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Hostname() != "127.0.0.1" {
		t.Fatal("redirect_uri:", q.Get("redirect_uri"))
	}
	back := url.Values{"code": {"the-code"}, "state": {q.Get("state")}, "client_id": {"oaiapp_1"}}
	paste := func(addr string) (int, string) {
		b, _ := json.Marshal(map[string]string{"url": addr})
		return do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, string(b))
	}
	other := *redirect
	other.Host = "127.0.0.1:1"
	if c, body := paste(other.String() + "?" + back.Encode()); c != http.StatusBadRequest || strings.Contains(body, "the-code") {
		t.Fatal("another port's address:", c, body)
	}
	if got := provider.Logins(provider.ChatGPTAPIID); len(got) != 0 {
		t.Fatal("kept after a refused paste:", got)
	}
	if c, body := paste(redirect.String() + "?" + back.Encode()); c != http.StatusNoContent {
		t.Fatal("its own address:", c, body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for st.State == "waiting" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		_, body = do("GET", AdminPath+"/signin/"+st.ID, lanPeer, key, "")
		json.Unmarshal([]byte(body), &st)
	}
	ref := provider.ChatGPTAPIID + "/" + provider.LoginID(provider.ChatGPTAPIID, "area@example.com")
	if st.State != "done" || st.Account == nil || st.Account.ID != ref || strings.Contains(body, "at-1") || strings.Contains(body, "rt-1") {
		t.Fatal("ended:", body)
	}
	if owner, _, _ := area.Owner(ref); owner != id {
		t.Fatal("owner:", owner)
	}
}

// Copilot signs an area in on an enterprise's <name>.ghe.com when the
// console sends its site, and on github.com when it sends none, as
// before; any other site is 400 and starts nothing.
func TestAdminSignInCopilotSite(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, key := makeArea(t, do, console, "A")
	openSignIns(t, do, console, id, "copilot", "codex")

	var started []provider.SignInOptions
	old := startAreaSignIn
	startAreaSignIn = func(agent string, o provider.SignInOptions) (provider.SignInState, error) {
		started = append(started, o)
		return provider.SignInState{ID: "si-site", Agent: agent, State: "waiting", URL: "https://github.com/login/device", Code: "ABCD-1234"}, nil
	}
	t.Cleanup(func() { startAreaSignIn = old })

	for body, want := range map[string]string{
		`{"agent":"copilot"}`:                                      "",
		`{"agent":"copilot","way":"device"}`:                       "",
		`{"agent":"copilot","way":"device","site":""}`:             "",
		`{"agent":"copilot","way":"device","site":"acme.ghe.com"}`: "acme.ghe.com",
		`{"agent":"copilot","site":"HTTPS://Acme-1.GHE.com/"}`:     "acme-1.ghe.com",
		`{"agent":"copilot","site":"https://a.ghe.com"}`:           "a.ghe.com",
	} {
		started = nil
		c, out := do("POST", AdminPath+"/signin", lanPeer, key, body)
		if c != http.StatusOK || len(started) != 1 || started[0].Site != want || !started[0].Area {
			t.Fatalf("%s: %d %s, started %+v", body, c, out, started)
		}
		if want != "" {
			// the provider's own reader takes it as that enterprise's host
			if h, err := provider.CopilotHost(started[0].Site); err != nil || h != want {
				t.Fatalf("%s: CopilotHost %q %v", body, h, err)
			}
		}
	}
	for _, site := range []string{"github.com", "https://github.com", "ghe.com", ".ghe.com", "-acme.ghe.com", "acme-.ghe.com",
		"a.b.ghe.com", "ghes.example.com", "acme.ghe.com.evil.com", "1.2.3.4", "acme.ghe.com:8443", "acme.ghe.com/path",
		"http://acme.ghe.com", "https://user@acme.ghe.com", "acme.ghe.com//", " acme.ghe.com", "acme.ghe.com?x=1",
		strings.Repeat("a", 64) + ".ghe.com"} {
		started = nil
		b, _ := json.Marshal(map[string]string{"agent": "copilot", "way": "device", "site": site})
		if c, out := do("POST", AdminPath+"/signin", lanPeer, key, string(b)); c != http.StatusBadRequest || !strings.Contains(out, adminBadRequest) || len(started) != 0 {
			t.Fatalf("site %q: %d %s, started %+v", site, c, out, started)
		}
	}
	// a site sent to one that signs in on one place alone
	if c, out := do("POST", AdminPath+"/signin", lanPeer, key, `{"agent":"codex","site":"acme.ghe.com"}`); c != http.StatusBadRequest || len(started) != 0 {
		t.Fatalf("codex with a site: %d %s", c, out)
	}
	// codex's device way still picks its own site
	if c, out := do("POST", AdminPath+"/signin", lanPeer, key, `{"agent":"codex","way":"device"}`); c != http.StatusOK || len(started) != 1 || started[0].Site != provider.CodexDeviceSite {
		t.Fatalf("codex device: %d %s, started %+v", c, out, started)
	}
}

// The options an area reads and the catalog the console reads list Sign
// in with ChatGPT by its pasted address, and Copilot's sites.
func TestAdminSignInOptionsSites(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, key := makeArea(t, do, console, "A")
	openSignIns(t, do, console, id, "copilot", provider.ChatGPTAPIID)

	read := func(path, secret string) []adminSignInWay {
		c, body := do("GET", AdminPath+path, lanPeer, secret, "")
		if c != http.StatusOK {
			t.Fatal(path, c, body)
		}
		var out struct{ SignIns []adminSignInWay }
		json.Unmarshal([]byte(body), &out)
		return out.SignIns
	}
	for _, tt := range []struct {
		path, secret string
	}{{"/options", key}, {"/catalog", console}} {
		ways := read(tt.path, tt.secret)
		i := slices.IndexFunc(ways, func(w adminSignInWay) bool { return w.Agent == "copilot" })
		j := slices.IndexFunc(ways, func(w adminSignInWay) bool { return w.Agent == provider.ChatGPTAPIID })
		if i < 0 || !slices.Equal(ways[i].Sites, []string{"github.com", "ghe.com"}) || !slices.Equal(ways[i].Ways, []string{wayDevice}) {
			t.Fatalf("%s copilot: %+v", tt.path, ways)
		}
		if j < 0 || !slices.Equal(ways[j].Ways, []string{wayPasteURL}) || ways[j].Sites != nil {
			t.Fatalf("%s chatgpt-api: %+v", tt.path, ways)
		}
	}
	if got := read("/options", key); len(got) != 2 {
		t.Fatal("an area reads what it was given alone:", got)
	}
}
