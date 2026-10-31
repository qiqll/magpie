package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The usage summary counts every call by the caller's user it named
// (T-020): one group a user, the calls that named none (a record written
// before the field, a request without X-Magpie-User) one group of User
// "", so the users' shares add up to the totals. A call turned away is in
// none, as it is in no total.
func TestSummaryByUser(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	recs := []Record{
		{Time: now, Agent: "codex", Provider: "relay", Model: "m", User: "alice", Input: 100, Output: 10, Status: 200},
		{Time: now, Agent: "codex", Provider: "relay", Model: "m", User: "alice", Input: 50, Output: 5, Status: 429},
		{Time: now, Agent: "claude-code", Provider: "relay", Model: "m", User: "bob", Input: 20, Output: 2, Status: 200},
		// magpie's own call for bob's request: in bob's share, and his own
		{Time: now, Agent: "magpie-router/1", Provider: "relay", Model: "m", User: "bob", Input: 3, Output: 1, Status: 200},
		// named no user, or written before the field was
		{Time: now, Agent: "codex", Provider: "relay", Model: "m", Input: 7, Status: 200},
		{Time: now.Add(-time.Hour), Agent: "codex", Provider: "relay", Model: "m", Input: 9, Output: 1, Status: 200},
		// turned away: in no group
		{Time: now, Agent: "codex", Requested: "m", User: "carol", Status: 429, Rejected: true},
		// out of the period
		{Time: now.Add(-40 * 24 * time.Hour), Agent: "codex", Provider: "relay", Model: "m", User: "dave", Input: 9999, Status: 200},
	}
	s := summarize(Month, now, recs)
	got := map[string]Group{}
	calls, input, output := 0, 0, 0
	for _, g := range s.Users {
		got[g.ID] = g
		calls, input, output = calls+g.Calls, input+g.Input, output+g.Output
	}
	if len(s.Users) != 3 {
		t.Fatalf("users: %+v", s.Users)
	}
	if a := got["alice"]; a.User != "alice" || a.Calls != 2 || a.Errors != 1 || a.Input != 150 || a.Output != 15 {
		t.Fatalf("alice: %+v", a)
	}
	if b := got["bob"]; b.User != "bob" || b.Calls != 2 || b.Input != 23 || b.Own.Calls != 1 || b.Own.Tokens != 4 {
		t.Fatalf("bob, magpie's own call for him counted as his own: %+v", b)
	}
	if n, ok := got[""]; !ok || n.User != "" || n.Calls != 2 || n.Input != 16 {
		t.Fatalf("the calls that named no user, one group: %+v", n)
	}
	if _, ok := got["carol"]; ok {
		t.Fatalf("a call turned away has no user's share: %+v", s.Users)
	}
	if _, ok := got["dave"]; ok {
		t.Fatalf("a call out of the period has no user's share: %+v", s.Users)
	}
	if calls != s.Calls || input != s.Input || output != s.Output {
		t.Fatalf("users add up to %d calls, %d in, %d out; totals %+v", calls, input, output, s.Totals)
	}
	if s.Users[0].ID != "alice" {
		t.Fatalf("most tokens first: %+v", s.Users)
	}
}

// Summarize reads the user from the log as Append wrote it, a line from
// before the field among the no user's calls, and a period summed again
// after more calls (the kept prefix, #2278) still has every user's.
func TestSummaryByUserFromTheLog(t *testing.T) {
	pageHome(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	holdClock(t, now)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if heldLog.f != nil {
			heldLog.f.Close()
			heldLog.f = nil
		}
	})
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"t":"` + now.Add(-time.Hour).Format(time.RFC3339Nano) + `","agent":"codex","provider":"relay","model":"m","in":5,"out":1,"cache_read":0,"cache_write":0,"ms":10,"status":200}` + "\n"
	if err := os.WriteFile(Path(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	Append(Record{Time: now, Agent: "codex", Provider: "relay", Model: "m", User: "alice", Input: 2, Status: 200})
	users := func() map[string]int {
		out := map[string]int{}
		for _, g := range Summarize(Today).Users {
			out[g.User] = g.Input
		}
		return out
	}
	if got := users(); len(got) != 2 || got["alice"] != 2 || got[""] != 5 {
		t.Fatalf("users from the log: %v", got)
	}
	Append(Record{Time: now, Agent: "codex", Provider: "relay", Model: "m", User: "bob", Input: 3, Status: 200})
	Append(Record{Time: now, Agent: "codex", Provider: "relay", Model: "m", User: "alice", Input: 4, Status: 200})
	if got := users(); len(got) != 3 || got["alice"] != 6 || got["bob"] != 3 || got[""] != 5 {
		t.Fatalf("users after more calls: %v", got)
	}
}

// A call a client reported (#1874) went through no gateway of magpie's,
// so the user it says is the client's word, as its caller key is: it is
// written with none.
func TestReportedCallKeepsNoUser(t *testing.T) {
	pageHome(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	holdClock(t, now)
	took, err := IngestReported([]Record{{Time: now, Agent: "codex", Provider: "relay", Model: "m", RequestID: "rep-1", User: "mallory", CallerKeyID: "k", Input: 4, Status: 200}})
	if err != nil || len(took) != 1 || !took[0] {
		t.Fatalf("ingest: %v %v", took, err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"rid":"rep-1"`) || strings.Contains(s, "mallory") {
		t.Fatalf("the reported call's line: %s", s)
	}
}

// The days this computer shares with the others it syncs with (OwnDays)
// carry no caller's user: it stays here, out of the sync server.
func TestSharedDaysCarryNoUser(t *testing.T) {
	pageHome(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	holdClock(t, now)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if heldLog.f != nil {
			heldLog.f.Close()
			heldLog.f = nil
		}
	})
	Append(Record{Time: now, Agent: "codex", Provider: "relay", Model: "m", User: "alice@example.com", Input: 2, Status: 200})
	n := 0
	for _, calls := range OwnDays(now.Add(-time.Hour)) {
		for _, c := range calls {
			n++
			if c.User != "" {
				t.Fatalf("a shared call carries its user: %+v", c.Record)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d shared calls, want 1", n)
	}
}
