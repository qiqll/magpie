package provider

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// The owner's sign-in to a plugin's account that an area owns fails with
// why (T-228), and the area's account keeps its tokens: before, settle
// folded the owner's sign-in into it, the area's tokens replaced and the
// owner's sign-in the area's admin's from then on. areaPluginKeys is what
// tells the host: with the area's claim let go, the same sign-in goes
// to the account again, as the owner's own.
func TestSignInOwnerOfAnAreasPluginAccountFails(t *testing.T) {
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	claudeHome(t)
	t.Cleanup(plugin.Settle)
	step := minuteEach(t)
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(step(), abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(step()); err != nil {
		t.Fatal(err)
	}
	// the area's sign-in, claimed as admin_plugin_signin.go claims one
	a, err := plugin.AuthorizeNew(step(), "fakeco", 1, map[string]string{"where": "work", "team": "a"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := plugin.Finish(step(), a.Session, "good")
	if err != nil {
		t.Fatal(err)
	}
	agent := PluginID("fakeco")
	user := pluginUser(saved)
	if user == "" {
		t.Fatalf("the area's account %+v has no name", saved)
	}
	ar, err := area.Create("学习", nil, []string{agent})
	if err != nil {
		t.Fatal(err)
	}
	ref := agent + "/" + LoginID(agent, user)
	if err := area.Claim(ar.ID, ref); err != nil {
		t.Fatal(err)
	}
	refresh := func() any {
		t.Helper()
		return plugin.Auths("fakeco")[saved.Account]["refresh"]
	}
	owner := func() SignInState {
		t.Helper()
		st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": "a/u1"})
		if err != nil {
			t.Fatal(err)
		}
		_ = SubmitSignInCallback(st.ID, "good")
		got, _ := SignInStatus(st.ID)
		return got
	}
	if st := owner(); st.State != "failed" || st.Error != plugin.ErrAreaOwned.Error() {
		t.Fatalf("the owner's sign-in to the area's account: %+v", st)
	}
	if got, n := refresh(), len(plugin.Auths("fakeco")); got != "r-a" || n != 1 {
		t.Fatalf("the area's account is %v, %d accounts", got, n)
	}
	// the owner's key that is an area's key is refused the same way
	k, err := plugin.APIKeyNew(step(), "fakeco", 0, nil, "sk-area-key")
	if err != nil {
		t.Fatal(err)
	}
	kref := agent + "/" + LoginID(agent, pluginUser(k))
	if err := area.Claim(ar.ID, kref); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginAPIKey(step(), "fakeco", 0, nil, "sk-area-key"); !errors.Is(err, plugin.ErrAreaOwned) {
		t.Fatalf("the owner's key that is the area's: %v, not ErrAreaOwned", err)
	}
	// let go of by the area, the account is the owner's to sign in to
	if err := area.Release(ref); err != nil {
		t.Fatal(err)
	}
	if st := owner(); st.State != "done" {
		t.Fatalf("the owner's sign-in once the area let go: %+v", st)
	}
	if got := refresh(); got != "r-a/u1" {
		t.Fatalf("the owner's sign-in once the area let go left %v", got)
	}
}
