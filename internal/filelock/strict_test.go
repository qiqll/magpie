package filelock

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestStrictHolder is the other magpie of TestStrictAcrossProcesses: it
// takes a Strict, says so, and holds it until its stdin is closed or it is
// killed.
func TestStrictHolder(t *testing.T) {
	path := os.Getenv("MAGPIE_FILELOCK_STRICT")
	if path == "" {
		t.Skip("run by TestStrictAcrossProcesses")
	}
	s := Strict{Path: func() string { return path }}
	if err := s.Take(); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	io.Copy(io.Discard, os.Stdin)
}

// T-079: a Strict held by another magpie is waited for, tried once more,
// then refused with BusyError naming it; the magpie killed, Clear clears
// the pid it left and the lock is taken again.
func TestStrictAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json.lock")
	holder := exec.Command(os.Args[0], "-test.run=^TestStrictHolder$", "-test.count=1")
	holder.Env = append(os.Environ(), "MAGPIE_FILELOCK_STRICT="+path)
	in, _ := holder.StdinPipe()
	out, _ := holder.StdoutPipe()
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); holder.Process.Kill(); holder.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "locked\n" {
		t.Fatalf("the other magpie: %q", line)
	}

	s := Strict{Path: func() string { return path }, Wait: 200 * time.Millisecond}
	start := time.Now()
	err := s.Take()
	var busy *BusyError
	if !errors.As(err, &busy) {
		s.Unlock()
		t.Fatalf("taken while another magpie held it: %v", err)
	}
	if took := time.Since(start); took < 400*time.Millisecond {
		t.Errorf("refused after %v: not after a try and one more", took)
	}
	// on Windows the holder's lock keeps its pid from being read
	if (busy.Pid != holder.Process.Pid && !(runtime.GOOS == "windows" && busy.Pid == 0)) || busy.File() != "f.json" {
		t.Errorf("said %q held by %d, want f.json by %d", busy.File(), busy.Pid, holder.Process.Pid)
	}
	// refused, it holds nothing: the next Take isn't kept on its mutex
	done := make(chan error, 1)
	go func() { done <- s.Take() }()
	select {
	case err := <-done:
		if !errors.As(err, &busy) {
			t.Fatalf("second take: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a refused Take kept its mutex")
	}

	// in Clear, a lock held by a magpie still running is left alone
	if left := Clear(path); left != 0 {
		t.Fatalf("cleared %d while its magpie ran", left)
	}
	holder.Process.Kill()
	holder.Wait()
	if left := Clear(path); left != holder.Process.Pid {
		t.Fatalf("cleared %d, want the killed magpie's %d", left, holder.Process.Pid)
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatalf("left in the lock file: %q", b)
	}
	if left := Clear(path); left != 0 {
		t.Fatalf("cleared %d from a lock file left empty", left)
	}
	if left := Clear(filepath.Join(t.TempDir(), "none.lock")); left != 0 {
		t.Fatalf("cleared %d from no file", left)
	}

	if err := s.Take(); err != nil {
		t.Fatalf("not taken once the other magpie ended: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != strconv.Itoa(os.Getpid())+"\n" {
		t.Errorf("held, the lock file says %q", b)
	}
	s.Unlock()
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Errorf("let go, the lock file says %q", b)
	}
}
