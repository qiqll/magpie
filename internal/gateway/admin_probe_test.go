package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
)

// An area that keeps adding keys magpie has can't learn which ones it
// has: past 20 409s in 10 minutes its add routes answer 429 with a
// Retry-After, whatever it sends, and another area is not held (T-186).
func TestAdminConflictsAreHeld(t *testing.T) {
	do, console, _ := adminWorld(t)
	_, study := makeArea(t, do, console, "学习", "plan")
	_, work := makeArea(t, do, console, "工作", "plan")
	if err := provider.AddKey("plan", "owner's", "sk-owners-probe-0001", ""); err != nil {
		t.Fatal(err)
	}
	add := func(secret, key string) (int, string) {
		b, _ := json.Marshal(map[string]string{"provider": "plan", "key": key})
		return do("POST", AdminPath+"/accounts/key", lanPeer, secret, string(b))
	}
	for i := 1; i <= conflictMost; i++ {
		if c, body := add(study, "sk-owners-probe-0001"); c != http.StatusConflict {
			t.Fatalf("409 number %d: %d %s", i, c, body)
		}
	}
	c, body := add(study, "sk-owners-probe-0001")
	if c != http.StatusTooManyRequests {
		t.Fatalf("the 21st: %d %s", c, body)
	}
	// a key magpie hasn't is held too: else 201 against 429 tells it apart
	if c, _ := add(study, "sk-not-in-magpie-0002"); c != http.StatusTooManyRequests {
		t.Fatal("a new key while held:", c)
	}
	if c, _ := do("POST", AdminPath+"/signin/claude-token", lanPeer, study, `{"token":"x"}`); c != http.StatusTooManyRequests {
		t.Fatal("claude-token while held:", c)
	}
	if c, body := add(work, "sk-owners-probe-0001"); c != http.StatusConflict {
		t.Fatal("another area is held:", c, body)
	}
	if c, body := add(work, "sk-work-own-0003"); c != http.StatusCreated {
		t.Fatal("another area can't add:", c, body)
	}
}

// The window lets go of a 409 ten minutes on, Retry-After says when,
// and the handler held is never run.
func TestConflictProbesWindow(t *testing.T) {
	var p conflictProbes
	start := time.Date(2026, 11, 17, 23, 55, 0, 0, time.UTC)
	for i := range conflictMost - 1 {
		p.conflicted("ar_a", start.Add(time.Duration(i)*time.Second))
	}
	if w := p.held("ar_a", start.Add(time.Minute)); w != 0 {
		t.Fatal("held at 19:", w)
	}
	p.conflicted("ar_a", start.Add(time.Minute))
	if w := p.held("ar_a", start.Add(2*time.Minute)); w != conflictWindow-2*time.Minute {
		t.Fatal("held for", w)
	}
	if w := p.held("ar_b", start); w != 0 {
		t.Fatal("another area held for", w)
	}
	if w := p.held("ar_a", start.Add(conflictWindow)); w != 0 {
		t.Fatal("still held when the first let go:", w)
	}

	var s Server
	ran := 0
	h := s.probeHeld(func(w http.ResponseWriter, r *http.Request, c adminCall) {
		ran++
		adminError(w, http.StatusConflict, adminConflict, "in magpie already")
	})
	c := adminCall{area: area.Area{ID: "ar_c"}}
	for range conflictMost {
		h(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), c)
	}
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("POST", "/", nil), c)
	if secs, _ := strconv.Atoi(w.Header().Get("Retry-After")); w.Code != http.StatusTooManyRequests || secs < 1 || secs > int(conflictWindow/time.Second) || ran != conflictMost {
		t.Fatalf("the 21st: %d, Retry-After %q, ran %d", w.Code, w.Header().Get("Retry-After"), ran)
	}
}
