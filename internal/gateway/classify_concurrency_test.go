package gateway

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type classifyResult struct {
	v      verdict
	cached bool
	err    error
}

func resetClassified(t *testing.T) {
	t.Helper()
	reset := func() {
		classified.Lock()
		defer classified.Unlock()
		clear(classified.m)
		clear(classified.failed)
		clear(classified.last)
		clear(classified.inflight)
	}
	reset()
	t.Cleanup(reset)
}

func TestConcurrentClassificationSharesAnswer(t *testing.T) {
	resetClassified(t)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		results := make(chan classifyResult, 17)
		var calls atomic.Int32
		want := verdict{Intent: "refactoring", Effort: "high", Score: 2, Sure: 0.9}
		ask := func(string, []string, before, bool, string) (verdict, error) {
			calls.Add(1)
			<-release
			return want, nil
		}
		run := func() {
			v, cached, err := classify(ask, t.Name(), []string{"refactoring"}, before{}, true, "rename this package")
			results <- classifyResult{v, cached, err}
		}
		go run()
		synctest.Wait()
		for range 16 {
			go run()
		}
		synctest.Wait()
		if n := calls.Load(); n != 1 {
			t.Errorf("concurrent calls = %d, want 1", n)
		}
		close(release)
		synctest.Wait()
		shared := 0
		for range 17 {
			got := <-results
			if got.v != want || got.err != nil {
				t.Fatalf("result = %+v, want %+v", got, want)
			}
			if got.cached {
				shared++
			}
		}
		if shared != 16 {
			t.Fatalf("shared answers = %d, want 16", shared)
		}
		v, cached, err := classify(ask, t.Name(), []string{"refactoring"}, before{}, true, "rename this package")
		if v != want || !cached || err != nil || calls.Load() != 1 {
			t.Fatalf("completed cache = %+v, %v, %v; calls = %d", v, cached, err, calls.Load())
		}
	})
}

func TestConcurrentClassificationDistinctKeys(t *testing.T) {
	resetClassified(t)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		results := make(chan classifyResult, 7)
		var calls atomic.Int32
		want := verdict{Intent: "refactoring"}
		ask := func(string, []string, before, bool, string) (verdict, error) {
			calls.Add(1)
			<-release
			return want, nil
		}
		for _, input := range []struct {
			model   string
			intents []string
			prev    before
			effort  bool
			text    string
		}{
			{t.Name(), []string{"refactoring"}, before{}, false, "rename"},
			{t.Name() + "/other", []string{"refactoring"}, before{}, false, "rename"},
			{t.Name(), []string{"testing"}, before{}, false, "rename"},
			{t.Name(), []string{"refactoring"}, before{Intent: "refactoring"}, false, "rename"},
			{t.Name(), []string{"refactoring"}, before{Effort: "high"}, false, "rename"},
			{t.Name(), []string{"refactoring"}, before{}, true, "rename"},
			{t.Name(), []string{"refactoring"}, before{}, false, "test"},
		} {
			go func() {
				v, cached, err := classify(ask, input.model, input.intents, input.prev, input.effort, input.text)
				results <- classifyResult{v, cached, err}
			}()
		}
		synctest.Wait()
		if n := calls.Load(); n != 7 {
			t.Errorf("distinct calls = %d, want 7", n)
		}
		close(release)
		synctest.Wait()
		for range 7 {
			if got := <-results; got.v != want || got.cached || got.err != nil {
				t.Fatalf("distinct result = %+v", got)
			}
		}
	})
}

func TestConcurrentClassificationSharesFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		rests bool
	}{
		{"upstream", errors.New("classifier down"), true},
		{"answer", answerError{errors.New("not a number")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetClassified(t)
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				results := make(chan classifyResult, 2)
				var calls atomic.Int32
				want := verdict{Intent: "refactoring"}
				ask := func(string, []string, before, bool, string) (verdict, error) {
					n := calls.Add(1)
					<-release
					if n == 1 {
						return want, tc.err
					}
					return want, nil
				}
				run := func() {
					v, cached, err := classify(ask, t.Name(), []string{"refactoring"}, before{}, false, "rename")
					results <- classifyResult{v, cached, err}
				}
				go run()
				synctest.Wait()
				go run()
				synctest.Wait()
				if n := calls.Load(); n != 1 {
					t.Errorf("concurrent calls = %d, want 1", n)
				}
				close(release)
				synctest.Wait()
				for range 2 {
					if got := <-results; got.v != (verdict{}) || got.cached || !errors.Is(got.err, tc.err) {
						t.Fatalf("shared failure = %+v, want %v", got, tc.err)
					}
				}
				if tc.rests {
					_, cached, err := classify(ask, t.Name(), []string{"refactoring"}, before{}, false, "rename")
					if cached || err == nil || !strings.Contains(err.Error(), "not asked again") || calls.Load() != 1 {
						t.Fatalf("cooldown = %v, %v; calls = %d", cached, err, calls.Load())
					}
					time.Sleep(classifyRest + time.Second)
				}
				v, cached, err := classify(ask, t.Name(), []string{"refactoring"}, before{}, false, "rename")
				if v != want || cached || err != nil || calls.Load() != 2 {
					t.Fatalf("retry = %+v, %v, %v; calls = %d", v, cached, err, calls.Load())
				}
			})
		})
	}
}

func TestConcurrentClassificationPanicReleasesWaiters(t *testing.T) {
	resetClassified(t)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		panicked := make(chan any, 1)
		result := make(chan classifyResult, 1)
		var calls atomic.Int32
		ask := func(string, []string, before, bool, string) (verdict, error) {
			calls.Add(1)
			<-release
			panic("classifier panic")
		}
		go func() {
			defer func() { panicked <- recover() }()
			classify(ask, t.Name(), nil, before{}, false, "rename")
		}()
		synctest.Wait()
		go func() {
			v, cached, err := classify(ask, t.Name(), nil, before{}, false, "rename")
			result <- classifyResult{v, cached, err}
		}()
		synctest.Wait()
		close(release)
		synctest.Wait()
		if got := <-panicked; got != "classifier panic" {
			t.Fatalf("panic = %v", got)
		}
		if got := <-result; got.cached || got.err == nil || calls.Load() != 1 {
			t.Fatalf("panic waiter = %+v; calls = %d", got, calls.Load())
		}
		want := verdict{Intent: "refactoring"}
		retry := func(string, []string, before, bool, string) (verdict, error) { return want, nil }
		v, cached, err := classify(retry, t.Name(), nil, before{}, false, "rename")
		if v != want || cached || err != nil {
			t.Fatalf("retry after panic = %+v, %v, %v", v, cached, err)
		}
	})
}

// A classifier that fails in one scope (a user over its limit, a key held
// to accounts the classifier has none of) rests in that scope alone: the
// next ask in another scope, or in none, is asked, while the failed one's
// next waits out its rest.
func TestClassifierRestsInItsScopeAlone(t *testing.T) {
	resetClassified(t)
	var calls atomic.Int32
	refused := func(string, []string, before, bool, string) (verdict, error) {
		calls.Add(1)
		return verdict{}, errors.New("over its limit")
	}
	answers := func(string, []string, before, bool, string) (verdict, error) {
		calls.Add(1)
		return verdict{Intent: "refactoring"}, nil
	}
	if _, _, err := classifyAs("key\x00alice", refused, t.Name(), []string{"refactoring"}, before{}, false, "rename"); err == nil {
		t.Fatal("alice's refusal answered")
	}
	for _, scope := range []string{"key\x00bob", ""} {
		v, cached, err := classifyAs(scope, answers, t.Name(), []string{"refactoring"}, before{}, false, "rename")
		if err != nil || cached || v.Intent != "refactoring" {
			t.Fatalf("%q after alice's refusal: %+v %v %v", scope, v, cached, err)
		}
	}
	if _, _, err := classifyAs("key\x00alice", answers, t.Name(), []string{"refactoring"}, before{}, false, "rename again"); err == nil || !strings.Contains(err.Error(), "not asked again") {
		t.Fatalf("alice asked again in its rest: %v", err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("asked %d times, want alice's, bob's and the unscoped one", n)
	}
}

// A caller's own refusal (callerRefused: its user over its limit, its key
// with none of the classifier's accounts) is not the classifier's failure:
// the group's card says nothing of it (ClassifierFailure), while a failure
// at the classifier still shows there.
func TestCallersRefusalIsNotTheClassifiers(t *testing.T) {
	resetClassified(t)
	refused := func(string, []string, before, bool, string) (verdict, error) {
		return verdict{}, callerRefused{errors.New("over its limit")}
	}
	if _, _, err := classifyAs("key\x00alice", refused, t.Name(), nil, before{}, false, "rename"); err == nil {
		t.Fatal("alice's refusal answered")
	}
	if why, _, ok := ClassifierFailure(t.Name()); ok {
		t.Fatalf("the card says %q of alice's refusal", why)
	}
	down := func(string, []string, before, bool, string) (verdict, error) {
		return verdict{}, errors.New("classifier down")
	}
	if _, _, err := classifyAs("key\x00bob", down, t.Name(), nil, before{}, false, "rename"); err == nil {
		t.Fatal("bob's failure answered")
	}
	if why, _, ok := ClassifierFailure(t.Name()); !ok || why != "classifier down" {
		t.Fatalf("the card says %q %v, want the classifier down", why, ok)
	}
}
