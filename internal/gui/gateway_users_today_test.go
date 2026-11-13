package gui

import (
	"fmt"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

// forgetUsersToday drops the day worked out by an earlier test, whose held
// clock may be this one's.
func forgetUsersToday(t *testing.T) {
	t.Helper()
	drop := func() {
		usersTodayMemo.Lock()
		usersTodayMemo.by = nil
		usersTodayMemo.Unlock()
	}
	drop()
	t.Cleanup(drop)
}

// A bound user's row says what it sent its provider today, under its own
// row's keys only: yesterday's calls, another provider's, another key
// group's user of the same name, calls naming no user and rejected ones
// are none of it; a try handed on is no request but its tokens count; and
// a user that sent nothing today says nothing (需求方 2026-11-13).
func TestGatewayUsersToday(t *testing.T) {
	sandboxHome(t)
	noGateway(t)
	forgetUsersToday(t)
	now := holdUsageClock(t, time.Date(2026, 11, 13, 15, 0, 0, 0, time.Local))
	day := usage.Today.Since(now)
	if _, err := access.Update("add-key", access.Change{Name: "Tenant A"}); err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	var keyID string
	for _, k := range keys {
		if k.Name == "Tenant A" {
			keyID = k.ID
		}
	}
	at := day.Add(time.Hour).UTC().Format(time.RFC3339)
	writeUsers(t, fmt.Sprintf(`{
  "users": {"codex": {"alice": {"who": "codex@a@x.com", "account": "a@x.com", "at": %q},
                      "idle": {"who": "codex@a@x.com", "account": "a@x.com", "at": %q}}},
  "keys": {%q: {"codex": {"alice": {"who": "codex@b@x.com", "account": "b@x.com", "at": %q}}}}
}`, at, at, keyID, at))
	rec := func(at time.Time, prov, user, key string, in, out int, f func(*usage.Record)) {
		r := usage.Record{Time: at, Agent: "codex", Provider: prov, Model: "gpt-5", Input: in, Output: out, Status: 200, User: user, CallerKeyID: key}
		if f != nil {
			f(&r)
		}
		usage.Append(r)
	}
	rec(day.Add(-time.Minute), "codex", "alice", "", 1000, 1000, nil) // yesterday
	rec(day.Add(2*time.Hour), "codex", "alice", "", 100, 20, nil)
	rec(day.Add(3*time.Hour), "codex", "alice", "", 50, 0, func(r *usage.Record) { r.HandedOn = true; r.Status = 429 })
	rec(day.Add(3*time.Hour), "codex", "alice", "", 7, 3, nil)
	rec(day.Add(4*time.Hour), "claude", "alice", "", 5000, 5000, nil)                                                   // another provider
	rec(day.Add(4*time.Hour), "codex", "alice", keyID, 9000, 9000, nil)                                                 // Tenant A's alice
	rec(day.Add(4*time.Hour), "codex", "", "", 4000, 4000, nil)                                                         // no user
	rec(day.Add(4*time.Hour), "", "alice", "", 3000, 3000, func(r *usage.Record) { r.Rejected = true; r.Status = 429 }) // rejected

	code, out := gatewayUsers(t, "")
	if code != 200 || len(out.Users) != 3 {
		t.Fatalf("%d %+v", code, out)
	}
	type said struct{ calls, tokens int }
	got := map[string]*said{}
	for _, u := range out.Users {
		k := u.User + "/" + u.Key
		if u.Today == nil {
			got[k] = nil
			continue
		}
		got[k] = &said{u.Today.Calls, u.Today.Tokens}
	}
	if s := got["alice/"]; s == nil || *s != (said{2, 180}) {
		t.Fatalf("alice without a key: %+v", s)
	}
	if s := got["alice/"+keyID]; s == nil || *s != (said{1, 18000}) {
		t.Fatalf("Tenant A's alice: %+v", s)
	}
	if s, ok := got["idle/"]; !ok || s != nil {
		t.Fatalf("idle: %v %+v", ok, s)
	}
	// a search's users say it too
	if _, out := gatewayUsers(t, "?q=tenant"); len(out.Users) != 1 || out.Users[0].Today == nil || out.Users[0].Today.Calls != 1 {
		t.Fatalf("searched: %+v", out.Users)
	}
}
