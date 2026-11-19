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
// and the area's (ids[0]) runs no route unless holding, or how long
// until they may and whether the console's total is the longer wait.
// Called with p.mu held.
func (p *pluginCalls) left(ids []string, now time.Time, holding bool) (wait time.Duration, console, ok bool) {
	ok = true
	for i, id := range ids {
		ts, most := p.recent(id, now), pluginCallsLimit(id)
		w := time.Duration(0)
		switch {
		case len(ts) >= most:
			w = ts[len(ts)-most].Add(conflictWindow).Sub(now)
		case i == 0 && !holding && p.running[id]:
			w = time.Second
		default:
			continue
		}
		ok = false
		if w > wait {
			wait, console = w, id == consoleBudget
		}
	}
	return wait, console, ok
}

// hold has ids' area run a plugin sign-in route from now until end: none
// other of the area's runs meanwhile. False, with how long until it may,
// while one runs or a count has no call left.
func (p *pluginCalls) hold(ids []string, now time.Time) (wait time.Duration, console, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if wait, console, ok = p.left(ids, now, false); !ok {
		return wait, console, false
	}
	p.running[ids[0]] = true
	return 0, false, true
}

// spend takes one host call of each of ids at now, for the route their
// area holds; none, and how long until one is left, when one has none.
func (p *pluginCalls) spend(ids []string, now time.Time) (wait time.Duration, console, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if wait, console, ok = p.left(ids, now, true); !ok {
		return wait, console, false
	}
	for _, id := range ids {
		p.asked[id] = append(p.asked[id], now)
	}
	return 0, false, true
}

func (p *pluginCalls) end(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, id)
}

// pluginCallsOut is the error of a host call not made, c's calls spent:
// try again after wait, the console's total when console.
type pluginCallsOut struct {
	wait    time.Duration
	console bool
}

func (e pluginCallsOut) Error() string { return "too many plugin calls" }

// pluginCallsHeld answers 429 too_many_plugin_calls for e.
func pluginCallsHeld(w http.ResponseWriter, e pluginCallsOut) {
	w.Header().Set("Retry-After", strconv.Itoa(max(int((e.wait+time.Second-1)/time.Second), 1)))
	if e.console {
		adminError(w, http.StatusTooManyRequests, adminTooManyPluginCalls, "the console asks plugins 400 times in 10 minutes across areas: try again later")
		return
	}
	adminError(w, http.StatusTooManyRequests, adminTooManyPluginCalls, "this area asks plugins 200 times in 10 minutes, one sign-in step at a time: try again later")
}

// pluginAsk spends one of a held route's host calls, or is the
// pluginCallsOut that says why not.
type pluginAsk func() error

// holdPluginCalls holds c's area's plugin calls for the route it runs,
// with the ask that spends each and the end that lets go of them, or
// false with 429 answered.
func (s *Server) holdPluginCalls(w http.ResponseWriter, c adminCall) (ask pluginAsk, end func(), ok bool) {
	ids := budgets(c)
	if wait, console, ok := s.pluginCalls.hold(ids, time.Now()); !ok {
		pluginCallsHeld(w, pluginCallsOut{wait, console})
		return nil, nil, false
	}
	ask = func() error {
		if wait, console, ok := s.pluginCalls.spend(ids, time.Now()); !ok {
			return pluginCallsOut{wait, console}
		}
		return nil
	}
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

// nextPrompt is plugin.NextPrompt, one host call spent first.
func nextPrompt(ctx context.Context, ask pluginAsk, id string, method int, inputs map[string]string) (*plugin.Prompt, error) {
	if err := ask(); err != nil {
		return nil, err
	}
	return plugin.NextPrompt(ctx, id, method, inputs)
}
