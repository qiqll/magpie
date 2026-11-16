package gateway

import (
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// An area's add routes answer 409 for an account magpie has already, so
// an area admin could test whether some key or account is in magpie by
// adding it. conflictProbes counts each area's 409s on those routes
// (accounts/key, signin/claude-token): past conflictMost in conflictWindow
// they answer that area 429 whatever it sends, until the window lets go of
// its oldest. Other areas are not held.
const (
	conflictMost          = 20
	conflictWindow        = 10 * time.Minute
	adminTooManyConflicts = "too_many_conflicts"
)

type conflictProbes struct {
	mu   sync.Mutex
	seen map[string][]time.Time // an area's 409s in the window, oldest first
}

// recent are id's 409s still in the window at now, the older let go of.
// Called with p.mu held.
func (p *conflictProbes) recent(id string, now time.Time) []time.Time {
	ts := p.seen[id]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= conflictWindow {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(p.seen, id)
		return nil
	}
	p.seen[id] = ts
	return ts
}

// held is how long id is still held at now, 0 for not.
func (p *conflictProbes) held(id string, now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	ts := p.recent(id, now)
	if len(ts) < conflictMost {
		return 0
	}
	return ts[len(ts)-conflictMost].Add(conflictWindow).Sub(now)
}

// conflicted counts a 409 answered id at now, and is how many it has had
// in the window.
func (p *conflictProbes) conflicted(id string, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string][]time.Time{}
	}
	ts := append(p.recent(id, now), now)
	p.seen[id] = ts
	return len(ts)
}

// statusOf keeps the status a handler answers.
type statusOf struct {
	http.ResponseWriter
	code int
}

func (w *statusOf) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// probeHeld runs an area's add route h, counting the 409s it answers, and
// answers 429 in its place while the area is held.
func (s *Server) probeHeld(h func(http.ResponseWriter, *http.Request, adminCall)) func(http.ResponseWriter, *http.Request, adminCall) {
	return func(w http.ResponseWriter, r *http.Request, c adminCall) {
		now := time.Now()
		if wait := s.conflicts.held(c.area.ID, now); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(max(int((wait+time.Second-1)/time.Second), 1)))
			adminError(w, http.StatusTooManyRequests, adminTooManyConflicts, "this area was told an account is in magpie already too often: try again later")
			return
		}
		sw := &statusOf{ResponseWriter: w}
		h(sw, r, c)
		if sw.code == http.StatusConflict {
			if n := s.conflicts.conflicted(c.area.ID, now); n == conflictMost {
				log.Printf("admin: area %s had %d conflicts in %s: its add routes answer 429 for now", c.area.ID, n, conflictWindow)
			}
		}
	}
}
