package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// importOf posts an import for the area with key secret.
func importOf(t *testing.T, do adminDo, secret string, in map[string]any) (int, importResult, string) {
	t.Helper()
	b, _ := json.Marshal(in)
	c, body := do("POST", AdminPath+"/accounts/import", lanPeer, secret, string(b))
	var out importResult
	json.Unmarshal([]byte(body), &out)
	return c, out, body
}

func importStatuses(out importResult) string {
	var s []string
	for _, r := range out.Results {
		s = append(s, strconv.Itoa(r.Index)+":"+r.Status)
	}
	return strings.Join(s, ",")
}

// claudeFile is a Claude Code .credentials.json, with an email beside it
// that isn't the account's.
func claudeFile(at, rt string, expires time.Time) string {
	return `{"claudeAiOauth":{"accessToken":"` + at + `","refreshToken":"` + rt + `","expiresAt":` +
		strconv.FormatInt(expires.UnixMilli(), 10) + `},"email":"file-says@example.com"}`
}

// ownersClaude adds owner@example.com's Claude account as magpie's own,
// no area's, Claude Code here left as it is.
func ownersClaude() ([]provider.ImportedAccount, error) {
	return provider.ImportLoginsWith(context.Background(), "claude", []string{claudeFile("at-owner", "rt-owner", time.Now().Add(time.Hour))}, false, provider.ImportOptions{NewOnly: true})
}

// fakeAnthropic answers Claude Code's profile ask for access token
// "at-<who>[-n]" as who@example.com's, and counts each ask.
func fakeAnthropic(t *testing.T) func() int {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		at := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(at, "at-") || strings.HasPrefix(at, "at-dead") {
			w.WriteHeader(http.StatusUnauthorized)
			io := `{"error":{"message":"invalid token ` + at + `"}}`
			w.Write([]byte(io))
			return
		}
		who, _, _ := strings.Cut(strings.TrimPrefix(at, "at-"), "-")
		json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"uuid": "u-" + who, "email": who + "@example.com"},
			"organization": map[string]any{"uuid": "o-" + who, "name": who + "'s", "organization_type": "claude_pro"}})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(provider.ClaudeProfileURLForTest(srv.URL))
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// An area imports Claude sign-ins: each is named by Anthropic's answer,
// never the file's email, and is the area's; one magpie has (the owner's,
// another area's) is a conflict that names nobody and writes nothing,
// and one whose access token has run out fails without Anthropic asked.
// No token reaches the answer or the log.
func TestAdminImportClaude(t *testing.T) {
	do, console, _ := adminWorld(t)
	logs := logsOf(t)
	asked := fakeAnthropic(t)
	aID, a := makeArea(t, do, console, "A")
	bID, b := makeArea(t, do, console, "B")
	openSignIns(t, do, console, aID, "claude")
	openSignIns(t, do, console, bID, "claude")
	// the owner's, by the window's import
	if rs, err := ownersClaude(); err != nil || rs[0].Status != "added" {
		t.Fatal("the owner's:", rs, err)
	}
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	before, _ := os.ReadFile(logins)

	c, out, body := importOf(t, do, a, map[string]any{"agent": "claude", "files": []string{
		claudeFile("at-ann", "rt-ann", time.Now().Add(time.Hour)),
		claudeFile("at-owner-2", "rt-owner-2", time.Now().Add(time.Hour)),
		claudeFile("at-dead", "rt-dead", time.Now().Add(-time.Hour)),
		claudeFile("at-dead2", "rt-dead2", time.Now().Add(time.Hour)),
	}})
	if c != http.StatusOK || importStatuses(out) != "1:added,2:conflict,3:failed,4:failed" || out.Added != 1 || out.Conflict != 1 || out.Failed != 2 {
		t.Fatal("A's import:", c, body)
	}
	if acc := out.Results[0].Account; acc == nil || acc.Name != "ann@example.com" || acc.Provider != "claude" || acc.Kind != "account" {
		t.Fatal("ann's account:", body)
	}
	if out.Results[1].Message != "this account is in magpie already" {
		t.Fatal("a conflict says:", body)
	}
	for _, s := range []string{"owner@", "file-says", "at-", "rt-"} {
		if strings.Contains(body, s) || strings.Contains(logs(), s) {
			t.Errorf("%q in the answer or the log: %s\n%s", s, body, logs())
		}
	}
	if !strings.Contains(logs(), "imported claude: added 1, conflict 1, failed 2") {
		t.Error("the log:", logs())
	}
	if n := asked(); n != 4 {
		t.Fatalf("Anthropic asked %d times, want 4: the owner's, and 3 of A's (not the one run out)", n)
	}
	if ar, _ := area.Get(aID); len(ar.Accounts) != 1 || !ownsRef(ar, out.Results[0].Account.ID) {
		t.Fatalf("A's accounts: %+v", ar.Accounts)
	}
	after, _ := os.ReadFile(logins)
	if strings.Contains(string(after), "rt-owner-2") || !strings.Contains(string(before), "rt-owner") || !strings.Contains(string(after), "rt-owner") {
		t.Fatal("the owner's account was written over")
	}
	// another area's is a conflict for B, and stays A's
	c, out, body = importOf(t, do, b, map[string]any{"agent": "claude", "files": []string{claudeFile("at-ann-2", "rt-ann-2", time.Now().Add(time.Hour))}})
	if c != http.StatusOK || importStatuses(out) != "1:conflict" || strings.Contains(body, "ann@") {
		t.Fatal("B's import of A's:", c, body)
	}
	if ar, _ := area.Get(aID); len(ar.Accounts) != 1 {
		t.Fatal("A lost its account")
	}
	if ar, _ := area.Get(bID); len(ar.Accounts) != 0 {
		t.Fatal("B has A's account")
	}
}

// importIDToken is a ChatGPT id_token naming who@example.com.
func importIDToken(who string) string {
	b, _ := json.Marshal(map[string]any{"email": who + "@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + who}})
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// An area's ChatGPT import goes through the proxy picked, puts it on the
// account with the opened date, and is held to the route's limits: 8 MB
// (413), 50 files, one import at a time (429 too_many_imports), the
// area's conflicts (429 at the door), and a subscription not opened is 404.
func TestAdminImportCodexLimits(t *testing.T) {
	do, console, _ := adminWorld(t)
	var mu sync.Mutex
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		asked++
		mu.Unlock()
		who := strings.TrimPrefix(in["refresh_token"], "r-")
		json.NewEncoder(w).Encode(map[string]any{"id_token": importIDToken(who), "access_token": "a-" + who, "refresh_token": "n-" + who})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(provider.CodexTokenURLForTest(srv.URL))
	aID, a := makeArea(t, do, console, "A")
	openSignIns(t, do, console, aID, "codex", "claude")
	codexLogin(t, "live@example.com")

	c, out, body := importOf(t, do, a, map[string]any{"agent": "codex", "files": []string{"r-cid\nr-dan"}, "proxy": map[string]any{"direct": true}, "opened": "2026-11-01"})
	if c != http.StatusOK || importStatuses(out) != "1:added,2:added" {
		t.Fatal("import:", c, body)
	}
	acc := out.Results[0].Account
	if acc == nil || acc.Name != "cid@example.com" || acc.Proxy == nil || !acc.Proxy.Direct || acc.Opened != "2026-11-01" {
		t.Fatal("the account:", body)
	}
	if strings.Contains(body, "r-cid") || strings.Contains(body, "n-cid") {
		t.Fatal("a token in the answer:", body)
	}
	// the same again: conflicts, nothing asked of ChatGPT
	mu.Lock()
	asked = 0
	mu.Unlock()
	if c, out, body := importOf(t, do, a, map[string]any{"agent": "codex", "files": []string{`{"tokens":{"refresh_token":"r-x","id_token":"` + importIDToken("cid") + `"}}`}}); c != http.StatusOK || importStatuses(out) != "1:conflict" {
		t.Fatal("again:", c, body)
	}
	if mu.Lock(); asked != 0 {
		mu.Unlock()
		t.Fatal("an account magpie has was refreshed")
	} else {
		mu.Unlock()
	}

	for _, x := range []struct {
		name string
		in   map[string]any
		code int
	}{
		{"not opened", map[string]any{"agent": "antigravity", "files": []string{"1//a"}}, http.StatusNotFound},
		{"no import", map[string]any{"agent": "copilot", "files": []string{"x"}}, http.StatusNotFound},
		{"no files", map[string]any{"agent": "codex", "files": []string{}}, http.StatusBadRequest},
		{"51 files", map[string]any{"agent": "codex", "files": make([]string, 51)}, http.StatusBadRequest},
		{"accessOnly for Claude", map[string]any{"agent": "claude", "files": []string{"x"}, "accessOnly": true}, http.StatusBadRequest},
		{"a bad opened", map[string]any{"agent": "codex", "files": []string{"r-e"}, "opened": "11/01"}, http.StatusBadRequest},
		{"another area's IP", map[string]any{"agent": "codex", "files": []string{"r-e"}, "proxy": map[string]any{"ip": "203.0.113.9", "id": "px_x"}}, http.StatusNotFound},
		{"201 accounts", map[string]any{"agent": "codex", "files": []string{strings.Repeat("r-many\n", 201)}}, http.StatusBadRequest},
	} {
		if c, _, body := importOf(t, do, a, x.in); c != x.code {
			t.Errorf("%s: %d %s", x.name, c, body)
		}
	}
	big := `{"agent":"codex","files":["` + strings.Repeat("r", 8<<20) + `"]}`
	if c, body := do("POST", AdminPath+"/accounts/import", lanPeer, a, big); c != http.StatusRequestEntityTooLarge || !strings.Contains(body, adminTooLarge) {
		t.Fatal("over 8 MB:", c, body[:min(len(body), 200)])
	}
	// the other routes keep their 64 KB
	if c, _ := do("POST", AdminPath+"/accounts/key", lanPeer, a, `{"provider":"plan","key":"`+strings.Repeat("k", 70<<10)+`"}`); c != http.StatusBadRequest {
		t.Fatal("accounts/key over 64 KB:", c)
	}
	areaImports.Lock()
	areaImports.m = map[string]bool{aID: true}
	areaImports.Unlock()
	c, body = do("POST", AdminPath+"/accounts/import", lanPeer, a, `{"agent":"codex","files":["r-f"]}`)
	areaImports.Lock()
	areaImports.m = nil
	areaImports.Unlock()
	if c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyImports) {
		t.Fatal("a second import:", c, body)
	}
}

// Conflicts count to the area's line: the line reached midway fails the
// rest, each saying why, and the next import is 429 at the door.
func TestAdminImportConflictLine(t *testing.T) {
	do, console, _ := adminWorld(t)
	fakeAnthropic(t)
	aID, a := makeArea(t, do, console, "A")
	openSignIns(t, do, console, aID, "claude")
	if rs, err := ownersClaude(); err != nil || rs[0].Status != "added" {
		t.Fatal("the owner's:", rs, err)
	}
	var files []string
	for i := range conflictMost + 2 {
		files = append(files, claudeFile("at-owner-"+strconv.Itoa(i), "rt-o"+strconv.Itoa(i), time.Now().Add(time.Hour)))
	}
	c, out, body := importOf(t, do, a, map[string]any{"agent": "claude", "files": files})
	if c != http.StatusOK || out.Conflict != conflictMost || out.Failed != 2 {
		t.Fatal("past the line:", c, body)
	}
	last := out.Results[len(out.Results)-1]
	if last.Status != keyFailed || last.Message != importConflictsHeld {
		t.Fatal("the last:", body)
	}
	if c, body := do("POST", AdminPath+"/accounts/import", lanPeer, a, `{"agent":"claude","files":["x"]}`); c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyConflicts) {
		t.Fatal("at the door:", c, body)
	}
}

// heldFrom leaves out a vendor's answer that says back part of a file's
// token, and keeps magpie's own reasons.
func TestAdminImportHeldFrom(t *testing.T) {
	files := []string{`{"refresh_token":"rt_9f8e7d6c5b4a3921"}`}
	if got := heldFrom("ChatGPT refused rt_9f8e7d6c5b4a3921 (400)", files); strings.Contains(got, "rt_9f8e") {
		t.Fatal("held:", got)
	}
	for _, own := range []string{"no refresh_token in it", "in the files twice", "an area has at most 500 accounts"} {
		if got := heldFrom(own, files); got != own {
			t.Errorf("%q said as %q", own, got)
		}
	}
}

// A plugin's keys are imported through its own key sign-in to a new
// account only (Factory's fk- keys): a new key is the area's, with the
// proxy picked; one the owner has is a conflict; opened is refused; the
// plugin turned off is 409 plugin_unavailable.
func TestAdminImportPluginKeys(t *testing.T) {
	do, _, id, key, spec := pluginArea(t, wayPasteCode)
	was := keyImports["fakeco"]
	keyImports["fakeco"] = func(files []string) []string {
		var out []string
		for _, f := range files {
			out = append(out, strings.Fields(f)...)
		}
		return out
	}
	t.Cleanup(func() {
		if was == nil {
			delete(keyImports, "fakeco")
		}
	})
	if _, err := plugin.APIKey(context.Background(), "fakeco", 0, nil, "k-owner", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	c, out, body := importOf(t, do, key, map[string]any{"agent": "fakeco", "files": []string{"k-imp1 k-owner"}, "proxy": map[string]any{"direct": true}})
	if c != http.StatusOK || importStatuses(out) != "1:added,2:conflict" || strings.Contains(body, "k-imp1") || strings.Contains(body, "k-owner") {
		t.Fatal("import:", c, body)
	}
	acc := out.Results[0].Account
	if acc == nil || acc.Proxy == nil || !acc.Proxy.Direct {
		t.Fatal("the account:", body)
	}
	if a, _ := area.Get(id); len(a.Accounts) != 1 || !ownsRef(a, acc.ID) {
		t.Fatalf("the area's: %+v", a.Accounts)
	}
	if c, _, body := importOf(t, do, key, map[string]any{"agent": "fakeco", "files": []string{"k-2"}, "opened": "2026-11-01"}); c != http.StatusBadRequest {
		t.Fatal("keys with opened:", c, body)
	}
	if err := plugin.SetOff(spec, true); err != nil {
		t.Fatal(err)
	}
	if c, _, body := importOf(t, do, key, map[string]any{"agent": "fakeco", "files": []string{"k-3"}}); c != http.StatusConflict || !strings.Contains(body, adminPluginUnavailable) {
		t.Fatal("the plugin off:", c, body)
	}
}
