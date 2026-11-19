package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// pluginArea is the admin world with the fake plugin (fakeco) installed
// and offered to areas as a reviewed plugin is: its "API key" method as
// api-key, its "Browser" one as paste-code ($FAKE_LOOPBACK: paste-url).
// It gives an area its admin key with fakeco opened to it.
func pluginArea(t *testing.T, browserWay string) (do adminDo, console, id, key, spec string) {
	t.Helper()
	bun := testenv.Bun(t)
	do, console, _ = adminWorld(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	spec, _ = filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	offerPlugin(t, areaPlugin{spec, []areaPluginMethod{{"api", "API key", wayAPIKey}, {"oauth", "Browser", browserWay}}})
	id, key = makeArea(t, do, console, "A")
	openSignIns(t, do, console, id, "fakeco")
	return do, console, id, key, spec
}

// offerPlugin has fakeco offered to areas as ap says, under the lock
// sign-ins read it by, until the test ends.
func offerPlugin(t *testing.T, ap areaPlugin) {
	t.Helper()
	areaPluginsMu.Lock()
	_, had := areaPlugins["fakeco"]
	areaPlugins["fakeco"] = ap
	areaPluginsMu.Unlock()
	if !had {
		t.Cleanup(func() {
			areaPluginsMu.Lock()
			delete(areaPlugins, "fakeco")
			areaPluginsMu.Unlock()
		})
	}
}

// logsOf has the log written to a buffer for the test, read by the func.
func logsOf(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var b strings.Builder
	was := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return b.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(was) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return b.String()
	}
}

// waitSignIn is the area's sign-in id once it no longer waits.
func waitSignIn(t *testing.T, do adminDo, key, id string) adminSignInState {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		c, body := do("GET", AdminPath+"/signin/"+id, lanPeer, key, "")
		var st adminSignInState
		json.Unmarshal([]byte(body), &st)
		if c != http.StatusOK || st.State != "waiting" {
			return st
		}
	}
	t.Fatal("the sign-in is still waiting")
	return adminSignInState{}
}

// An area's options list a plugin subscription opened to it, by the
// methods reviewed for areas, and the console's catalog lists it to open;
// one not opened is not listed, and its routes are 404, as are another
// area's. The plugin turned off is 409 plugin_unavailable; a method not
// offered is 400, and a way that no longer matches its index 409.
func TestAdminPluginSignInOffered(t *testing.T) {
	do, console, _, key, spec := pluginArea(t, wayPasteCode)
	c, body := do("GET", AdminPath+"/options", lanPeer, key, "")
	var opts struct{ SignIns []adminSignInWay }
	json.Unmarshal([]byte(body), &opts)
	if c != http.StatusOK || len(opts.SignIns) != 1 {
		t.Fatal("options:", c, body)
	}
	si := opts.SignIns[0]
	if si.Agent != "fakeco" || !si.Plugin || len(si.Methods) != 2 || strings.Join(si.Ways, ",") != "api-key,paste-code" ||
		si.Methods[0].Method != 0 || si.Methods[0].Way != wayAPIKey || si.Methods[0].Label != "API key" || si.Methods[0].Asks ||
		si.Methods[1].Method != 1 || si.Methods[1].Way != wayPasteCode || !si.Methods[1].Asks {
		t.Fatal("fakeco offered:", body)
	}
	if c, body := do("GET", AdminPath+"/catalog", lanPeer, console, ""); c != http.StatusOK || !strings.Contains(body, `"agent":"fakeco"`) {
		t.Fatal("catalog:", c, body)
	}

	_, other := makeArea(t, do, console, "B")
	for _, path := range []string{"/signin/plugin", "/signin/plugin/prompt"} {
		if c, body := do("POST", AdminPath+path, lanPeer, other, `{"agent":"fakeco","method":0,"way":"api-key","key":"k-b"}`); c != http.StatusNotFound {
			t.Fatal(path, "for an area not given fakeco:", c, body)
		}
	}
	if c, body := do("POST", AdminPath+"/signin", lanPeer, key, `{"agent":"fakeco"}`); c != http.StatusBadRequest {
		t.Fatal("a plugin's subscription at the built-ins' route:", c, body)
	}
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":0,"way":"paste-code"}`); c != http.StatusConflict || !strings.Contains(body, "reload options") {
		t.Fatal("a way that isn't the index's now:", c, body)
	}
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":7,"way":"device"}`); c != http.StatusBadRequest {
		t.Fatal("a method not offered:", c, body)
	}
	offerPlugin(t, areaPlugin{spec, []areaPluginMethod{{"api", "API key", wayAPIKey}}})
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-code"}`); c != http.StatusBadRequest {
		t.Fatal("a method an area isn't offered:", c, body)
	}
	offerPlugin(t, areaPlugin{"@someone/else", []areaPluginMethod{{"api", "API key", wayAPIKey}}})
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":0,"way":"api-key","key":"k-a"}`); c != http.StatusConflict || !strings.Contains(body, adminPluginUnavailable) {
		t.Fatal("another package serving fakeco:", c, body)
	}
	offerPlugin(t, areaPlugin{spec, []areaPluginMethod{{"api", "API key", wayAPIKey}}})
	if err := plugin.SetOff(spec, true); err != nil {
		t.Fatal(err)
	}
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":0,"way":"api-key","key":"k-a"}`); c != http.StatusConflict || !strings.Contains(body, adminPluginUnavailable) {
		t.Fatal("the plugin turned off:", c, body)
	}
	if c, body := do("GET", AdminPath+"/options", lanPeer, key, ""); !strings.Contains(body, `"signIns":[]`) {
		t.Fatal("options with the plugin off:", c, body)
	}
}

// A key signs the area in to a new account at once, which is the area's,
// with the proxy picked; the same key again is a conflict that leaves the
// account as it was, and so is one the owner has. A key takes no opened.
func TestAdminPluginKey(t *testing.T) {
	do, _, id, key, _ := pluginArea(t, wayPasteCode)
	ctx := context.Background()
	if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, "k-owner", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":0,"way":"api-key","key":"k-area","proxy":{"direct":true}}`)
	var acc adminAccount
	json.Unmarshal([]byte(body), &acc)
	if c != http.StatusCreated || acc.Provider != "fakeco" || acc.Proxy == nil || !acc.Proxy.Direct || strings.Contains(body, "k-area") {
		t.Fatal("a new key:", c, body)
	}
	if a, err := area.Get(id); err != nil || !ownsRef(a, acc.ID) {
		t.Fatalf("the key's account %s isn't the area's: %+v %v", acc.ID, a, err)
	}
	for _, k := range []string{"k-area", "k-owner"} {
		if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":0,"way":"api-key","key":"`+k+`"}`); c != http.StatusConflict || strings.Contains(body, k) {
			t.Fatal(k, "again:", c, body)
		}
	}
	if a, _ := area.Get(id); len(a.Accounts) != 1 {
		t.Fatalf("the area has %d accounts", len(a.Accounts))
	}
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":0,"way":"api-key","key":"k-3","opened":"2026-11-01"}`); c != http.StatusBadRequest {
		t.Fatal("a key with opened:", c, body)
	}
}

// An area answers a plugin's questions one at a time: a list's answer
// must be one of it, the plugin's check refuses an answer without saying
// it back, and an answer typed unseen is not sent back. Then the code its
// page shows signs in to a new account, the area's; the same account
// again is a conflict.
func TestAdminPluginSignInByCode(t *testing.T) {
	do, _, id, key, _ := pluginArea(t, wayPasteCode)
	prompt := func(body string) (p struct {
		Prompt *plugin.Prompt
		Inputs map[string]string
		Hidden []string
		Error  string
	}) {
		t.Helper()
		c, b := do("POST", AdminPath+"/signin/plugin/prompt", lanPeer, key, body)
		if c != http.StatusOK {
			t.Fatal("prompt:", c, b)
		}
		json.Unmarshal([]byte(b), &p)
		return p
	}
	if p := prompt(`{"agent":"fakeco","method":1}`); p.Prompt == nil || p.Prompt.Key != "where" {
		t.Fatalf("first question: %+v", p)
	}
	if p := prompt(`{"agent":"fakeco","method":1,"value":"moon"}`); p.Error == "" {
		t.Fatalf("an answer not in the list: %+v", p)
	}
	if p := prompt(`{"agent":"fakeco","method":1,"inputs":{"where":"work"},"value":"echo-secret"}`); p.Error != pluginWithheld {
		t.Fatalf("the plugin's refusal that says the answer back: %+v", p)
	}
	if p := prompt(`{"agent":"fakeco","method":1,"inputs":{"where":"vault"}}`); p.Prompt == nil || p.Prompt.Key != "pin" || p.Prompt.Type != "password" {
		t.Fatalf("where vault: %+v", p)
	}
	if p := prompt(`{"agent":"fakeco","method":1,"inputs":{"where":"vault"},"value":"4321"}`); p.Prompt != nil || p.Inputs["pin"] != "" || p.Inputs["where"] != "vault" || len(p.Hidden) != 1 || p.Hidden[0] != "pin" {
		t.Fatalf("a PIN typed unseen: %+v", p)
	}
	p := prompt(`{"agent":"fakeco","method":1,"inputs":{"where":"work"},"value":"blue"}`)
	if p.Prompt != nil || p.Inputs["team"] != "blue" {
		t.Fatalf("all asked: %+v", p)
	}

	start := func() adminSignInState {
		t.Helper()
		c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-code","inputs":{"where":"work","team":"blue"}}`)
		var st adminSignInState
		json.Unmarshal([]byte(body), &st)
		if c != http.StatusOK || st.State != "waiting" || st.Paste != "code" || st.Instructions != "Paste the code" {
			t.Fatal("start:", c, body)
		}
		t.Cleanup(func() { provider.CancelSignIn(st.ID) })
		return st
	}
	st := start()
	if c, body := do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, `{"code":"good"}`); c != http.StatusNoContent {
		t.Fatal("the code:", c, body)
	}
	done := waitSignIn(t, do, key, st.ID)
	if done.State != "done" || done.Account == nil || done.Account.Name != "blue@fake" {
		t.Fatalf("signed in: %+v", done)
	}
	if a, err := area.Get(id); err != nil || !ownsRef(a, done.Account.ID) {
		t.Fatalf("the account isn't the area's: %+v %v", a, err)
	}
	st = start()
	do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, `{"code":"good"}`)
	if again := waitSignIn(t, do, key, st.ID); again.State != "conflict" || again.Account != nil {
		t.Fatalf("the same account again: %+v", again)
	}
	if a, _ := area.Get(id); len(a.Accounts) != 1 {
		t.Fatalf("the area has %d accounts", len(a.Accounts))
	}
}

// A plugin's sign-in that fails for the plugin's own reason tells the
// area only that it failed (T-229): the plugin's words are in magpie's
// log, and nowhere in what the area reads.
func TestAdminPluginFailureSaidInTheLogAlone(t *testing.T) {
	do, _, _, key, _ := pluginArea(t, wayPasteCode)
	logs := logsOf(t)
	c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-code","inputs":{"where":"work","team":"red"}}`)
	var st adminSignInState
	json.Unmarshal([]byte(body), &st)
	if c != http.StatusOK {
		t.Fatal("start:", c, body)
	}
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	c, body = do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, `{"code":"expired"}`)
	if c != http.StatusBadRequest || strings.Contains(body, "expired") || !strings.Contains(body, provider.ErrPluginFailed.Error()) {
		t.Fatal("the paste's answer:", c, body)
	}
	ended := waitSignIn(t, do, key, st.ID)
	if ended.State != "failed" || ended.Message != provider.ErrPluginFailed.Error() {
		t.Fatalf("how it ended: %+v", ended)
	}
	if !strings.Contains(logs(), "the sign-in page expired") {
		t.Fatalf("the plugin's reason isn't in the log:\n%s", logs())
	}

	// a page opened on this computer is no way an area can finish
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-code","inputs":{"where":"work","team":"cli-opened"}}`); c != http.StatusBadRequest || strings.Contains(body, `"id"`) {
		t.Fatal("a page opened here:", c, body)
	}
}

// A plugin's sign-in that comes back to its port here is offered as
// paste-url, and the address pasted finishes it; one listed as another
// way is 409 as it starts, and adds nothing.
func TestAdminPluginSignInByAddress(t *testing.T) {
	t.Setenv("FAKE_LOOPBACK", "1")
	do, _, id, key, spec := pluginArea(t, wayPasteURL)
	c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-url","inputs":{"where":"work","team":"green"}}`)
	var st adminSignInState
	json.Unmarshal([]byte(body), &st)
	if c != http.StatusOK || st.Paste != "url" {
		t.Fatal("start:", c, body)
	}
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	page, _ := url.Parse(st.URL)
	b, _ := json.Marshal(map[string]string{"url": page.Query().Get("redirect_uri") + "?state=" + page.Query().Get("state") + "&code=good"})
	if c, body := do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, string(b)); c != http.StatusNoContent {
		t.Fatal("the address:", c, body)
	}
	if done := waitSignIn(t, do, key, st.ID); done.State != "done" || done.Account == nil || done.Account.Name != "green@fake" {
		t.Fatalf("signed in: %+v", done)
	}

	offerPlugin(t, areaPlugin{spec, []areaPluginMethod{{"oauth", "Browser", wayPasteCode}}})
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-code","inputs":{"where":"work","team":"violet"}}`); c != http.StatusConflict || !strings.Contains(body, "reload options") {
		t.Fatal("a method that comes out another way:", c, body)
	}
	if a, _ := area.Get(id); len(a.Accounts) != 1 {
		t.Fatalf("the area has %d accounts", len(a.Accounts))
	}
}

// A code sign-in an area makes to an account the owner signed in to is a
// conflict, and the owner's record keeps its tokens.
func TestAdminPluginSignInLeavesTheOwners(t *testing.T) {
	do, _, id, key, _ := pluginArea(t, wayPasteCode)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a, err := plugin.Authorize(ctx, "fakeco", 1, map[string]string{"where": "work", "team": "owner"}, plugin.NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Finish(ctx, a.Session, "good"); err != nil {
		t.Fatal(err)
	}
	refreshOf := func() string {
		for _, pp := range plugin.Cached() {
			for _, acc := range pp.Accounts {
				if pp.ID == "fakeco" && acc.AccountID == "owner@fake" {
					return acc.Key
				}
			}
		}
		return ""
	}
	ownerKey := refreshOf()
	c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-code","inputs":{"where":"work","team":"owner"}}`)
	var st adminSignInState
	json.Unmarshal([]byte(body), &st)
	if c != http.StatusOK {
		t.Fatal("start:", c, body)
	}
	t.Cleanup(func() { provider.CancelSignIn(st.ID) })
	do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, `{"code":"good"}`)
	if ended := waitSignIn(t, do, key, st.ID); ended.State != "conflict" || ended.Account != nil || strings.Contains(ended.Message, "owner@fake") {
		t.Fatalf("a sign-in to the owner's account: %+v", ended)
	}
	if a, _ := area.Get(id); len(a.Accounts) != 0 {
		t.Fatalf("the area has %d accounts", len(a.Accounts))
	}
	b, err := os.ReadFile(plugin.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]map[string]any
	json.Unmarshal(b, &saved)
	if len(saved) != 1 || ownerKey == "" || saved[ownerKey]["refresh"] != "r-owner" {
		t.Fatalf("plugin-auth.json after: %s", b)
	}
}

// A sign-in starts only with the answers the method asks, each one it
// takes: one left out, one it never asks, or one off its list is 400,
// and nothing starts.
func TestAdminPluginStartChecksAnswers(t *testing.T) {
	do, _, _, key, _ := pluginArea(t, wayPasteCode)
	for _, in := range []string{`{"where":"work"}`, `{"where":"work","team":"blue","extra":"x"}`, `{"where":"moon"}`, `{"where":"work","team":"echo-1234"}`} {
		c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-code","inputs":`+in+`}`)
		if c != http.StatusBadRequest || strings.Contains(body, `"id"`) || strings.Contains(body, "echo-1234") {
			t.Fatal(in, c, body)
		}
	}
}

// Command Code's page posts its key, so its sign-in is finished by a key
// pasted, which goes through the key way to a new account only: a key
// the owner has is a conflict that leaves it as it was, and each such
// conflict is counted to the area, which is 429 once it has had 20.
func TestAdminPluginSignInByPastedKey(t *testing.T) {
	t.Setenv("FAKE_LOOPBACK", "1")
	t.Cleanup(provider.PluginPostsCallbackForTest("fakeco", "https://fake.invalid/keys"))
	do, _, id, key, _ := pluginArea(t, wayPasteKey)
	if _, err := plugin.APIKey(context.Background(), "fakeco", 0, nil, "k-owner", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	start := func() (int, adminSignInState) {
		t.Helper()
		c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-key","inputs":{"where":"work","team":"blue"}}`)
		var st adminSignInState
		json.Unmarshal([]byte(body), &st)
		if c == http.StatusOK {
			t.Cleanup(func() { provider.CancelSignIn(st.ID) })
			if st.Paste != "key" || st.KeysURL != "https://fake.invalid/keys" {
				t.Fatal("start:", body)
			}
		}
		return c, st
	}
	c, st := start()
	if c != http.StatusOK {
		t.Fatal("start:", c)
	}
	if c, body := do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, `{"key":"k-area"}`); c != http.StatusNoContent {
		t.Fatal("the key:", c, body)
	}
	if done := waitSignIn(t, do, key, st.ID); done.State != "done" || done.Account == nil {
		t.Fatalf("signed in: %+v", done)
	}
	// the owner's record is never written: not even the same bytes again
	before, err := os.Stat(plugin.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // a write after this has a later time
	conflicts := 0
	for range 25 {
		c, st := start()
		if c == http.StatusTooManyRequests {
			break
		}
		if c != http.StatusOK {
			t.Fatal("start:", c)
		}
		do("POST", AdminPath+"/signin/"+st.ID+"/callback", lanPeer, key, `{"key":"k-owner"}`)
		if ended := waitSignIn(t, do, key, st.ID); ended.State != "conflict" {
			t.Fatalf("the owner's key: %+v", ended)
		}
		conflicts++
	}
	if after, err := os.Stat(plugin.AuthPath()); err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("plugin-auth.json was written by a conflict: %v → %v (%v)", before.ModTime(), after.ModTime(), err)
	}
	if conflicts != conflictMost {
		t.Fatalf("held after %d conflicts, not %d", conflicts, conflictMost)
	}
	if a, _ := area.Get(id); len(a.Accounts) != 1 {
		t.Fatalf("the area has %d accounts", len(a.Accounts))
	}
	p, _ := provider.Find("fakeco")
	if n := len(p.AccountIDs()); n != 2 {
		t.Fatalf("fakeco has %d accounts, not the owner's and the area's", n)
	}
}

// An area's plugin sign-in routes run one at a time (T-231): while a
// start waits in the plugin host, the area's prompt is 429
// too_many_plugin_calls and another area's isn't. Each area asks the host
// pluginCallsMost times in the window, the start route held with the
// prompt past it, and the console consolePluginCallsMost across the areas
// it acts for, an area's own calls not among them.
func TestAdminPluginCallsHeld(t *testing.T) {
	do, console, _, key, _ := pluginArea(t, wayPasteCode)
	const ask = `{"agent":"fakeco","method":1}`
	area := func(note string) (string, string) {
		t.Helper()
		id, key := makeArea(t, do, console, note)
		openSignIns(t, do, console, id, "fakeco")
		return id, key
	}
	held := func(c int, body string) bool {
		return c == http.StatusTooManyRequests && strings.Contains(body, adminTooManyPluginCalls)
	}

	was := startAreaPluginSignIn
	t.Cleanup(func() { startAreaPluginSignIn = was })
	in, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	startAreaPluginSignIn = func(string, int, map[string]string, provider.SignInOptions) (provider.SignInState, error) {
		close(in)
		<-release
		return provider.SignInState{}, errors.New("stopped by the test")
	}
	started := make(chan int, 1)
	go func() {
		c, _ := do("POST", AdminPath+"/signin/plugin", lanPeer, key, `{"agent":"fakeco","method":1,"way":"paste-code","inputs":{"where":"home"}}`)
		started <- c
	}()
	<-in
	if c, body := do("POST", AdminPath+"/signin/plugin/prompt", lanPeer, key, ask); !held(c, body) {
		t.Fatal("the area's prompt while its start runs:", c, body)
	}
	_, other := area("B")
	if c, body := do("POST", AdminPath+"/signin/plugin/prompt", lanPeer, other, ask); c != http.StatusOK {
		t.Fatal("another area's prompt meanwhile:", c, body)
	}
	once.Do(func() { close(release) })
	if c := <-started; c != http.StatusBadRequest {
		t.Fatal("the start held:", c)
	}
	startAreaPluginSignIn = was
	if c, body := do("POST", AdminPath+"/signin/plugin/prompt", lanPeer, key, ask); c != http.StatusOK {
		t.Fatal("the area's prompt once its start ended:", c, body)
	}

	_, spent := area("C")
	for i := range pluginCallsMost {
		if c, body := do("POST", AdminPath+"/signin/plugin/prompt", lanPeer, spent, ask); c != http.StatusOK {
			t.Fatal("prompt", i, c, body)
		}
	}
	if c, body := do("POST", AdminPath+"/signin/plugin/prompt", lanPeer, spent, ask); !held(c, body) {
		t.Fatal("a prompt past the area's line:", c, body)
	}
	if c, body := do("POST", AdminPath+"/signin/plugin", lanPeer, spent, `{"agent":"fakeco","method":0,"way":"api-key","key":"k-c"}`); !held(c, body) {
		t.Fatal("a start past the area's line:", c, body)
	}
	if c, body := do("POST", AdminPath+"/signin/plugin/prompt", lanPeer, key, ask); c != http.StatusOK {
		t.Fatal("another area's prompt once one is held:", c, body)
	}

	// the console's: the areas' own calls above spent none of it
	d, _ := area("D")
	e, _ := area("E")
	for _, id := range []string{d, e} {
		for i := range consolePluginCallsMost / 2 {
			if c, body := do("POST", AdminPath+"/areas/"+id+"/signin/plugin/prompt", lanPeer, console, ask); c != http.StatusOK {
				t.Fatal("the console's prompt", i, "for", id, c, body)
			}
		}
	}
	f, fKey := area("F")
	if c, body := do("POST", AdminPath+"/areas/"+f+"/signin/plugin/prompt", lanPeer, console, ask); !held(c, body) || !strings.Contains(body, "the console") {
		t.Fatal("the console's prompt past its line:", c, body)
	}
	if c, body := do("POST", AdminPath+"/signin/plugin/prompt", lanPeer, fKey, ask); c != http.StatusOK {
		t.Fatal("the area's own prompt once the console is held:", c, body)
	}
}
