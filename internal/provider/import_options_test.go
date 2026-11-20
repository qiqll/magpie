package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeImportRefresh answers ChatGPT's token endpoint for refresh tokens
// "r-<who>" with who@example.com's sign-in, counting each asked.
func fakeImportRefresh(t *testing.T) map[string]int {
	t.Helper()
	var mu sync.Mutex
	asked := map[string]int{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		rt := body["refresh_token"]
		mu.Lock()
		asked[rt]++
		mu.Unlock()
		// r-kept2 is kept's sign-in made again
		who := strings.TrimSuffix(strings.TrimPrefix(rt, "r-"), "2")
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": who + "@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-" + who}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "n-" + strings.TrimPrefix(rt, "r-"),
		})
	}))
	t.Cleanup(fake.Close)
	old := codexTokenURL
	t.Cleanup(func() { codexTokenURL = old })
	codexTokenURL = fake.URL
	return asked
}

// An area's import (NewOnly) adds a new ChatGPT account, claimed before it
// is written, and tells one magpie has as a conflict: no name, nothing of
// it written. The file's own id_token is never taken for whose it is, only
// to refuse before ChatGPT is asked (T-234). The window's import of the
// same file still renews it ("updated").
func TestImportNewOnlyCodex(t *testing.T) {
	home := claudeHome(t)
	asked := fakeImportRefresh(t)
	codexSignIn(t, home, "live@example.com", "r-live")
	if _, err := ImportLogins(context.Background(), "codex", []string{`{"type":"codex","refresh_token":"r-kept"}`}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(loginsPath())
	asked["r-kept"] = 0

	var claimed []string
	o := ImportOptions{NewOnly: true, Claim: func(agent, user string) error {
		claimed = append(claimed, agent+":"+user)
		return nil
	}}
	idOf := func(who string) string {
		return fakeJWT(map[string]any{"email": who + "@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-" + who}})
	}
	// kept's file has another refresh token now, its id_token says whose
	again := `{"type":"codex","refresh_token":"r-kept2","id_token":"` + idOf("kept") + `","account_id":"acct-kept"}`
	rs, err := ImportLoginsWith(context.Background(), "codex", []string{
		`{"type":"codex","refresh_token":"r-new","id_token":"` + idOf("file-says") + `","email":"file-says@example.com"}`,
		`{"type":"codex","refresh_token":"r-live"}`,
		again,
	}, false, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := importStatuses(rs); got != "new@example.com:added,:conflict,:conflict" {
		t.Fatalf("got %s", got)
	}
	for _, r := range rs[1:] {
		if r.Error != ErrKnownAccount.Error() {
			t.Errorf("a conflict says %q", r.Error)
		}
	}
	if asked["r-live"] != 0 || asked["r-kept2"] != 0 {
		t.Fatalf("a refresh token magpie has, or one of an account it has, was refreshed: %v", asked)
	}
	if len(claimed) != 1 || claimed[0] != "codex:new@example.com" {
		t.Fatalf("claimed %v", claimed)
	}
	after, _ := os.ReadFile(loginsPath())
	if !bytes.Contains(after, []byte("new@example.com")) || bytes.Contains(after, []byte("n-kept2")) || !bytes.Contains(after, []byte("n-kept")) {
		t.Fatal("logins.json after:", string(after))
	}
	if !bytes.Contains(before, []byte("n-kept")) {
		t.Fatal("kept wasn't saved first:", string(before))
	}

	// a claim refused keeps the account out: nothing written
	o.Claim = func(string, string) error { return ErrKnownAccount }
	rs, _ = ImportLoginsWith(context.Background(), "codex", []string{`{"type":"codex","refresh_token":"r-other","id_token":"` + idOf("other") + `"}`}, false, o)
	if got := importStatuses(rs); got != ":conflict" {
		t.Fatalf("claim refused: %s", got)
	}
	if b, _ := os.ReadFile(loginsPath()); bytes.Contains(b, []byte("other@example.com")) {
		t.Fatal("an unclaimed account was written")
	}

	// the window's import is as it was
	rs, _ = ImportLogins(context.Background(), "codex", []string{again})
	if got := importStatuses(rs); got != "kept@example.com:updated" {
		t.Fatalf("the window's import: %s", got)
	}
}

// An area's ChatGPT import judges the account a file names before ChatGPT
// is asked (T-234): an old export of an account magpie has, by its
// id_token or its access token's claims, is a conflict with its refresh
// token never sent, and a sign-in that is a refresh token alone fails,
// saying why. Another workspace of the same email is still new. The
// window's import of a refresh token alone is as it was.
func TestImportNewOnlyCodexJudgedBeforeRefresh(t *testing.T) {
	home := claudeHome(t)
	asked := fakeImportRefresh(t)
	codexSignIn(t, home, "live@example.com", "r-live")
	if rs, err := ImportLogins(context.Background(), "codex", []string{`{"type":"codex","refresh_token":"r-owner"}`}); err != nil || importStatuses(rs) != "owner@example.com:added" {
		t.Fatal(importStatuses(rs), err)
	}
	before, _ := os.ReadFile(loginsPath())
	o := ImportOptions{NewOnly: true, Claim: func(string, string) error { return nil }}
	id := func(who, ws string) string {
		return fakeJWT(map[string]any{"email": who + "@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": ws}})
	}
	at := fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "https://api.openai.com/profile": map[string]any{"email": "OWNER@example.com"},
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-owner"}})
	rs, err := ImportLoginsWith(context.Background(), "codex", []string{
		`{"type":"codex","refresh_token":"r-old1","id_token":"` + id("owner", "acct-owner") + `"}`,
		`{"tokens":{"refresh_token":"r-old2","access_token":"` + at + `"}}`,
		`{"type":"codex","refresh_token":"r-old3","id_token":"` + id("owner", "") + `"}`,
		"r-bare",
		`{"type":"codex","refresh_token":"r-noid","email":"owner@example.com"}`,
		`{"type":"codex","refresh_token":"r-seat","id_token":"` + id("owner", "acct-team") + `"}`,
		// Codex's own sign-in, the agent signed in to it here
		`{"type":"codex","refresh_token":"r-live2","id_token":"` + id(" Live", "") + `"}`,
		// a file whose claim and account_id disagree: either names it
		`{"type":"codex","refresh_token":"r-old4","id_token":"` + id("owner", "acct-other") + `","account_id":"acct-owner"}`,
		// one new account by two sign-ins: the first is asked
		`{"type":"codex","refresh_token":"r-twice","id_token":"` + id("twin", "acct-twin") + `"}`,
		`{"type":"codex","refresh_token":"r-twice2","id_token":"` + id("TWIN", "acct-twin") + `"}`,
	}, false, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := importStatuses(rs); got != ":conflict,:conflict,:conflict,#4:failed,owner@example.com:failed,seat@example.com:added,:conflict,:conflict,twice@example.com:added,#10:failed" {
		t.Fatalf("got %s", got)
	}
	for _, r := range rs[3:5] {
		if r.Error != codexRefreshOnly {
			t.Errorf("a refresh token alone says %q", r.Error)
		}
	}
	if rs[9].Error != "in the files twice" {
		t.Errorf("one account twice says %q", rs[9].Error)
	}
	for _, rt := range []string{"r-old1", "r-old2", "r-old3", "r-bare", "r-noid", "r-live2", "r-old4", "r-twice2"} {
		if asked[rt] != 0 {
			t.Fatalf("%s was sent to ChatGPT: %v", rt, asked)
		}
	}
	if asked["r-seat"] != 1 {
		t.Fatalf("another workspace of the email wasn't asked: %v", asked)
	}
	after, _ := os.ReadFile(loginsPath())
	if !bytes.Contains(after, []byte("n-owner")) || !bytes.Contains(before, []byte("n-owner")) {
		t.Fatal("the owner's sign-in was written over:", string(after))
	}
	// the window's import takes a refresh token alone, as it did
	if rs, _ := ImportLogins(context.Background(), "codex", []string{"r-win"}); importStatuses(rs) != "win@example.com:added" {
		t.Fatalf("the window's: %s", importStatuses(rs))
	}
}

// An area's Claude import names the account by Anthropic's answer for its
// access token, never the file's email; a token Anthropic refuses fails,
// and one already in magpie is a conflict.
func TestImportNewOnlyClaudeAsksAnthropic(t *testing.T) {
	claudeHome(t)
	var mu sync.Mutex
	var auths []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer at-good" && r.Header.Get("Authorization") != "Bearer at-good2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"account":      map[string]any{"uuid": "u-1", "email": "real@example.com"},
			"organization": map[string]any{"uuid": "o-1", "name": "Org", "organization_type": "claude_max"},
		})
	}))
	t.Cleanup(fake.Close)
	t.Cleanup(ClaudeProfileURLForTest(fake.URL))
	o := ImportOptions{NewOnly: true, Claim: func(string, string) error { return nil }}
	file := func(at, rt string) string {
		return `{"claudeAiOauth":{"accessToken":"` + at + `","refreshToken":"` + rt + `","expiresAt":` +
			strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `},"email":"someone-else@example.com"}`
	}
	rs, err := ImportLoginsWith(context.Background(), "claude", []string{file("at-good", "rt-good"), file("at-bad", "rt-bad")}, false, o)
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].Status != "added" || rs[0].User != "real@example.com" {
		t.Fatalf("named by %+v", rs[0])
	}
	if rs[1].Status != "failed" || strings.Contains(rs[1].Error, "at-bad") || strings.Contains(rs[1].Error, "rt-bad") {
		t.Fatalf("refused: %+v", rs[1])
	}
	if b, _ := os.ReadFile(loginsPath()); bytes.Contains(b, []byte("someone-else")) {
		t.Fatal("the file's email was kept")
	}
	// the same account by another sign-in: a conflict, its tokens kept
	rs, _ = ImportLoginsWith(context.Background(), "claude", []string{file("at-good2", "rt-good2")}, false, o)
	if importStatuses(rs) != ":conflict" {
		t.Fatalf("again: %s", importStatuses(rs))
	}
	if b, _ := os.ReadFile(loginsPath()); bytes.Contains(b, []byte("rt-good2")) || !bytes.Contains(b, []byte("rt-good")) {
		t.Fatal("the account's tokens were written over")
	}
	// the window never asks Anthropic
	mu.Lock()
	n := len(auths)
	mu.Unlock()
	if _, err := ImportLogins(context.Background(), "claude", []string{file("at-x", "rt-x")}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != n {
		t.Fatal("the window's import asked Anthropic")
	}
}

// An area's Antigravity import is a conflict for an account magpie has by
// Google's answer, its refresh token kept, and Admit's reason fails an
// account before Google is asked.
func TestImportNewOnlyGoogle(t *testing.T) {
	googleSandbox(t, &fakeGoogle{})
	f := &fakeGoogleImport{users: map[string]string{"1//first": "g@x.com", "1//second": "g@x.com", "1//third": "t@x.com"}}
	var mu sync.Mutex
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked++
		mu.Unlock()
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	oldToken, oldProd, oldDaily, oldWho, oldVer := googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL, antigravityVersionURL
	googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL, antigravityVersionURL = srv.URL+"/token", srv.URL+"/prod", srv.URL+"/daily", srv.URL+"/userinfo", srv.URL+"/latest-arm64-mac.yml"
	t.Cleanup(func() {
		googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL, antigravityVersionURL = oldToken, oldProd, oldDaily, oldWho, oldVer
	})
	if rs, err := ImportGoogleAccounts(context.Background(), "antigravity", []string{"1//first"}); err != nil || importStatuses(rs) != "g@x.com:added" {
		t.Fatal(importStatuses(rs), err)
	}
	o := ImportOptions{NewOnly: true, Claim: func(string, string) error { return nil }}
	rs, err := ImportGoogleAccountsWith(context.Background(), "antigravity", []string{"1//second\n1//first"}, o)
	if err != nil {
		t.Fatal(err)
	}
	if importStatuses(rs) != ":conflict,:conflict" {
		t.Fatalf("got %s", importStatuses(rs))
	}
	if b, _ := os.ReadFile(loginsPath()); bytes.Contains(b, []byte("1//second")) || !bytes.Contains(b, []byte("1//first")) {
		t.Fatal("a known account's refresh token was written over")
	}
	// the window's import of the same renews it, as it did
	if rs, _ := ImportGoogleAccounts(context.Background(), "antigravity", []string{"1//second"}); importStatuses(rs) != "g@x.com:updated" {
		t.Fatalf("the window's: %s", importStatuses(rs))
	}
	mu.Lock()
	asked = 0
	mu.Unlock()
	o.Admit = func() (func(ImportedAccount) ImportedAccount, string) { return nil, "an area has at most 500 accounts" }
	rs, _ = ImportGoogleAccountsWith(context.Background(), "antigravity", []string{"1//third"}, o)
	if rs[0].Status != "failed" || rs[0].Error != "an area has at most 500 accounts" {
		t.Fatalf("not admitted: %+v", rs[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != 0 {
		t.Fatal("Google was asked of an account not admitted")
	}
}

// An area's import kept access token only names the account only once
// ChatGPT takes the access token and its own claims name the id_token's
// account: a forged id_token beside another account's token fails, and
// so does a token ChatGPT refuses.
func TestImportNewOnlyAccessOnlyAsksChatGPT(t *testing.T) {
	claudeHome(t)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path+" "+r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		if r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("chatgpt-account-id") == "acct-bob" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"plan_type":"plus"}`)
	}))
	t.Cleanup(srv.Close)
	old := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	exp := float64(time.Now().Add(time.Hour).Unix())
	id := func(who string) string {
		return fakeJWT(map[string]any{"email": who + "@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-" + who, "chatgpt_plan_type": "plus"}})
	}
	at := func(who, mark string) string {
		return fakeJWT(map[string]any{"exp": exp, "mark": mark, "https://api.openai.com/profile": map[string]any{"email": who + "@example.com"},
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-" + who, "chatgpt_plan_type": "plus"}})
	}
	file := func(idTok, atTok string) string {
		return `{"tokens":{"id_token":"` + idTok + `","access_token":"` + atTok + `"}}`
	}
	o := ImportOptions{NewOnly: true, Claim: func(string, string) error { return nil }}
	rs, err := ImportLoginsWith(context.Background(), "codex", []string{
		file(id("ann"), at("ann", "1")),
		file(id("victim"), at("eve", "2")),
		file(id("bob"), at("bob", "refused")),
	}, true, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := importStatuses(rs); got != "ann@example.com:added,#2:failed,#3:failed" {
		t.Fatalf("got %s", got)
	}
	if b, _ := os.ReadFile(loginsPath()); bytes.Contains(b, []byte("victim@")) || bytes.Contains(b, []byte("bob@")) {
		t.Fatal("an account ChatGPT didn't vouch for was kept")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || !slices.Contains(asked, "/backend-api/wham/usage acct-ann") || !slices.Contains(asked, "/backend-api/wham/usage acct-bob") {
		t.Fatalf("asked %v", asked)
	}
}
