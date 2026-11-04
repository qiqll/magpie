package gateway

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// usersBackups is every copy of user-accounts.json dropCopiedSeats left
// beside it.
func usersBackups(t *testing.T) []string {
	t.Helper()
	out, err := filepath.Glob(usersPath() + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// copiedUsers writes user-accounts.json as T-075 left it: bob, the shared
// users' bob (Front's, a key that shares them), on me@, and the same seat
// written under Tenant, which keeps its users its own since T-089.
func copiedUsers(t *testing.T) (keys []access.Key, secrets []string, was []byte) {
	t.Helper()
	keys, secrets = newCaller(t, "Tenant", "Front")
	shareUsers(t, keys[1].ID)
	at := ago(time.Hour)
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"codex": {"bob": bound("me@example.com", "", at)}},
		keys[1].ID: {"codex": {"bob": bound("me@example.com", "", at)}},
	}})
	was, err := os.ReadFile(usersPath())
	if err != nil {
		t.Fatal(err)
	}
	return keys, secrets, was
}

// The owner's option A (T-091): Tenant's bob, a copy T-075 wrote of the
// shared bob's seat, is let go of it when the file is read, the shared
// bob keeping it; Tenant's bob is bound afresh on its next request, to
// an account of its own. A copy of the file as it was is beside it, and
// the log says which seat went, by key id, never the key's secret.
func TestCopiedSeatOfASharedUserLetGo(t *testing.T) {
	codexSignedIn(t, "spare@example.com") // me: acct-1, spare: acct-2
	asked, _ := usersUpstream(t, nil)
	keys, secrets, was := copiedUsers(t)
	logs := keepLog(t)
	s := byUserUpTo(t, New(), 2)
	if rec := userPostWith(s, secrets[0], "bob", "tenant's bob"); rec.Code != 200 {
		t.Fatalf("Tenant's bob: %d %s", rec.Code, rec.Body.String())
	}
	if got := asked(true); len(got) != 1 || got[0] != "acct-2" {
		t.Fatalf("Tenant's bob asked %v, not an account of its own (acct-2)", got)
	}
	if r := lastRoute(s).User; r == nil || r.Why != "bound" || r.Account != "spare@example.com" {
		t.Fatalf("Tenant's bob: %+v", r)
	}
	f := usersFile(t)
	if b := f.Keys[keys[1].ID]["codex"]["bob"]; b.Account != "me@example.com" || len(b.More) != 0 {
		t.Fatalf("Front's bob changed: %+v", b)
	}
	if b := f.Keys[keys[0].ID]["codex"]["bob"]; b.Account != "spare@example.com" || len(b.More) != 0 {
		t.Fatalf("Tenant's bob: %+v", b)
	}
	backups := usersBackups(t)
	if len(backups) != 1 {
		t.Fatalf("copies of the file: %v", backups)
	}
	if b, err := os.ReadFile(backups[0]); err != nil || string(b) != string(was) {
		t.Fatalf("the copy isn't the file as it was: %v %s", err, b)
	}
	if fi, err := os.Stat(backups[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the copy's mode: %v %v", fi.Mode(), err)
	}
	said := logs.String()
	if !strings.Contains(said, keys[0].ID) || !strings.Contains(said, "codex@me@example.com") || !strings.Contains(said, `"bob"`) {
		t.Fatalf("the log doesn't say which seat went: %s", said)
	}
	for _, secret := range secrets {
		if strings.Contains(said, secret) {
			t.Fatalf("the log has a key's secret: %s", said)
		}
	}
	// read again, nothing more to let go of: no second copy
	if rec := userPostWith(s, secrets[0], "bob", "again"); rec.Code != 200 {
		t.Fatalf("Tenant's bob again: %d %s", rec.Code, rec.Body.String())
	}
	if backups := usersBackups(t); len(backups) != 1 {
		t.Fatalf("copies of the file after nothing more to let go of: %v", backups)
	}
}

// A copied seat beside one of the key's own: the copy goes, the own seat
// stays, now its first, with its tier and its last use.
func TestCopiedSeatLetGoOwnKept(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	keys, _ := newCaller(t, "Tenant")
	at := ago(time.Hour)
	tenant := bound("me@example.com", "pro", at)
	tenant.More = []userSeat{{Who: "codex@spare@example.com", Account: "spare@example.com", At: at}}
	writeUsers(t, userFile{
		Users: map[string]map[string]userBinding{"codex": {"bob": bound("me@example.com", "", at)}},
		Keys:  map[string]map[string]map[string]userBinding{keys[0].ID: {"codex": {"bob": tenant}}},
	})
	if err := SettleUsers(); err != nil {
		t.Fatal(err)
	}
	f := usersFile(t)
	if b := f.Users["codex"]["bob"]; b.Account != "me@example.com" {
		t.Fatalf("the keyless bob changed: %+v", b)
	}
	b := f.Keys[keys[0].ID]["codex"]["bob"]
	if b.Who != "codex@spare@example.com" || len(b.More) != 0 || b.Tier != "pro" || !b.Used.Equal(at) {
		t.Fatalf("Tenant's bob: %+v", b)
	}
}

// Tenant's bob, copied a seat of the shared bob's, spends no Codex reset
// of that account: let go of it, it has none.
func TestCopiedSeatSpendsNoReset(t *testing.T) {
	codexSignedIn(t, "spare@example.com") // me: acct-1, spare: acct-2
	t.Setenv("MAGPIE_ADDR", "")
	b := newResetBackend(t, "acct-1", "acct-2")
	_, secrets, _ := copiedUsers(t)
	h := lanGuard(byUserUpTo(t, New(), 1).Handler())
	r := httptest.NewRequest("POST", provider.RemoteCodexResetPath+"?user="+url.QueryEscape("me@example.com"), nil)
	r.RemoteAddr, r.Host = "192.168.1.9:5000", "127.0.0.1:3425"
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	r.Header.Set(UserHeader, "bob")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("Tenant's bob on the shared bob's account: %d %s", w.Code, w.Body.String())
	}
	if _, spent := b.seen(); spent != "" {
		t.Fatalf("spent %q", spent)
	}
}

// A key turned back to keep its users its own: its bob, one with the
// shared bob while it shared and so on its seat, is let go of it as the
// change is made (SettleUsers, as the window and the CLI call it).
func TestKeyTurnedOwnLetsGoOfTheSharedSeat(t *testing.T) {
	codexSignedIn(t)
	keys, _ := newCaller(t, "Tenant", "Front")
	shareUsers(t, keys[0].ID, keys[1].ID)
	at := ago(time.Hour)
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"codex": {"bob": bound("me@example.com", "", at)}},
		keys[1].ID: {"codex": {"bob": bound("me@example.com", "", at)}},
	}})
	if err := SettleUsers(); err != nil {
		t.Fatal(err)
	}
	if backups := usersBackups(t); len(backups) != 0 {
		t.Fatalf("both shared: nothing to let go of, yet copied: %v", backups)
	}
	if _, err := access.Update("users-key", access.Change{Key: keys[0].ID, SharesUsers: false}); err != nil {
		t.Fatal(err)
	}
	if err := SettleUsers(); err != nil {
		t.Fatal(err)
	}
	f := usersFile(t)
	if _, kept := f.Keys[keys[0].ID]; kept {
		t.Fatalf("Tenant's bob kept the shared seat: %+v", f.Keys[keys[0].ID])
	}
	if b := f.Keys[keys[1].ID]["codex"]["bob"]; b.Account != "me@example.com" {
		t.Fatalf("Front's bob: %+v", b)
	}
	if backups := usersBackups(t); len(backups) != 1 {
		t.Fatalf("copies of the file: %v", backups)
	}
}

// Which keys share unknown (the key store doesn't read: no key shares
// then), or no copy of the file written: nothing is let go of, and the
// file is left as it is.
func TestCopiedSeatKeptWhileUnsure(t *testing.T) {
	t.Run("key store unknown", func(t *testing.T) {
		// the shared bob the keyless one, of the shared users whatever
		// the key store says
		codexSignedIn(t)
		keys, _ := newCaller(t, "Tenant")
		at := ago(time.Hour)
		writeUsers(t, userFile{
			Users: map[string]map[string]userBinding{"codex": {"bob": bound("me@example.com", "", at)}},
			Keys:  map[string]map[string]map[string]userBinding{keys[0].ID: {"codex": {"bob": bound("me@example.com", "", at)}}},
		})
		was, err := os.ReadFile(usersPath())
		if err != nil {
			t.Fatal(err)
		}
		known := userSharersKnown
		userSharersKnown = func() bool { return false }
		t.Cleanup(func() { userSharersKnown = known })
		sharers := userSharers
		userSharers = func() map[string]bool { return map[string]bool{} }
		t.Cleanup(func() { userSharers = sharers })
		if err := SettleUsers(); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(usersPath()); string(b) != string(was) {
			t.Fatalf("rewritten: %s", b)
		}
		if backups := usersBackups(t); len(backups) != 0 {
			t.Fatalf("copied: %v", backups)
		}
		// known again: let go of then
		userSharersKnown, userSharers = known, sharers
		if err := SettleUsers(); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(usersPath()); string(b) == string(was) {
			t.Fatal("the key store reads again, and the copy is kept")
		}
	})
	t.Run("no copy written", func(t *testing.T) {
		codexSignedIn(t)
		keys, _, was := copiedUsers(t)
		write := writeUsersBackup
		writeUsersBackup = func(string, []byte, fs.FileMode) error { return errors.New("disk full") }
		t.Cleanup(func() { writeUsersBackup = write })
		if err := SettleUsers(); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(usersPath()); string(b) != string(was) {
			t.Fatalf("rewritten without a copy: %s", b)
		}
		userBinds.Lock()
		_, kept := userBinds.m[keys[0].ID]["codex"]["bob"]
		userBinds.Unlock()
		if !kept {
			t.Fatal("let go of in memory without a copy")
		}
	})
	t.Run("file doesn't read", func(t *testing.T) {
		// read while both keys shared, then broken, and Tenant turned
		// back to its own users: what was read stays as it was
		codexSignedIn(t)
		keys, _ := newCaller(t, "Tenant", "Front")
		shareUsers(t, keys[0].ID, keys[1].ID)
		at := ago(time.Hour)
		writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
			keys[0].ID: {"codex": {"bob": bound("me@example.com", "", at)}},
			keys[1].ID: {"codex": {"bob": bound("me@example.com", "", at)}},
		}})
		if err := SettleUsers(); err != nil {
			t.Fatal(err)
		}
		bad := []byte(`{"keys": {`)
		if err := os.WriteFile(usersPath(), bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := access.Update("users-key", access.Change{Key: keys[0].ID, SharesUsers: false}); err != nil {
			t.Fatal(err)
		}
		SettleUsers()
		if b, _ := os.ReadFile(usersPath()); string(b) != string(bad) {
			t.Fatalf("a file that doesn't read rewritten: %s", b)
		}
		if backups := usersBackups(t); len(backups) != 0 {
			t.Fatalf("copied: %v", backups)
		}
		userBinds.Lock()
		_, kept := userBinds.m[keys[0].ID]["codex"]["bob"]
		userBinds.Unlock()
		if !kept {
			t.Fatal("let go of while the file doesn't read")
		}
	})
}
