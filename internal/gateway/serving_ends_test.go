package gateway

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// A gateway stopped has ended the work it started: ListenAndServe returns
// only once each WhileServing func it ran (and KeepFresh, the watch, the
// hub prune beside them) has returned, so a prune under way when it stops
// takes no lock file in a home removed after (T-206).
func TestServingWorkEndsBeforeListenAndServeReturns(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0") // never where a magpie runs
	started, parked, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var ended atomic.Bool
	was := slices.Clone(WhileServing)
	t.Cleanup(func() { WhileServing = was })
	// work still under way when the gateway's ctx ends, as a prune is
	WhileServing = append(WhileServing, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(parked)
		<-release
		ended.Store(true)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- New().ListenAndServe(ctx) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("the gateway ended before serving: %v", err)
	}
	cancel()
	<-parked
	// while the work runs, the gateway hasn't returned: one that doesn't
	// wait returns once its drain is done, well within this
	select {
	case err := <-done:
		t.Fatalf("ListenAndServe returned (%v) while the work it started still ran", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !ended.Load() {
		t.Fatal("ListenAndServe returned before the work it started ended")
	}
}
