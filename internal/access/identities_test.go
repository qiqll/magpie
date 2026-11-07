package access

import (
	"slices"
	"testing"
	"time"
)

// Models › Accounts filters by the keys that work now (Identities), each
// held as a request sending it is: a key turned off, an issued key past
// its expiry, and one whose issuer is off are left out; an issued key is
// held to its issuer's models too.
func TestIdentitiesLeaveOutKeysThatDontWork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	advance := clockAt(t, time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC))
	on, _ := addKey(t, "On")
	off, _ := addKey(t, "Off")
	if _, err := Update("off-key", Change{Key: off}); err != nil {
		t.Fatal(err)
	}
	boss, _ := addKey(t, "Boss")
	if _, err := Update("models-key", Change{Key: boss, Models: []string{"claude/*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("issuer-key", Change{Key: boss, Issues: true}); err != nil {
		t.Fatal(err)
	}
	child, err := IssueKey(Issue{Name: "Child", TTL: 2 * time.Hour, Issuer: boss})
	if err != nil {
		t.Fatal(err)
	}
	short, err := IssueKey(Issue{Name: "Short", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ids := func() (out []string, models map[string][]string) {
		t.Helper()
		ws, err := Identities()
		if err != nil {
			t.Fatal(err)
		}
		models = map[string][]string{}
		for _, w := range ws {
			out = append(out, w.KeyID)
			models[w.KeyID] = w.Models
		}
		return out, models
	}
	got, models := ids()
	if want := []string{on, boss, child.ID, short.ID}; !slices.Equal(got, want) {
		t.Fatalf("keys %v, want %v (not the one off)", got, want)
	}
	if !slices.Equal(models[child.ID], []string{"claude/*"}) {
		t.Fatalf("the issued key is held to %v, not its issuer's models", models[child.ID])
	}
	advance(time.Hour)
	if got, _ := ids(); slices.Contains(got, short.ID) {
		t.Fatal("an expired key is listed")
	}
	if _, err := Update("off-key", Change{Key: boss}); err != nil {
		t.Fatal(err)
	}
	if got, _ := ids(); !slices.Equal(got, []string{on}) {
		t.Fatalf("keys %v once the issuer is off, want only %v", got, on)
	}
}
