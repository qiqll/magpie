package filelock

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Strict is a Mutex that never goes on without the file's lock: a
// change to a file the user's own settings are in is better not made at
// all than made over another magpie's. Take waits for another magpie to
// let go, tries once more, and then answers BusyError, for the change to
// be refused and the user asked to make it again. On a file system with
// no locks at all it goes on without one, as Mutex does: nothing could
// ever be changed there otherwise. A lock file that can't be opened is
// not such a system (too many files open, one another user made): it is
// tried once more and the change refused with the open's error, which a
// caller tells apart as it would the file's own (a folder not the user's).
//
// The lock is the system's own (flock, LockFileEx), on the open file: it
// is let go when the process holding it ends, however it ends, so a magpie
// that crashed or was killed never keeps another from it. The holder's pid
// is written in the lock file while it is held, to say who held it; one
// left there by a magpie that ended holding it is cleared as the next one
// starts (Clear).
type Strict struct {
	mu sync.Mutex
	// Path is the lock file: the file guarded, with ".lock".
	Path func() string
	// Locked, when set, is called once both locks are held: to have the
	// file guarded read afresh.
	Locked func()
	// wait is how long each of the two tries waits for another magpie;
	// 0 is half of Wait, so both together wait as long as Mutex does.
	wait time.Duration
	f    *os.File
}

// SetWait sets how long each of the two tries waits.
func (s *Strict) SetWait(d time.Duration) {
	s.mu.Lock()
	s.wait = d
	s.mu.Unlock()
}

// BusyError is a change refused because another magpie held the file's
// lock through a try and the one after it. The change wasn't made.
type BusyError struct {
	// Path is the lock file; Pid the magpie said to hold it, 0 unknown.
	Path string
	Pid  int
}

func (e *BusyError) Error() string {
	return "another magpie is changing " + e.File() + " right now, so this change wasn't saved: try again in a moment"
}

// File is the name of the file guarded, as the user knows it.
func (e *BusyError) File() string { return strings.TrimSuffix(filepath.Base(e.Path), ".lock") }

// Take takes the lock, or answers *BusyError with neither lock held.
func (s *Strict) Take() error {
	s.mu.Lock()
	path := s.Path()
	wait := s.wait
	if wait <= 0 {
		wait = Wait / 2
	}
	f, got, err := take(path, wait)
	switch got {
	case timedOut:
		log.Printf("%s: another magpie%s held it for %v; trying once more", path, pidSaid(holder(path)), wait)
		f, got, err = take(path, wait)
	case openFailed:
		log.Printf("%s: %v; trying once more", path, err)
		time.Sleep(100 * time.Millisecond) // no other magpie to wait for
		f, got, err = take(path, wait)
	}
	switch got {
	case openFailed:
		s.mu.Unlock()
		log.Printf("%s: %v, a try and one more; the change wasn't saved", path, err)
		return fmt.Errorf("this change wasn't saved: %w", err)
	case timedOut:
		s.mu.Unlock()
		e := &BusyError{Path: path, Pid: holder(path)}
		log.Printf("%s: another magpie%s held it through a try and one more; the change wasn't saved", path, pidSaid(e.Pid))
		return e
	case taken:
		s.f = f
		// who holds it, for another magpie to say: only ever said, so a
		// write that fails costs nothing but the name
		f.Truncate(0)
		f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	if s.Locked != nil {
		s.Locked()
	}
	return nil
}

// Unlock lets go of what Take took.
func (s *Strict) Unlock() {
	if f := s.f; f != nil {
		s.f = nil
		f.Truncate(0) // held by no one now
		Unlock(f)
		f.Close()
	}
	s.mu.Unlock()
}

// Clear looks at the lock file at path as a magpie starts, and clears
// what a magpie that ended while holding it left there: the pid it wrote.
// The lock itself went with that magpie (the system lets go of it); the
// file is kept, as Lock says why. Held now, it is another magpie's that is
// running, and left alone. left is the pid cleared, -1 for a record that
// says none, 0 for nothing cleared.
func Clear(path string) (left int) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0 // none there, or not to be opened: nothing left in it either
	}
	defer f.Close()
	if ok, err := TryLock(f); err != nil || !ok {
		return 0
	}
	defer Unlock(f)
	b, err := io.ReadAll(f)
	if err != nil || len(b) == 0 {
		return 0
	}
	if f.Truncate(0) != nil {
		return 0
	}
	left = -1
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
		left = pid
	}
	// nothing logged: magpie's log isn't open yet as it starts, and a
	// terminal's command isn't to print it
	return left
}

// holder is the pid written in the lock file at path, 0 when it can't be
// read: on Windows another process's lock keeps the file from being read.
func holder(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func pidSaid(pid int) string {
	if pid <= 0 {
		return ""
	}
	return " (pid " + strconv.Itoa(pid) + ")"
}
