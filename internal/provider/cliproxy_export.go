package provider

// An account's sign-in written out as CLIProxyAPI's own auth file, for its
// auth-dir: the reverse of the import (login_import.go, google_import.go).
// The fields and the file's name are CLIProxyAPI's, as its storage writes
// them (internal/auth/codex/token.go and filename.go, internal/auth/claude/,
// sdk/auth/antigravity.go). The file holds the account's refresh token: a
// ChatGPT or Claude one is spent when either side refreshes it, so the
// account then works in one of the two only, as one taken in does here.

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/yetone/magpie/internal/settings"
)

// CLIProxyAgents are the agents whose accounts CLIProxyAPI takes as auth
// files.
var CLIProxyAgents = []string{"codex", "claude", "antigravity"}

// CLIProxyAuthFile is the account user of agent as CLIProxyAPI's auth
// file: its name and its JSON.
func CLIProxyAuthFile(agent, user string) (name string, data []byte, err error) {
	if settings.AccountDiscoveryOff() {
		return "", nil, errAccountDiscoveryOff
	}
	if PluginServes(agent) {
		return "", nil, fmt.Errorf("%s accounts are its plugin's; magpie holds no sign-in of its own to export", agent)
	}
	var v map[string]any
	switch agent {
	case "codex":
		name, v, err = cliproxyCodex(user)
	case "claude":
		name, v, err = cliproxyClaude(user)
	case "antigravity":
		name, v, err = cliproxyAntigravity(user)
	default:
		return "", nil, fmt.Errorf("CLIProxyAPI takes no auth file for %s accounts", agent)
	}
	if err != nil {
		return "", nil, err
	}
	data, err = json.MarshalIndent(v, "", "  ")
	return cliproxyFileName(name), data, err
}

// exportedLogin is the sign-in of the account user of agent: the one the
// agent is signed in to now, read from its own files, or the one kept.
func exportedLogin(agent, user string) (savedLogin, error) {
	if l, ok := liveLogin(agent); ok && strings.EqualFold(l.User, user) {
		return l, nil
	}
	if agent == "claude" {
		// Claude Code may have refreshed it in the account's own directory
		syncSavedClaude(user)
	}
	for _, l := range readLogins() {
		if l.Agent == agent && strings.EqualFold(l.User, user) && !l.own() {
			if l.Lapsed != "" {
				// its vendor refused the sign-in: a dead token, nothing to carry
				return savedLogin{}, fmt.Errorf("%s has to be signed in again before it can be exported: %s", user, l.Lapsed)
			}
			return l, nil
		}
	}
	return savedLogin{}, fmt.Errorf("no %s account %q", map[string]string{"codex": "ChatGPT", "claude": "Claude"}[agent], user)
}

func cliproxyCodex(user string) (string, map[string]any, error) {
	l, err := exportedLogin("codex", user)
	if err != nil {
		return "", nil, err
	}
	var raw map[string]any
	var a codexAuth
	if json.Unmarshal(l.Auth, &raw) != nil || json.Unmarshal(l.Auth, &a) != nil || a.Tokens.RefreshToken == "" || a.AuthMode == "apikey" {
		return "", nil, fmt.Errorf("the ChatGPT sign-in of %s has no refresh token to export", user)
	}
	id := jwtClaims(a.Tokens.IDToken)
	email := cmp.Or(claimString(id, "email"), strings.SplitN(l.User, " · ", 2)[0])
	claimed := claimString(id, "https://api.openai.com/auth", "chatgpt_account_id")
	accountID := cmp.Or(claimed, a.Tokens.AccountID)
	plan := cmp.Or(claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"), l.Plan, "free")
	last, _ := raw["last_refresh"].(string)
	v := map[string]any{"type": "codex", "id_token": a.Tokens.IDToken, "access_token": a.Tokens.AccessToken,
		"refresh_token": a.Tokens.RefreshToken, "account_id": accountID, "last_refresh": last,
		"email": email, "expired": jwtExpiry(a.Tokens.AccessToken), "plan_type": plan}
	// codex-<account hash>-<email>-<plan>.json, as CredentialFileName names
	// it: the hash of the ID token's own account id only, as CLIProxyAPI's
	name := "codex-"
	if claimed != "" {
		name += shortHash(claimed) + "-"
	}
	return name + email + "-" + planForFile(plan) + ".json", v, nil
}

func cliproxyClaude(user string) (string, map[string]any, error) {
	l, err := exportedLogin("claude", user)
	if err != nil {
		return "", nil, err
	}
	var raw struct {
		OAuth claudeAuth `json:"claudeAiOauth"`
	}
	if json.Unmarshal(l.Auth, &raw) != nil || raw.OAuth.RefreshToken == "" {
		return "", nil, fmt.Errorf("the Claude sign-in of %s has no refresh token to export", user)
	}
	var acct struct {
		Email   string `json:"emailAddress"`
		Account string `json:"accountUuid"`
		Org     string `json:"organizationUuid"`
		OrgName string `json:"organizationName"`
	}
	_ = json.Unmarshal(l.Profile, &acct)
	email := cmp.Or(acct.Email, strings.SplitN(l.User, " · ", 2)[0])
	v := map[string]any{"type": "claude", "id_token": "", "access_token": raw.OAuth.AccessToken,
		"refresh_token": raw.OAuth.RefreshToken, "last_refresh": "", "email": email, "expired": ""}
	if !l.Renewed.IsZero() {
		v["last_refresh"] = l.Renewed.Format(time.RFC3339)
	}
	if raw.OAuth.ExpiresAt > 0 {
		v["expired"] = time.UnixMilli(raw.OAuth.ExpiresAt).UTC().Format(time.RFC3339)
	}
	for k, s := range map[string]string{"account_uuid": acct.Account, "organization_uuid": acct.Org, "organization_name": acct.OrgName} {
		if s != "" {
			v[k] = s
		}
	}
	// claude-<organization or account hash>-<email>.json
	name := "claude-"
	if who := cmp.Or(acct.Org, acct.Account); who != "" {
		name += shortHash(who) + "-"
	}
	return name + email + ".json", v, nil
}

func cliproxyAntigravity(user string) (string, map[string]any, error) {
	for _, g := range googleLogins("antigravity") {
		if !strings.EqualFold(g.User, user) {
			continue
		}
		a := g.acct.auth
		if a.RefreshToken == "" {
			break
		}
		v := map[string]any{"type": "antigravity", "access_token": a.AccessToken, "refresh_token": a.RefreshToken,
			"email": g.User, "expired": "", "expires_in": 0, "timestamp": time.Now().UnixMilli()}
		if a.Expiry > 0 {
			exp := time.UnixMilli(a.Expiry)
			v["expired"] = exp.UTC().Format(time.RFC3339)
			v["expires_in"] = max(0, int64(time.Until(exp).Seconds()))
		}
		if a.Project != "" {
			v["project_id"] = a.Project
		}
		return "antigravity-" + g.User + ".json", v, nil
	}
	return "", nil, fmt.Errorf("no Antigravity account %q with a sign-in to export", user)
}

// jwtExpiry is when a JWT runs out, as RFC 3339; "" when it doesn't say.
func jwtExpiry(tok string) string {
	if exp, _ := jwtClaims(tok)["exp"].(float64); exp > 0 {
		return time.Unix(int64(exp), 0).UTC().Format(time.RFC3339)
	}
	return ""
}

// shortHash is the first 8 hex digits of s's SHA-256, CLIProxyAPI's hash
// in a file's name.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// planForFile is a plan as CLIProxyAPI puts it in a file's name: its words
// in lower case, a dash apart.
func planForFile(plan string) string {
	parts := strings.FieldsFunc(plan, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return strings.ToLower(strings.Join(parts, "-"))
}

// cliproxyFileName keeps a name to one file's, on every system: no
// folder in it, nor a character Windows refuses in a name.
func cliproxyFileName(name string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < ' ' {
			return '_'
		}
		return r
	}, name)
}
