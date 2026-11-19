package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// #5016, through the window's Cancel: a plugin sign-in cancelled while the
// plugin waits for its vendor's desktop app (AStudio's session file,
// plugins#658) saves nothing when the file comes after; and one whose
// authorize fails says the plugin's reason, not "s.a.callback is not a
// function".
func TestCancelledPluginSignInSavesNothing(t *testing.T) {
	bun := testenv.Bun(t)
	home := claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/signinend/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := StartPluginSignIn("signinend", 2, nil); err == nil || !strings.Contains(err.Error(), "this AStudio account is banned") {
		t.Fatalf("a failed authorize: %v", err)
	}

	st, err := StartPluginSignIn("signinend", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	marked := func(name string) bool {
		for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(home, name)); err == nil {
				return true
			}
		}
		return false
	}
	if !marked("waiting") {
		t.Fatal("the plugin never began waiting")
	}
	CancelSignIn(st.ID)
	if got, _ := SignInStatus(st.ID); got.State != "canceled" {
		t.Fatalf("after Cancel: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(home, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !marked("returned") {
		t.Fatal("the plugin's wait never came true")
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if plugin.SignedIn("signinend") {
		b, _ := os.ReadFile(plugin.AuthPath())
		t.Fatalf("a cancelled sign-in was saved: %s", b)
	}
	if got, _ := SignInStatus(st.ID); got.State != "canceled" {
		t.Fatalf("after the plugin answered: %+v", got)
	}
}

// T-262: an area's plugin sign-in cancelled while the plugin trades its
// pasted code (a rotated key, the area turned off, the owner's sign-in
// beside it) leaves no account behind, which no area would claim and the
// owner's pool would serve. Only what that sign-in saved goes: the
// owner's account stays, and so does it when the area's came to the same
// one.
func TestCancelledPluginSignInLeavesNoAreaAccount(t *testing.T) {
	bun := testenv.Bun(t)
	home := claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/signinend/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	put := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	marked := func(name string) bool {
		for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(home, name)); err == nil {
				return true
			}
		}
		return false
	}
	saved := func() string {
		t.Helper()
		b, err := os.ReadFile(plugin.AuthPath())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// the owner's, its code traded at once
	put("key", "tok-owner")
	put("go", "")
	st, err := StartPluginSignIn("signinend", 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, "the code"); err != nil {
		t.Fatal(err)
	}
	if got, _ := SignInStatus(st.ID); got.State != "done" || !strings.Contains(saved(), "tok-owner") {
		t.Fatalf("the owner's sign-in: %+v", got)
	}

	// an area's, cancelled while the plugin holds its code: the plugin
	// saves once its wait ends, after the cancel
	areaCancelled := func(key string) {
		t.Helper()
		for _, name := range []string{"go", "waiting", "returned"} {
			_ = os.Remove(filepath.Join(home, name))
		}
		put("key", key)
		st, err := StartPluginSignInWith("signinend", 8, nil, SignInOptions{Area: true})
		if err != nil {
			t.Fatal(err)
		}
		if !st.PasteCode {
			t.Fatalf("not a code sign-in: %+v", st)
		}
		pasted := make(chan error, 1)
		go func() { pasted <- SubmitSignInCallback(st.ID, "the code") }()
		if !marked("waiting") {
			t.Fatal("the plugin never began trading the code")
		}
		CancelSignIn(st.ID)
		if got, _ := SignInStatus(st.ID); got.State != "canceled" {
			t.Fatalf("after Cancel: %+v", got)
		}
		put("go", "")
		select {
		case <-pasted:
		case <-ctx.Done():
			t.Fatal("the pasted code never came back")
		}
		if got, _ := SignInStatus(st.ID); got.State != "canceled" {
			t.Fatalf("after the plugin answered: %+v", got)
		}
	}

	// to the owner's account: magpie has it, nothing is saved, and the
	// owner's is as it was
	areaCancelled("tok-owner")
	if !strings.Contains(saved(), "tok-owner") {
		t.Fatalf("the owner's account went with an area's cancelled sign-in to it: %s", saved())
	}
	// to a new one: the cancel reaches the plugin's host, which saves
	// nothing of what the plugin answers after
	var savedAfter atomic.Bool
	pluginSavedForTest = func(*signInFlow) { savedAfter.Store(true) }
	t.Cleanup(func() { pluginSavedForTest = nil })
	areaCancelled("tok-area")
	pluginSavedForTest = nil
	if b := saved(); savedAfter.Load() || strings.Contains(b, "tok-area") || !strings.Contains(b, "tok-owner") {
		t.Fatalf("after an area's sign-in cancelled as the plugin traded its code (saved: %v): %s", savedAfter.Load(), b)
	}

	// cancelled once the host saved it, before the sign-in is finished:
	// what it saved is signed out again
	for _, name := range []string{"waiting", "returned"} {
		_ = os.Remove(filepath.Join(home, name))
	}
	put("key", "tok-late-area")
	put("go", "")
	pluginSavedForTest = func(s *signInFlow) { CancelSignIn(s.status().ID) }
	st, err = StartPluginSignInWith("signinend", 8, nil, SignInOptions{Area: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, "the code"); err != nil {
		t.Fatal(err)
	}
	pluginSavedForTest = nil
	if got, _ := SignInStatus(st.ID); got.State != "canceled" {
		t.Fatalf("cancelled as it saved: %+v", got)
	}
	if b := saved(); strings.Contains(b, "tok-late-area") || !strings.Contains(b, "tok-owner") {
		t.Fatalf("after an area's sign-in cancelled as it saved: %s", b)
	}
}
