package provider

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filelock"
)

// Edit, what a command or a TUI key changes a provider with, writes
// only what its change changed onto providers.json as it is when written:
// a setting set between its read of the provider and its write stays.

func editRelay(t *testing.T) {
	t.Helper()
	providersLockHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111", Models: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
}

// A provider of the user's own: Unlisted, a fallback, a key and its picks
// changed while its queue, its limit a minute and a key's weight are set.
func TestEditKeepsWhatWasSetMeanwhile(t *testing.T) {
	editRelay(t)
	_, now, err := Edit("relay", func(p *Provider) error {
		// meanwhile: the GUI, or another magpie
		if err := SetQueue("relay", 5, 60); err != nil {
			t.Fatal(err)
		}
		if err := SetKeyWeight("relay", keyID("sk-personal-1111"), 4); err != nil {
			t.Fatal(err)
		}
		p.Unlisted = true
		p.Fallback = []string{"relay/a"}
		p.Models = []string{"b"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !now.Unlisted || len(now.Fallback) != 1 {
		t.Fatalf("Edit answered %+v", now)
	}
	s, _ := storedPicks("relay")
	if !s.Unlisted || !slices.Equal(s.Fallback, []string{"relay/a"}) || !slices.Equal(s.Models, []string{"b"}) {
		t.Errorf("the change wasn't written: unlisted %v fallback %v models %v", s.Unlisted, s.Fallback, s.Models)
	}
	if s.QueueLimit != 5 || s.QueueWait != 60 {
		t.Errorf("the queue set meanwhile was written over: %d, %ds", s.QueueLimit, s.QueueWait)
	}
	if p, _ := Find("relay"); p.KeyWeight != 4 {
		t.Errorf("the key's weight set meanwhile was written over: %d", p.KeyWeight)
	}
}

// A subscription's own settings, changed as an account's name and another's
// cap are given: all stay.
func TestEditOfAnAccountKeepsWhatWasSetMeanwhile(t *testing.T) {
	providersLockHome(t)
	if _, _, err := Edit("codex", func(p *Provider) error {
		if err := SetAccountName("codex", "new@example.com", "Work"); err != nil {
			t.Fatal(err)
		}
		if err := SetAccountCap("codex", "me@example.com", 70); err != nil {
			t.Fatal(err)
		}
		p.Unlisted = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, _ := storedPicks("codex")
	if !s.Unlisted {
		t.Error("Unlisted wasn't written")
	}
	if s.AccountNames["new@example.com"] != "Work" {
		t.Errorf("the name given meanwhile was written over: %+v", s.AccountNames)
	}
	if s, _ := AccountCapsOf("codex", "me@example.com").Of("5 hours"); s != 70 {
		t.Errorf("the cap set meanwhile was written over: %d%%", s)
	}
}

// A change Save refuses is refused, and one to a provider removed meanwhile
// doesn't make it again: providers.json byte for byte as it was.
func TestEditRefusedWritesNothing(t *testing.T) {
	editRelay(t)
	file := filepath.Join(appdir.Config(), "providers.json")
	was, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Edit("relay", func(p *Provider) error {
		p.Chat = ""
		return nil
	}); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("a provider left with no base URL: %v", err)
	}
	if now, _ := os.ReadFile(file); !bytes.Equal(was, now) {
		t.Fatalf("a refused change wrote providers.json:\n%s", now)
	}
	if _, _, err := Edit("relay", func(p *Provider) error {
		if err := Delete("relay"); err != nil {
			t.Fatal(err)
		}
		p.Unlisted = true
		return nil
	}); err == nil || !strings.Contains(err.Error(), "removed meanwhile") {
		t.Fatalf("a change to a provider removed meanwhile: %v", err)
	}
	if _, ok := storedPicks("relay"); ok {
		t.Fatal("a provider removed meanwhile was made again")
	}
}

// A change that changes nothing writes nothing, and a field cleared is
// cleared.
func TestEditClearsAndLeavesAlone(t *testing.T) {
	editRelay(t)
	if _, _, err := Edit("relay", func(p *Provider) error { p.Fallback = []string{"relay/a"}; return nil }); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(appdir.Config(), "providers.json")
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	was, _ := os.ReadFile(file)
	if _, _, err := Edit("relay", func(p *Provider) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(file); !bytes.Equal(was, now) {
		t.Fatal("a change of nothing rewrote providers.json")
	}
	if st2, _ := os.Stat(file); !st2.ModTime().Equal(st.ModTime()) {
		t.Fatal("a change of nothing wrote providers.json")
	}
	if _, _, err := Edit("relay", func(p *Provider) error { p.Fallback = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("relay"); len(s.Fallback) != 0 {
		t.Fatalf("the fallback cleared stayed: %v", s.Fallback)
	}
}

// The picks changed by Edit are saved with the list they were picked from,
// as Save saves them.
func TestEditPicksAsSaveDoes(t *testing.T) {
	editRelay(t)
	if _, _, err := Edit("relay", func(p *Provider) error { p.Models = []string{"b"}; return nil }); err != nil {
		t.Fatal(err)
	}
	byEdit, _ := storedPicks("relay")
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"a"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	bySave, _ := storedPicks("relay")
	if !slices.Equal(byEdit.PickedFrom, bySave.PickedFrom) || !slices.Equal(byEdit.Delisted, bySave.Delisted) {
		t.Fatalf("Edit saved the picks from %v (set aside %v), Save from %v (%v)", byEdit.PickedFrom, byEdit.Delisted, bySave.PickedFrom, bySave.Delisted)
	}
}

// What Save checks of one field against another is checked on the record
// as it is when written: a certificate check skipped on a provider made a
// preset's meanwhile is refused, nothing written.
func TestEditChecksTheRecordAsItIsThen(t *testing.T) {
	editRelay(t)
	_, _, err := Edit("relay", func(p *Provider) error {
		if err := editStored("relay", func(q *Provider) bool { q.Preset = "openai"; return true }); err != nil {
			t.Fatal(err)
		}
		p.InsecureTLS = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "certificate check") {
		t.Fatalf("a certificate check skipped on a preset's provider: %v", err)
	}
	if s, _ := storedPicks("relay"); s.InsecureTLS {
		t.Fatal("the refused change was written")
	}
}

// while another magpie holds providers.json, Edit is
// refused with BusyError and providers.json is as it was.
func TestEditRefusedWhileAnotherMagpieHolds(t *testing.T) {
	editRelay(t)
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	shortWait(t, 200*time.Millisecond)
	holdProviders(t)
	_, _, err = Edit("relay", func(p *Provider) error { p.Unlisted = true; return nil })
	var busy *filelock.BusyError
	if !errors.As(err, &busy) || !strings.Contains(err.Error(), "another magpie is changing providers.json") {
		t.Fatalf("an edit while another magpie held providers.json answered %v", err)
	}
	if now, _ := os.ReadFile(Path()); !bytes.Equal(before, now) {
		t.Fatalf("a refused edit wrote providers.json:\n%s", now)
	}
}
