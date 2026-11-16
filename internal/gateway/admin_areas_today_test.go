package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// writeAreas writes areas.json by hand: each area's accounts with the
// Added a test gives them, which area.Claim (now) can't.
func writeAreas(t testing.TB, areas []area.Area) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"version": 1, "areas": areas})
	if err := os.MkdirAll(filepath.Dir(area.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(area.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // a new mtime for the read cache
}

// The areas list's one visit of the ledger gives each area what its own
// usage gives it: calls from each account's Added on, a key's by its
// fingerprint and an account's by Record.Account() under its provider,
// a contested account's in no area, and a call two of an area's accounts
// name counted once.
func TestAdminAreasTodayIsEachAreasUsage(t *testing.T) {
	adminWorld(t) // "plan" takes keys; "sub" is no provider, so its refs are accounts
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	k1, k2, shared := provider.KeyID("sk-areas-k1"), provider.KeyID("sk-areas-k2"), provider.KeyID("sk-areas-shared")
	areas := []area.Area{
		{ID: "ar_a", Note: "A", Accounts: []area.Account{{Ref: "plan/" + k1, Added: ago(3 * time.Minute)}}},
		{ID: "ar_b", Note: "B", Accounts: []area.Account{{Ref: "plan/" + k2, Added: ago(time.Minute)}, {Ref: "sub/alice@x", Added: ago(2 * time.Minute)}}},
		{ID: "ar_c", Note: "C", Accounts: []area.Account{{Ref: "sub/Alice@X", Added: ago(4 * time.Minute)}}},
		{ID: "ar_d", Note: "D", Accounts: []area.Account{{Ref: "plan/" + shared, Added: ago(5 * time.Minute)}}},
		{ID: "ar_e", Note: "E", Accounts: []area.Account{{Ref: "plan/" + shared, Added: ago(5 * time.Minute)}, {Ref: "sub/carol", Added: ago(5 * time.Minute)}}},
		{ID: "ar_f", Note: "F"},
		{ID: "ar_g", Note: "G", Accounts: []area.Account{{Ref: "sub/bob", Added: ago(5 * time.Minute)}, {Ref: "sub/BOB", Added: ago(2 * time.Minute)}}},
	}
	writeAreas(t, areas)
	for _, at := range []time.Duration{6 * time.Minute, 150 * time.Second, 90 * time.Second, 30 * time.Second, 0} {
		ts := ago(at)
		for _, r := range []usage.Record{
			{Provider: "plan", ProviderKeyID: k1, Model: "m1", Status: 200, Input: 1, Output: 2},
			{Provider: "plan", ProviderKeyID: k2, Model: "m1", Status: 500, Input: 3},
			{Provider: "plan", ProviderKeyID: shared, Model: "m1", Status: 200, Input: 5},
			{Provider: "other", ProviderKeyID: k1, Model: "m1", Status: 200, Input: 7},
			{Provider: "sub", ProviderAccount: "ALICE@x", Model: "m2", Status: 200, Input: 11, CacheRead: 4},
			{Provider: "sub", Host: "chatgpt.com as alice@x", Model: "m2", Status: 429},
			{Provider: "sub", ProviderAccount: "carol", Model: "m2", Status: 200, Output: 13},
			{Provider: "sub", ProviderAccount: "bob", Model: "m2", Status: 200, Input: 17, CacheWrite: 1},
			{Provider: "plan", ProviderAccount: "bob", Model: "m2", Status: 200, Input: 19},
		} {
			r.Time = ts
			usage.Append(r)
		}
	}
	listed, _ := area.List()
	accs := make([][]adminAccount, len(listed))
	for i, a := range listed {
		accs[i] = areaAccounts(a)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []*time.Location{time.Local, time.UTC, tokyo} {
		got := areasToday(listed, accs, loc, now)
		if len(got) != len(listed) {
			t.Fatalf("%s: %d areas, want %d", loc, len(got), len(listed))
		}
		for _, a := range listed {
			if want := areaUsage(a, usage.Today, loc, now).Totals; got[a.ID] != want {
				t.Errorf("%s: area %s: one visit %+v, its own usage %+v", loc, a.Note, got[a.ID], want)
			}
		}
	}
	// and the two agree on something: each is counted where it should be
	got := areasToday(listed, accs, time.UTC, now)
	if got["ar_a"].Requests == 0 || got["ar_b"].Failed == 0 || got["ar_c"].Requests == 0 || got["ar_g"].Input == 0 {
		t.Fatalf("nothing counted: %+v", got)
	}
	if got["ar_d"] != (adminTotals{}) || got["ar_f"] != (adminTotals{}) || got["ar_e"].Input != 0 {
		t.Fatalf("contested or no account counted: %+v", got)
	}
	if g := got["ar_g"]; g.Input != 17*4 || g.CacheWrite != 4 { // the 6m call is before bob's Added
		t.Fatalf("G's call named by two of its accounts: %+v", g)
	}
	if v := areasOf(listed, accs, now); len(v) != len(listed) || v[0].ID != listed[0].ID || v[0].Today != areasToday(listed, accs, time.Local, now)[listed[0].ID] {
		t.Fatalf("the list: %+v", v)
	}
}

// BenchmarkAreasToday is the areas list's usage for 50 areas over a day
// of 10,000 calls: each area's own visit of the ledger (as the list read
// it before T-185), and one visit for them all.
func BenchmarkAreasToday(b *testing.B) {
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		b.Setenv(k, b.TempDir())
	}
	now := time.Now()
	var areas []area.Area
	for i := range 50 {
		a := area.Area{ID: fmt.Sprintf("ar_%02d", i), Note: fmt.Sprint(i)}
		for j := range 4 {
			a.Accounts = append(a.Accounts, area.Account{Ref: fmt.Sprintf("sub/u%02d-%d@x", i, j), Added: now.Add(-time.Hour)})
		}
		areas = append(areas, a)
	}
	writeAreas(b, areas)
	start := now.Add(-time.Hour)
	if mid := usage.Today.Since(now); start.Before(mid) {
		start = mid
	}
	for i := range 10000 {
		usage.Append(usage.Record{Time: start.Add(time.Duration(i) * now.Sub(start) / 10000), Provider: "sub", ProviderAccount: fmt.Sprintf("u%02d-%d@x", i%60, i%4), Model: "m1", Status: 200, Input: 10, Output: 5})
	}
	listed, _ := area.List()
	accs := make([][]adminAccount, len(listed))
	for i, a := range listed {
		accs[i] = areaAccounts(a)
	}
	b.Run("each", func(b *testing.B) {
		for b.Loop() {
			for _, a := range listed {
				_ = areaUsage(a, usage.Today, time.Local, now).Totals
			}
		}
	})
	b.Run("once", func(b *testing.B) {
		for b.Loop() {
			_ = areasToday(listed, accs, time.Local, now)
		}
	})
}
