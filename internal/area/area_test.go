package area

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func sandbox(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	last.Lock()
	last.read, last.areas = false, nil
	last.Unlock()
}

// An area is made, renamed, turned off, and deleted only once it owns
// no account; a note two areas would share is refused, in any case.
func TestAreaLifecycle(t *testing.T) {
	sandbox(t)
	study, err := Create(" 学习 ", []string{"deepseek", "deepseek", " "}, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if study.Note != "学习" || len(study.Providers) != 1 || study.ID == "" {
		t.Fatal("made:", study)
	}
	if _, err := Create("学习", nil, nil); !errors.Is(err, ErrNoteInUse) {
		t.Fatal("a second 学习:", err)
	}
	work, err := Create("Work", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update(work.ID, Spec{Note: ptr("work")}); err != nil {
		t.Fatal("renamed to its own note in another case:", err)
	}
	if _, err := Update(work.ID, Spec{Note: ptr("学习")}); !errors.Is(err, ErrNoteInUse) {
		t.Fatal("renamed onto another's note:", err)
	}
	for _, bad := range []string{"", "   ", "a\nb", string(make([]rune, 41))} {
		if _, err := Create(bad, nil, nil); err == nil {
			t.Fatalf("note %q taken", bad)
		}
	}
	if a, err := Update(study.ID, Spec{Off: ptr(true)}); err != nil || !a.Off {
		t.Fatal("turned off:", a, err)
	}
	if err := Claim(study.ID, "deepseek/fp1"); err != nil {
		t.Fatal(err)
	}
	if err := Delete(study.ID); !errors.Is(err, ErrNotEmpty) {
		t.Fatal("deleted while it owns an account:", err)
	}
	if err := Release("deepseek/fp1"); err != nil {
		t.Fatal(err)
	}
	if err := Delete(study.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(study.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted area found:", err)
	}
	if err := Delete("ar_none"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// An account belongs to one area at most: a claim of one any area owns
// is refused, its own or another's, and Owner names who owns it.
func TestClaimIsExclusive(t *testing.T) {
	sandbox(t)
	a, _ := Create("A", nil, nil)
	b, _ := Create("B", nil, nil)
	if err := Claim(a.ID, "codex/l1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if err := Claim(id, "codex/l1"); !errors.Is(err, ErrTaken) {
			t.Fatal("claimed twice:", id, err)
		}
	}
	if err := Claim("ar_none", "codex/l2"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if id, contested, _ := Owner("codex/l1"); id != a.ID || contested {
		t.Fatal("owner:", id, contested)
	}
	if id, _, _ := Owner("codex/l2"); id != "" {
		t.Fatal("unowned has an owner:", id)
	}
	if err := Rename("codex/l1", "codex/l9"); err != nil {
		t.Fatal(err)
	}
	if id, _, _ := Owner("codex/l9"); id != a.ID {
		t.Fatal("renamed ref lost its area:", id)
	}
}

// A key replaced in place keeps the refs it had (T-186), so what it spent
// under them stays its area's; an areas.json from before Previous reads
// as it did.
func TestRenameKeepsPrevious(t *testing.T) {
	sandbox(t)
	old := `{"version":1,"areas":[{"id":"ar_old","note":"旧","created":"2026-11-01T00:00:00Z","accounts":[{"ref":"plan/k1","added":"2026-11-01T00:00:00Z"}]}]}`
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Get("ar_old")
	if err != nil || len(a.Accounts) != 1 || a.Accounts[0].Ref != "plan/k1" || a.Accounts[0].Previous != nil {
		t.Fatal("an old areas.json:", a, err)
	}
	for _, step := range [][2]string{{"plan/k1", "plan/k2"}, {"plan/k2", "plan/k3"}, {"plan/k3", "plan/k1"}} {
		if err := Rename(step[0], step[1]); err != nil {
			t.Fatal(err)
		}
	}
	a, _ = Get("ar_old")
	if c := a.Accounts[0]; c.Ref != "plan/k1" || !slices.Equal(c.Previous, []string{"plan/k2", "plan/k3"}) || !c.Added.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("after three replacements: %+v", c)
	}
	// a list's copy is its own
	a.Accounts[0].Previous[0] = "x"
	if b, _ := Get("ar_old"); b.Accounts[0].Previous[0] != "plan/k2" {
		t.Fatal("List shares Previous with its cache")
	}
}

// Two magpies claiming one account at once: exactly one gets it.
func TestConcurrentClaims(t *testing.T) {
	sandbox(t)
	var ids []string
	for _, n := range []string{"A", "B", "C", "D"} {
		a, _ := Create(n, nil, nil)
		ids = append(ids, a.ID)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if Claim(ids[i%len(ids)], "codex/same") == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if won != 1 {
		t.Fatal("claims won:", won)
	}
	areas, _ := List()
	n := 0
	for _, a := range areas {
		if a.Has("codex/same") {
			n++
		}
	}
	if n != 1 {
		t.Fatal("areas owning it:", n)
	}
}

// A ref two areas claim (written by hand) is no area's; areas.json that
// can't be parsed keeps the last good read, is unknown with none, and is
// never written over.
func TestUnreadableAndContested(t *testing.T) {
	sandbox(t)
	os.MkdirAll(dirOf(), 0o755)
	os.WriteFile(Path(), []byte(`{"version":1,"areas":[{"id":"ar_a","note":"A","accounts":[{"ref":"x/1"}]},{"id":"ar_b","note":"B","accounts":[{"ref":"x/1"}]}]}`), 0o600)
	if id, contested, err := Owner("x/1"); id != "" || !contested || err != nil {
		t.Fatal("contested:", id, contested, err)
	}
	os.WriteFile(Path(), []byte(`{"areas":[`), 0o600)
	if areas, err := List(); err != nil || len(areas) != 2 {
		t.Fatal("last good read not kept:", areas, err)
	}
	last.Lock()
	last.read = false
	last.Unlock()
	if _, err := List(); !errors.Is(err, ErrUnreadable) {
		t.Fatal("unknown taken for no areas:", err)
	}
	if _, err := Create("C", nil, nil); !errors.Is(err, ErrUnreadable) {
		t.Fatal("unreadable file written over:", err)
	}
	if b, _ := os.ReadFile(Path()); string(b) != `{"areas":[` {
		t.Fatal("file changed:", string(b))
	}
}

func ptr[T any](v T) *T { return &v }

func dirOf() string { p := Path(); return p[:len(p)-len("/areas.json")] }
