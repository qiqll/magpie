package usage

import (
	"os"
	"testing"
	"time"
)

// FirstServed takes each account's earliest served row from when it was
// added: not a failed one, a probe, an app's report or one from before
// it was added; no ledger is none served, an unreadable one an error.
func TestFirstServedEarliestServedRow(t *testing.T) {
	added := time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second)
	at := func(d int) time.Time { return added.Add(time.Duration(d) * time.Hour) }
	k := [2]string{"codex", "me@example.com"}
	if got, err := FirstServed(map[[2]string]time.Time{k: added}); err != nil || len(got) != 0 {
		t.Fatalf("no ledger: %v %v", got, err)
	}
	for _, r := range []Record{
		{Time: at(-2), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(1), Provider: "codex", ProviderAccount: "me@example.com", Status: 401},
		{Time: at(2), Provider: "codex", ProviderAccount: "me@example.com", Status: 200, Source: SourceProbe},
		{Time: at(3), Provider: "codex", ProviderAccount: "me@example.com", Status: 200, Source: SourceReported},
		{Time: at(4), Provider: "codex", ProviderAccount: "me@example.com", Status: 200},
		{Time: at(5), Provider: "codex", ProviderAccount: "Me@Example.com", Status: 200},
	} {
		Append(r)
	}
	got, err := FirstServed(map[[2]string]time.Time{k: added})
	if err != nil || !got[k].Equal(at(4)) {
		t.Fatalf("first served %v (%v), want %v", got[k], err, at(4))
	}
	if err := os.Chmod(Path(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(Path(), 0o600) })
	if os.Getuid() != 0 {
		if _, err := FirstServed(map[[2]string]time.Time{k: added}); err == nil {
			t.Fatal("an unreadable ledger read as none served")
		}
	}
}
