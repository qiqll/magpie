package provider

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An account comes out as CLIProxyAPI's own auth file: its fields and its
// name as CLIProxyAPI writes them (the names' hashes are CLIProxyAPI's own
// test vectors, internal/auth/claude/filename_test.go), and magpie's import
// reads each back as the same account.
func TestCLIProxyAuthFile(t *testing.T) {
	home := claudeHome(t)
	codexSignIn(t, home, "live@example.com", "r-live")
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	team, err := codexLogin(fakeJWT(map[string]any{"email": "team@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "team", "chatgpt_account_id": "acct-team"}}),
		fakeJWT(map[string]any{"exp": float64(exp.Unix())}), "r-team", "")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := claudeLogin(claudeCredentials{raw: map[string]any{}, OAuth: claudeAuth{AccessToken: "sk-ant-oat01-a", RefreshToken: "sk-ant-ort01-r",
		ExpiresAt: exp.UnixMilli(), SubscriptionType: "max"}},
		map[string]any{"emailAddress": "user@example.com", "accountUuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "organizationUuid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"})
	if err != nil {
		t.Fatal(err)
	}
	ag, _ := json.Marshal(googleAuth{AccessToken: "ya29.a", RefreshToken: "1//g", Expiry: exp.UnixMilli(), Project: "proj-g"})
	if err := writeLogins([]savedLogin{team, claude, {Agent: "antigravity", User: "g@example.com", Auth: ag}}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		agent, user, name string
		want              map[string]any
	}{
		{"codex", "team@example.com · Team", "codex-" + shortHash("acct-team") + "-team@example.com-team.json",
			map[string]any{"type": "codex", "refresh_token": "r-team", "account_id": "acct-team", "email": "team@example.com", "plan_type": "team",
				"expired": exp.UTC().Format(time.RFC3339)}},
		// the one Codex is signed in to, read from its own auth.json; its ID
		// token names no account id, so the name has no hash (CLIProxyAPI
		// hashes the claim only, not auth.json's account_id)
		{"codex", "live@example.com", "codex-live@example.com-plus.json",
			map[string]any{"type": "codex", "refresh_token": "r-live", "email": "live@example.com", "plan_type": "plus"}},
		{"claude", "user@example.com", "claude-00f765af-user@example.com.json",
			map[string]any{"type": "claude", "access_token": "sk-ant-oat01-a", "refresh_token": "sk-ant-ort01-r", "email": "user@example.com",
				"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "organization_uuid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
				"expired": exp.UTC().Format(time.RFC3339)}},
		{"antigravity", "g@example.com", "antigravity-g@example.com.json",
			map[string]any{"type": "antigravity", "access_token": "ya29.a", "refresh_token": "1//g", "email": "g@example.com", "project_id": "proj-g"}},
	} {
		name, data, err := CLIProxyAuthFile(c.agent, c.user)
		if err != nil {
			t.Fatalf("%s %s: %v", c.agent, c.user, err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name != c.name {
			t.Fatalf("%s %s named %q, want %q", c.agent, c.user, name, c.name)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Fatalf("%s: %s = %v, want %v (%s)", name, k, got[k], v, data)
			}
		}
		// read back by magpie's own import as the same sign-in
		switch c.agent {
		case "antigravity":
			es, err := parseGoogleImport(c.agent, string(data))
			if err != nil || len(es) != 1 || es[0].err != "" || es[0].refreshToken != c.want["refresh_token"] || es[0].project != "proj-g" || es[0].email != c.user {
				t.Fatalf("%s read back: %+v %v", name, es, err)
			}
		default:
			es, err := parseLoginImport(c.agent, string(data))
			if err != nil || len(es) != 1 || es[0].err != "" || es[0].refreshToken != c.want["refresh_token"] {
				t.Fatalf("%s read back: %+v %v", name, es, err)
			}
		}
	}

	// one magpie doesn't have, and an agent CLIProxyAPI takes no file for
	if _, _, err := CLIProxyAuthFile("codex", "nobody@example.com"); err == nil {
		t.Fatal("an account magpie doesn't have exported")
	}
	if _, _, err := CLIProxyAuthFile("copilot", "x"); err == nil {
		t.Fatal("a Copilot account exported")
	}
	// one whose vendor refused its sign-in carries a dead token: refused
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == "codex" {
			ls[i].Lapsed = "token_revoked"
		}
	}
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CLIProxyAuthFile("codex", "team@example.com · Team"); err == nil || !strings.Contains(err.Error(), "signed in again") {
		t.Fatalf("a lapsed account exported: %v", err)
	}
}

// A name is one file's, whatever the account is called.
func TestCLIProxyFileNameStaysOneFile(t *testing.T) {
	if got := cliproxyFileName("antigravity-../../a/b\\c:d*e?f\"g<h>i|j.json"); strings.ContainsAny(got, `/\:*?"<>|`) {
		t.Fatalf("%q", got)
	}
}
