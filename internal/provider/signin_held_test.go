package provider

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// What the user gave a sign-in is never said back on the page, whichever
// way the vendor's refusal carries it: as it is, escaped in an address,
// quoted, or cut short. A pasted address's code, state and fragment are
// each looked for, not only the whole address; an answer typed unseen is
// at any length. A refusal holding none of them is said as it is.
func TestSignInSecretsLeftOut(t *testing.T) {
	const code = "Zm9vYmFy+YmF6/cXV4"
	pasted := PastedSecrets("  http://localhost:1455/auth/callback?code=" + url.QueryEscape(code) + "&state=c3RhdGUtMTIz#frag=ZnJhZ21lbnQ  ")
	for _, why := range []string{
		"refused " + code,
		"refused " + url.QueryEscape(code),
		"refused " + code[:12] + "…",
		"state c3RhdGUtMTIz didn't match",
		"fragment ZnJhZ21lbnQ",
	} {
		if said, key := signInHeld(why, 0, pasted); said == why || key != SignInHeld {
			t.Errorf("%q is said as %q (%q)", why, said, key)
		}
	}
	// quoted, as a JSON or Go string carries it
	pass := `pa"ss\w0rd`
	if said, _ := signInHeld("refused "+strconv.Quote(pass), 0, SignInSecrets{Given: []string{pass}}); strings.Contains(said, "w0rd") {
		t.Errorf("a quoted key is said: %q", said)
	}
	// Claude's code#state: each part
	if said, _ := signInHeld("bad state c3RhdGUtNDU2", 0, PastedSecrets("Y29kZS0xMjM0NTY#c3RhdGUtNDU2")); strings.Contains(said, "c3RhdGUtNDU2") {
		t.Errorf("a code#state's state is said: %q", said)
	}
	// an answer typed unseen, short as it is
	if said, _ := signInHeld("pin 4821 refused", 0, SignInSecrets{Hidden: []string{"4821"}}); strings.Contains(said, "4821") {
		t.Errorf("a hidden answer is said: %q", said)
	}
	// an answer short and typed in sight is no secret, nor magpie's own words
	for _, why := range []string{"no team work at FakeCo", "paste the whole address the browser ended on, starting with http://"} {
		if said, key := signInHeld(why, 0, SignInSecrets{Given: []string{"work"}}.With(pasted)); said != why || key != "" {
			t.Errorf("%q is said as %q", why, said)
		}
	}
	// the vendor's status kept, as its key fills it in
	if said, key := signInHeld("refused "+code, 401, pasted); key != SignInHeldStatus || !strings.Contains(said, "HTTP 401") || strings.Contains(said, "{status}") {
		t.Errorf("with its status: %q %q", said, key)
	}
	// a proxy's user and password, as the log leaves them out
	if said := SignInSaid("proxyconnect tcp: socks5://bob:hunter22@10.0.0.2:1080 refused", 0); strings.Contains(said, "hunter22") {
		t.Errorf("a proxy's password is said: %q", said)
	}
}

// A sign-in's state the page follows, and its error when its address is
// pasted into the Import box, never say back the code its vendor refused
// with the code in its words (no address, nothing the log rule knows).
func TestPastedSignInFailureSaysNoCode(t *testing.T) {
	claudeHome(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "no such code " + r.Form.Get("code")})
	}))
	t.Cleanup(fake.Close)
	oldTok, oldAddr := codexTokenURL, codexCallbackAddr
	t.Cleanup(func() { codexTokenURL, codexCallbackAddr = oldTok, oldAddr })
	codexTokenURL = fake.URL
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()

	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	defer CancelSignIn(st.ID)
	const code = "ac_Q29kZVNlY3JldDEy"
	done, _ := PastedSignIns([]string{callbackAddress(t, st, code, "")})
	if len(done) != 1 || done[0].Status != "failed" || strings.Contains(done[0].Error, code) || !strings.Contains(done[0].Error, "left out") {
		t.Fatalf("the import says %+v", done)
	}
	cur, _ := SignInStatus(st.ID)
	if cur.State != "failed" || !strings.Contains(cur.Error, code) {
		t.Fatalf("the vendor's refusal didn't say the code back, so this tests nothing: %+v", cur)
	}
	if said := cur.Said(); strings.Contains(said.Error, code) || said.SaidKey == "" {
		t.Fatalf("the page is told %+v", said)
	}
}
