package gui

import (
	"fmt"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// forgetUsersToday drops the day worked out by an earlier test, whose held
// clock may be this one's.
func forgetUsersToday(t *testing.T) {
	t.Helper()
	drop := func() {
		usersTodayMemo.Lock()
		usersTodayMemo.days = nil
		usersTodayMemo.Unlock()
	}
	drop()
	t.Cleanup(drop)
}

// A bound user's row says what it sent its provider today, under its own
// row's keys only: yesterday's calls, another provider's, another key
// group's user of the same name, calls naming no user, rejected ones and ones an app reported itself
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
                      "idle": {"who": "codex@a@x.com", "account": "a@x.com", "at": %q},
                      "far": {"who": "codex@a@x.com", "account": "a@x.com", "at": %q}}},
  "keys": {%q: {"codex": {"alice": {"who": "codex@b@x.com", "account": "b@x.com", "at": %q}}}}
}`, at, at, at, keyID, at))
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
	rec(day.Add(4*time.Hour), "claude", "alice", "", 5000, 5000, nil)                                                      // another provider
	rec(day.Add(4*time.Hour), "codex", "alice", keyID, 9000, 9000, nil)                                                    // Tenant A's alice
	rec(day.Add(4*time.Hour), "codex", "", "", 4000, 4000, nil)                                                            // no user
	rec(day.Add(4*time.Hour), "", "alice", "", 3000, 3000, func(r *usage.Record) { r.Rejected = true; r.Status = 429 })    // rejected
	rec(day.Add(4*time.Hour), "codex", "alice", "", 2000, 2000, func(r *usage.Record) { r.Source = usage.SourceReported }) // reported by an app
	// far's day is Kiritimati's (UTC+14): a minute before it began is
	// yesterday there, a minute after it today
	far, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Skip(err)
	}
	farDay := usage.Today.Since(now.In(far))
	rec(farDay.Add(-time.Minute), "codex", "far", "", 600, 0, nil)
	rec(farDay.Add(time.Minute), "codex", "far", "", 40, 2, nil)

	code, out := gatewayUsers(t, "")
	if code != 200 || len(out.Users) != 4 {
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
	// in the reader's zone
	if _, out := gatewayUsers(t, "?q=far&tz=Pacific%2FKiritimati"); len(out.Users) != 1 || out.Users[0].Today == nil || out.Users[0].Today.Calls != 1 || out.Users[0].Today.Tokens != 42 {
		t.Fatalf("far in Kiritimati: %+v", out.Users)
	}
	// a search's users say it too
	if _, out := gatewayUsers(t, "?q=tenant"); len(out.Users) != 1 || out.Users[0].Today == nil || out.Users[0].Today.Calls != 1 {
		t.Fatalf("searched: %+v", out.Users)
	}
}

// An account's row names the users bound to it, the earliest bound first,
// each with when it was bound to that account (a spare's own time, the
// earliest under any of its keys) and marked a spare where it is one; and
// says what went through the account today, whoever asked: a call naming
// no user too, never another account's, a rejected one's or yesterday's;
// an account with nothing today says nothing (需求方 2026-11-13).
func TestGatewayAccountsBoundAndToday(t *testing.T) {
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
	t1, t2, t3 := day.Add(-48*time.Hour).UTC(), day.Add(-24*time.Hour).UTC(), day.Add(time.Hour).UTC()
	f := func(x time.Time) string { return x.Format(time.RFC3339) }
	// alice: A first under no key since t2, and under Tenant A since t1, so
	// t1; bob: A a spare since t3 (B its first); carol: B first, no time
	writeUsers(t, fmt.Sprintf(`{
  "users": {"codex": {"alice": {"who": "codex@a@x.com", "account": "A@x.com", "at": %q},
                      "bob": {"who": "codex@b@x.com", "account": "b@x.com", "at": %q, "more": [{"who": "codex@a@x.com", "account": "A@x.com", "at": %q}]},
                      "carol": {"who": "codex@b@x.com", "account": "b@x.com"}},
            "relay": {"dan": {"who": %q, "account": "First"}},
            "local": {"eve": {"who": "local", "account": "local"}}},
  "keys": {%q: {"codex": {"alice": {"who": "codex@a@x.com", "account": "A@x.com", "at": %q}}}}
}`, f(t2), f(t1), f(t3), "relay#"+provider.KeyID("sk-1"), keyID, f(t1)))
	rec := func(at time.Time, acct, user string, in, out int, fn func(*usage.Record)) {
		r := usage.Record{Time: at, Agent: "codex", Provider: "codex", ProviderAccount: acct, Model: "gpt-5", Input: in, Output: out, Status: 200, User: user}
		if fn != nil {
			fn(&r)
		}
		usage.Append(r)
	}
	rec(day.Add(-time.Minute), "A@x.com", "alice", 1000, 1000, nil) // yesterday
	rec(day.Add(2*time.Hour), "A@x.com", "alice", 100, 20, nil)
	rec(day.Add(2*time.Hour), "a@X.com", "", 30, 0, nil)                                                               // no user, the account's name in another case
	rec(day.Add(3*time.Hour), "A@x.com", "bob", 5, 5, func(r *usage.Record) { r.HandedOn = true; r.Status = 429 })     // handed on: tokens, no request
	rec(day.Add(3*time.Hour), "A@x.com", "bob", 900, 900, func(r *usage.Record) { r.Rejected = true; r.Status = 429 }) // rejected
	rec(day.Add(3*time.Hour), "c@x.com", "", 700, 700, nil)                                                            // another account
	rec(day.Add(3*time.Hour), "A@x.com", "", 800, 800, func(r *usage.Record) { r.Source = usage.SourceReported })
	// a key's seat, and a provider's with neither account nor key
	rec(day.Add(3*time.Hour), "", "dan", 11, 1, func(r *usage.Record) { r.Provider, r.ProviderKeyID = "relay", provider.KeyID("sk-1") })
	rec(day.Add(3*time.Hour), "", "eve", 3, 2, func(r *usage.Record) { r.Provider = "local" }) // reported by an app

	code, out := gatewayUsers(t, "")
	if code != 200 || len(out.Accounts) != 4 {
		t.Fatalf("%d %+v", code, out)
	}
	by := map[string]userAccountJSON{}
	for _, x := range out.Accounts {
		by[x.Seat] = x
	}
	a, b := by["codex@a@x.com"], by["codex@b@x.com"]
	if a.Seat == "" || b.Seat == "" {
		t.Fatalf("accounts: %+v", out.Accounts)
	}
	for seat, want := range map[string]int{"relay#" + provider.KeyID("sk-1"): 12, "local": 5} {
		if d := by[seat].Today; d == nil || d.Calls != 1 || d.Tokens != want {
			t.Errorf("%s today: %+v", seat, d)
		}
	}
	// alice's two rows each name their key, for the page to tell them apart
	if a.Bound[0].Key != keyID || a.Bound[0].KeyName != "Tenant A" || a.Bound[1].Key != "" {
		t.Errorf("alice's keys: %+v", a.Bound)
	}
	type bound struct {
		user  string
		at    time.Time
		spare bool
	}
	said := func(bs []accountUserJSON) []bound {
		var out []bound
		for _, x := range bs {
			out = append(out, bound{x.User, x.At.UTC(), x.Spare})
		}
		return out
	}
	// alice twice, once a row per key group; bob a spare
	if got, want := said(a.Bound), []bound{{"alice", t1, false}, {"alice", t2, false}, {"bob", t3, true}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("A's users: %v, want %v", got, want)
	}
	if got, want := said(b.Bound), []bound{{"bob", t1, false}, {"carol", time.Time{}, false}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("b's users: %v, want %v", got, want)
	}
	if a.Today == nil || a.Today.Calls != 2 || a.Today.Tokens != 160 {
		t.Fatalf("A today: %+v", a.Today)
	}
	if b.Today != nil {
		t.Fatalf("b today: %+v", b.Today)
	}
}
