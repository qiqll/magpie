package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// adminDo is a request to the gateway as another computer (or this one)
// makes it, with secret as its key ("" none).
type adminDo func(method, path, from, secret, body string, hdr ...string) (int, string)

func adminGateway(t *testing.T) adminDo {
	t.Helper()
	h := lanGuard(New().Handler())
	return func(method, path, from, secret, body string, hdr ...string) (int, string) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, path, rd)
		r.RemoteAddr = from
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
}

const lanPeer, otherPeer, loopback = "192.168.1.9:5000", "10.9.9.9:5000", "127.0.0.1:5000"

// adminWorld is a magpie shared on the LAN with a provider taking keys
// ("plan", its upstream counting requests), a console key, and the
// admin allow list letting 192.168.1.0/24 in.
func adminWorld(t *testing.T) (do adminDo, console string, reached *atomic.Int32) {
	t.Helper()
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv(AdminAllowIPsEnv, "192.168.1.0/24")
	reached = &atomic.Int32{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	t.Cleanup(up.Close)
	for _, id := range []string{"plan", "other"} {
		if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "upstream-secret-" + id, Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
			t.Fatal(err)
		}
	}
	newCaller(t, "Laptop") // shared on the LAN, with a gateway key
	_, console, err := access.AddAdmin(access.AdminConsole, "", "Console")
	if err != nil {
		t.Fatal(err)
	}
	return adminGateway(t), console, reached
}

// makeArea is an area the console makes, and its admin key.
func makeArea(t *testing.T, do adminDo, console, note string, providers ...string) (string, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"note": note, "providers": providers, "signIns": []string{}})
	c, body := do("POST", AdminPath+"/areas", lanPeer, console, string(b))
	if c != http.StatusCreated {
		t.Fatalf("making %s: %d %s", note, c, body)
	}
	var out struct {
		Area     adminArea
		AdminKey string
	}
	json.Unmarshal([]byte(body), &out)
	if out.Area.ID == "" || !strings.HasPrefix(out.AdminKey, access.Prefix) {
		t.Fatal("made:", body)
	}
	return out.Area.ID, out.AdminKey
}

// An admin key, an area's or the console's, reaches the admin routes
// alone: every other route the router has is refused it, from this
// computer and from another, and no upstream is reached.
func TestAdminKeyEveryRoute(t *testing.T) {
	do, console, reached := adminWorld(t)
	_, study := makeArea(t, do, console, "学习", "plan")
	mux := New().routes()
	admin := map[string]bool{}
	for _, rs := range adminRoutes {
		for p := range rs {
			admin[p] = true
			if !slices.Contains(mux.patterns, p) {
				t.Errorf("adminRoutes names %q, which the router doesn't have", p)
			}
		}
	}
	for _, p := range mux.patterns {
		if strings.HasPrefix(strings.TrimPrefix(strings.TrimPrefix(p, "GET "), "POST "), AdminPath) && !admin[p] {
			t.Errorf("route %q is under %s and in no admin role's routes", p, AdminPath)
		}
	}
	for _, from := range []string{lanPeer, loopback} {
		for name, secret := range map[string]string{"area": study, "console": console} {
			for _, p := range mux.patterns {
				if admin[p] {
					continue
				}
				for _, mp := range requestsFor(p) {
					c, b := do(mp[0], mp[1], from, secret, chatReq)
					if c != http.StatusForbidden || !strings.Contains(b, AdminKeyCode) {
						t.Errorf("%s admin key from %s: %s %s (%s): %d %.200s", name, from, mp[0], mp[1], p, c, b)
					}
				}
			}
			if c, b := do("POST", "/v1//chat/completions", from, secret, chatReq); c == 200 || strings.Contains(b, `"choices"`) {
				t.Errorf("%s admin key from %s: an unclean chat path: %d %.200s", name, from, c, b)
			}
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("an admin key reached the upstream %d times", n)
	}
	// and a gateway key, read-only or full, reaches no admin route
	keys, _ := access.List()
	full, _ := access.Update("copy-key", access.Change{Key: keys[0].ID})
	for p := range admin {
		for _, mp := range requestsFor(p) {
			for _, from := range []string{lanPeer, loopback} {
				if c, b := do(mp[0], mp[1], from, full, "{}"); c != http.StatusUnauthorized || !strings.Contains(b, adminKeyRequired) {
					t.Errorf("gateway key from %s: %s %s: %d %.200s", from, mp[0], mp[1], c, b)
				}
			}
		}
	}
}

// Areas never see or touch each other's accounts: each lists its own,
// another's id is a 404 to remove, the same key added twice is a
// conflict that names no owner, and the console reads every area and
// adds or removes no account.
func TestAdminAreasApart(t *testing.T) {
	do, console, _ := adminWorld(t)
	studyID, study := makeArea(t, do, console, "学习", "plan")
	_, work := makeArea(t, do, console, "工作", "plan", "other")

	add := func(secret, prov, key string) (int, string) {
		b, _ := json.Marshal(map[string]string{"provider": prov, "name": "k", "key": key})
		return do("POST", AdminPath+"/accounts/key", lanPeer, secret, string(b))
	}
	c, b := add(study, "plan", "sk-study-0000000001")
	if c != http.StatusCreated || strings.Contains(b, "sk-study-0000000001") {
		t.Fatal("study's key:", c, b)
	}
	var mine adminAccount
	json.Unmarshal([]byte(b), &mine)
	if mine.Kind != "key" || mine.Provider != "plan" || mine.Masked == "" {
		t.Fatal("study's account:", b)
	}
	if c, b := add(work, "plan", "sk-study-0000000001"); c != http.StatusConflict || strings.Contains(b, "学习") {
		t.Fatal("the same key in another area:", c, b)
	}
	if c, b := add(study, "other", "sk-study-0000000002"); c != http.StatusNotFound {
		t.Fatal("a provider the area wasn't given:", c, b)
	}
	if c, b := add(work, "other", "sk-work-00000000001"); c != http.StatusCreated {
		t.Fatal("work's key:", c, b)
	}
	list := func(secret string) []adminAccount {
		c, b := do("GET", AdminPath+"/accounts", lanPeer, secret, "")
		if c != 200 {
			t.Fatal("list:", c, b)
		}
		var out []adminAccount
		json.Unmarshal([]byte(b), &out)
		return out
	}
	if got := list(study); len(got) != 1 || got[0].ID != mine.ID {
		t.Fatal("study lists:", got)
	}
	if got := list(work); len(got) != 1 || got[0].Provider != "other" {
		t.Fatal("work lists:", got)
	}
	// work removes study's, by its real id: not found, and it stays
	rm := func(secret, id string) (int, string) {
		b, _ := json.Marshal(map[string]string{"id": id})
		return do("POST", AdminPath+"/accounts/remove", lanPeer, secret, string(b))
	}
	if c, b := rm(work, mine.ID); c != http.StatusNotFound {
		t.Fatal("work removed study's:", c, b)
	}
	// nor one of the owner's, no area's
	owner := "plan/" + provider.KeyID("upstream-secret-plan")
	if c, b := rm(study, owner); c != http.StatusNotFound {
		t.Fatal("study removed the owner's:", c, b)
	}
	if p, _ := provider.Find("plan"); !slices.ContainsFunc(p.KeyList(), func(k provider.KeyInfo) bool { return "plan/"+k.ID == mine.ID }) {
		t.Fatal("study's key gone from magpie")
	}
	// the console reads both, and adds and removes nothing
	c, b = do("GET", AdminPath+"/areas/"+studyID+"/accounts", lanPeer, console, "")
	if c != 200 || !strings.Contains(b, mine.ID) {
		t.Fatal("console reads study's:", c, b)
	}
	if c, b := add(console, "plan", "sk-console-00000001"); c != http.StatusForbidden || !strings.Contains(b, adminAreaOnly) {
		t.Fatal("console added a key:", c, b)
	}
	if c, b := rm(console, mine.ID); c != http.StatusForbidden {
		t.Fatal("console removed one:", c, b)
	}
	// an area reads none of the console's routes, another area's least
	for _, path := range []string{"/areas", "/areas/" + studyID, "/areas/" + studyID + "/usage", "/catalog"} {
		if c, b := do("GET", AdminPath+path, lanPeer, work, ""); c != http.StatusForbidden || !strings.Contains(b, adminConsoleOnly) {
			t.Errorf("work read %s: %d %.200s", path, c, b)
		}
	}
	if c, b := do("POST", AdminPath+"/areas/"+studyID+"/delete", lanPeer, work, ""); c != http.StatusForbidden {
		t.Fatal("work deleted study:", c, b)
	}
	// an area that owns accounts isn't deleted; once empty, it is, and
	// its key reaches nothing
	if c, b := do("POST", AdminPath+"/areas/"+studyID+"/delete", lanPeer, console, ""); c != http.StatusConflict {
		t.Fatal("deleted while it owns an account:", c, b)
	}
	if c, b := rm(study, mine.ID); c != http.StatusNoContent {
		t.Fatal("study removed its own:", c, b)
	}
	if p, _ := provider.Find("plan"); slices.ContainsFunc(p.KeyList(), func(k provider.KeyInfo) bool { return "plan/"+k.ID == mine.ID }) {
		t.Fatal("study's key still in magpie")
	}
	if c, b := do("POST", AdminPath+"/areas/"+studyID+"/delete", lanPeer, console, ""); c != http.StatusNoContent {
		t.Fatal("deleting the empty area:", c, b)
	}
	for _, from := range []string{lanPeer, loopback} {
		if c, b := do("GET", AdminPath+"/me", from, study, ""); c != http.StatusUnauthorized {
			t.Fatal("the deleted area's key from", from, c, b)
		}
	}
}

// The admin routes answer an enabled admin key alone, from this computer
// as from another, and from another only on the admin allow list, by the
// connection's own address.
func TestAdminWhoAndWhence(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, study := makeArea(t, do, console, "娱乐")
	for _, from := range []string{lanPeer, loopback} {
		for _, secret := range []string{"", "sk-magpie-key-nope", study + "x"} {
			if c, b := do("GET", AdminPath+"/me", from, secret, ""); c != http.StatusUnauthorized && c != http.StatusForbidden {
				t.Errorf("from %s with %q: %d %.200s", from, secret, c, b)
			}
		}
	}
	if c, b := do("GET", AdminPath+"/me?key="+study, lanPeer, study, ""); c != http.StatusBadRequest {
		t.Fatal("a key in the address:", c, b)
	}
	if c, b := do("GET", AdminPath+"/me", otherPeer, study, "", "X-Forwarded-For", "192.168.1.9"); c != http.StatusForbidden || !strings.Contains(b, adminIPNotAllowed) {
		t.Fatal("from off the admin allow list:", c, b)
	}
	// a proxy or tunnel here: someone else's, refused unless the proxy is
	// trusted, and then judged by the address it names
	for _, h := range []string{"X-Forwarded-For", "Cf-Connecting-IP", "Forwarded"} {
		if c, b := do("GET", AdminPath+"/me", loopback, study, "", h, "203.0.113.9"); c != http.StatusForbidden || !strings.Contains(b, adminIPNotAllowed) {
			t.Fatal("through a proxy here,", h, c, b)
		}
	}
	t.Setenv("MAGPIE_TRUST_PROXY", "1")
	if c, b := do("GET", AdminPath+"/me", loopback, study, "", "X-Forwarded-For", "203.0.113.9"); c != http.StatusForbidden {
		t.Fatal("a trusted proxy's client off the list:", c, b)
	}
	if c, b := do("GET", AdminPath+"/me", loopback, study, "", "X-Forwarded-For", "203.0.113.9, 192.168.1.20"); c != 200 {
		t.Fatal("a trusted proxy's client on the list:", c, b)
	}
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	c, b := do("GET", AdminPath+"/me", loopback, study, "")
	if c != 200 || !strings.Contains(b, `"role":"area"`) || !strings.Contains(b, "娱乐") {
		t.Fatal("me:", c, b)
	}
	// turned off, then rotated: the old value reaches nothing
	if c, b := do("POST", AdminPath+"/areas/"+id+"/update", lanPeer, console, `{"off":true}`); c != 200 {
		t.Fatal(c, b)
	}
	if c, _ := do("GET", AdminPath+"/me", lanPeer, study, ""); c != http.StatusUnauthorized {
		t.Fatal("an area turned off:", c)
	}
	do("POST", AdminPath+"/areas/"+id+"/update", lanPeer, console, `{"off":false}`)
	c, b = do("POST", AdminPath+"/areas/"+id+"/rotate-key", lanPeer, console, "")
	var rot struct{ AdminKey string }
	json.Unmarshal([]byte(b), &rot)
	if c != 200 || rot.AdminKey == "" || rot.AdminKey == study {
		t.Fatal("rotated:", c, b)
	}
	if c, _ := do("GET", AdminPath+"/me", lanPeer, study, ""); c != http.StatusUnauthorized {
		t.Fatal("the old value after a rotation:", c)
	}
	if c, _ := do("GET", AdminPath+"/me", lanPeer, rot.AdminKey, ""); c != 200 {
		t.Fatal("the new value:", c)
	}
	// an admin key is changed only as an admin key may be
	keys, _ := access.List()
	k := keys[slices.IndexFunc(keys, func(k access.Key) bool { return k.Admin == access.AdminArea })]
	for _, a := range []string{"access-key", "accounts-key", "models-key", "issuer-key", "users-key", "mcp-key"} {
		if _, err := access.Update(a, access.Change{Key: k.ID}); err == nil {
			t.Errorf("%s changed an admin key", a)
		}
	}
	if _, _, err := access.AddAdmin(access.AdminConsole, "", "Second"); err == nil {
		t.Fatal("a second console key")
	}
	// the console with no areas.json to read is told so, never no areas
	t.Setenv(AdminAllowIPsEnv, "")
	if c, b := do("GET", AdminPath+"/areas", lanPeer, console, ""); c != http.StatusForbidden {
		t.Fatal("an empty admin allow list let another computer in:", c, b)
	}
}

// What an area's accounts answered, and only that, counts as the area's,
// from when it got each: none with no account, never the whole ledger.
func TestAdminUsageIsTheAreasAccounts(t *testing.T) {
	do, console, _ := adminWorld(t)
	studyID, study := makeArea(t, do, console, "学习", "plan")
	_, empty := makeArea(t, do, console, "空", "plan")
	b, _ := json.Marshal(map[string]string{"provider": "plan", "name": "mine", "key": "sk-study-0000000001"})
	if c, body := do("POST", AdminPath+"/accounts/key", lanPeer, study, string(b)); c != http.StatusCreated {
		t.Fatal(c, body)
	}
	fp := provider.KeyID("sk-study-0000000001")
	now := time.Now()
	a, _ := area.Get(studyID)
	usage.Append(usage.Record{Time: a.Accounts[0].Added.Add(-time.Minute), Provider: "plan", ProviderKeyID: fp, Model: "m1", Status: 200, Input: 1000})
	usage.Append(usage.Record{Time: now, Provider: "plan", ProviderKeyID: fp, Model: "m1", Status: 200, Input: 10, Output: 2})
	usage.Append(usage.Record{Time: now, Provider: "plan", ProviderKeyID: provider.KeyID("upstream-secret-plan"), Model: "m1", Status: 200, Input: 500})
	usage.Append(usage.Record{Time: now, Provider: "other", ProviderKeyID: fp, Model: "m1", Status: 200, Input: 700})

	read := func(secret string) AreaUsage {
		c, body := do("GET", AdminPath+"/usage?period=7d", lanPeer, secret, "")
		if c != 200 {
			t.Fatal(c, body)
		}
		var u AreaUsage
		json.Unmarshal([]byte(body), &u)
		return u
	}
	u := read(study)
	if u.Totals.Requests != 1 || u.Totals.Input != 10 || u.Totals.Output != 2 || len(u.ByModel) != 1 || u.ByModel[0].Model != "m1" || len(u.ByDay) != 1 || u.ByDay[0].Day == "" || len(u.ByAccount) != 1 || u.ByAccount[0].Name != "mine" {
		t.Fatalf("study's usage: %+v", u)
	}
	if u := read(empty); u.Totals.Requests != 0 || u.Totals.Input != 0 {
		t.Fatalf("an area with no account: %+v", u)
	}
	c, body := do("GET", AdminPath+"/areas/"+studyID+"/usage?period=today", lanPeer, console, "")
	if c != 200 || !strings.Contains(body, `"inputTokens":10`) {
		t.Fatal("the console reads it:", c, body)
	}
	if c, _ := do("GET", AdminPath+"/usage?period=year", lanPeer, study, ""); c != http.StatusBadRequest {
		t.Fatal("a period it doesn't know:", c)
	}
}

// A sign-in an area started makes the account it is done with the area's;
// one magpie had already stays whose it was, and one another area got
// first is no second area's. A sign-in is read, pasted into and canceled
// by the area that started it alone.
func TestAdminSignInClaims(t *testing.T) {
	do, console, _ := adminWorld(t)
	pid := subscriptionOf("codex")
	if pid == "" {
		t.Skip("no Codex subscription provider in this build")
	}
	a, _ := makeArea(t, do, console, "A")
	b, _ := makeArea(t, do, console, "B")
	done := provider.SignInState{Agent: "codex", User: "new@example.com", State: "done"}
	if st := claimOnDone(a)(done); st.State != "done" {
		t.Fatal("a new account:", st)
	}
	ref := pid + "/" + provider.LoginID("codex", "new@example.com")
	if owner, _, _ := area.Owner(ref); owner != a {
		t.Fatal("owner:", owner)
	}
	if st := claimOnDone(b)(done); st.State != "conflict" {
		t.Fatal("A's account signed in to by B:", st)
	}
	again := done
	again.User, again.Again = "owners@example.com", true
	if st := claimOnDone(b)(again); st.State != "conflict" {
		t.Fatal("an account magpie had:", st)
	}
	if owner, _, _ := area.Owner(pid + "/" + provider.LoginID("codex", "owners@example.com")); owner != "" {
		t.Fatal("the owner's account moved to", owner)
	}
	areaSignIns.Lock()
	areaSignIns.m = map[string]string{"si-a": a}
	areaSignIns.Unlock()
	if !ownSignIn(adminCall{area: area.Area{ID: a}}, "si-a") || ownSignIn(adminCall{area: area.Area{ID: b}}, "si-a") || ownSignIn(adminCall{area: area.Area{ID: a}}, "") {
		t.Fatal("a sign-in read by an area that didn't start it")
	}
}

// An area signing in again to its own account stays done; one deleted
// before its sign-in ends gets no account, and nothing is left in magpie.
func TestAdminSignInAgainAndGone(t *testing.T) {
	do, console, _ := adminWorld(t)
	a, _ := makeArea(t, do, console, "A")
	done := provider.SignInState{Agent: "codex", User: "own@example.com", State: "done"}
	if st := claimOnDone(a)(done); st.State != "done" {
		t.Fatal("first:", st)
	}
	again := done
	again.Again = true
	if st := claimOnDone(a)(again); st.State != "done" {
		t.Fatal("its own signed in again:", st)
	}
	if st := claimOnDone("ar_gone")(provider.SignInState{Agent: "codex", User: "late@example.com", State: "done"}); st.State != "failed" {
		t.Fatal("a deleted area's:", st)
	}
}

// An account two areas list (areas.json written by hand) is contested:
// listed as such to each, removed by neither, counted in neither's usage.
func TestAdminContestedAccount(t *testing.T) {
	do, console, _ := adminWorld(t)
	aID, aKey := makeArea(t, do, console, "A", "plan")
	bID, _ := makeArea(t, do, console, "B", "plan")
	b, _ := json.Marshal(map[string]string{"provider": "plan", "name": "k", "key": "sk-shared-000000001"})
	if c, body := do("POST", AdminPath+"/accounts/key", lanPeer, aKey, string(b)); c != http.StatusCreated {
		t.Fatal(c, body)
	}
	ref := "plan/" + provider.KeyID("sk-shared-000000001")
	// B is given it too, by hand
	raw, _ := os.ReadFile(area.Path())
	var f struct {
		Version int         `json:"version"`
		Areas   []area.Area `json:"areas"`
	}
	json.Unmarshal(raw, &f)
	for i := range f.Areas {
		if f.Areas[i].ID == bID {
			f.Areas[i].Accounts = append(f.Areas[i].Accounts, area.Account{Ref: ref, Added: time.Now()})
		}
	}
	out, _ := json.Marshal(f)
	os.WriteFile(area.Path(), out, 0o600)
	time.Sleep(10 * time.Millisecond) // a new mtime for the read cache
	c, body := do("GET", AdminPath+"/accounts", lanPeer, aKey, "")
	if c != 200 || !strings.Contains(body, `"contested":true`) {
		t.Fatal("listed:", c, body)
	}
	rb, _ := json.Marshal(map[string]string{"id": ref})
	if c, body := do("POST", AdminPath+"/accounts/remove", lanPeer, aKey, string(rb)); c != http.StatusNotFound {
		t.Fatal("a contested account removed:", c, body)
	}
	usage.Append(usage.Record{Time: time.Now(), Provider: "plan", ProviderKeyID: provider.KeyID("sk-shared-000000001"), Model: "m1", Status: 200, Input: 9})
	if c, body := do("GET", AdminPath+"/areas/"+aID+"/usage?period=today", lanPeer, console, ""); c != 200 || !strings.Contains(body, `"requests":0`) {
		t.Fatal("a contested account's calls counted:", c, body)
	}
}
