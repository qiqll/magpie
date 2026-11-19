package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// staleAreaCall is the call area id's admin key (secret key) was let in
// with, its key then rotated by a way that cancels nothing (the CLI's
// gateway-key rotate), as an import or a batch of keys under way meets
// it (T-260); and the new secret.
func staleAreaCall(t *testing.T, id, key string) (adminCall, string) {
	t.Helper()
	before, ok := access.Authenticate(key)
	if !ok {
		t.Fatal("the area's key")
	}
	a, err := area.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return adminCall{who: before, area: a}, rotateKeyOf(t, id)
}

// rotateKeyOf rotates area id's admin key as access.Update does it, and
// gives the new secret.
func rotateKeyOf(t *testing.T, id string) string {
	t.Helper()
	k, has, err := access.AreaKey(id)
	if err != nil || !has {
		t.Fatal("its key:", has, err)
	}
	secret, err := access.Update("rotate-key", access.Change{Key: k.ID})
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// A batch of keys let in before its area's key was rotated adds none
// after: each is read again before it is claimed (stillActs), the rest
// fail saying why, and nothing is written to the provider. One turned off
// meanwhile adds none either. The new secret's, and the console's for
// the area, add as before.
func TestAdminKeysAfterKeyChangedAddNothing(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, key := makeArea(t, do, console, "A", "plan")
	c, secret := staleAreaCall(t, id, key)
	s := New()
	batch := func(c adminCall, keys ...string) keysOut {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", AdminPath+"/accounts/keys", strings.NewReader(addKeyBody(map[string]any{"provider": "plan", "keys": keys})))
		s.adminAddKeys(w, r, c)
		var out keysOut
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		return out
	}
	out := batch(c, "sk-stale-0000000001", "sk-stale-0000000002")
	if out.Added != 0 || out.Failed != 2 || out.Results[0].Message != errKeyChanged.Error() || out.Results[1].Message != errKeyChanged.Error() {
		t.Fatalf("the old secret's batch: %+v", out)
	}
	p, _ := provider.Find("plan")
	for _, k := range []string{"sk-stale-0000000001", "sk-stale-0000000002"} {
		if p.HasKey(k) {
			t.Fatal("written to the provider:", k)
		}
	}
	if a, _ := area.Get(id); len(a.Accounts) != 0 {
		t.Fatalf("the area's: %+v", a.Accounts)
	}
	// the new secret's adds
	if code, out, body := addKeys(t, do, secret, map[string]any{"provider": "plan", "keys": []string{"sk-fresh-0000000001"}}); code != http.StatusOK || out.Added != 1 {
		t.Fatal("the new secret's:", code, body)
	}
	// the area turned off as the batch runs
	now, ok := access.Authenticate(secret)
	if !ok {
		t.Fatal("the new secret")
	}
	a, _ := area.Get(id)
	off := true
	if _, err := area.Update(id, area.Spec{Off: &off}); err != nil {
		t.Fatal(err)
	}
	if out := batch(adminCall{who: now, area: a}, "sk-off-00000000001"); out.Added != 0 || out.Results[0].Message != errAreaOff.Error() {
		t.Fatalf("with the area off: %+v", out)
	}
	if out := batch(adminCall{area: a, console: true}, "sk-console-0000001"); out.Added != 1 {
		t.Fatalf("the console's: %+v", out)
	}
}

// An import whose area key is rotated while it runs claims no account
// after (T-260): the key is read again before each account is admitted
// and again just before it is claimed, as the vendor was asked whose it
// is in between. Here the key is rotated as Anthropic is first asked, so
// those accounts are kept out at their claim and nothing of them is
// written; one let in before the rotate and read after asks Anthropic
// of none. None of them is counted as the area's failures (T-233), so
// the new secret's import goes on; with the area turned off it adds none
// either.
func TestAdminImportAfterKeyChangedClaimsNothing(t *testing.T) {
	do, console, _ := adminWorld(t)
	id, key := makeArea(t, do, console, "A")
	openSignIns(t, do, console, id, "claude")
	var once sync.Once
	var mu sync.Mutex
	asked, secret := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the key rotated as the import waits on the vendor: no sleep,
		// the ask itself is the moment
		once.Do(func() {
			// not rotateKeyOf: t.Fatal is the test's goroutine's alone
			k, has, err := access.AreaKey(id)
			var s string
			if err == nil && has {
				s, err = access.Update("rotate-key", access.Change{Key: k.ID})
			}
			if err != nil || !has {
				t.Error("rotating A's key:", has, err)
			}
			mu.Lock()
			secret = s
			mu.Unlock()
		})
		mu.Lock()
		asked++
		mu.Unlock()
		who, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "at-"), "-")
		json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"uuid": "u-" + who, "email": who + "@example.com"},
			"organization": map[string]any{"uuid": "o-" + who, "name": who + "'s", "organization_type": "claude_pro"}})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(provider.ClaudeProfileURLForTest(srv.URL))
	askedNow := func() int { mu.Lock(); defer mu.Unlock(); return asked }
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	s := New()
	imp := func(c adminCall, files ...string) importResult {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", AdminPath+"/accounts/import", strings.NewReader(`{"agent":"claude","files":[`+strings.Join(jsonEach(files...), ",")+`]}`))
		s.adminImport(w, r, c)
		var out importResult
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		return out
	}
	callOf := func(id, secret string) adminCall {
		t.Helper()
		who, ok := access.Authenticate(secret)
		if !ok {
			t.Fatal("the secret")
		}
		a, err := area.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		return adminCall{who: who, area: a}
	}
	failsLeft := func(c adminCall) int {
		left, _ := s.importFailsLeft(c, time.Now())
		return left
	}

	c := callOf(id, key)
	out := imp(c, claudeFile("at-mid", "rt-mid", time.Now().Add(time.Hour)), claudeFile("at-mid2", "rt-mid2", time.Now().Add(time.Hour)))
	if importStatuses(out) != "1:failed,2:failed" || out.Results[0].Message != errKeyChanged.Error() || out.Results[1].Message != errKeyChanged.Error() {
		t.Fatalf("rotated midway: %+v", out)
	}
	if a, _ := area.Get(id); len(a.Accounts) != 0 {
		t.Fatalf("the area's: %+v", a.Accounts)
	}
	if b, _ := os.ReadFile(logins); strings.Contains(string(b), "rt-mid") {
		t.Fatal("the account was written")
	}
	if n := failsLeft(c); n != importFailMost {
		t.Fatalf("the area may fail %d more, want %d: those kept out were counted", n, importFailMost)
	}
	mu.Lock()
	fresh := secret
	mu.Unlock()
	now := callOf(id, fresh)
	if out := imp(now, claudeFile("at-new", "rt-new", time.Now().Add(time.Hour))); importStatuses(out) != "1:added" {
		t.Fatalf("the new secret's: %+v", out)
	}

	// the area turned off: refused at admit, no ask
	off := true
	if _, err := area.Update(id, area.Spec{Off: &off}); err != nil {
		t.Fatal(err)
	}
	was := askedNow()
	if out := imp(now, claudeFile("at-off", "rt-off", time.Now().Add(time.Hour))); importStatuses(out) != "1:failed" || out.Results[0].Message != errAreaOff.Error() {
		t.Fatalf("the area off: %+v", out)
	}
	if n := askedNow() - was; n != 0 {
		t.Fatalf("Anthropic asked %d times for an area turned off", n)
	}

	// let in before a rotate, read after: refused at admit, no ask
	id2, key2 := makeArea(t, do, console, "B")
	openSignIns(t, do, console, id2, "claude")
	stale, _ := staleAreaCall(t, id2, key2)
	was = askedNow()
	if out := imp(stale, claudeFile("at-late", "rt-late", time.Now().Add(time.Hour)), claudeFile("at-late2", "rt-late2", time.Now().Add(time.Hour))); importStatuses(out) != "1:failed,2:failed" || out.Results[0].Message != errKeyChanged.Error() {
		t.Fatalf("the old secret's: %+v", out)
	}
	if n := askedNow() - was; n != 0 {
		t.Fatalf("Anthropic asked %d times for an import its key no longer acts for", n)
	}
	if a, _ := area.Get(id2); len(a.Accounts) != 0 {
		t.Fatalf("B's: %+v", a.Accounts)
	}
}

func jsonEach(ss ...string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		b, _ := json.Marshal(s)
		out[i] = string(b)
	}
	return out
}

// A plugin's key sign-in whose area key was rotated while the plugin
// saved claims nothing, and what the plugin saved for it is signed out
// again (SignOutSaved, as T-262 does): the key can be added fresh after.
func TestAdminPluginKeyAfterKeyChangedSignsOut(t *testing.T) {
	_, _, id, key, _ := pluginArea(t, wayPasteCode)
	c, _ := staleAreaCall(t, id, key)
	pp, _, on := areaPluginOf("fakeco")
	ms := areaMethods("fakeco", pp)
	i := slices.IndexFunc(ms, func(m adminPluginMethod) bool { return m.Way == wayAPIKey })
	if !on || i < 0 {
		t.Fatal("fakeco's key way")
	}
	_, status, code, msg := New().areaPluginKey(c, pp, ms[i], nil, "k-stale", nil, provider.SignInOptions{})
	if status != http.StatusUnauthorized || code != adminKeyRequired || msg != errKeyChanged.Error() {
		t.Fatal("the old secret's key:", status, code, msg)
	}
	if a, _ := area.Get(id); len(a.Accounts) != 0 {
		t.Fatalf("the area's: %+v", a.Accounts)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, "k-stale", plugin.NewAccount); err != nil {
		t.Fatal("the key's account was left saved:", err)
	}
}
