package netproxy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// A test binary testenv isolated reads no system proxy, though the
// machine has one: a Mac's proxy app (127.0.0.1:7897) took plugin's and
// gui's test requests and held a plugin host's address check for 15
// seconds.
func TestIsolatedReadsNoSystemProxy(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the machine's proxy is stood in for by scutil, macOS' tool")
	}
	if os.Getenv(testenv.Marker) == "" {
		t.Fatal("netproxy's tests aren't isolated by testenv")
	}
	bin := t.TempDir()
	testenv.Program(t, filepath.Join(bin, "scutil"), "#!/bin/sh\ncat <<'EOF'\n<dictionary> {\n  HTTPSEnable : 1\n  HTTPSPort : 7897\n  HTTPSProxy : 127.0.0.1\n}\nEOF\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// the stand-in is what a read of the machine's proxy reaches
	if p, ok := system(); !ok || p.URL != "http://127.0.0.1:7897" {
		t.Fatalf("the machine's proxy reads as %+v, %v: the stand-in wasn't reached", p, ok)
	}
	if p, ok := readSystem(); !ok || p.URL != "" {
		t.Fatalf("an isolated test binary read the machine's proxy: %+v, %v", p, ok)
	}
}
