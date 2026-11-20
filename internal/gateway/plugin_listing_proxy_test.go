package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin account's model list is asked through the account's own proxy,
// as a built-in account's is fetched through its: each of two accounts
// behind its own proxy is told the list its proxy carried.
func TestPluginModelListThroughAccountProxy(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	// the vendor, on this computer, answers a list asked of it direct with
	// nothing, so it names no model. A name that resolves nowhere, as
	// models.invalid was, still asks DNS, and a TUN proxy's fake-IP DNS
	// (Clash's 198.18/15) answers it and holds the connection: the lists
	// asked before the accounts have proxies went out to the network.
	// 0.0.0.0 is neither loopback nor on the LAN to host.js (loopback,
	// lanIP), so an account's proxy is used for it, and a direct dial of it
	// stays on this computer.
	vendorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer vendorSrv.Close()
	_, port, _ := net.SplitHostPort(vendorSrv.Listener.Addr().String())
	vendor := "0.0.0.0:" + port
	// each proxy answers the vendor's list with a model of its own
	proxyOf := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Host != vendor || r.URL.Path != "/list" {
				http.Error(w, "not the list: "+r.URL.String(), 400)
				return
			}
			fmt.Fprint(w, name)
		}))
	}
	pa, pb := proxyOf("via-a"), proxyOf("via-b")
	defer pa.Close()
	defer pb.Close()
	t.Setenv("FAKE_BASE", "http://"+vendor+"/v1")
	t.Setenv("FAKE_MODELS", "http://"+vendor+"/list")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{"a", "b"} {
		st, err := provider.StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("fakeco")
	if err != nil {
		t.Fatal(err)
	}
	p.AccountProxies = map[string]string{"a@fake": pa.URL, "b@fake": pb.URL}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	ps, err := plugin.Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, pp := range ps {
		if pp.ID == "fakeco" {
			for _, a := range pp.Accounts {
				got[a.AccountID] = a.Models
			}
		}
	}
	for who, want := range map[string]string{"a@fake": "fake-via-a", "b@fake": "fake-via-b"} {
		if !slices.Contains(got[who], want) {
			t.Fatalf("%s's models %v, want %s among them (all: %v)", who, got[who], want, got)
		}
	}
	if slices.Contains(got["a@fake"], "fake-via-b") || slices.Contains(got["b@fake"], "fake-via-a") {
		t.Fatalf("an account's list came through the other's proxy: %v", got)
	}
}
