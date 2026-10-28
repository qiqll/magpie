package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// namesAre has hideAccounts know accounts and providers, until t ends.
func namesAre(t *testing.T, ids []string, refs ...provider.AccountRef) {
	oldN, oldP := namesOfAccounts, providerIDs
	t.Cleanup(func() { namesOfAccounts, providerIDs = oldN, oldP; forgetAccountNames() })
	namesOfAccounts = func() []provider.AccountRef { return refs }
	providerIDs = func() []string { return ids }
	forgetAccountNames()
}

// What a caller is told names none of magpie's accounts or keys. An email
// goes wherever it stands; any other name only where magpie writes one
// (brackets, quotes, "@", "<provider>/"), so a name that is also a word,
// a provider's id or a path ("default", "claude", "/v1/models") leaves the
// message's own words alone. Two names side by side both go.
func TestHideAccounts(t *testing.T) {
	namesAre(t, []string{"codex", "claude", "openai"},
		provider.AccountRef{User: "me@example.com"}, provider.AccountRef{User: "team@example.com · Team"},
		provider.AccountRef{User: "alice-work"}, provider.AccountRef{User: "default"}, provider.AccountRef{User: "claude"},
		provider.AccountRef{User: "key", Key: true}, provider.AccountRef{User: "Work key", Key: true},
		provider.AccountRef{User: "models", Key: true}, provider.AccountRef{User: "API key"})
	for in, want := range map[string]string{
		"Codex (me@example.com) is at 92%":             "Codex (an account) is at 92%",
		"codex/team@example.com · Team is held":        "codex/an account is held",
		"me@example.com,spare@example.com held":        "an account,an account held",
		"codex/alice-work is held":                     "codex/an account is held",
		"copilot (alice-work): 401":                    "copilot (an account): 401",
		`no account "alice-work"`:                      `no account "an account"`,
		"(alice-work,alice-work)":                      "(an account,an account)",
		"its accounts are me@example.com, alice-work":  "its accounts are an account, an account",
		"openai (Work key): 401":                       "openai (a key): 401",
		"turn alice-work on again":                     "turn alice-work on again",
		"the default model is gone":                    "the default model is gone",
		"Claude refused it":                            "Claude refused it",
		"GET /v1/models failed":                        "GET /v1/models failed",
		"no API key was sent":                          "no API key was sent",
		"no key was sent, try a key":                   "no key was sent, try a key",
		"the vendor says stranger@corp.io is disabled": "the vendor says an account is disabled",
		"an account is held":                           "an account is held",
	} {
		if got := hideAccounts(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	// a vendor's body passed on as it came stays valid JSON
	body := hideAccountsBody([]byte(`{"error":{"message":"workspace of me@example.com is deactivated","type":"invalid_request_error"}}`))
	var e struct{ Error struct{ Message string } }
	if json.Unmarshal(body, &e) != nil || e.Error.Message != "workspace of an account is deactivated" {
		t.Fatalf("body %s", body)
	}
	// in a stream, the error event's line alone
	in := "data: {\"type\":\"text\",\"text\":\"write to me@example.com\"}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"me@example.com is out\"}}\n\n"
	want := "data: {\"type\":\"text\",\"text\":\"write to me@example.com\"}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"an account is out\"}}\n\n"
	if got := string(hideErrorEvents([]byte(in))); got != want {
		t.Fatalf("stream:\n%s", got)
	}
}

// A held key's own calls carry nothing of the accounts or keys that
// answered them.
func TestHeldKeyUsageWithoutAccounts(t *testing.T) {
	namesAre(t, []string{"codex"}, provider.AccountRef{User: "me@example.com"})
	var r usageapi.Row
	r.Record = usage.Record{Provider: "codex", Host: "chatgpt.com as me@example.com", ProviderAccount: "me@example.com",
		ProviderKeyName: "Work key", ProviderKeyID: "k1", SessionAccount: "me@example.com", Error: "codex (me@example.com): 429"}
	l := withoutAccounts(usageapi.Ledger{Rows: []usageapi.Row{r}, Accounts: []usageapi.Account{{ID: "me@example.com"}}})
	b, _ := json.Marshal(l)
	if l.Accounts != nil || l.Rows[0].Host != "chatgpt.com" || l.Rows[0].Error != "codex (an account): 429" || strings.Contains(string(b), "me@example.com") || strings.Contains(string(b), "Work key") {
		t.Fatalf("%s", b)
	}
}

// A vendor's own error that names the account, passed on to the caller as
// a status, a whole body and a stream: the email never reaches it.
func TestVendorErrorWithoutTheAccount(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"the workspace of owner@corp.example is deactivated","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"relay/m","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/chat/completions", `{"model":"relay/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"relay/m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code < 400 || strings.Contains(rec.Body.String(), "owner@corp.example") || !strings.Contains(rec.Body.String(), "deactivated") {
			t.Fatalf("%s %s: %d %s", c.path, c.body, rec.Code, rec.Body.String())
		}
	}
}
