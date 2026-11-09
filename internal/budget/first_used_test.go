package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A row the gateway records as served on an account keeps the account's
// first use (provider.NoteAccountUsed); magpie's own Test, a failed try,
// a row with no account and a plugin's cache keep-alive ping don't.
func TestServedRowKeepsFirstUse(t *testing.T) {
	path := provider.Path()
	os.MkdirAll(filepath.Dir(path), 0o700)
	added := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	b, _ := json.Marshal(map[string]any{"providers": []map[string]any{{
		"id": "codex", "accountAdded": map[string]time.Time{"me@example.com": added},
	}}})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range []usage.Record{
		{Provider: "codex", ProviderAccount: "me@example.com", Status: 200, Source: usage.SourceProbe},
		{Provider: "codex", ProviderAccount: "me@example.com", Status: 429},
		{Provider: "codex", Status: 200},
		{Provider: "codex", ProviderAccount: "me@example.com", Status: 200, Kind: usage.KindKeepalive},
	} {
		Append(r)
	}
	if at := provider.AccountFirstUsedOf("codex", "me@example.com"); !at.IsZero() {
		t.Fatalf("kept a first use from a row that wasn't a use: %v", at)
	}
	served := time.Now().Add(-time.Minute)
	Append(usage.Record{Time: served, Provider: "codex", ProviderAccount: "Me@Example.com", Status: 200})
	if at := provider.AccountFirstUsedOf("codex", "me@example.com"); !at.Equal(served.UTC().Truncate(time.Second)) {
		t.Fatalf("first use %v, want %v", at, served)
	}
}
