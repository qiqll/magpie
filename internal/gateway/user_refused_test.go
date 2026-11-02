package gateway

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// refusedLog is magpie's log while a test runs, its lines on asking.
func refusedLog(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	was, flags := log.Writer(), log.Flags()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(was)
		log.SetFlags(flags)
	})
	refusedLogged.Lock()
	clear(refusedLogged.m)
	refusedLogged.Unlock()
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, l := range strings.Split(buf.String(), "\n") {
			if strings.Contains(l, "gateway: user ") && strings.Contains(l, " refused (") {
				out = append(out, l)
			}
		}
		return out
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// rejectedOf waits for the usage log's Rejected row of user, and returns
// it; none within the wait fails the test.
func rejectedOf(t *testing.T, user string) usage.Record {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, r := range usage.Load(time.Time{}) {
			if r.User == user && r.Rejected {
				return r
			}
		}
	}
	t.Fatalf("no Rejected row of %s in %+v", user, usage.Load(time.Time{}))
	return usage.Record{}
}

// A user's turn refused for every account being taken is a Rejected row
// in the usage log, with the user, the provider, the status and why, no
// tokens, and a line in magpie's log; its X-Magpie-User-Limit is
// "seats", where a user whose own account is out of its allowance for a
// while is told "quota". Before T-062 both were "quota", and neither the
// usage log nor magpie's log had the refusal.
func TestUserRefusedSeatsIsLoggedAndTold(t *testing.T) {
	codexSignedIn(t, "b@example.com")
	out, asked := limitUpstream(t)
	lines := refusedLog(t)
	s := byUser(New())
	for _, user := range []string{"alice", "bob"} {
		if rec := tierPost(s, user, "pro", "hi"); rec.Code != 200 {
			t.Fatalf("%s: %d %s", user, rec.Code, rec.Body.String())
		}
	}
	bobs := asked(true)[1]

	rec := tierPost(s, "carol", "pro", "hi")
	if limit, until := limitOf(t, rec); rec.Code != 429 || limit != "seats" || !until.IsZero() || !strings.Contains(rec.Body.String(), userLimitSaid) {
		t.Fatalf("carol, every account taken: %d %q until %v %s", rec.Code, limit, until, rec.Body.String())
	}
	row := rejectedOf(t, "carol")
	if row.Provider != "codex" || row.Status != 429 || !strings.Contains(row.Error, "no account of the provider is free") || row.Input+row.Output != 0 || row.Model != "codex/gpt-5.5" && row.Model != "gpt-5.5" {
		t.Fatalf("carol's row: %+v", row)
	}
	if got := lines(); len(got) != 1 || !strings.Contains(got[0], "user carol refused (seats, 429) by codex") || !strings.Contains(got[0], "no account of the provider is free") {
		t.Fatalf("log: %q", got)
	}

	// bob's own account out for 25 minutes: no spare is looked for, and
	// the limit is its account's own
	out(bobs, 1500)
	if rec := tierPost(s, "bob", "pro", "spent"); rec.Code != 429 {
		t.Fatalf("bob out: %d %s", rec.Code, rec.Body.String())
	}
	rec = tierPost(s, "bob", "pro", "again")
	if limit, until := limitOf(t, rec); rec.Code != 429 || limit != "quota" || !near(until, 25*time.Minute) {
		t.Fatalf("bob, its account out: %d %q until %v %s", rec.Code, limit, until, rec.Body.String())
	}
	if row := rejectedOf(t, "bob"); row.Status != 429 || row.Provider != "codex" || !strings.Contains(row.Error, "rests until") {
		t.Fatalf("bob's row: %+v", row)
	}
	if got := asked(true); len(got) != 1 {
		t.Fatalf("asked %v, want bob's spent request alone", got)
	}
}

// A user at its tier's most whose one account has to be signed in again
// is told "signin", by a turn and by a drawing alike, with a Rejected row
// each; nothing is asked. Before T-062 it was "quota", and the drawing
// had no row.
func TestUserLapsedIsToldSignin(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	drawn := codexDrawn(t)
	lapseSaved(t, "spare@example.com")
	refusedLog(t)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
		"bob": {Who: "codex@spare@example.com", Account: "spare@example.com", At: time.Now().UTC(), Tier: "free"},
	}}})
	s := byUserUpTo(t, New(), 1)
	rec := userAt(s, "/v1/images/generations", "", []byte(`{"model":"codex/gpt-image-2","prompt":"a magpie"}`), "bob")
	limitedNotLapsed(t, rec)
	if limit := rec.Header().Get(UserLimitHeader); limit != "signin" {
		t.Fatalf("drawing: %q", limit)
	}
	if got := drawn(); len(got) != 0 {
		t.Fatalf("drew on %v", got)
	}
	row := rejectedOf(t, "bob")
	if row.Provider != "codex" || row.Status != 429 || !strings.Contains(row.Error, "to be signed in again") || row.Input+row.Output != 0 {
		t.Fatalf("drawing's row: %+v", row)
	}
	if lapsedRefusal(&UserBound{User: "bob"}, "").limit != "signin" {
		t.Fatal("a turn's lapse isn't signin")
	}
}

// One line a user, provider and kind every refusedLogEvery: the others
// within it are counted into the next line, a kind or a user of its own
// is logged at once, and a line can't be split by what a user or an
// account is named.
func TestUserRefusedLogEvery(t *testing.T) {
	lines := refusedLog(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	was := refusedNow
	refusedNow = func() time.Time { return now }
	t.Cleanup(func() { refusedNow = was })
	seats := userRefusal{status: http.StatusTooManyRequests, limit: limitSeats, msg: "every account taken"}
	u := &UserBound{User: "alice", pid: "codex"}

	logUserRefused(u, "", seats)
	for range 3 {
		now = now.Add(time.Minute)
		logUserRefused(u, "", seats)
	}
	if got := lines(); len(got) != 1 {
		t.Fatalf("within the window: %q", got)
	}
	logUserRefused(u, "", userRefusal{status: http.StatusTooManyRequests, limit: limitSignin, msg: "to be signed in again"})
	logUserRefused(&UserBound{User: "bob", pid: "codex"}, "", seats)
	logUserRefused(&UserBound{User: "alice", pid: "claude"}, "", seats)
	if got := lines(); len(got) != 4 {
		t.Fatalf("another kind, user and provider each at once: %q", got)
	}
	now = now.Add(2 * time.Minute) // 5 minutes after the first
	logUserRefused(u, "", seats)
	got := lines()
	if len(got) != 5 || !strings.Contains(got[4], "user alice refused (seats, 429) by codex") || !strings.Contains(got[4], "and 3 more like it since 2026-11-02T12:00:00Z") {
		t.Fatalf("the window over: %q", got)
	}
	now = now.Add(time.Second)
	logUserRefused(u, "", seats)
	if got := lines(); len(got) != 5 {
		t.Fatalf("a new window: %q", got)
	}

	logUserRefused(&UserBound{User: "eve\ngateway: forged", Account: "a b\nc", pid: "codex"}, "", userRefusal{status: 400, msg: "bad\nline"})
	got = lines()
	if last := got[len(got)-1]; !strings.Contains(last, `user "eve\ngateway: forged" refused (request, 400)`) || !strings.Contains(last, `bound to "a b\nc"`) || !strings.Contains(last, `"bad\nline"`) {
		t.Fatalf("names quoted: %q", got)
	}
}
