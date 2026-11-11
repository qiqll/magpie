package provider

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// An account's opening time (T-154) is kept in providers.json's
// accountOpened, by the account in lower case, changed and taken off by
// SetAccountOpened alone: the editor's Save of a form without it keeps it,
// a rename moves it, the user's removal drops it, and the other fields of
// the record are left as they were.
func TestAccountOpenedKept(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	at := time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := SetAccountOpened("codex", "New@example.com", at); err != nil {
		t.Fatal(err)
	}
	if got := AccountOpenedOf("codex", "new@example.com"); !got.Equal(at) {
		t.Fatalf("kept %v, set %v", got, at)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Providers []map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range file.Providers {
		if strings.Contains(string(p["id"]), "codex") {
			var m map[string]time.Time
			found = json.Unmarshal(p["accountOpened"], &m) == nil && m["new@example.com"].Equal(at)
			if _, ok := p["accountAdded"]; !ok {
				t.Errorf("the record lost accountAdded: %s", raw)
			}
		}
	}
	if !found {
		t.Fatalf("providers.json has no accountOpened for the account: %s", raw)
	}

	later := at.Add(24 * time.Hour)
	if err := SetAccountOpened("codex", "new@example.com", later); err != nil {
		t.Fatal(err)
	}
	if got := AccountOpenedOf("codex", "new@example.com"); !got.Equal(later) {
		t.Fatalf("changed to %v, kept %v", later, got)
	}

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	q := *p
	q.AccountOpened = nil
	if err := Save(q); err != nil {
		t.Fatal(err)
	}
	if got := AccountOpenedOf("codex", "new@example.com"); !got.Equal(later) {
		t.Fatalf("Save of a form without it: %v", got)
	}

	if err := moveAccountSettings("codex", map[string]string{"new@example.com": "Renamed@example.com"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); !s.AccountOpened["renamed@example.com"].Equal(later) || len(s.AccountOpened) != 1 {
		t.Fatalf("after the rename: %v", s.AccountOpened)
	}
	if err := moveAccountSettings("codex", map[string]string{"renamed@example.com": "new@example.com"}); err != nil {
		t.Fatal(err)
	}

	if err := SetAccountOpened("codex", "new@example.com", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountOpened != nil {
		t.Fatalf("taken off, kept %v", s.AccountOpened)
	}

	if err := SetAccountOpened("codex", "new@example.com", later); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountOpened != nil {
		t.Fatalf("removed by the user, its opening stayed: %v", s.AccountOpened)
	}
}

// A time out of bounds, an account the subscription doesn't list and a
// provider of keys are refused, and nothing is written.
func TestAccountOpenedRefused(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	for _, at := range []time.Time{time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC), time.Now().Add(31 * 24 * time.Hour)} {
		if err := SetAccountOpened("codex", "new@example.com", at); err == nil {
			t.Errorf("%v kept", at)
		}
	}
	if err := SetAccountOpened("codex", "nobody@example.com", time.Now()); err == nil {
		t.Error("an account it doesn't list was given a time")
	}
	if s, _ := storedPicks("codex"); s.AccountOpened != nil {
		t.Fatalf("refused, yet kept %v", s.AccountOpened)
	}
}

// The opening goes with providers.json to another computer (Stored, then
// Mirror onto that computer's own record) and with a backup (Restore).
func TestAccountOpenedThroughASync(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	at := time.Now().Add(-3 * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := SetAccountOpened("codex", "new@example.com", at); err != nil {
		t.Fatal(err)
	}
	ps, err := Stored()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	var there []Provider
	if err := json.Unmarshal(b, &there); err != nil {
		t.Fatal(err)
	}

	firstUsedFixture(t, time.Hour) // the other computer, with its own record
	if _, ok := storedPicks("codex"); !ok {
		t.Fatal("the other computer has no record of Codex's")
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if got := AccountOpenedOf("codex", "new@example.com"); !got.Equal(at) {
		t.Errorf("after a sync: %v", got)
	}

	firstUsedFixture(t, time.Hour)
	if _, _, err := Restore(there); err != nil {
		t.Fatal(err)
	}
	if got := AccountOpenedOf("codex", "new@example.com"); !got.Equal(at) {
		t.Errorf("after a restore: %v", got)
	}
}
