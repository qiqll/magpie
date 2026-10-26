package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// What rests in the process serving the gateway is told to a console on
// this computer that doesn't serve it (the Gateway page's User bindings):
// each rest by what it rests by, with why and until when, one ended not
// among them; a magpie that doesn't tell them is unknown, never nothing
// resting.
func TestOwnerRestsTold(t *testing.T) {
	fresh(t)
	until := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	restingUntil.Lock()
	restingUntil.m["relay#abc"], restingUntil.note["relay#abc"] = until, Rest{Why: failQuota, Status: 429}
	restingUntil.m["codex@a@x.com/gpt-5"], restingUntil.note["codex@a@x.com/gpt-5"] = until, Rest{Why: failRate, Model: "gpt-5"}
	restingUntil.m["ended"], restingUntil.note["ended"] = time.Now().Add(-time.Minute), Rest{Why: failQuota}
	restingUntil.Unlock()

	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))
	got, ok := OwnerRests()
	if !ok {
		t.Fatal("the serving magpie's rests weren't read")
	}
	if r := got["relay#abc"]; r.Why != failQuota || !r.Until.Equal(until) || r.Status != 429 {
		t.Errorf("relay#abc: %+v", r)
	}
	if r := got["codex@a@x.com/gpt-5"]; r.Why != failRate || r.Model != "gpt-5" || !r.Until.Equal(until) {
		t.Errorf("a model's rest: %+v", r)
	}
	if _, ok := got["ended"]; ok || len(got) != 2 {
		t.Errorf("an ended rest told, or more: %+v", got)
	}

	// another machine is told nothing, even where Handler is served
	// without lanGuard
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", RestsPath, nil)
	r.RemoteAddr = "192.168.1.9:5000"
	New().Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "relay#abc") {
		t.Errorf("another machine: %d %s", w.Code, w.Body)
	}

	// an older magpie, or one that refuses: unknown
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer old.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(old.URL, "http://"))
	if got, ok := OwnerRests(); ok || got != nil {
		t.Errorf("a magpie that doesn't tell its rests: %v %v", got, ok)
	}
}
