package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// socksTo is a SOCKS5 proxy that carries every connection to addr,
// whatever host it is asked for, counting them; with no addr it answers
// every CONNECT with "connection refused", so a proxy that won't carry the
// request fails at once.
func socksTo(t *testing.T, addr string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var h [2]byte
				io.ReadFull(c, h[:])
				io.CopyN(io.Discard, c, int64(h[1]))
				c.Write([]byte{5, 0})
				var r [4]byte
				io.ReadFull(c, r[:])
				switch r[3] {
				case 1:
					io.CopyN(io.Discard, c, 4+2)
				case 3:
					var l [1]byte
					io.ReadFull(c, l[:])
					io.CopyN(io.Discard, c, int64(l[0])+2)
				}
				if addr == "" {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				up, err := net.Dial("tcp", addr)
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				n.Add(1)
				c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}(c)
		}
	}()
	return "socks5://" + ln.Addr().String(), &n
}

func closedPort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

// A plugin's request goes through a SOCKS5 proxy, as a built-in's does,
// though Bun can't use one itself; and a proxy that won't carry the
// request, SOCKS or HTTP, is said as Go's transport says it ("proxyconnect
// …"), so the gateway asks the next account and rests none, as for a
// built-in's.
func TestPluginSOCKSProxy(t *testing.T) {
	sandbox(t)
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "vendor "+r.Host)
	}))
	defer vendor.Close()
	socks, carried := socksTo(t, strings.TrimPrefix(vendor.URL, "http://"))
	t.Setenv("FAKE_BASE", "http://vendor.invalid/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	fetch := func(proxy, url string) (int, string, error) {
		res, err := Fetch(netproxy.With(ctx, proxy), FetchRequest{Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
			URL: url, Method: "POST", Body: []byte(`{}`)})
		if err != nil {
			return 0, "", err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return res.StatusCode, string(b), err
	}
	if _, b, err := fetch(socks, "http://vendor.invalid/v1/chat/completions"); err != nil || b != "vendor vendor.invalid" || carried.Load() != 1 {
		t.Fatalf("through the SOCKS proxy: %q, %v; it carried %d", b, err, carried.Load())
	}
	// A SOCKS proxy that answers CONNECT with "connection refused", rather
	// than a just-freed port whose answer races the fetch. Its refusal is
	// there when it is asked.
	deadSOCKS, _ := socksTo(t, "")
	// Bound before deadHTTP frees a port: a listener started later can be
	// handed that port and answer the dead HTTP proxy's requests.
	if netproxy.ForBun(deadSOCKS) == deadSOCKS {
		t.Fatal("the SOCKS proxy has no bridge")
	}
	deadHTTP := "http://" + closedPort(t)
	for _, x := range []struct{ proxy, url, says string }{
		{deadHTTP, "http://vendor.invalid/v1/chat/completions", "proxyconnect tcp: dial tcp 127.0.0.1:"},
		{deadHTTP, "https://vendor.invalid/v1/chat/completions", "proxyconnect tcp: dial tcp 127.0.0.1:"},
	} {
		if _, b, err := fetch(x.proxy, x.url); err == nil || !strings.Contains(err.Error(), x.says) {
			t.Fatalf("%s through %s, which is down: %q, %v; want it to say %q", x.url, x.proxy, b, err, x.says)
		}
	}
	// The bridge's 502 is a response for HTTP, while Bun 1.4.3 and later
	// report a failed HTTPS CONNECT as a transport error. Either form is
	// the proxy's failure the gateway takes, so it asks the next account
	// and rests none.
	for _, url := range []string{"http://vendor.invalid/v1/chat/completions", "https://vendor.invalid/v1/chat/completions"} {
		code, b, err := fetch(deadSOCKS, url)
		if err != nil {
			if !strings.HasPrefix(err.Error(), "proxyconnect tcp: 502 Bad Gateway") {
				t.Fatalf("%s through a SOCKS proxy that refuses: %d %q, %v", url, code, b, err)
			}
			continue
		}
		if code != 502 || !strings.HasPrefix(b, "socks connect tcp 127.0.0.1:") {
			t.Fatalf("%s through a SOCKS proxy that refuses: %d %q, %v", url, code, b, err)
		}
	}
}
