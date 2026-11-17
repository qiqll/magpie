package backup

import (
	"context"
	"errors"
	"os"
	"testing"
)

// RunAuto runs while the gateway serves, and the gateway waits for it when
// it stops (a restart, a quit; T-206). A tick whose ctx ends while it
// collects writes nothing: no backup, no temporary file left, and none
// noted, so the next tick makes it.
func TestAutoBackupCutByTheGatewaysEnd(t *testing.T) {
	autoHome(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	was := autoCollect
	t.Cleanup(func() { autoCollect = was })
	autoCollect = func(app string) (Bundle, error) {
		b, err := was(app)
		stop() // the gateway stopped while the setup was read
		return b, err
	}
	b, err := AutoTick(ctx, t0, "test")
	if !errors.Is(err, context.Canceled) || b != nil {
		t.Fatalf("a tick cut short: made %+v, %v", b, err)
	}
	c, err := LoadAuto()
	if err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(c.dir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Errorf("left in the backups' folder: %s", e.Name())
	}
	autoCollect = was
	if b, err := AutoTick(context.Background(), t0, "test"); err != nil || b == nil {
		t.Fatalf("the next tick made %+v, %v", b, err)
	}
	if l := autoList(t); len(l) != 1 {
		t.Fatalf("want the one backup, have %+v", l)
	}
}

// A write ends between its parts: what was written of it is removed, and
// nothing is at a backup's name.
func TestAutoBackupWriteCut(t *testing.T) {
	autoHome(t)
	b, err := autoCollect("test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	dir := t.TempDir()
	if _, err := writeAuto(ctx, dir, ReasonAuto, b, autoSealed{}, t0); !errors.Is(err, context.Canceled) {
		t.Fatalf("a write cut short: %v", err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Errorf("left: %s", e.Name())
	}
}
