package provider

import (
	"errors"
	"testing"
)

// A setup-token's account is the one its name says before it is added,
// and a token whose name another token's account has is refused, that
// account's token left as it was.
func TestClaudeTokenNameNeverWritesOver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const tok = "sk-ant-oat01-first-token-aaaaaaaaaaaaaaaa"
	user, err := ClaudeTokenUser(tok)
	if err != nil {
		t.Fatal(err)
	}
	before := LoginID("claude", user)
	got, added, err := AddClaudeSetupTokenThrough(tok, nil)
	if err != nil || !added || got != user {
		t.Fatal(got, added, err)
	}
	if after := LoginID("claude", user); after != before {
		t.Fatalf("its id moved as it was added: %s, then %s", before, after)
	}
	// another token given the same name, as a collision would be
	if _, _, err := AddClaudeSetupTokenThrough("sk-ant-oat01-other-token-bbbbbbbbbbbbbbbb", nil); err != nil {
		t.Fatal(err)
	}
	other, _ := ClaudeTokenUser("sk-ant-oat01-other-token-bbbbbbbbbbbbbbbb")
	if other == user {
		t.Fatal("two tokens, one name")
	}
	// a third whose name is made to match the first's
	old := claudeTokenNameFor
	claudeTokenNameFor = func(string) string { return user }
	t.Cleanup(func() { claudeTokenNameFor = old })
	if _, _, err := AddClaudeSetupTokenThrough("sk-ant-oat01-third-token-cccccccccccccccc", nil); !errors.Is(err, ErrClaudeTokenName) {
		t.Fatal("a colliding name was added:", err)
	}
	for _, l := range Logins("claude") {
		if l.User == user {
			loginsMu.Lock()
			for _, s := range readLogins() {
				if s.User == user && claudeSetupToken(s.Auth) != tok {
					t.Fatal("the first account's token was written over")
				}
			}
			loginsMu.Unlock()
		}
	}
}
