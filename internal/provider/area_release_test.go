package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/area"
)

// An area's key removed from magpie, by anyone, is no longer the area's;
// one replaced in place is the area's under its new value.
func TestAreaLetsGoOfARemovedKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-first-000000001", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sk-area-0000000001", "sk-area-0000000002", "sk-area-0000000003"} {
		if err := AddKey("relay", "", k, ""); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := area.Create("学习", nil, nil)
	one, two, three := "relay/"+KeyID("sk-area-0000000001"), "relay/"+KeyID("sk-area-0000000002"), "relay/"+KeyID("sk-area-0000000003")
	for _, ref := range []string{one, two, three} {
		if err := area.Claim(a.ID, ref); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveKey("relay", KeyID("sk-area-0000000001")); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveKeys("relay", []string{KeyID("sk-area-0000000002")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReplaceKey("relay", KeyID("sk-area-0000000003"), "sk-area-0000000004"); err != nil {
		t.Fatal(err)
	}
	got, _ := area.Get(a.ID)
	if got.Has(one) || got.Has(two) || got.Has(three) || !got.Has("relay/"+KeyID("sk-area-0000000004")) {
		t.Fatal("the area's accounts:", got.Accounts)
	}
	// a removal that fails lets go of nothing
	if err := RemoveKey("relay", "no-such-key"); err == nil {
		t.Fatal("removed a key it doesn't have")
	}
	if got, _ := area.Get(a.ID); len(got.Accounts) != 1 {
		t.Fatal("after a failed removal:", got.Accounts)
	}
}

// An area's signed-in account forgotten is no longer the area's.
func TestAreaLetsGoOfAForgottenAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, u := range []string{"owner@example.com", "area@example.com"} {
		if _, _, err := addLoginOf(codexTestLogin(t, u), u == "area@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := area.Create("工作", nil, nil)
	ref := "codex/" + LoginID("codex", "area@example.com")
	if err := area.Claim(a.ID, ref); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("codex", "area@example.com"); err != nil {
		t.Fatal(err)
	}
	if owner, _, _ := area.Owner(ref); owner != "" {
		t.Fatal("the forgotten account is still", owner+"'s")
	}
}

// A key provider deleted takes its keys from their areas: added again
// later, a key of the same value is no area's.
func TestAreaLetsGoOfADeletedProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-first-000000001", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "", "sk-area-0000000001", ""); err != nil {
		t.Fatal(err)
	}
	a, _ := area.Create("学习", nil, nil)
	ref := "relay/" + KeyID("sk-area-0000000001")
	area.Claim(a.ID, ref)
	if err := Delete("relay"); err != nil {
		t.Fatal(err)
	}
	if owner, _, _ := area.Owner(ref); owner != "" {
		t.Fatal("a deleted provider's key still", owner+"'s")
	}
}
