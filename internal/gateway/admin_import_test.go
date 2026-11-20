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

// fakeGoogleSignIns answers Google's refresh of "1//<who>[-n]" with access token
// "at-<who>[-n]", and userinfo for it as who@example.com's.
func fakeGoogleSignIns(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			r.ParseForm()
			rt := r.Form.Get("refresh_token")
			if !strings.HasPrefix(rt, "1//") || strings.HasPrefix(rt, "1//dead") {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad token ` + rt + `"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + strings.TrimPrefix(rt, "1//"), "expires_in": 3600})
		case r.URL.Path == "/userinfo":
			at := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")
			who, _, _ := strings.Cut(at, "-")
			json.NewEncoder(w).Encode(map[string]string{"email": who + "@example.com"})
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			w.Write([]byte(`{"cloudaicompanionProject":"found-proj","currentTier":{"id":"standard-tier","name":"Gemini Code Assist Standard"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(provider.GoogleForTest(srv.URL))
}

// An area imports Gemini CLI's sign-ins as the window's reader reads them
// (T-232): each is named by Google's userinfo, never the file's email, and
// is the area's; one magpie has, the owner's by Google's answer or its
// refresh token, is a conflict that names no one and writes no token.
func TestAdminImportGemini(t *testing.T) {
	do, console, _ := adminWorld(t)
	logs := logsOf(t)
	fakeGoogleSignIns(t)
	aID, a := makeArea(t, do, console, "A")
	openSignIns(t, do, console, aID, "gemini")
	// the owner's, by the window's import
	if rs, err := provider.ImportGoogleAccounts(context.Background(), "gemini", []string{"1//owner"}); err != nil || len(rs) != 1 || rs[0].Status != "added" {
		t.Fatal("the owner's:", rs, err)
	}
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")

	cpa := `{"type":"gemini","email":"file-says@example.com","project_id":"file-proj","token":{"access_token":"ya29.x","refresh_token":"1//ann"}}`
	c, out, body := importOf(t, do, a, map[string]any{"agent": "gemini", "files": []string{
		cpa,
		`{"access_token":"ya29.y","refresh_token":"1//owner-2","token_type":"Bearer","expiry_date":1}`,
		"1//owner\n1//dead",
	}})
	if c != http.StatusOK || importStatuses(out) != "1:added,2:conflict,3:conflict,4:failed" || out.Added != 1 || out.Conflict != 2 || out.Failed != 1 {
		t.Fatal("A's import:", c, body)
	}
	if acc := out.Results[0].Account; acc == nil || acc.Name != "ann@example.com" || acc.Provider != "gemini" || acc.Kind != "account" {
		t.Fatal("ann's account:", body)
	}
	if ar, _ := area.Get(aID); len(ar.Accounts) != 1 || !ownsRef(ar, out.Results[0].Account.ID) {
		t.Fatalf("A's accounts: %+v", ar.Accounts)
	}
	for _, s := range []string{"owner@", "file-says", "1//", "at-", "ya29"} {
		if strings.Contains(body, s) || strings.Contains(logs(), s) {
			t.Errorf("%q in the answer or the log: %s\n%s", s, body, logs())
		}
	}
	if !strings.Contains(logs(), "imported gemini: added 1, conflict 2, failed 1") {
		t.Error("the log:", logs())
	}
	after, _ := os.ReadFile(logins)
	if strings.Contains(string(after), "1//owner-2") || !strings.Contains(string(after), `"1//owner"`) || strings.Contains(string(after), "file-says") {
		t.Fatalf("the owner's account was written over, or the file's name kept: %s", after)
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
		n := asked
		mu.Unlock()
		who := strings.TrimPrefix(in["refresh_token"], "r-")
		json.NewEncoder(w).Encode(map[string]any{"id_token": importIDToken(who), "access_token": "a-" + who, "refresh_token": "n-" + who + strconv.Itoa(n)})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(provider.CodexTokenURLForTest(srv.URL))
	aID, a := makeArea(t, do, console, "A")
	openSignIns(t, do, console, aID, "codex", "claude")
	codexLogin(t, "live@example.com")

	// a file names whose it is (T-234): a refresh token alone isn't taken
	file := func(rt, who string) string {
		return `{"tokens":{"refresh_token":"` + rt + `","id_token":"` + importIDToken(who) + `"}}`
	}
	c, out, body := importOf(t, do, a, map[string]any{"agent": "codex", "files": []string{file("r-cid", "cid"), file("r-dan", "dan")}, "proxy": map[string]any{"direct": true}, "opened": "2026-11-01"})
	if c != http.StatusOK || importStatuses(out) != "1:added,2:added" {
		t.Fatal("import:", c, body)
	}
	acc := out.Results[0].Account
	if acc == nil || acc.Name != "cid@example.com" || acc.Proxy == nil || !acc.Proxy.Direct || acc.Opened != "2026-11-01" {
		t.Fatal("the account:", body)
	}
	if strings.Contains(body, "r-cid") || strings.Contains(body, "n-cid") || strings.Contains(body, "a-cid") {
		t.Fatal("a token in the answer:", body)
	}
	// the same account by another sign-in: a conflict, magpie's kept; a
	// forged id_token saying it is cid's isn't believed
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	if c, out, body := importOf(t, do, a, map[string]any{"agent": "codex", "files": []string{`{"tokens":{"refresh_token":"r-cid","id_token":"` + importIDToken("dan") + `"}}`}}); c != http.StatusOK || importStatuses(out) != "1:conflict" || strings.Contains(body, "cid@") || strings.Contains(body, "dan@") {
		t.Fatal("again:", c, body)
	}
	if b, _ := os.ReadFile(logins); !strings.Contains(string(b), "n-cid") {
		t.Fatal("cid's sign-in was written over")
	}
	// an old export of an account magpie has is a conflict before ChatGPT
	// is asked, and a refresh token alone fails, saying why (T-234)
	mu.Lock()
	was := asked
	mu.Unlock()
	if c, out, body := importOf(t, do, a, map[string]any{"agent": "codex", "files": []string{file("r-cid-old", "cid"), "r-eve"}}); c != http.StatusOK || importStatuses(out) != "1:conflict,2:failed" ||
		!strings.Contains(body, "nothing in it names whose account it is") || strings.Contains(body, "cid@") || strings.Contains(body, "r-eve") {
		t.Fatal("an old export:", c, body)
	}
	mu.Lock()
	if asked != was {
		t.Errorf("ChatGPT was asked %d times for an account magpie has or a refresh token alone", asked-was)
	}
	mu.Unlock()

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
	for name, running := range map[string]map[string]bool{
		// false: the area's own import, not the console's (T-235)
		"a second import of the area": {aID: false},
		"as many as magpie takes":     {"x1": false, "x2": false, "x3": false, "x4": false},
	} {
		areaImports.Lock()
		areaImports.m = running
		areaImports.Unlock()
		c, body = do("POST", AdminPath+"/accounts/import", lanPeer, a, big)
		areaImports.Lock()
		areaImports.m = nil
		areaImports.Unlock()
		if c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyImports) {
			t.Fatal(name, "(its body not read):", c, body[:min(len(body), 200)])
		}
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

// deadClaude are n Claude Code sign-ins Anthropic refuses, each its own.
func deadClaude(tag string, n int) []string {
	var files []string
	for i := range n {
		files = append(files, claudeFile("at-dead-"+tag+strconv.Itoa(i), "rt-dead-"+tag+strconv.Itoa(i), time.Now().Add(time.Hour)))
	}
	return files
}

// Accounts that fail count to the area's line (T-233): past it the
// import's rest fail without their vendor asked, saying why, and so does
// the next import's, a good account's too. No token reaches the log.
func TestAdminImportFailLine(t *testing.T) {
	do, console, _ := adminWorld(t)
	logs := logsOf(t)
	asked := fakeAnthropic(t)
	aID, a := makeArea(t, do, console, "A")
	openSignIns(t, do, console, aID, "claude")

	c, out, body := importOf(t, do, a, map[string]any{"agent": "claude", "files": deadClaude("a", importFailMost+3)})
	if c != http.StatusOK || out.Failed != importFailMost+3 || out.Added != 0 {
		t.Fatal("past the line:", c, body)
	}
	if n := asked(); n != importFailMost {
		t.Fatalf("Anthropic asked %d times, want %d: none past the line", n, importFailMost)
	}
	for _, r := range out.Results[importFailMost:] {
		if r.Message != importFailsHeld {
			t.Fatal("a row past the line:", body)
		}
	}
	if out.Results[importFailMost-1].Message == importFailsHeld {
		t.Fatal("the line's own row was held:", body)
	}
	c, out, body = importOf(t, do, a, map[string]any{"agent": "claude", "files": []string{claudeFile("at-ann", "rt-ann", time.Now().Add(time.Hour))}})
	if c != http.StatusOK || importStatuses(out) != "1:failed" || out.Results[0].Message != importFailsHeld {
		t.Fatal("the next import:", c, body)
	}
	if n := asked(); n != importFailMost {
		t.Fatalf("Anthropic asked %d times after the line", n)
	}
	if !strings.Contains(logs(), "imports had 20 accounts fail") || strings.Contains(logs(), "at-dead") {
		t.Error("the log:", logs())
	}
}

// The console's imports for areas spend its own total too (T-233,
// shared with T-222's): once its failed accounts across areas reach it,
// a fresh area's import by the console asks no vendor, while that
// area's own key still may.
func TestAdminImportConsoleFailTotal(t *testing.T) {
	do, console, _ := adminWorld(t)
	asked := fakeAnthropic(t)
	byConsole := func(id string, files []string) (int, importResult, string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"agent": "claude", "files": files})
		c, body := do("POST", AdminPath+"/areas/"+id+"/accounts/import", lanPeer, console, string(b))
		var out importResult
		json.Unmarshal([]byte(body), &out)
		return c, out, body
	}
	spent := 0
	for i := 0; spent < consoleImportFailMost; i++ {
		id, _ := makeArea(t, do, console, "败"+strconv.Itoa(i))
		openSignIns(t, do, console, id, "claude")
		n := min(importFailMost-1, consoleImportFailMost-spent)
		if c, out, body := byConsole(id, deadClaude(strconv.Itoa(i)+"-", n)); c != http.StatusOK || out.Failed != n || asked() != spent+n {
			t.Fatal("the console's import:", c, asked(), body)
		}
		spent += n
	}
	id, key := makeArea(t, do, console, "新")
	openSignIns(t, do, console, id, "claude")
	c, out, body := byConsole(id, []string{claudeFile("at-ann", "rt-ann", time.Now().Add(time.Hour))})
	if c != http.StatusOK || importStatuses(out) != "1:failed" || out.Results[0].Message != importFailsHeld || asked() != spent {
		t.Fatal("past the console's total:", c, asked(), body)
	}
	if c, out, body := importOf(t, do, key, map[string]any{"agent": "claude", "files": []string{claudeFile("at-ann", "rt-ann", time.Now().Add(time.Hour))}}); c != http.StatusOK || importStatuses(out) != "1:added" {
		t.Fatal("the area's own import:", c, body)
	}
}

// A plugin's keys its sign-in refuses count to the same line (T-233):
// past it the rest fail before areaPluginKey is called. The fake takes
// any key, so its browser method is offered as the key way: every key
// sign-in through it fails, as a refused one does.
func TestAdminImportFailLinePluginKeys(t *testing.T) {
	do, _, _, key, spec := pluginArea(t, wayPasteCode)
	offerPlugin(t, areaPlugin{spec, []areaPluginMethod{{"oauth", "Browser", wayAPIKey}}})
	was := keyImports["fakeco"]
	keyImports["fakeco"] = func(files []string) []string { return strings.Fields(strings.Join(files, " ")) }
	t.Cleanup(func() {
		if was == nil {
			delete(keyImports, "fakeco")
		} else {
			keyImports["fakeco"] = was
		}
	})
	var keys []string
	for i := range importFailMost + 2 {
		keys = append(keys, "k-bad"+strconv.Itoa(i))
	}
	c, out, body := importOf(t, do, key, map[string]any{"agent": "fakeco", "files": []string{strings.Join(keys, " ")}})
	if c != http.StatusOK || out.Failed != importFailMost+2 {
		t.Fatal("past the line:", c, body)
	}
	for i, r := range out.Results {
		if held := r.Message == importFailsHeld; held != (i >= importFailMost) {
			t.Fatalf("row %d: %s", i+1, body)
		}
	}
}

// The console's imports take adminConsoleImportsMost of magpie's
// adminImportsMost at most (T-235): with two running for areas A and B,
// the console's import for C is 429 at the door, its body not read,
// while C's own key still takes a slot, and once they end the console's
// next is taken again.
func TestAdminImportConsoleShare(t *testing.T) {
	do, console, _ := adminWorld(t)
	arrived := make(chan string)
	release := make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		who := strings.TrimPrefix(in["refresh_token"], "r-")
		select {
		case arrived <- who:
		case <-r.Context().Done():
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id_token": importIDToken(who), "access_token": "a-" + who, "refresh_token": "n-" + who})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(let)
	t.Cleanup(provider.CodexTokenURLForTest(srv.URL))
	ids := map[string]string{}
	keys := map[string]string{}
	for _, n := range []string{"a", "b", "c"} {
		ids[n], keys[n] = makeArea(t, do, console, n)
		openSignIns(t, do, console, ids[n], "codex")
	}
	byConsole := func(id, body string) (int, string) {
		return do("POST", AdminPath+"/areas/"+id+"/accounts/import", lanPeer, console, body)
	}
	codes := make(chan string, 2)
	pending := 2
	// a failure midway still ends both imports before the test does
	t.Cleanup(func() {
		let()
		for ; pending > 0; pending-- {
			<-codes
		}
	})
	for _, n := range []string{"a", "b"} {
		go func() {
			b, _ := json.Marshal(map[string]any{"agent": "codex", "files": []string{`{"tokens":{"refresh_token":"r-` + n + `","id_token":"` + importIDToken(n) + `"}}`}})
			c, body := byConsole(ids[n], string(b))
			codes <- strconv.Itoa(c) + " " + body
		}()
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case who := <-arrived:
			seen[who] = true
		case r := <-codes:
			pending--
			t.Fatal("an import ended before the vendor was asked:", r)
		}
	}
	if !seen["a"] || !seen["b"] {
		t.Fatal("the vendor was asked for", seen)
	}
	big := `{"agent":"codex","files":["` + strings.Repeat("r", 8<<20) + `"]}`
	c, body := byConsole(ids["c"], big)
	if c != http.StatusTooManyRequests || !strings.Contains(body, adminTooManyImports) || !strings.Contains(body, "the console") {
		t.Fatal("the console's third (its body not read):", c, body[:min(len(body), 200)])
	}
	if c, body := do("POST", AdminPath+"/accounts/import", lanPeer, keys["c"], big); c != http.StatusRequestEntityTooLarge {
		t.Fatal("C's own import, the console's two running:", c, body[:min(len(body), 200)])
	}
	let()
	for pending > 0 {
		r := <-codes
		pending--
		if !strings.HasPrefix(r, "200 ") || !strings.Contains(r, `"added":1`) {
			t.Fatal("a held import:", r)
		}
	}
	if c, body := byConsole(ids["c"], big); c != http.StatusRequestEntityTooLarge {
		t.Fatal("the console's next, its two ended:", c, body[:min(len(body), 200)])
	}
	areaImports.Lock()
	left := len(areaImports.m)
	areaImports.Unlock()
	if left != 0 {
		t.Fatal("slots left taken:", left)
	}
	// with the console's two and an area's own running, another area's
	// own takes the fourth; at four, both are told magpie is full
	for _, x := range []struct {
		name    string
		running map[string]bool
		secret  string
		code    int
		says    string
	}{
		{"C's own beside the console's two and B's own", map[string]bool{"x1": true, "x2": true, ids["b"]: false}, keys["c"], http.StatusRequestEntityTooLarge, ""},
		{"the console at four", map[string]bool{"x1": true, "x2": false, "x3": false, "x4": false}, console, http.StatusTooManyRequests, "magpie is running as many"},
		{"C's own at four", map[string]bool{"x1": true, "x2": true, "x3": false, "x4": false}, keys["c"], http.StatusTooManyRequests, "magpie is running as many"},
	} {
		areaImports.Lock()
		areaImports.m = x.running
		areaImports.Unlock()
		path := AdminPath + "/accounts/import"
		if x.secret == console {
			path = AdminPath + "/areas/" + ids["c"] + "/accounts/import"
		}
		c, body := do("POST", path, lanPeer, x.secret, big)
		areaImports.Lock()
		areaImports.m = nil
		areaImports.Unlock()
		if c != x.code || !strings.Contains(body, x.says) {
			t.Errorf("%s: %d %s", x.name, c, body[:min(len(body), 200)])
		}
	}
}
