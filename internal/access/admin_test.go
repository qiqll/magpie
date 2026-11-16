package access

import (
	"testing"
)

// Admin keys are kept last: a key added, issued or moved goes before
// them, so the window's list of gateway keys alone has each key where the
// store has it. An admin key itself isn't moved.
func TestAdminKeysStayLast(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Update("add-key", Change{Name: "A"}); err != nil {
		t.Fatal(err)
	}
	admin, _, err := AddAdmin(AdminArea, "ar_1", "Area admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update("add-key", Change{Name: "B"}); err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	if len(keys) != 3 || keys[1].Name != "B" || keys[2].ID != admin.ID {
		t.Fatal("a key added after an admin key:", names(keys))
	}
	if _, err := Update("move-key", Change{Key: keys[0].ID, Index: ptr(2)}); err == nil {
		t.Fatal("a key moved past the admin keys")
	}
	if _, err := Update("move-key", Change{Key: keys[0].ID, Index: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("move-key", Change{Key: admin.ID, Index: ptr(0)}); err == nil {
		t.Fatal("an admin key moved")
	}
	keys, _ = List()
	if names(keys) != "B,A,Area admin" {
		t.Fatal("order:", names(keys))
	}
	if _, err := Update("rotate-key", Change{Key: admin.ID, Secret: "my-own-value-123"}); err == nil {
		t.Fatal("an admin key given a value of the user's own")
	}
	if who, ok := Authenticate(mustCopy(t, admin.ID)); !ok || who.Admin != AdminArea || who.Area != "ar_1" || !who.ReadOnly || len(who.Accounts) != 1 {
		t.Fatal("the admin key's caller:", who, ok)
	}
}

func names(keys []Key) string {
	s := ""
	for i, k := range keys {
		if i > 0 {
			s += ","
		}
		s += k.Name
	}
	return s
}

func mustCopy(t *testing.T, id string) string {
	t.Helper()
	s, err := Update("copy-key", Change{Key: id})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ptr[T any](v T) *T { return &v }
