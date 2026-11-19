package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
)

// changeConsoleKey changes the console's key as access.Update does it
// (the window's console-rotate, -off, -remove, or the CLI's gateway-key),
// giving the new secret for a rotate.
func changeConsoleKey(t *testing.T, action string) string {
	t.Helper()
	keys, err := access.List()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(keys, func(k access.Key) bool { return k.Admin == access.AdminConsole })
	if i < 0 {
		t.Fatal("no console key")
	}
	secret, err := access.Update(action, access.Change{Key: keys[i].ID})
	if err != nil {
		t.Fatal(action, err)
	}
	return secret
}

// The console's key rotated, turned off or removed cancels the sign-ins
// its old secret started, for every area (CancelConsoleSignIns, T-261),
// as an area key's rotate cancels its own. The areas' own keys' go on,
// and the old secret starts none.
func TestAdminConsoleKeyChangedCancelsItsSignIns(t *testing.T) {
	do, console, _ := adminWorld(t)
	a, _ := makeArea(t, do, console, "A")
	b, _ := makeArea(t, do, console, "B")
	openSignIns(t, do, console, a, provider.ChatGPTAPIID)
	vendor := httptest.NewServer(http.NotFoundHandler()) // a paste-url start asks it nothing
	t.Cleanup(vendor.Close)
	t.Cleanup(provider.SIWCForTest(vendor.URL, vendor.URL+"/v1"))
	start := func(path, secret string) string {
		t.Helper()
		c, body := do("POST", path, lanPeer, secret, `{"agent":"chatgpt-api","way":"paste-url"}`)
		var st adminSignInState
		json.Unmarshal([]byte(body), &st)
		if c != http.StatusOK || st.ID == "" || st.State != "waiting" {
			t.Fatal("start:", c, body)
		}
		t.Cleanup(func() { provider.CancelSignIn(st.ID) })
		return st.ID
	}
	waiting := func(sid string) bool {
		got, ok := provider.SignInStatus(sid)
		return ok && got.State == "waiting"
	}
	ofConsole := start(AdminPath+"/areas/"+a+"/signin", console)
	// one sign-in to a subscription at a time: area B's own is its
	// record, which a cancel of it takes out
	// (a sign-in recorded after drops it: it is put again before each change)
	const ofB = "si-area-b"
	putB := func() {
		areaSignIns.Lock()
		areaSignIns.m[ofB] = areaSignIn{area: b}
		areaSignIns.Unlock()
	}
	putB()
	t.Cleanup(func() {
		areaSignIns.Lock()
		delete(areaSignIns.m, ofB)
		areaSignIns.Unlock()
	})
	recorded := func(sid string) bool {
		areaSignIns.Lock()
		defer areaSignIns.Unlock()
		_, ok := areaSignIns.m[sid]
		return ok
	}
	fresh := changeConsoleKey(t, "rotate-key")
	CancelConsoleSignIns()
	if waiting(ofConsole) {
		t.Fatal("the console's old secret's sign-in after its rotate")
	}
	if !recorded(ofB) {
		t.Fatal("the area's own key's sign-in after the console's rotate")
	}
	if c, _ := do("POST", AdminPath+"/areas/"+a+"/signin", lanPeer, console, `{"agent":"chatgpt-api","way":"paste-url"}`); c != http.StatusUnauthorized {
		t.Fatal("the console's old secret starts one:", c)
	}
	// the new secret's goes on until the key is turned off
	again := start(AdminPath+"/areas/"+a+"/signin", fresh)
	if !waiting(again) {
		t.Fatal("the new secret's sign-in")
	}
	putB()
	changeConsoleKey(t, "off-key")
	CancelConsoleSignIns()
	if waiting(again) {
		t.Fatal("the console's sign-in after its key was turned off")
	}
	if !recorded(ofB) {
		t.Fatal("the area's own key's sign-in after the console's off")
	}
}

// A console sign-in recorded after the console's key changed while it
// started is let go at once by keepAreaSignIn (T-261), that one alone:
// the cancel that came first never saw it. The new secret's is kept.
// (Each read before the next: recording one drops those magpie hasn't.)
func TestAdminConsoleSignInKeptAfterKeyChangeIsLetGo(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, _ := makeArea(t, do, console, "A")
	before, ok := access.Authenticate(console)
	if !ok {
		t.Fatal("the console's key")
	}
	after, ok := access.Authenticate(changeConsoleKey(t, "rotate-key"))
	if !ok {
		t.Fatal("the console's new secret")
	}
	a, err := area.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	kept := func(sid string) bool {
		areaSignIns.Lock()
		defer areaSignIns.Unlock()
		_, ok := areaSignIns.m[sid]
		return ok
	}
	t.Cleanup(func() {
		areaSignIns.Lock()
		defer areaSignIns.Unlock()
		for _, sid := range []string{"si-console-new", "si-console-old"} {
			delete(areaSignIns.m, sid)
		}
	})
	keepAreaSignIn(adminCall{who: after, area: a, console: true}, "si-console-new", nil)
	if !kept("si-console-new") {
		t.Fatal("the console's new secret's let go")
	}
	keepAreaSignIn(adminCall{who: before, area: a, console: true}, "si-console-old", nil)
	if kept("si-console-old") {
		t.Fatal("the console's old secret's kept")
	}
}

// A console sign-in that ends after the console's key was changed by a
// way that cancels nothing (the CLI's gateway-key) claims no account for
// the area, and the account is taken out again (T-261). The new
// secret's claims as before.
func TestAdminConsoleSignInDoneAfterKeyChangeClaimsNothing(t *testing.T) {
	do, console, _ := adminWorld(t)
	pid := subscriptionOf("codex")
	if pid == "" {
		t.Skip("no Codex subscription provider in this build")
	}
	id, _ := makeArea(t, do, console, "A")
	before, ok := access.Authenticate(console)
	if !ok {
		t.Fatal("the console's key")
	}
	after, ok := access.Authenticate(changeConsoleKey(t, "rotate-key"))
	if !ok {
		t.Fatal("the console's new secret")
	}
	a, err := area.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	ref := func(user string) string { return pid + "/" + provider.LoginID("codex", user) }
	done := func(user string) provider.SignInState {
		return provider.SignInState{Agent: "codex", User: user, State: "done"}
	}
	if st := claimDone(adminCall{who: before, area: a, console: true})(done("old@example.com")); st.State != "failed" {
		t.Fatal("the console's old secret's:", st)
	}
	if owner, _, _ := area.Owner(ref("old@example.com")); owner != "" {
		t.Fatal("the console's old secret's account is", owner+"'s")
	}
	if st := claimDone(adminCall{who: after, area: a, console: true})(done("new@example.com")); st.State != "done" {
		t.Fatal("the console's new secret's:", st)
	}
	if owner, _, _ := area.Owner(ref("new@example.com")); owner != id {
		t.Fatal("the console's new secret's account is", owner+"'s")
	}
}

// A batch of keys, or an import, the console started before its key was
// rotated or removed adds nothing after (stillActs, T-261), for an area
// turned off too; the rest fail saying why. The new secret's adds, to an
// area turned off as well.
func TestAdminConsoleKeysAfterKeyChangedAddNothing(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, _ := makeArea(t, do, console, "A", "plan")
	before, ok := access.Authenticate(console)
	if !ok {
		t.Fatal("the console's key")
	}
	secret := changeConsoleKey(t, "rotate-key")
	after, ok := access.Authenticate(secret)
	if !ok {
		t.Fatal("the console's new secret")
	}
	off := true
	if _, err := area.Update(id, area.Spec{Off: &off}); err != nil {
		t.Fatal(err)
	}
	a, err := area.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	batch := func(c adminCall, keys ...string) keysOut {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", AdminPath+"/areas/"+id+"/accounts/keys", strings.NewReader(addKeyBody(map[string]any{"provider": "plan", "keys": keys})))
		s.adminAddKeys(w, r, c)
		var out keysOut
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		return out
	}
	stale := adminCall{who: before, area: a, console: true}
	if out := batch(stale, "sk-cstale-00000001", "sk-cstale-00000002"); out.Added != 0 || out.Failed != 2 || out.Results[0].Message != errKeyChanged.Error() || out.Results[1].Message != errKeyChanged.Error() {
		t.Fatalf("the console's old secret's batch: %+v", out)
	}
	p, _ := provider.Find("plan")
	for _, k := range []string{"sk-cstale-00000001", "sk-cstale-00000002"} {
		if p.HasKey(k) {
			t.Fatal("written to the provider:", k)
		}
	}
	if err := stillActs(stale); err != errKeyChanged {
		t.Fatal("an import's read of the console's old secret:", err)
	}
	if out := batch(adminCall{who: after, area: a, console: true}, "sk-cfresh-00000001"); out.Added != 1 {
		t.Fatalf("the console's new secret's, the area off: %+v", out)
	}
	changeConsoleKey(t, "remove-key")
	if out := batch(adminCall{who: after, area: a, console: true}, "sk-cgone-000000001"); out.Added != 0 || out.Results[0].Message != errKeyChanged.Error() {
		t.Fatalf("the console's removed key's batch: %+v", out)
	}
}
