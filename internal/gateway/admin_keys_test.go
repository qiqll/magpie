package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
)

// addKeyBody is an accounts/key or accounts/keys body.
func addKeyBody(fields map[string]any) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

// keysOut is an accounts/keys answer.
type keysOut struct {
	Results                 []keyRow
	Added, Conflict, Failed int
}

func addKeys(t *testing.T, do adminDo, secret string, fields map[string]any) (int, keysOut, string) {
	t.Helper()
	c, body := do("POST", AdminPath+"/accounts/keys", lanPeer, secret, addKeyBody(fields))
	var out keysOut
	json.Unmarshal([]byte(body), &out)
	return c, out, body
}

// A key's protocol and plan are the ones the window's key row takes:
// /options and /catalog list them for each provider, a key added with
// them has them, and one its provider has no address or plan for is 400
// with nothing added. A key sent with an opened date is 400 on both key
// routes: a key has none.
func TestAdminKeyProtocolPlanOpened(t *testing.T) {
	do, console, _ := adminWorld(t)
	if err := provider.Save(provider.Provider{ID: "zai-k", Name: "Z", Preset: "zai", Key: "upstream-secret-zai", Chat: "https://api.z.ai/api/paas/v4", Anthropic: "https://api.z.ai/api/anthropic", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	_, key := makeArea(t, do, console, "A", "plan", "zai-k")

	for _, tt := range []struct{ path, secret string }{{"/options", key}, {"/catalog", console}} {
		c, body := do("GET", AdminPath+tt.path, lanPeer, tt.secret, "")
		var out struct{ Providers []adminProvider }
		json.Unmarshal([]byte(body), &out)
		i := slices.IndexFunc(out.Providers, func(p adminProvider) bool { return p.ID == "plan" })
		j := slices.IndexFunc(out.Providers, func(p adminProvider) bool { return p.ID == "zai-k" })
		if c != 200 || i < 0 || j < 0 {
			t.Fatal(tt.path, c, body)
		}
		if p := out.Providers[i]; !slices.Equal(p.Protocols, []string{"", "chat"}) || p.Plans != nil {
			t.Fatalf("%s plan: %+v", tt.path, p)
		}
		if p := out.Providers[j]; !slices.Equal(p.Protocols, []string{"", "anthropic", "chat"}) || len(p.Plans) != 2 || p.Plans[1] != (adminPlan{ID: "coding", Name: "Coding Plan"}) {
			t.Fatalf("%s zai-k: %+v", tt.path, p)
		}
		if !strings.Contains(body, `"protocols":["","chat"]`) {
			t.Fatalf("%s: a provider's protocols as told: %s", tt.path, body)
		}
	}

	add := func(fields map[string]any) (int, string) {
		return do("POST", AdminPath+"/accounts/key", lanPeer, key, addKeyBody(fields))
	}
	if c, body := add(map[string]any{"provider": "zai-k", "name": "c", "key": "sk-zai-coding-000001", "protocol": "anthropic", "plan": "coding"}); c != http.StatusCreated {
		t.Fatal("protocol and plan:", c, body)
	}
	p, _ := provider.Find("zai-k")
	got := slices.IndexFunc(p.KeyList(), func(k provider.KeyInfo) bool {
		return k.ID == provider.KeyID("sk-zai-coding-000001") && k.Protocol == provider.Anthropic && k.Plan == "coding"
	})
	if got < 0 {
		t.Fatalf("the key as kept: %+v", p.KeyList())
	}
	// the provider's own plan is kept as none, as SetKeyPlan keeps it
	if c, body := add(map[string]any{"provider": "zai-k", "key": "sk-zai-api-00000001", "plan": "api"}); c != http.StatusCreated {
		t.Fatal("its own plan:", c, body)
	}
	p, _ = provider.Find("zai-k")
	if i := slices.IndexFunc(p.KeyList(), func(k provider.KeyInfo) bool { return k.ID == provider.KeyID("sk-zai-api-00000001") }); i < 0 || p.KeyList()[i].Plan != "" {
		t.Fatalf("its own plan kept: %+v", p.KeyList())
	}

	for name, fields := range map[string]map[string]any{
		"a protocol it has no address for": {"provider": "plan", "key": "sk-bad-0000000001", "protocol": "anthropic"},
		"a protocol magpie doesn't know":   {"provider": "plan", "key": "sk-bad-0000000002", "protocol": "grpc"},
		"a plan of a preset with none":     {"provider": "plan", "key": "sk-bad-0000000003", "plan": "coding"},
		"a plan the preset doesn't sell":   {"provider": "zai-k", "key": "sk-bad-0000000004", "plan": "max"},
		"an opened date":                   {"provider": "plan", "key": "sk-bad-0000000005", "opened": "2026-11-01"},
		"an empty opened date":             {"provider": "plan", "key": "sk-bad-0000000006", "opened": ""},
	} {
		if c, body := add(fields); c != http.StatusBadRequest || strings.Contains(body, "sk-bad") {
			t.Errorf("%s: %d %s", name, c, body)
		}
		fields["keys"] = []string{fields["key"].(string)}
		delete(fields, "key")
		if c, body, _ := addKeys(t, do, key, fields); c != http.StatusBadRequest {
			t.Errorf("%s, many: %d %+v", name, c, body)
		}
	}
	for _, id := range []string{"plan", "zai-k"} {
		p, _ := provider.Find(id)
		for _, k := range p.KeyList() {
			if strings.HasPrefix(k.Masked, "sk-b") {
				t.Fatalf("a refused key was added to %s: %+v", id, k)
			}
		}
	}
	c, body := do("GET", AdminPath+"/accounts", lanPeer, key, "")
	if c != 200 || strings.Contains(body, `"opened"`) {
		t.Fatal("a key told with an opened date:", c, body)
	}
}

// Many keys at once are each claimed and added on their own, told in
// the order sent: one magpie has is a conflict that stays the owner's,
// one sent twice a duplicate, one too long failed, and the others are
// the area's, named in turn. Those added stay; none is rolled back.
func TestAdminManyKeys(t *testing.T) {
	do, console, _ := adminWorld(t)
	if err := provider.AddKey("plan", "owner's", "sk-owners-many-0001", ""); err != nil {
		t.Fatal(err)
	}
	id, key := makeArea(t, do, console, "A", "plan")
	long := "sk-" + strings.Repeat("x", 4100)
	c, out, body := addKeys(t, do, key, map[string]any{"provider": "plan", "name": "team", "protocol": "chat", "keys": []string{
		"  sk-many-0000000001 ", "", "sk-owners-many-0001", "sk-many-0000000001", long, "sk-many-0000000002",
	}})
	if c != 200 || strings.Contains(body, "sk-many-0000000001") || strings.Contains(body, "sk-owners-many-0001") {
		t.Fatal(c, body)
	}
	want := []string{keyAdded, keyConflict, keyDuplicate, keyFailed, keyAdded}
	if len(out.Results) != len(want) || out.Added != 2 || out.Conflict != 1 || out.Failed != 1 {
		t.Fatalf("results: %s", body)
	}
	for i, r := range out.Results {
		if r.Index != i+1 || r.Status != want[i] || r.Masked == "" {
			t.Errorf("row %d: %+v, want %s", i+1, r, want[i])
		}
	}
	if a := out.Results[0].Account; a == nil || a.Name != "team" || a.Kind != "key" {
		t.Fatalf("first: %+v", out.Results[0])
	}
	if a := out.Results[4].Account; a == nil || a.Name != "team #5" {
		t.Fatalf("fifth: %+v", out.Results[4])
	}
	if out.Results[1].Account != nil || out.Results[1].Message == "" {
		t.Fatalf("the conflict: %+v", out.Results[1])
	}
	p, _ := provider.Find("plan")
	for _, k := range p.KeyList() {
		if k.ID == provider.KeyID("sk-many-0000000001") && k.Protocol != provider.Chat {
			t.Fatalf("its protocol: %+v", k)
		}
	}
	a, _ := area.Get(id)
	if len(a.Accounts) != 2 || !a.Has("plan/"+provider.KeyID("sk-many-0000000002")) {
		t.Fatalf("the area owns: %+v", a.Accounts)
	}
	if owner, _, _ := area.Owner("plan/" + provider.KeyID("sk-owners-many-0001")); owner != "" {
		t.Fatal("the owner's key moved to", owner)
	}

	for name, keys := range map[string][]string{"none": {}, "blank lines alone": {" ", ""}, "101": make([]string, 101)} {
		if name == "101" {
			for i := range keys {
				keys[i] = fmt.Sprintf("sk-over-%010d", i)
			}
		}
		if c, _, body := addKeys(t, do, key, map[string]any{"provider": "plan", "keys": keys}); c != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, c, body)
		}
	}
	if c, _, body := addKeys(t, do, key, map[string]any{"provider": "other", "keys": []string{"sk-other-000000001"}}); c != http.StatusNotFound {
		t.Fatal("a provider the area wasn't given:", c, body)
	}
}

// The area's 500 accounts hold across one request: past them the rest
// fail, each saying why, and those added before stay.
func TestAdminManyKeysAreaFull(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, key := makeArea(t, do, console, "A", "plan")
	for i := range adminAccountsLimit - 1 {
		if err := area.Claim(id, fmt.Sprintf("plan/fp_held%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	c, out, body := addKeys(t, do, key, map[string]any{"provider": "plan", "keys": []string{"sk-full-0000000001", "sk-full-0000000002", "sk-full-0000000003"}})
	if c != 200 || out.Added != 1 || out.Failed != 2 {
		t.Fatal(c, body)
	}
	for _, r := range out.Results[1:] {
		if r.Status != keyFailed || r.Message != "an area has at most 500 accounts" {
			t.Fatalf("past the limit: %+v", r)
		}
	}
	if a, _ := area.Get(id); len(a.Accounts) != adminAccountsLimit {
		t.Fatal("the area owns", len(a.Accounts))
	}
}

// Each conflict of a request counts toward the area's conflict line;
// reaching it fails the rest, and the area's next add is 429 at the
// door, on either key route.
func TestAdminManyKeysConflictLine(t *testing.T) {
	do, console, _ := adminWorld(t)
	var keys []string
	for i := range conflictMost + 2 {
		k := fmt.Sprintf("sk-owners-line-%04d", i)
		if err := provider.AddKey("plan", "", k, ""); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	keys = append(keys, "sk-line-new-0000001")
	_, key := makeArea(t, do, console, "A", "plan")
	c, out, body := addKeys(t, do, key, map[string]any{"provider": "plan", "keys": keys})
	if c != 200 || out.Conflict != conflictMost || out.Failed != 3 || out.Added != 0 {
		t.Fatal(c, body)
	}
	if r := out.Results[len(out.Results)-1]; r.Status != keyFailed || r.Message != "too many conflicts; try again later" {
		t.Fatalf("after the line: %+v", r)
	}
	if c, _, body := addKeys(t, do, key, map[string]any{"provider": "plan", "keys": []string{"sk-line-new-0000002"}}); c != http.StatusTooManyRequests {
		t.Fatal("the next request:", c, body)
	}
	if c, body := do("POST", AdminPath+"/accounts/key", lanPeer, key, addKeyBody(map[string]any{"provider": "plan", "key": "sk-line-new-0000003"})); c != http.StatusTooManyRequests {
		t.Fatal("one key after:", c, body)
	}
	p, _ := provider.Find("plan")
	if slices.ContainsFunc(p.KeyList(), func(k provider.KeyInfo) bool { return strings.HasPrefix(k.Masked, "sk-l") }) {
		t.Fatal("a key past the line was added")
	}
}

// An area's direct has its account go through no proxy: "direct" is its
// own proxy, told as {"direct":true}; taken off with proxy "", it is
// gone. An owner's value set since wins, and the area can neither write
// over it nor take it off; one set before is never written over.
func TestAdminAccountDirect(t *testing.T) {
	do, console, _ := adminWorld(t)
	_, key := makeArea(t, do, console, "A", "plan")
	added := func(k string) string {
		c, body := do("POST", AdminPath+"/accounts/key", lanPeer, key, addKeyBody(map[string]any{"provider": "plan", "key": k}))
		if c != http.StatusCreated {
			t.Fatal(c, body)
		}
		return "plan/" + provider.KeyID(k)
	}
	own := func(ref string) string {
		p, _ := provider.Find("plan")
		_, fp, _ := strings.Cut(ref, "/")
		return p.AccountProxies[fp]
	}
	setProxy := func(fields map[string]any) (int, adminAccount, string) {
		c, body := do("POST", AdminPath+"/accounts/proxy", lanPeer, key, addKeyBody(fields))
		var acc adminAccount
		json.Unmarshal([]byte(body), &acc)
		return c, acc, body
	}
	ref := added("sk-direct-00000001")
	c, acc, body := setProxy(map[string]any{"id": ref, "direct": true})
	if c != 200 || acc.Proxy == nil || !acc.Proxy.Direct || acc.Proxy.Overridden || acc.Proxy.IP != "" || !strings.Contains(body, `"proxy":{"direct":true,"overridden":false}`) {
		t.Fatal("direct:", c, body)
	}
	if v := own(ref); v != "direct" {
		t.Fatalf("its own proxy: %q", v)
	}
	if c, _, body := setProxy(map[string]any{"id": ref, "direct": true, "ip": "203.0.113.7", "proxy": "px_0000000000"}); c != http.StatusBadRequest {
		t.Fatal("direct and a proxy:", c, body)
	}
	if c, acc, body := setProxy(map[string]any{"id": ref, "proxy": ""}); c != 200 || acc.Proxy != nil || own(ref) != "" {
		t.Fatal("taken off:", c, body, own(ref))
	}

	// the owner's, set since: overridden, kept through a direct and a detach
	setProxy(map[string]any{"id": ref, "direct": true})
	_, fp, _ := strings.Cut(ref, "/")
	setOwn(t, "plan", fp, "http://owner.example:8080")
	c, body = do("GET", AdminPath+"/accounts", lanPeer, key, "")
	if c != 200 || !strings.Contains(body, `"proxy":{"direct":true,"overridden":true}`) {
		t.Fatal("overridden:", c, body)
	}
	if c, _, body := setProxy(map[string]any{"id": ref, "direct": true}); c != http.StatusConflict || own(ref) != "http://owner.example:8080" {
		t.Fatal("direct over the owner's:", c, body, own(ref))
	}
	setProxy(map[string]any{"id": ref, "proxy": ""})
	if v := own(ref); v != "http://owner.example:8080" {
		t.Fatalf("the owner's taken off: %q", v)
	}

	// the owner's, set before the area's first: never written over
	ref2 := added("sk-direct-00000002")
	_, fp2, _ := strings.Cut(ref2, "/")
	setOwn(t, "plan", fp2, "http://owner.example:8081")
	if c, _, body := setProxy(map[string]any{"id": ref2, "direct": true}); c != http.StatusConflict || own(ref2) != "http://owner.example:8081" {
		t.Fatal("direct over the owner's first:", c, body, own(ref2))
	}
	if c, _, body := setProxy(map[string]any{"id": "plan/fp_not_the_areas", "direct": true}); c != http.StatusNotFound {
		t.Fatal("another's account:", c, body)
	}
}

// setOwn writes the owner's own proxy for the account key of provider
// pid, as the Providers editor does.
func setOwn(t *testing.T, pid, key, url string) {
	t.Helper()
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccountProxies == nil {
		p.AccountProxies = map[string]string{}
	}
	p.AccountProxies[key] = url
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
}

// A subscription account's opening date, sent with its sign-in as
// YYYY-MM-DD, is kept on the account once the sign-in leaves it the
// area's, and the area's account list tells it. A date that isn't one,
// or one renewal.CheckOpened refuses, is 400 and starts nothing.
func TestAdminSignInOpened(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, key := makeArea(t, do, console, "A")
	openSignIns(t, do, console, id, "codex")
	codexLogin(t, "paid@example.com")

	var started []provider.SignInOptions
	old := startAreaSignIn
	startAreaSignIn = func(agent string, o provider.SignInOptions) (provider.SignInState, error) {
		started = append(started, o)
		return provider.SignInState{ID: "si-opened", Agent: agent, State: "waiting", URL: "https://auth.openai.com/"}, nil
	}
	t.Cleanup(func() { startAreaSignIn = old })

	day := time.Now().AddDate(0, 0, -3).Format(time.DateOnly)
	for _, bad := range []string{"2026/11/01", "11-01", "1999-12-31", time.Now().AddDate(0, 0, 40).Format(time.DateOnly), "2026-11-01T00:00:00Z"} {
		if c, body := do("POST", AdminPath+"/signin", lanPeer, key, addKeyBody(map[string]any{"agent": "codex", "way": wayPasteURL, "opened": bad})); c != http.StatusBadRequest {
			t.Errorf("opened %q: %d %s", bad, c, body)
		}
	}
	if len(started) != 0 {
		t.Fatal("a refused date started a sign-in")
	}
	if c, body := do("POST", AdminPath+"/signin", lanPeer, key, addKeyBody(map[string]any{"agent": "codex", "way": wayPasteURL, "opened": day})); c != http.StatusOK || len(started) != 1 {
		t.Fatal(c, body)
	}
	st := started[0].Done(provider.SignInState{Agent: "codex", User: "paid@example.com", State: "done"})
	if st.State != "done" || st.Error != "" {
		t.Fatal("done:", st)
	}
	at := provider.AccountOpenedOf("codex", "paid@example.com")
	if at.IsZero() || at.In(time.Local).Format(time.DateOnly) != day {
		t.Fatalf("kept %v, sent %s", at, day)
	}
	c, body := do("GET", AdminPath+"/accounts", lanPeer, key, "")
	if c != 200 || !strings.Contains(body, `"opened":"`+day+`"`) {
		t.Fatal("listed:", c, body)
	}

	// none sent: none kept, as before
	codexLogin(t, "paid@example.com", "plain@example.com")
	if c, body := do("POST", AdminPath+"/signin", lanPeer, key, `{"agent":"codex","way":"paste-url"}`); c != http.StatusOK {
		t.Fatal(c, body)
	}
	started[1].Done(provider.SignInState{Agent: "codex", User: "plain@example.com", State: "done"})
	if at := provider.AccountOpenedOf("codex", "plain@example.com"); !at.IsZero() {
		t.Fatal("an opened date kept with none sent:", at)
	}
	// a conflict keeps none on the account it isn't the area's
	other, _ := makeArea(t, do, console, "B")
	if st := openOnDone(claimOnDone(other), time.Now().AddDate(0, 0, -1))(provider.SignInState{Agent: "codex", User: "plain@example.com", State: "done"}); st.State != "conflict" {
		t.Fatal("B on A's:", st)
	}
	if at := provider.AccountOpenedOf("codex", "plain@example.com"); !at.IsZero() {
		t.Fatal("an opened date kept on another area's account:", at)
	}
}

// codexLogin has Codex signed in to users (the last one current), as a
// sign-in would leave it.
func codexLogin(t *testing.T, users ...string) {
	t.Helper()
	jwt := func(claims any) string {
		b, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":     jwt(map[string]any{"email": user, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": user}}),
			"access_token": jwt(map[string]any{"exp": time.Now().Add(24 * time.Hour).Unix()}), "refresh_token": "fake-refresh", "account_id": user,
		}}
		b, _ := json.Marshal(auth)
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		provider.ForgetAccounts()
	}
	t.Cleanup(provider.ForgetAccounts)
}
