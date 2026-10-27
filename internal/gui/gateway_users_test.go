package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

type gatewayUsersOut struct {
	Total    int               `json:"total"`
	Matched  int               `json:"matched"`
	Accounts []userAccountJSON `json:"accounts"`
	Users    []userJSON        `json:"users"`
	Error    string            `json:"error"`
	Mode     *string           `json:"mode"`
	Tiers    *gateway.Tiers    `json:"tiers"`
}

// servingAt puts what answers at the gateway's address in h's hands, so
// a test never reaches a magpie running on this computer; it counts the
// requests made to it.
func servingAt(t *testing.T, h http.HandlerFunc) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(s.URL, "http://"))
	return &n
}

// noGateway is nothing serving the gateway: nothing listens at its
// address (port 1 takes root to listen on).
func noGateway(t *testing.T) { t.Setenv("MAGPIE_ADDR", "127.0.0.1:1") }

func gatewayUsers(t *testing.T, q string) (int, gatewayUsersOut) {
	t.Helper()
	mux := http.NewServeMux()
	gatewayUserRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/gateway/users"+q, nil))
	var out gatewayUsersOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return w.Code, out
}

func writeUsers(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(provider.Path()), "user-accounts.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The Gateway page's User bindings: every binding of user-accounts.json,
// a key's and those without one, each account counted with its users, the
// key named, narrowed by words; no file is nobody, one that doesn't read
// an error, never nobody.
func TestGatewayUsersRoute(t *testing.T) {
	sandboxHome(t)
	noGateway(t)
	if code, out := gatewayUsers(t, ""); code != 200 || out.Total != 0 || len(out.Users) != 0 {
		t.Fatalf("no file: %d %+v", code, out)
	}
	if _, err := access.Update("add-key", access.Change{Name: "Tenant A"}); err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	var keyID string
	for _, k := range keys {
		if k.Name == "Tenant A" {
			keyID = k.ID
		}
	}
	at := time.Date(2026, 10, 22, 4, 21, 33, 0, time.UTC).Format(time.RFC3339)
	writeUsers(t, fmt.Sprintf(`{
  "users": {"antigravity": {"local-1": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q}}},
  "keys": {%q: {"antigravity": {
    "1": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q},
    "2": {"who": "antigravity@b@x.com", "account": "b@x.com", "at": %q}
  }}, "gone-key": {"codex": {"9": {"who": "codex@c@x.com", "account": "c@x.com", "at": %q}}}}
}`, at, keyID, at, at, at))
	code, out := gatewayUsers(t, "")
	if code != 200 || out.Total != 4 || out.Matched != 4 || len(out.Users) != 4 {
		t.Fatalf("%d %+v", code, out)
	}
	counts := map[string]int{}
	for _, a := range out.Accounts {
		counts[a.Provider+" "+a.Account] = a.Users
	}
	if counts["antigravity a@x.com"] != 2 || counts["antigravity b@x.com"] != 1 || counts["codex c@x.com"] != 1 || len(counts) != 3 {
		t.Fatalf("accounts %+v", out.Accounts)
	}
	named := map[string]string{}
	for _, u := range out.Users {
		named[u.User] = u.KeyName
		if u.At.IsZero() || u.Account == "" {
			t.Fatalf("user %+v", u)
		}
	}
	if named["1"] != "Tenant A" || named["local-1"] != "" || named["9"] != "" {
		t.Fatalf("key names %v", named)
	}
	// narrowed: every word must match the user, account, provider or key
	if _, out := gatewayUsers(t, "?q=b%40x"); out.Matched != 1 || len(out.Users) != 1 || out.Users[0].User != "2" || out.Total != 4 || out.Accounts != nil {
		t.Fatalf("q=b@x: %+v", out)
	}
	if _, out := gatewayUsers(t, "?q=tenant+a%40x"); out.Matched != 1 || out.Users[0].User != "1" {
		t.Fatalf("q=tenant a@x: %+v", out)
	}

	// many: a page of them, and how many match
	var b strings.Builder
	b.WriteString(`{"users": {"antigravity": {`)
	for i := range usersShown + 50 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"u%d": {"who": "antigravity@a@x.com", "account": "a@x.com", "at": %q}`, i, at)
	}
	b.WriteString(`}}}`)
	writeUsers(t, b.String())
	if _, out := gatewayUsers(t, ""); out.Total != usersShown+50 || out.Matched != usersShown+50 || len(out.Users) != usersShown {
		t.Fatalf("many: total %d matched %d shown %d", out.Total, out.Matched, len(out.Users))
	}

	writeUsers(t, `{"users": `)
	if code, out := gatewayUsers(t, ""); code == 200 || !strings.Contains(out.Error, "user-accounts.json") {
		t.Fatalf("unreadable: %d %+v", code, out)
	}
}

// An account is said gone only when its provider's accounts and keys are
// known and it isn't among them: a key removed is, the same key written
// in another case isn't, and so is a provider removed whole. A gateway key
// list that can't be read names no key removed. A search leaves the
// accounts out.
func TestGatewayUsersGone(t *testing.T) {
	sandboxHome(t)
	noGateway(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-1", KeyName: "First", Keys: []provider.KeyAccount{{Key: "sk-2", Name: "Second"}}}); err != nil {
		t.Fatal(err)
	}
	on := "relay#" + provider.KeyID("sk-1")
	at := time.Now().UTC().Format(time.RFC3339)
	writeUsers(t, fmt.Sprintf(`{"users": {
  "relay": {
    "on": {"who": %q, "account": "First", "at": %q},
    "cased": {"who": %q, "account": "First", "at": %q},
    "gone": {"who": "relay#0000000000", "account": "Old", "at": %q}
  },
  "removed": {"r": {"who": "removed@a@x.com", "account": "a@x.com", "at": %q}}
}, "keys": {"no-such-key": {"relay": {"k": {"who": %q, "account": "First", "at": %q}}}}}`,
		on, at, strings.ToUpper(on), at, at, at, on, at))
	_, out := gatewayUsers(t, "")
	gone := map[string][2]bool{}
	for _, a := range out.Accounts {
		gone[a.Provider+" "+a.Account] = [2]bool{a.Gone, a.ProviderGone}
	}
	want := map[string][2]bool{"relay First": {false, false}, "relay Old": {true, false}, "removed a@x.com": {true, true}}
	for k, w := range want {
		if gone[k] != w {
			t.Errorf("%s: gone, provider gone = %v, want %v (%+v)", k, gone[k], w, out.Accounts)
		}
	}
	if counts := len(out.Accounts); counts != 3 {
		t.Errorf("the same key in another case counted apart: %+v", out.Accounts)
	}
	for _, u := range out.Users {
		if u.User == "k" && (!u.KeyGone || u.KeyName != "") {
			t.Errorf("a key not in the list read isn't said removed: %+v", u)
		}
	}
	// with the gateway keys unreadable, no key is said removed
	os.MkdirAll(access.Path(), 0o700) // a folder where the file is: it can't be read
	_, out = gatewayUsers(t, "")
	for _, u := range out.Users {
		if u.KeyGone {
			t.Errorf("an unreadable key list said %s's key removed", u.User)
		}
	}
	if code, out := gatewayUsers(t, "?q=cased"); code != 200 || out.Accounts != nil || out.Matched != 1 {
		t.Fatalf("a search: %d %+v", code, out)
	}
}

// fakeServing answers at the gateway's address as a magpie that binds as
// mode says, holding to tiers (nil: it says none), and tells rests at
// RestsPath when rests isn't nil (an older magpie doesn't).
func fakeServing(t *testing.T, mode string, tiers *gateway.Tiers, rests map[string]gateway.Rest) *atomic.Int32 {
	t.Helper()
	return servingAt(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			info := map[string]any{"name": "magpie", "version": "test", "bind": mode}
			if tiers != nil {
				info["tiers"] = tiers
			}
			writeJSON(w, info)
		case r.URL.Path == gateway.RestsPath && rests != nil:
			writeJSON(w, map[string]any{"object": "rests", "data": rests})
		default:
			http.NotFound(w, r)
		}
	})
}

// A user bound by user has its first account and its spares, in order,
// each named as the accounts list names it; an account is counted once
// per user, as a first or a spare. A file written before spares (no
// "more") reads as a first alone. Each account says whether it is out as
// the magpie serving the gateway tells its rests, a model's rest with its
// model, and how that magpie binds, its mode and, by user, its tiers.
// Each user says the tier its last request named; one from before tiers
// says none, which the page shows as the default.
func TestGatewayUsersSpares(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-1", KeyName: "First", Keys: []provider.KeyAccount{{Key: "sk-2", Name: "Second"}, {Key: "sk-3", Name: "Third"}}}); err != nil {
		t.Fatal(err)
	}
	one, two, three := "relay#"+provider.KeyID("sk-1"), "relay#"+provider.KeyID("sk-2"), "relay#"+provider.KeyID("sk-3")
	at := time.Date(2026, 10, 22, 4, 21, 33, 0, time.UTC).Format(time.RFC3339)
	writeUsers(t, fmt.Sprintf(`{"users": {"relay": {
  "old": {"who": %q, "account": "First", "at": %q},
  "spared": {"who": %q, "account": "First", "at": %q, "more": [{"who": %q, "account": "Second", "at": %q}, {"who": %q, "account": "Third", "at": %q}], "tier": "pro"}
}}}`, one, at, one, at, two, at, three, at))
	tiers := &gateway.Tiers{Default: "free", Tiers: []gateway.Tier{{Name: "free", Credits: 1000, MaxAccounts: 2}, {Name: "pro", Credits: 5000, MaxAccounts: 4}}}
	until := time.Now().Add(40 * time.Minute).UTC().Truncate(time.Second)
	fakeServing(t, "user", tiers, map[string]gateway.Rest{
		one:            {Why: "quota", Until: until},
		three + "/m-1": {Why: "rate", Until: until, Model: "m-1"},
	})
	code, out := gatewayUsers(t, "")
	if code != 200 || out.Total != 2 || out.Mode == nil || *out.Mode != "user" || out.Tiers == nil || fmt.Sprint(*out.Tiers) != fmt.Sprint(*tiers) {
		t.Fatalf("%d %+v", code, out)
	}
	users := map[string]userJSON{}
	for _, u := range out.Users {
		users[u.User] = u
	}
	if u := users["old"]; u.Account != "First" || u.Seat != strings.ToLower(one) || len(u.Spares) != 0 || u.Tier != "" {
		t.Errorf("a binding from before spares: %+v", u)
	}
	u := users["spared"]
	if u.Account != "First" || u.Tier != "pro" || len(u.Spares) != 2 || u.Spares[0].Account != "Second" || u.Spares[1].Account != "Third" ||
		u.Spares[0].Seat != strings.ToLower(two) || u.Spares[1].At.IsZero() {
		t.Fatalf("its first and spares, in order: %+v", u)
	}
	accounts := map[string]userAccountJSON{}
	for _, a := range out.Accounts {
		accounts[a.Seat] = a
	}
	if len(out.Accounts) != 3 {
		t.Fatalf("accounts %+v", out.Accounts)
	}
	first, second, third := accounts[strings.ToLower(one)], accounts[strings.ToLower(two)], accounts[strings.ToLower(three)]
	if first.Users != 2 || first.Spares != 0 || second.Users != 1 || second.Spares != 1 || third.Spares != 1 {
		t.Errorf("counted: %+v", out.Accounts)
	}
	if !first.OutKnown || first.Out != "quota" || first.OutUntil == nil || !first.OutUntil.Equal(until) || first.OutModel != "" {
		t.Errorf("a resting first: %+v", first)
	}
	if !second.OutKnown || second.Out != "" || second.OutUntil != nil {
		t.Errorf("one that can answer: %+v", second)
	}
	if !third.OutKnown || third.Out != "rate" || third.OutModel != "m-1" {
		t.Errorf("one of its models resting: %+v", third)
	}

	// bound by session: said, and the bindings kept as they are
	fakeServing(t, "session", nil, map[string]gateway.Rest{})
	if _, out := gatewayUsers(t, ""); out.Mode == nil || *out.Mode != "session" || out.Tiers != nil || out.Total != 2 || len(out.Accounts) != 3 {
		t.Errorf("by session: %+v", out)
	}
}

// What rests is the serving magpie's alone: one that doesn't tell it (an
// older one) leaves every account unknown, never able to answer, and so
// does an account its provider no longer has. With no gateway running,
// nothing rests and the binding is what one started here would read. A
// search asks the serving magpie nothing, and reads no account's rests.
func TestGatewayUsersOutUnknown(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-1", KeyName: "First", Keys: []provider.KeyAccount{{Key: "sk-2", Name: "Second"}}}); err != nil {
		t.Fatal(err)
	}
	one, two := "relay#"+provider.KeyID("sk-1"), "relay#"+provider.KeyID("sk-2")
	at := time.Now().UTC().Format(time.RFC3339)
	writeUsers(t, fmt.Sprintf(`{"users": {"relay": {
  "u": {"who": %q, "account": "First", "at": %q, "more": [{"who": %q, "account": "Second", "at": %q}]},
  "v": {"who": "relay#0000000000", "account": "Old", "at": %q}
}}}`, one, at, two, at, at))
	asked := fakeServing(t, "user", nil, nil)
	_, out := gatewayUsers(t, "")
	if out.Mode == nil || *out.Mode != "user" || len(out.Accounts) != 3 {
		t.Fatalf("%+v", out)
	}
	for _, a := range out.Accounts {
		if a.OutKnown || a.Out != "" {
			t.Errorf("%s said out or not, with no rests told: %+v", a.Account, a)
		}
	}
	before := asked.Load()
	if _, out := gatewayUsers(t, "?q=second"); out.Matched != 1 || out.Accounts != nil || out.Mode != nil || out.Tiers != nil || len(out.Users[0].Spares) != 1 {
		t.Errorf("a search: %+v", out)
	}
	if n := asked.Load() - before; n != 0 {
		t.Errorf("a search asked the serving magpie %d times", n)
	}

	// no gateway running: nothing rests, and the binding is this one's
	noGateway(t)
	t.Setenv("MAGPIE_BIND", "")
	_, out = gatewayUsers(t, "")
	if out.Mode == nil || *out.Mode != "session" || out.Tiers != nil {
		t.Errorf("no gateway: %+v", out)
	}
	// bound by user there, the tiers it would read: none known with a
	// user-tiers.json that doesn't read, the built-in ones with none
	t.Setenv("MAGPIE_BIND", "user")
	tiers := filepath.Join(filepath.Dir(provider.Path()), "user-tiers.json")
	os.WriteFile(tiers, []byte(`{"default":`), 0o600)
	if _, out := gatewayUsers(t, ""); out.Mode == nil || *out.Mode != "user" || out.Tiers != nil {
		t.Errorf("no gateway, by user, tiers unread: %+v", out)
	}
	os.Remove(tiers)
	if _, out := gatewayUsers(t, ""); out.Tiers == nil || out.Tiers.Default != "free" || len(out.Tiers.Tiers) != 4 || out.Tiers.Tiers[2] != (gateway.Tier{Name: "pro", Credits: 5000, MaxAccounts: 4, Share: 1}) {
		t.Errorf("no gateway, by user: %+v", out.Tiers)
	}
	t.Setenv("MAGPIE_BIND", "")
	for _, a := range out.Accounts {
		if want := !a.Gone; a.OutKnown != want || a.Out != "" {
			t.Errorf("no gateway, %s: %+v", a.Account, a)
		}
	}
}

// A magpie that holds the gateway's port but doesn't answer in time (one
// starting, or busy) is not "no gateway": how it binds and what rests are
// unknown, never bound by session with every account able to answer.
func TestGatewayUsersOwnerSlow(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	writeUsers(t, fmt.Sprintf(`{"users": {"relay": {"u": {"who": %q, "account": "First", "at": %q}}}}`, "relay#"+provider.KeyID("sk-1"), time.Now().UTC().Format(time.RFC3339)))
	release := make(chan struct{})
	servingAt(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) }) // before the server closes
	t.Setenv("MAGPIE_BIND", "")
	_, out := gatewayUsers(t, "")
	if out.Mode == nil || *out.Mode != "" || len(out.Accounts) != 1 {
		t.Fatalf("a slow magpie said how it binds: %+v", out)
	}
	if a := out.Accounts[0]; a.Gone || a.OutKnown || a.Out != "" {
		t.Errorf("a slow magpie's account said known: %+v", a)
	}
}
