package gateway

import (
	"log"
	"net/http"
	"sync"
	"time"
)

// An area's add routes answer 409 for an account magpie has already, so
// an area admin could test whether some key or account is in magpie by
// adding it. conflictProbes counts each area's 409s on those routes
// (accounts/key, signin/claude-token, and each key accounts/keys is
// told is there: adminAddKeys): past conflictMost in conflictWindow
// they answer that area 429 whatever it sends, until the window lets go of
// its oldest. Other areas are not held.
//
// The console acting for an area (forArea) spends that area's count, and
// a total of its own across every area too (T-222, magpie-console's
// docs/magpie-api.md §18): past consoleConflictMost in the window its
// add routes answer 429 for any area, so making more areas lends it no
// more tries. An area's own key spends its area's count alone.
const (
	conflictMost          = 20
	consoleConflictMost   = 40
	conflictWindow        = 10 * time.Minute
	adminTooManyConflicts = "too_many_conflicts"
)

// consoleBudget is the id the console's total is counted under, in
// conflictProbes and proxyChecks: never an area's (area ids are ar_…).
const consoleBudget = "\x00console"

// budgets are the counts a call spends: its area's, then the console's
// own when the console acts for the area.
func budgets(c adminCall) []string {
	if c.console {
		return []string{c.area.ID, consoleBudget}
	}
	return []string{c.area.ID}
}

// conflictLimit is how many conflicts id may have in the window.
func conflictLimit(id string) int {
	if id == consoleBudget {
		return consoleConflictMost
	}
	return conflictMost
}

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
	ts, most := p.recent(id, now), conflictLimit(id)
	if len(ts) < most {
		return 0
	}
	return ts[len(ts)-most].Add(conflictWindow).Sub(now)
}

// count is how many 409s id has had in the window at now.
func (p *conflictProbes) count(id string, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.recent(id, now))
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
		if s.heldAnswered(w, c, now) {
			return
		}
		sw := &statusOf{ResponseWriter: w}
		h(sw, r, c)
		if sw.code == http.StatusConflict {
			s.conflicted(c, now)
		}
	}
}

// conflictHeld is how long c is still held at now, 0 for not, and
// whether the console's total is what holds it.
func (s *Server) conflictHeld(c adminCall, now time.Time) (wait time.Duration, console bool) {
	for _, id := range budgets(c) {
		if w := s.conflicts.held(id, now); w > wait {
			wait, console = w, id == consoleBudget
		}
	}
	return wait, console
}

// conflicted counts a conflict c was told of at now, to each count c
// spends, and says whether c is held from now on. Reaching a line is
// logged with the count, never the key.
func (s *Server) conflicted(c adminCall, now time.Time) bool {
	held := false
	for _, id := range budgets(c) {
		n, most := s.conflicts.conflicted(id, now), conflictLimit(id)
		switch {
		case n == most && id == consoleBudget:
			log.Printf("admin: the console had %d conflicts across areas in %s: its add routes answer 429 for now", n, conflictWindow)
		case n == most:
			log.Printf("admin: area %s had %d conflicts in %s: its add routes answer 429 for now", id, n, conflictWindow)
		}
		held = held || n >= most
	}
	return held
}
