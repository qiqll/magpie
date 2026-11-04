package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// T-110: the saved proxies may hold a password, as the proxy does, and an
// automatic backup isn't sealed: they are left out of it, and a restore
// keeps this computer's own (KeepOwn).
func TestAutoBackupLeavesOutSavedProxies(t *testing.T) {
	autoHome(t)
	if err := settings.SetProxies([]settings.SavedProxy{{Name: "Home", URL: "http://user:saved-SYNTHETIC-pass@10.0.0.9:3128"}}); err != nil {
		t.Fatal(err)
	}
	b, err := AutoTick(t0, "test")
	if err != nil || b == nil {
		t.Fatal(b, err)
	}
	data, err := os.ReadFile(filepath.Join(AutoDefaultDir(), b.Name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SYNTHETIC") || strings.Contains(string(data), "10.0.0.9") {
		t.Fatal("a saved proxy is in the backup")
	}
}
