package gateway

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// An area's plugin sign-in routes ask the plugin host for each step:
// signin/plugin/prompt up to three times a call (its next question, the
// answer's check, the question after), signin/plugin up to 34 (its
// answers walked, then the start). That host serves every plugin
// subscription's requests too, so an area could keep it busy with calls
// that cost it nothing (T-231). pluginCalls holds each area's to one
// such route at a time and pluginCallsMost host calls in conflictWindow,
// and the console's to consolePluginCallsMost across every area it acts
// for besides (budgets): past either the route answers 429
// too_many_plugin_calls with Retry-After and asks the host nothing more.
// A sign-in of 16 questions, each answered once at the prompt route and
// then started, asks the host 83 times; one of three questions started
// again and again asks 7 times a start, so the conflict line's 20 come
// first. In memory: a restart clears it.
const (
	pluginCallsMost         = 200
	consolePluginCallsMost  = 400
	adminTooManyPluginCalls = "too_many_plugin_calls"
)

// pluginCallsLimit is how many host calls id may make in the window.
func pluginCallsLimit(id string) int {
	if id == consoleBudget {
		return consolePluginCallsMost
	}
	return pluginCallsMost
}

type pluginCalls struct {
	mu      sync.Mutex
	asked   map[string][]time.Time
	running map[string]bool
}

// recent are id's host calls still in the window at now, the older let
// go of. Called with p.mu held.
func (p *pluginCalls) recent(id string, now time.Time) []time.Time {
	if p.asked == nil {
		p.asked, p.running = map[string][]time.Time{}, map[string]bool{}
	}
	ts := p.asked[id]
	for len(ts) > 0 && now.Sub(ts[0]) >= conflictWindow {
		ts = ts[1:]
	}
	if len(ts) == 0 {
		delete(p.asked, id)
		return nil
	}
	p.asked[id] = ts
	return ts
}

// left says whether each of ids has a call left in the window at now,
// and the area's (ids[0]) runs no route unless holding, or why not: how
// long until they may, whether the console's total is the longer wait,
// and whether the area's other route running is all that holds it.
// Called with p.mu held.
func (p *pluginCalls) left(ids []string, now time.Time, holding bool) pluginCallsOut {
	var out pluginCallsOut
	for _, id := range ids {
		ts, most := p.recent(id, now), pluginCallsLimit(id)
		if len(ts) >= most {
			if w := ts[len(ts)-most].Add(conflictWindow).Sub(now); w > out.wait {
				out.wait, out.console = w, id == consoleBudget
			}
		}
	}
	if out.wait == 0 && !holding && p.running[ids[0]] {
		out.wait, out.busy = time.Second, true
	}
	return out
}

// hold has ids' area run a plugin sign-in route from now until end: none
// other of the area's runs meanwhile. False, with how long until it may,
// while one runs or a count has no call left.
func (p *pluginCalls) hold(ids []string, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if out := p.left(ids, now, false); out.wait > 0 {
		return out
	}
	p.running[ids[0]] = true
	return nil
}

// spend takes one host call of each of ids at now, for the route their
// area holds; none, and why not, when one has none left.
func (p *pluginCalls) spend(ids []string, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if out := p.left(ids, now, true); out.wait > 0 {
		return out
	}
	for _, id := range ids {
		p.asked[id] = append(p.asked[id], now)
	}
	return nil
}

func (p *pluginCalls) end(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, id)
}

// pluginCallsOut is the error of a host call not made: try again after
// wait. The console's total is spent when console; the area's other
// route still runs when busy; else the area's calls are spent.
type pluginCallsOut struct {
	wait          time.Duration
	console, busy bool
}

func (e pluginCallsOut) Error() string { return "too many plugin calls" }

// pluginCallsHeld answers 429 too_many_plugin_calls for e.
func pluginCallsHeld(w http.ResponseWriter, e pluginCallsOut) {
	w.Header().Set("Retry-After", strconv.Itoa(max(int((e.wait+time.Second-1)/time.Second), 1)))
	switch {
	case e.console:
		adminError(w, http.StatusTooManyRequests, adminTooManyPluginCalls, "the console asks plugins 400 times in 10 minutes across areas: try again later")
	case e.busy:
		adminError(w, http.StatusTooManyRequests, adminTooManyPluginCalls, "this area's other plugin sign-in step is still running: one at a time")
	default:
		adminError(w, http.StatusTooManyRequests, adminTooManyPluginCalls, "this area asks plugins 200 times in 10 minutes: try again later")
	}
}

// pluginAsk spends one of a held route's host calls, or is the
// pluginCallsOut that says why not.
type pluginAsk func() error

// holdPluginCalls holds c's area's plugin calls for the route it runs,
// with the ask that spends each and the end that lets go of them, or
// false with 429 answered.
func (s *Server) holdPluginCalls(w http.ResponseWriter, c adminCall) (ask pluginAsk, end func(), ok bool) {
	ids := budgets(c)
	if err := s.pluginCalls.hold(ids, time.Now()); err != nil {
		callsAnswered(w, err)
		return nil, nil, false
	}
	ask = func() error { return s.pluginCalls.spend(ids, time.Now()) }
	return ask, func() { s.pluginCalls.end(c.area.ID) }, true
}

// callsAnswered answers 429 when err is a host call not made, c's calls
// spent, and says whether it did.
func callsAnswered(w http.ResponseWriter, err error) bool {
	var out pluginCallsOut
	if !errors.As(err, &out) {
		return false
	}
	pluginCallsHeld(w, out)
	return true
}

// hostNextPrompt and hostValidate are the plugin host's, vars so a test
// counts what is asked of it.
var (
	hostNextPrompt = plugin.NextPrompt
	hostValidate   = plugin.Validate
)

// nextPrompt is the host's next question, one host call spent first.
func nextPrompt(ctx context.Context, ask pluginAsk, id string, method int, inputs map[string]string) (*plugin.Prompt, error) {
	if err := ask(); err != nil {
		return nil, err
	}
	return hostNextPrompt(ctx, id, method, inputs)
}
