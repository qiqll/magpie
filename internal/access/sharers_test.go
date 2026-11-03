package access

import (
	"maps"
	"os"
	"slices"
	"testing"
	"time"
)

// No key shares its users until it is set to (T-089: a key that was there
// before is its own too); "users-key" sets it and takes it off, an issued
// key can't be set, and UserSharers says the keys as the store has them,
// read again when its bytes change, none when it can't be read.
func TestUserSharers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clockAt(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	if got := UserSharers(); len(got) != 0 {
		t.Fatalf("no store: %v", got)
	}
	a, _ := addKey(t, "Front")
	b, _ := addKey(t, "Tenant")
	if got := UserSharers(); len(got) != 0 {
		t.Fatalf("none set: %v", got)
	}
	if _, err := Update("users-key", Change{Key: a, SharesUsers: true}); err != nil {
		t.Fatal(err)
	}
	if got := UserSharers(); !slices.Equal(slices.Collect(maps.Keys(got)), []string{a}) {
		t.Fatalf("Front set: %v", got)
	}
	keys, _ := List()
	if i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == a }); i < 0 || !keys[i].SharesUsers {
		t.Fatalf("listed: %+v", keys)
	}
	if _, err := Update("issuer-key", Change{Key: b, Issues: true}); err != nil {
		t.Fatal(err)
	}
	one, err := IssueKey(Issue{TTL: time.Hour, Issuer: b})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update("users-key", Change{Key: one.ID, SharesUsers: true}); err == nil {
		t.Fatal("an issued key let share its users")
	}
	// an issued key written to share by hand still doesn't
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	all, _ := load()
	for i := range all {
		if all[i].ID == one.ID {
			all[i].SharesUsers = true
		}
	}
	save(all)
	mu.Unlock()
	if got := UserSharers(); len(got) != 1 || !got[a] {
		t.Fatalf("an issued key among the sharers: %v", got)
	}
	// a store that can't be read: unknown, no key shares; read again once
	// it can
	if err := os.WriteFile(Path(), []byte(`[{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := UserSharers(); len(got) != 0 {
		t.Fatalf("unreadable: %v", got)
	}
	if err := os.WriteFile(Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := UserSharers(); len(got) != 1 || !got[a] {
		t.Fatalf("readable again: %v", got)
	}
	if _, err := Update("users-key", Change{Key: a}); err != nil {
		t.Fatal(err)
	}
	if got := UserSharers(); len(got) != 0 {
		t.Fatalf("Front its own again: %v", got)
	}
}
