package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestParseGoogleImportFormats(t *testing.T) {
	cases := []struct {
		name, data string
		want       []googleImport // n and err not compared unless set
		errs       []string       // per entry, "" for none
	}{
		{"cockpit export", `[{"email":"a@x.com","refresh_token":"1//a"},{"email":"b@x.com","refresh_token":"1//b"}]`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a"}, {email: "b@x.com", refreshToken: "1//b"}}, []string{"", ""}},
		{"single object, camelCase", `{"email":"a@x.com","refreshToken":"1//a","projectId":"p1"}`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a", project: "p1"}}, []string{""}},
		{"manager export response", `{"accounts":[{"email":"a@x.com","refresh_token":"1//a"}]}`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a"}}, []string{""}},
		{"manager account file", `{"id":"u1","email":"a@x.com","name":"A","token":{"access_token":"ya29.x","refresh_token":"1//a","expires_in":3599,"expiry_timestamp":1790000000,"token_type":"Bearer","email":"a@x.com","project_id":"proj-a"}}`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a", project: "proj-a"}}, []string{""}},
		{"cliproxyapi auth file", `{"type":"antigravity","access_token":"ya29.x","refresh_token":"1//a","expires_in":3599,"timestamp":1790000000000,"expired":"2026-09-29T10:00:00Z","email":"a@x.com","project_id":"proj-a"}`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a", project: "proj-a"}}, []string{""}},
		{"cliproxyapi gemini file refused", `{"type":"gemini","refresh_token":"1//g","email":"g@x.com"}`,
			[]googleImport{{email: "g@x.com", refreshToken: "1//g"}}, []string{"a gemini sign-in, not Antigravity's"}},
		{"bare tokens, one a line", "\xef\xbb\xbf1//a\n\n# a comment\n1//b\r\n",
			[]googleImport{{refreshToken: "1//a"}, {refreshToken: "1//b"}}, []string{"", ""}},
		{"array of token strings", `["1//a","1//b"]`,
			[]googleImport{{refreshToken: "1//a"}, {refreshToken: "1//b"}}, []string{"", ""}},
		{"duplicates and missing tokens", `[{"email":"a@x.com","refresh_token":"1//a"},{"email":"A@x.com","refresh_token":"1//z"},{"email":"b@x.com","refresh_token":"1//a"},{"email":"c@x.com"},3]`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a"}, {email: "A@x.com", refreshToken: "1//z"}, {email: "b@x.com", refreshToken: "1//a"}, {email: "c@x.com"}, {}},
			[]string{"", "in the file twice", "in the file twice", "no refresh_token in it", "not an account"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseGoogleImport("antigravity", c.data)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d entries, want %d: %+v", len(got), len(c.want), got)
			}
			for i, g := range got {
				w := c.want[i]
				if g.email != w.email || g.refreshToken != w.refreshToken || g.project != w.project || g.err != c.errs[i] || g.n != i+1 {
					t.Errorf("entry %d = %+v, want %+v err %q", i, g, w, c.errs[i])
				}
			}
		})
	}
	for _, bad := range []string{"", "  ", "{not json", "[]", "{}"} {
		es, err := parseGoogleImport("antigravity", bad)
		if bad == "{}" {
			// an object with nothing in it is one entry, without a token
			if err != nil || len(es) != 1 || es[0].err == "" {
				t.Errorf("%q: %+v, %v", bad, es, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q: no error (%+v)", bad, es)
		}
	}
}

// fakeGoogleImport is Google's token and userinfo endpoints and Code
// Assist, knowing a few refresh tokens.
type fakeGoogleImport struct {
	mu        sync.Mutex
	users     map[string]string // refresh token → email
	clients   []string
	refreshed []string
}

func (f *fakeGoogleImport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	switch {
	case r.URL.Path == "/token":
		form, _ := url.ParseQuery(string(b))
		f.clients = append(f.clients, form.Get("client_id"))
		rt := form.Get("refresh_token")
		f.refreshed = append(f.refreshed, rt)
		if rt == "1//garbage" { // what Google answers for a token it doesn't know
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"Bad Request"}`)
			return
		}
		if form.Get("grant_type") != "refresh_token" || f.users[rt] == "" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
			return
		}
		io.WriteString(w, `{"access_token":"at-`+rt+`","expires_in":3600}`)
	case r.URL.Path == "/userinfo":
		at := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")
		if f.users[at] == "" || strings.HasPrefix(f.users[at], "nouser") {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"email": f.users[at]})
	case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
		io.WriteString(w, `{"cloudaicompanionProject":"found-proj","paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"}}`)
	case strings.HasSuffix(r.URL.Path, "latest-arm64-mac.yml"):
		io.WriteString(w, "version: 3.1.4\n")
	default:
		http.NotFound(w, r)
	}
}

func TestImportGoogleAccounts(t *testing.T) {
	googleSandbox(t, &fakeGoogle{})
	f := &fakeGoogleImport{users: map[string]string{"1//a": "a@x.com", "1//b": "b@x.com", "1//c": "c@x.com", "1//n": "nouser"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	oldToken, oldProd, oldDaily, oldWho := googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL
	googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL = srv.URL+"/token", srv.URL+"/prod", srv.URL+"/daily", srv.URL+"/userinfo"
	t.Cleanup(func() {
		googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL = oldToken, oldProd, oldDaily, oldWho
	})

	// b is in magpie already, with the same sign-in
	if err := addGoogleLogin("antigravity", "b@x.com", "", googleAuth{RefreshToken: "1//b", Project: "pb"}); err != nil {
		t.Fatal(err)
	}
	cockpit := `[{"email":"a@x.com","refresh_token":"1//a"},{"email":"b@x.com","refresh_token":"1//b"},{"email":"dead@x.com","refresh_token":"1//dead"}]`
	cpa := `{"type":"antigravity","refresh_token":"1//n","email":"n@x.com","project_id":"file-proj"}`
	res, err := ImportGoogleAccounts(context.Background(), "antigravity", []string{cockpit, cpa, "1//c"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ImportedAccount{
		{User: "a@x.com", Status: "added", Plan: "Google AI Pro"},
		{User: "b@x.com", Status: "exists"},
		{User: "dead@x.com", Status: "failed"},
		{User: "n@x.com", Status: "added", Plan: "Google AI Pro"}, // userinfo refused: the file's email
		{User: "c@x.com", Status: "added", Plan: "Google AI Pro"},
	}
	if len(res) != len(want) {
		t.Fatalf("results = %+v", res)
	}
	for i, r := range res {
		if r.User != want[i].User || r.Status != want[i].Status || r.Plan != want[i].Plan {
			t.Errorf("result %d = %+v, want %+v", i, r, want[i])
		}
	}
	if !strings.Contains(res[2].Error, "expired or revoked") {
		t.Errorf("dead's error = %q", res[2].Error)
	}
	// no token in what is said back
	out, _ := json.Marshal(res)
	for _, tok := range []string{"1//", "at-"} {
		if strings.Contains(string(out), tok) {
			t.Errorf("results carry a token: %s", out)
		}
	}
	// refreshed with Antigravity's own client; b, already here, wasn't asked for
	for _, c := range f.clients {
		if c != antigravityApp.clientID {
			t.Errorf("refreshed with client %q", c)
		}
	}
	for _, rt := range f.refreshed {
		if rt == "1//b" {
			t.Error("an account already in magpie was refreshed")
		}
	}
	// kept as signed-in accounts are, with their project
	users := map[string]string{}
	for _, l := range googleLogins("antigravity") {
		users[l.User] = l.acct.auth.Project
	}
	if len(users) != 4 || users["a@x.com"] != "found-proj" || users["c@x.com"] != "found-proj" || users["b@x.com"] != "pb" {
		t.Errorf("logins = %v", users)
	}
	b, _ := os.ReadFile(loginsPath())
	if !strings.Contains(string(b), `"1//a"`) {
		t.Error("a's sign-in not kept in logins.json")
	}

	// again: nothing new; a new sign-in of a is taken in its place
	f.mu.Lock()
	f.users["1//a2"] = "a@x.com"
	f.mu.Unlock()
	res, err = ImportGoogleAccounts(context.Background(), "antigravity", []string{cockpit, `[{"refresh_token":"1//a2"}]`})
	if err != nil {
		t.Fatal(err)
	}
	st := []string{}
	for _, r := range res {
		st = append(st, r.User+":"+r.Status)
	}
	if strings.Join(st, " ") != "a@x.com:exists b@x.com:exists dead@x.com:failed a@x.com:updated" {
		t.Errorf("again = %v", st)
	}
	for _, l := range googleLogins("antigravity") {
		if l.User == "a@x.com" && l.acct.auth.RefreshToken != "1//a2" {
			t.Error("a's newer sign-in wasn't kept")
		}
	}

	// Google's bare "Bad Request" is explained
	res, err = ImportGoogleAccounts(context.Background(), "antigravity", []string{"1//garbage"})
	if err != nil || len(res) != 1 || res[0].Status != "failed" || !strings.Contains(res[0].Error, "isn't a refresh token") || strings.Contains(res[0].Error, "1//garbage") {
		t.Errorf("bad request = %+v, %v", res, err)
	}

	// only Google's apps (Gemini CLI's in TestImportGeminiAccounts); a
	// file of nothing says so
	if _, err := ImportGoogleAccounts(context.Background(), "codex", []string{cockpit}); err == nil {
		t.Error("imported into Codex")
	}
	if _, err := ImportGoogleAccounts(context.Background(), "antigravity", []string{"{broken"}); err == nil {
		t.Error("broken JSON imported")
	}
	// one broken file among several is listed by its place, not passed over
	// (#1453)
	res, err = ImportGoogleAccounts(context.Background(), "antigravity", []string{cockpit, "{broken"})
	if err != nil || len(res) != 4 || res[3].File != 2 || res[3].User != "" || res[3].Status != "failed" || res[3].Error == "" {
		t.Errorf("a broken file among several = %+v, %v", res, err)
	}
}

// Gemini CLI's accounts are imported as Antigravity's are (T-232): its
// own oauth_creds.json, CLIProxyAPI's gemini file and bare refresh tokens,
// each refreshed with Gemini CLI's client and named by Google alone, never
// by the file. Gemini CLI's own sign-in is never imported over, and an
// area's import (NewOnly) of an account magpie has is a conflict that
// writes nothing.
func TestImportGeminiAccounts(t *testing.T) {
	googleSandbox(t, &fakeGoogle{})
	f := &fakeGoogleImport{users: map[string]string{"1//g": "g@x.com", "1//g2": "g@x.com", "1//h": "h@x.com",
		"1//n": "nouser", "1//own": "own@x.com", "1//own2": "own@x.com", "1//k": "k@x.com"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	oldToken, oldProd, oldDaily, oldWho := googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL
	googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL = srv.URL+"/token", srv.URL+"/prod", srv.URL+"/daily", srv.URL+"/userinfo"
	t.Cleanup(func() {
		googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL = oldToken, oldProd, oldDaily, oldWho
	})
	// Gemini CLI signed in here as own@x.com
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ownCreds := `{"access_token":"ya29.own","refresh_token":"1//own","token_type":"Bearer","expiry_date":1}`
	os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(ownCreds), 0o600)
	os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":"own@x.com"}`), 0o600)

	oauthCreds := `{"access_token":"ya29.g","refresh_token":"1//g","scope":"https://www.googleapis.com/auth/cloud-platform","token_type":"Bearer","expiry_date":1790000000000}`
	cpa := `{"type":"gemini","email":"says-h@x.com","project_id":"file-proj","token":{"access_token":"ya29.h","refresh_token":"1//h","token_uri":"https://oauth2.googleapis.com/token"}}`
	noUser := `{"type":"gemini","email":"liar@x.com","token":{"refresh_token":"1//n"}}`
	ag := `{"type":"antigravity","email":"ag@x.com","refresh_token":"1//ag"}`
	res, err := ImportGoogleAccounts(context.Background(), "gemini", []string{oauthCreds, cpa, noUser, ag, "1//own\n1//own2"})
	if err != nil {
		t.Fatal(err)
	}
	st := []string{}
	for _, r := range res {
		st = append(st, r.User+":"+r.Status)
	}
	if strings.Join(st, " ") != "g@x.com:added h@x.com:added #3:failed #4:failed own@x.com:exists own@x.com:exists" {
		t.Fatalf("results = %v (%+v)", st, res)
	}
	if !strings.Contains(res[3].Error, "not Gemini CLI's") {
		t.Errorf("an Antigravity file says %q", res[3].Error)
	}
	out, _ := json.Marshal(res)
	for _, tok := range []string{"1//", "at-", "ya29", "liar@", "ag@"} {
		if strings.Contains(string(out), tok) {
			t.Errorf("results carry a token or a file's name: %s", out)
		}
	}
	// refreshed with Gemini CLI's own client; its own sign-in wasn't
	f.mu.Lock()
	for _, c := range f.clients {
		if c != geminiApp.clientID {
			t.Errorf("refreshed with client %q", c)
		}
	}
	for _, rt := range f.refreshed {
		if rt == "1//own" || rt == "1//ag" {
			t.Errorf("%s was refreshed", rt)
		}
	}
	f.mu.Unlock()
	// kept by Google's names, with Code Assist's project; Gemini CLI's own
	// file left as it was
	users := map[string]string{}
	for _, l := range googleLogins("gemini") {
		users[l.User] = l.acct.auth.RefreshToken
	}
	if len(users) != 3 || users["g@x.com"] != "1//g" || users["h@x.com"] != "1//h" || users["own@x.com"] != "1//own" {
		t.Errorf("logins = %v", users)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "oauth_creds.json")); string(b) != ownCreds {
		t.Errorf("Gemini CLI's own sign-in written over: %s", b)
	}
	b, _ := os.ReadFile(loginsPath())
	for _, s := range []string{"says-h@", "liar@", "1//own2"} {
		if strings.Contains(string(b), s) {
			t.Errorf("%q kept in logins.json", s)
		}
	}

	// an area's import: a new account is added, Google naming it; one
	// magpie has, by its refresh token or Google's answer, is a conflict
	// that names no one and writes nothing
	before, _ := os.ReadFile(loginsPath())
	res, err = ImportGoogleAccountsWith(context.Background(), "gemini", []string{"1//g\n1//g2\n1//own2\n1//k"}, ImportOptions{NewOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	st = st[:0]
	for _, r := range res {
		st = append(st, r.User+":"+r.Status)
	}
	if strings.Join(st, " ") != ":conflict :conflict :conflict k@x.com:added" {
		t.Fatalf("new-only = %v (%+v)", st, res)
	}
	after, _ := os.ReadFile(loginsPath())
	if strings.Contains(string(after), "1//g2") || strings.Contains(string(after), "1//own2") || !strings.Contains(string(after), `"1//k"`) {
		t.Errorf("a conflict was written, or the new one wasn't:\nbefore %s\nafter %s", before, after)
	}
}

// An area's import of Gemini CLI's own account, by another sign-in, is a
// conflict on a magpie that hasn't listed its Gemini CLI accounts yet
// (logins.json has no row for it), the account named by its id_token or,
// with none, by Google, when Gemini CLI says no email of its own; with
// Google not answering for it, the area's import is refused whole.
func TestImportGeminiOwnNewOnly(t *testing.T) {
	idToken := func(email string) string {
		b, _ := json.Marshal(map[string]any{"email": email, "azp": geminiApp.clientID})
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	for _, c := range []struct {
		name, creds string
		want        string
	}{
		{"named in google_accounts.json", `{"refresh_token":"1//own"}`, ":conflict k@x.com:added"},
		{"named by its id_token", `{"refresh_token":"1//own","id_token":"` + idToken("own@x.com") + `"}`, ":conflict k@x.com:added"},
		{"named by Google", `{"refresh_token":"1//own"}`, ":conflict k@x.com:added"},
		{"Google not answering", `{"refresh_token":"1//dead-own"}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			googleSandbox(t, &fakeGoogle{})
			f := &fakeGoogleImport{users: map[string]string{"1//own": "own@x.com", "1//own2": "own@x.com", "1//k": "k@x.com"}}
			srv := httptest.NewServer(f)
			defer srv.Close()
			oldToken, oldWho := googleTokenURL, googleUserInfoURL
			googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL = srv.URL+"/token", srv.URL+"/prod", srv.URL+"/daily", srv.URL+"/userinfo"
			t.Cleanup(func() { googleTokenURL, googleUserInfoURL = oldToken, oldWho })
			home, _ := os.UserHomeDir()
			dir := filepath.Join(home, ".gemini")
			os.MkdirAll(dir, 0o700)
			os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(c.creds), 0o600)
			if c.name == "named in google_accounts.json" {
				os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":"own@x.com"}`), 0o600)
			}
			res, err := ImportGoogleAccountsWith(context.Background(), "gemini", []string{"1//own2\n1//k"}, ImportOptions{NewOnly: true})
			if c.want == "" {
				if err == nil {
					t.Fatalf("imported with Gemini CLI's own account unknown: %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var st []string
			for _, r := range res {
				st = append(st, r.User+":"+r.Status)
			}
			if strings.Join(st, " ") != c.want {
				t.Fatalf("results = %v", st)
			}
			b, _ := os.ReadFile(loginsPath())
			if strings.Contains(string(b), "1//own2") {
				t.Errorf("the owner's account written: %s", b)
			}
			// Google is asked about Gemini CLI's own only when nothing here
			// names it
			f.mu.Lock()
			askedOwn := slices.Contains(f.refreshed, "1//own")
			f.mu.Unlock()
			if askedOwn != (c.name == "named by Google") {
				t.Errorf("Google asked for Gemini CLI's own account: %v", askedOwn)
			}
		})
	}
}
