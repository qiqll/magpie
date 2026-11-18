package provider

// Whose a Claude sign-in is, asked of Anthropic with its access token, as
// Claude Code asks it once it has signed in (/api/oauth/profile). The
// window never asks this: magpie keeps a Claude sign-in as it came and
// asks Anthropic nothing with it. An area's import does (T-211, the owner's
// call of 2026-11-18): it names the account by Anthropic's answer, never
// by what the file says, so a file can't name another's account.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// claudeProfileURL is where it is asked; a var so tests point it elsewhere.
var claudeProfileURL = claudeBase + "/api/oauth/profile"

// ClaudeProfileURLForTest points claudeProfileURL at a test server; the
// func it returns puts it back.
func ClaudeProfileURLForTest(u string) func() {
	old := claudeProfileURL
	claudeProfileURL = u
	return func() { claudeProfileURL = old }
}

// claudeProfile is Anthropic's answer, the parts magpie keeps.
type claudeProfile struct {
	Account struct {
		UUID  string `json:"uuid"`
		Email string `json:"email"`
	} `json:"account"`
	Organization struct {
		UUID          string `json:"uuid"`
		Name          string `json:"name"`
		Type          string `json:"organization_type"`
		RateLimitTier string `json:"rate_limit_tier"`
	} `json:"organization"`
}

// errClaudeProfileRefused: Anthropic didn't take the access token.
var errClaudeProfileRefused = errors.New("Anthropic didn't take its access token: it has expired or been signed out. Run Claude Code on this account once so it refreshes it, export it again, then import it")

// askClaudeProfile asks Anthropic whose accessToken is, through ctx's
// proxy. The token is never in what it answers.
func askClaudeProfile(ctx context.Context, accessToken string) (claudeProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeProfileURL, nil)
	if err != nil {
		return claudeProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the address
		}
		return claudeProfile{}, fmt.Errorf("Anthropic couldn't be asked whose it is: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return claudeProfile{}, errClaudeProfileRefused
	case resp.StatusCode != http.StatusOK:
		return claudeProfile{}, fmt.Errorf("Anthropic couldn't be asked whose it is: HTTP %d", resp.StatusCode)
	}
	var p claudeProfile
	if err := json.Unmarshal(b, &p); err != nil || strings.TrimSpace(p.Account.Email) == "" {
		return claudeProfile{}, errors.New("Anthropic didn't say whose account it is")
	}
	p.Account.Email = strings.TrimSpace(p.Account.Email)
	return p, nil
}

// claudeCheckedLogin is e as magpie keeps it, named by Anthropic's answer
// for its access token: the file's email, uuids and organization are not
// used. One whose access token ran out, by the file's own expiresAt, isn't
// asked.
func claudeCheckedLogin(ctx context.Context, e loginImport, scopes []string) (savedLogin, error) {
	if e.accessToken == "" {
		return savedLogin{}, errors.New("no access token in it, which Anthropic is asked whose it is with")
	}
	if e.expiresAt > 0 && time.UnixMilli(e.expiresAt).Before(time.Now()) {
		return savedLogin{}, errClaudeProfileRefused
	}
	p, err := askClaudeProfile(ctx, e.accessToken)
	if err != nil {
		return savedLogin{}, err
	}
	c := claudeCredentials{raw: map[string]any{}, OAuth: claudeAuth{AccessToken: e.accessToken, RefreshToken: e.refreshToken,
		ExpiresAt: e.expiresAt, Scopes: scopes,
		SubscriptionType: strings.TrimPrefix(p.Organization.Type, "claude_"), RateLimitTier: p.Organization.RateLimitTier}}
	if c.OAuth.ExpiresAt == 0 {
		c.OAuth.ExpiresAt = 1 // spent: Claude Code refreshes it the first time it runs on it
	}
	acct := map[string]any{"emailAddress": p.Account.Email}
	for k, v := range map[string]string{"accountUuid": p.Account.UUID, "organizationUuid": p.Organization.UUID, "organizationName": p.Organization.Name} {
		if v != "" {
			acct[k] = v
		}
	}
	return claudeLogin(c, acct)
}
