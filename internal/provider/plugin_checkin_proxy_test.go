package provider

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// recordingProxy is an HTTP proxy that answers each request itself, as the
// vendor would, and tells what it was asked.
func recordingProxy(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

// #2777 (zRain on Discord): a plugin's check-in goes through the proxy its
// usage goes through: the account's own, else its provider's, and never
// the global one past them. With the provider set to direct and the
// global proxy a residential entry the vendor refused, the balance read
// but each check-in, the loop's and a press, went out through the global
// proxy (ECONNRESET).
func TestPluginCheckinGoesThroughItsProxy(t *testing.T) {
	bun := testenv.Bun(t)
	home := claudeHome(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_CHECKIN", filepath.Join(home, "presses"))
	t.Cleanup(plugin.Settle)
	global, globalSaw := recordingProxy(t)
	own, ownSaw := recordingProxy(t)
	// the vendor, on this computer, refuses a check-in sent to it direct:
	// only a proxy answers for it. A name that resolves nowhere, as
	// vendor.invalid was, still asks DNS, and a TUN proxy's fake-IP DNS
	// (Clash's 198.18/15) answers it and holds the connection past the
	// step's minute. 0.0.0.0 is no loopback name to host.js, so the
	// account's proxy is used, and a direct dial of it stays on this
	// computer.
	var ownDirect atomic.Bool
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownDirect.CompareAndSwap(false, strings.HasSuffix(r.URL.Path, "/ck-own"))
		http.Error(w, "not through a proxy", http.StatusForbidden)
	}))
	t.Cleanup(vendor.Close)
	_, port, _ := net.SplitHostPort(vendor.Listener.Addr().String())
	url := "http://0.0.0.0:" + port + "/checkin"
	t.Setenv("FAKE_CHECKIN_URL", url)
	// the global proxy, set before the host starts, as the user's is
	s := settings.Load()
	s.Proxy = global.URL
	s.PluginCheckins = map[string]bool{"fakeco": true}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	step := minuteEach(t)
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(step(), abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(step()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ck-direct", "ck-own"} {
		if _, err := PluginAPIKey(step(), "fakeco", 0, nil, k); err != nil {
			t.Fatal(err)
		}
	}
	ps := pluginCheckinProviders()
	if len(ps) != 1 || len(ps[0].accounts) != 2 {
		t.Fatalf("the plugins that check in: %+v", ps)
	}
	// the provider direct, one account through a proxy of its own
	auths := plugin.Auths("fakeco")
	var ownUser, directUser string
	for _, l := range pluginLogins(mustPlugin(t)) {
		switch auths[l.acct.Key]["key"] {
		case "ck-own":
			ownUser = l.User
		case "ck-direct":
			directUser = l.User
		}
	}
	if ownUser == "" || directUser == "" || ownUser == directUser {
		t.Fatalf("the accounts: %q, %q (%+v)", ownUser, directUser, ps[0].accounts)
	}
	setProxies(t, PluginID("fakeco"), "direct", map[string]string{accountKey(ownUser): own.URL})

	check := func(name string, rs []WorkBuddyCheckin) {
		t.Helper()
		if len(rs) != 2 {
			t.Fatalf("%s: %+v", name, rs)
		}
		for _, r := range rs {
			switch r.User {
			case ownUser:
				if r.Outcome != CheckinClaimed {
					t.Errorf("%s: the account with its own proxy: %+v", name, r)
				}
			case directUser:
				// sent direct: the vendor refuses it
				if r.Outcome != CheckinFailed {
					t.Errorf("%s: the direct account: %+v", name, r)
				}
			default:
				t.Errorf("%s: an answer for %q: %+v", name, r.User, r)
			}
		}
		if g := globalSaw(); len(g) != 0 {
			t.Errorf("%s: the global proxy was asked %q", name, g)
		}
		if ownDirect.Load() {
			t.Errorf("%s: the account with its own proxy was sent direct", name)
		}
	}

	// the loop's, on a day of its own
	day := time.Date(2026, 10, 6, 10, 0, 0, 0, beijing)
	loop := pluginCheckiner{now: func() time.Time { return day }, only: func(id string) bool { return PluginCheckinOn(settings.Load(), id) }}
	check("the loop", loop.checkinNow(step(), false))
	if o := ownSaw(); !slices.Equal(o, []string{url + "/ck-own"}) {
		t.Fatalf("the loop: the account's proxy was asked %q", o)
	}
	// a press, today
	check("a press", CheckInPlugins(step(), "fakeco"))
	if o := ownSaw(); !slices.Equal(o, []string{url + "/ck-own", url + "/ck-own"}) {
		t.Fatalf("a press: the account's proxy was asked %q", o)
	}
}
