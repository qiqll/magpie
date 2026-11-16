package gateway

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// slowFirst streams the role chunk at once, then for its first slow
// requests its text only after wait (or never, the request gone first),
// and for every one after its text at once.
type slowFirst struct {
	mu   sync.Mutex
	slow int
	wait time.Duration
	n    int
}

func (s *slowFirst) asked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *slowFirst) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.mu.Lock()
	s.n++
	slow := s.n <= s.slow
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	io.WriteString(w, `data: {"id":"s","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n")
	w.(http.Flusher).Flush()
	if slow {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(s.wait):
		}
	}
	io.WriteString(w, `data: {"id":"s","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`+"\n\n"+"data: [DONE]\n\n")
}

func slowAgainOn(t *testing.T, firstToken int, on bool) {
	t.Helper()
	if err := settings.SetGroupDefaults(false, firstToken, 0, on); err != nil {
		t.Fatal(err)
	}
}

const streamHi = `"stream":true,"messages":[{"role":"user","content":"hi"}]}`

// A provider an agent names alone, slow to start past the default
// first-token time, is asked once more of the same account with SlowAgain
// on, before any of the reply reaches the agent (qmcloud: 48s with no
// token, then the agent gave up). Off, it is waited for, as before.
func TestSlowAgainAsksAProviderNamedAloneOnce(t *testing.T) {
	fresh(t)
	slowAgainOn(t, 1, true)
	a := &slowFirst{slow: 1, wait: 10 * time.Second}
	slowOn(t, "a", a)
	s := New()
	began := time.Now()
	code, body := postAs(t, s, "", `{"model":"a/m",`+streamHi)
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	if code != 200 || !strings.Contains(body, "answer") || a.asked() != 2 {
		t.Fatalf("%d %s (asked %d)", code, body, a.asked())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Fail != failSlow || !r.Tries[0].Same || r.Tries[0].Rest != nil || r.Tries[1].Status != 200 || r.Tries[1].Same {
		t.Fatalf("tries: %+v", r.Tries)
	}

	// off: the one asked is waited for, as before
	fresh(t)
	slowAgainOn(t, 1, false)
	a = &slowFirst{slow: 1, wait: 2 * time.Second}
	slowOn(t, "a", a)
	s = New()
	if code, body := postAs(t, s, "", `{"model":"a/m",`+streamHi); code != 200 || !strings.Contains(body, "answer") || a.asked() != 1 {
		t.Fatalf("off: %d %s (asked %d)", code, body, a.asked())
	}

	// on, but the default waits (no first-token time): nothing to let go
	fresh(t)
	slowAgainOn(t, 0, true)
	a = &slowFirst{slow: 1, wait: 2 * time.Second}
	slowOn(t, "a", a)
	s = New()
	if code, body := postAs(t, s, "", `{"model":"a/m",`+streamHi); code != 200 || !strings.Contains(body, "answer") || a.asked() != 1 {
		t.Fatalf("waits: %d %s (asked %d)", code, body, a.asked())
	}
}

// Asked once more and slow again, the last one left is waited for to the
// end: it is asked again once a request, never let go with an error.
func TestSlowAgainWaitsOutTheSecondTry(t *testing.T) {
	fresh(t)
	slowAgainOn(t, 1, true)
	a := &slowFirst{slow: 2, wait: 2 * time.Second}
	slowOn(t, "a", a)
	s := New()
	code, body := postAs(t, s, "", `{"model":"a/m",`+streamHi)
	if code != 200 || !strings.Contains(body, "answer") || a.asked() != 2 {
		t.Fatalf("%d %s (asked %d)", code, body, a.asked())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || !r.Tries[0].Same || r.Tries[1].Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// In a group, a member slow to start is asked once more of the same
// account before the next member; slow again, the next is asked. One
// request counts one slow start towards a rest, as before: the model
// rests on its account only when a second request is slow too.
func TestSlowAgainInAGroup(t *testing.T) {
	fresh(t)
	slowAgainOn(t, 1, true)
	a := &slowFirst{slow: 1, wait: 10 * time.Second}
	b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g",`+streamHi)
	if code != 200 || !strings.Contains(body, "answer") || a.asked() != 2 || b.n != 0 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.asked(), b.n)
	}

	// slow both times: the next member answers, and a rests
	fresh(t)
	slowAgainOn(t, 1, true)
	a = &slowFirst{slow: 2, wait: 10 * time.Second}
	b = &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered})
	s = New()
	began := time.Now()
	code, body = postAs(t, s, "", `{"model":"group/g",`+streamHi)
	if took := time.Since(began); took > 8*time.Second {
		t.Fatalf("took %s", took)
	}
	if code != 200 || !strings.Contains(body, "from b") || a.asked() != 2 || b.n != 1 {
		t.Fatalf("again slow: %d %s (a %d, b %d)", code, body, a.asked(), b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 3 || !r.Tries[0].Same || r.Tries[0].Rest != nil || r.Tries[1].Same || r.Tries[1].Fail != failSlow || r.Tries[1].Rest != nil {
		t.Fatalf("tries: %+v", r.Tries)
	}

	// a second request slow on it again: now its model rests
	a.mu.Lock()
	a.slow = 4
	a.mu.Unlock()
	b.replies = append(b.replies, reply{200, "text/event-stream", chatStreamOK})
	code, body = postAs(t, s, "", `{"model":"group/g",`+streamHi)
	if code != 200 || !strings.Contains(body, "from b") || a.asked() != 4 {
		t.Fatalf("second: %d %s (a %d)", code, body, a.asked())
	}
	r = s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 3 || r.Tries[0].Rest != nil || r.Tries[1].Rest == nil {
		t.Fatalf("second tries: %+v", r.Tries)
	}
}
