package provider

import (
	"strings"
	"testing"
)

// A sign-in's failure reason the page shows never holds a token, a code,
// a sign-in's address or the key the user gave it: one that may is left
// out, as the log leaves it out, and the vendor's HTTP status is kept.
// magpie's own words naming a bare scheme or a loopback address are said.
func TestSignInSaidLeavesSecretsOut(t *testing.T) {
	for _, why := range []string{
		`token exchange: {"access_token":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc"}`,
		"refresh refused: Bearer abcdefghijklmnop",
		"open https://claude.ai/oauth/authorize?code=true&state=Xyz123",
		"the address http://localhost:1455/auth/callback?code=ac_abc123 didn't finish it",
		"http://127.0.0.1:54545/callback?state=abcdef didn't match",
		"invalid key sk-ant-api03-abcdefghij",
		`{"device_code":"GMMhmHCXhWEzkobqIHGG_EnNYYsAkukHspeYUk9E8"}`,
	} {
		if got := SignInSaid(why, 0); got == why || strings.Contains(got, "code=") || strings.Contains(got, "eyJ") || strings.Contains(got, "sk-") || strings.Contains(got, "://") {
			t.Errorf("%q is said as %q", why, got)
		}
	}
	held := "token exchange failed: " + `{"error":"invalid_grant","state":"Q2xhdWRlU3RhdGU"}`
	if got := SignInSaid(held, 400); !strings.Contains(got, "HTTP 400") || strings.Contains(got, "Q2xhdWRlU3RhdGU") {
		t.Errorf("with its status: %q", got)
	}
	// the key the user gave, said back by the plugin
	if got := SignInSaid("key qwerty-123456 was refused", 0, "qwerty-123456"); strings.Contains(got, "qwerty") {
		t.Errorf("the key is said: %q", got)
	}
	if got := SignInSaid("refused", 0, "ab"); got != "refused" {
		t.Errorf("a short answer leaves the reason out: %q", got)
	}
	for _, why := range []string{
		"proxy must look like http://127.0.0.1:7890 or socks5://127.0.0.1:1080",
		"paste the whole address the browser ended on, starting with http://",
		"invalid_grant: Token has been expired or revoked",
		"claude runs on its plugin: sign in through the plugin (magpie plugin login claude)",
		"dial tcp: lookup auth.openai.com: no such host",
	} {
		if got := SignInSaid(why, 0); got != why {
			t.Errorf("%q is said as %q", why, got)
		}
	}
}

// The page is told a failed sign-in's reason as SignInSaid says it.
func TestSignInStateSaid(t *testing.T) {
	st := SignInState{State: "failed", Error: "open https://auth.example/authorize?state=abc", status: 401}
	if got := st.Said(); got.Error == st.Error || !strings.Contains(got.Error, "HTTP 401") {
		t.Fatalf("said %+v", got)
	}
	if got := (SignInState{State: "waiting", URL: "https://auth.example/authorize?state=abc"}).Said(); got.URL == "" || got.Error != "" {
		t.Fatalf("a sign-in under way keeps its page: %+v", got)
	}
}
