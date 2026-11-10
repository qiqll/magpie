package provider

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filelock"
)

// TestProvidersLockHolder is the other magpie of the tests below: it takes
// providers.json's lock as a magpie does, says so, and holds it until its
// stdin is closed (or it is killed).
func TestProvidersLockHolder(t *testing.T) {
	path := os.Getenv("MAGPIE_PROVIDERS_HOLD")
	if path == "" {
		t.Skip("run by TestProvidersChangeRefusedWhileAnotherMagpieHolds")
	}
	s := filelock.Strict{Path: func() string { return path }}
	if err := s.Take(); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	io.Copy(io.Discard, os.Stdin)
}

// holdProviders starts another magpie holding providers.json's lock, ended
// when the test is.
func holdProviders(t *testing.T) *exec.Cmd {
	t.Helper()
	holder := exec.Command(os.Args[0], "-test.run=^TestProvidersLockHolder$", "-test.count=1")
	holder.Env = append(os.Environ(), "MAGPIE_PROVIDERS_HOLD="+Path()+".lock")
	in, _ := holder.StdinPipe()
	out, _ := holder.StdoutPipe()
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); holder.Process.Kill(); holder.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "locked\n" {
		t.Fatalf("the other magpie: %q", line)
	}
	return holder
}

// shortWait makes each of providersMu's two tries wait d.
func shortWait(t *testing.T, d time.Duration) {
	t.Helper()
	providersMu.SetWait(d)
	t.Cleanup(func() { providersMu.SetWait(0) })
}

// While another magpie holds providers.json's lock, a change waits,
// tries once more, and is then refused with BusyError, providers.json as
// it was; it never goes on without the lock. Once that magpie is killed,
// the next magpie to start clears what it left in the lock file, and the
// change is made.
func TestProvidersChangeRefusedWhileAnotherMagpieHolds(t *testing.T) {
	providersLockHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	shortWait(t, 300*time.Millisecond)
	holder := holdProviders(t)

	start := time.Now()
	err = SetRPM("relay", 30)
	var busy *filelock.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("a change while another magpie held providers.json answered %v", err)
	}
	if took := time.Since(start); took < 600*time.Millisecond {
		t.Errorf("refused after %v: not after a try and one more", took)
	}
	// on Windows the holder's lock keeps its pid from being read
	if busy.Pid != holder.Process.Pid && !(runtime.GOOS == "windows" && busy.Pid == 0) {
		t.Errorf("said held by pid %d, not the other magpie's %d", busy.Pid, holder.Process.Pid)
	}
	if busy.File() != "providers.json" {
		t.Errorf("said of %q", busy.File())
	}
	if after, _ := os.ReadFile(Path()); !bytes.Equal(after, before) {
		t.Fatalf("providers.json was written while another magpie held it:\n%s", after)
	}
	// nothing held here after the refusal: another change in this magpie
	// isn't kept waiting on it
	if err := SetAccountCap("codex", "new@example.com", 70); !errors.As(err, &busy) {
		t.Fatalf("a second change answered %v", err)
	}

	// the other magpie crashes, leaving its pid in the lock file
	holder.Process.Kill()
	holder.Wait()
	if b, _ := os.ReadFile(Path() + ".lock"); len(bytes.TrimSpace(b)) == 0 {
		t.Fatal("the other magpie left nothing to clear in the lock file")
	}
	// the system lets go of a killed process's lock at once on Unix, and
	// soon after on Windows
	for deadline := time.Now().Add(5 * time.Second); ClearLeftLock() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("nothing cleared once the other magpie was killed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b, _ := os.ReadFile(Path() + ".lock"); len(b) != 0 {
		t.Errorf("the lock file still says %q once a magpie started", b)
	}
	if err := SetRPM("relay", 30); err != nil {
		t.Fatalf("a change after the other magpie ended: %v", err)
	}
	if p, err := Find("relay"); err != nil || p.MaxRPM != 30 {
		t.Fatalf("the change wasn't written: %+v %v", p.MaxRPM, err)
	}
	if b, _ := os.ReadFile(Path() + ".lock"); len(b) != 0 {
		t.Errorf("the lock file says %q after this magpie let go", b)
	}
}
