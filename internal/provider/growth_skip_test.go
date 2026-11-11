package provider

import (
	"encoding/json"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// A skipped ramp-up is kept apart from the windows' shares (T-140), so an
// account cap the user sets after it holds the windows the skip let go, as
// on any account, and a window's own share still comes before it. Before
// T-140 the skip was a 100 on each window, and an account cap left them
// out: the 5 hours here was held at nothing.
func TestSkipGrowthThenAccountCap(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	keepLast(SubscriptionQuota{Provider: "codex", User: "new@example.com", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 10, Span: 5 * time.Hour}, {Name: "7 days", Used: 10, Span: 7 * 24 * time.Hour},
	}}, "")
	if err := SetWindowCap("codex", "new@example.com", "7 days", 70); err != nil {
		t.Fatal(err)
	}
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if s := heldAt(t, "5 hours"); s != 100 {
		t.Fatalf("skipped, the 5 hours held at %d%%", s)
	}
	if err := SetAccountCap("codex", "new@example.com", 80); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := p.CapsOf("new@example.com").Of("5 hours"); s != 80 {
		t.Errorf("an account cap after the skip: the gateway holds the 5 hours at %d%%", s)
	}
	if s, _ := p.CapsOf("new@example.com").Of("7 days"); s != 70 {
		t.Errorf("an account cap after the skip: the user's own week at %d%%", s)
	}
	// lifted, the skip still stands: the policy doesn't come back
	if err := SetAccountCap("codex", "new@example.com", 0); err != nil {
		t.Fatal(err)
	}
	if s := heldAt(t, "5 hours"); s != 100 {
		t.Errorf("the account cap lifted, the 5 hours held at %d%%", s)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g != nil {
		t.Errorf("the account cap lifted, the row says %+v", g)
	}
	// once skipped, a second skip writes nothing
	before, _ := storedPicks("codex")
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if after, _ := storedPicks("codex"); !after.AccountGrowthSkipped["new@example.com"].Equal(before.AccountGrowthSkipped["new@example.com"]) {
		t.Errorf("skipped again, the time moved: %v to %v", before.AccountGrowthSkipped, after.AccountGrowthSkipped)
	}
}

// A skip from before T-140 was a 100 on each window the policy held, with
// no field of its own: read as it is, those windows are the user's own
// 100 (the policy holds none of them, and an account cap leaves them out),
// and nothing rewrites them.
func TestSkipGrowthFromBefore(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	keepLast(SubscriptionQuota{Provider: "codex", User: "new@example.com", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 10, Span: 5 * time.Hour}, {Name: "7 days", Used: 10, Span: 7 * 24 * time.Hour},
	}}, "")
	old := map[string]int{"5 hours": 100, "7 days": 100}
	if err := editStored("codex", func(q *Provider) bool {
		q.AccountWindowCaps = map[string]map[string]int{"new@example.com": maps.Clone(old)}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g != nil {
		t.Errorf("skipped before T-140, the row says %+v", g)
	}
	if err := SetAccountCap("codex", "new@example.com", 80); err != nil {
		t.Fatal(err)
	}
	if s := heldAt(t, "5 hours"); s != 100 {
		t.Errorf("skipped before T-140, an account cap holds its 5 hours at %d%%", s)
	}
	s, _ := storedPicks("codex")
	if !maps.Equal(s.AccountWindowCaps["new@example.com"], old) || s.AccountGrowthSkipped != nil {
		t.Errorf("an old skip rewritten: windows %v, skipped %v", s.AccountWindowCaps, s.AccountGrowthSkipped)
	}
}

// The skip is magpie's own record of the account: providers.json keeps it
// as accountGrowthSkipped, the editor's Save of a form without it keeps
// it, a rename moves it (moveAccountSettings), and the user removing the
// account lets it go (ForgetLogin, forgetAdded), so signed in again it
// ramps up again. The policy's chart doesn't mark it.
func TestSkipGrowthKeptApart(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Providers []map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range file.Providers {
		if strings.Contains(string(p["id"]), "codex") {
			_, found = p["accountGrowthSkipped"]
		}
	}
	if !found {
		t.Fatalf("providers.json has no accountGrowthSkipped: %s", raw)
	}
	for _, a := range GrowthAccounts(time.Now()) {
		if a.Account == "new@example.com" {
			t.Errorf("skipped, the chart marks it: %+v", a)
		}
	}

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	q := *p
	q.AccountGrowthSkipped = nil
	if err := Save(q); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthSkipped["new@example.com"].IsZero() {
		t.Fatalf("Save of a form without it: %v", s.AccountGrowthSkipped)
	}

	if err := moveAccountSettings("codex", map[string]string{"new@example.com": "Renamed@example.com"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthSkipped["renamed@example.com"].IsZero() || len(s.AccountGrowthSkipped) != 1 {
		t.Fatalf("after the rename: %v", s.AccountGrowthSkipped)
	}
	if err := moveAccountSettings("codex", map[string]string{"renamed@example.com": "new@example.com"}); err != nil {
		t.Fatal(err)
	}

	if err := ForgetLogin("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthSkipped != nil {
		t.Fatalf("removed by the user, its skip stayed: %v", s.AccountGrowthSkipped)
	}
}

// A skip goes with providers.json to another computer (sync's Stored and
// Mirror) and with a backup (Restore): the account there is let go too.
func TestSkipGrowthThroughASync(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	ps, err := Stored()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	var there []Provider
	if err := json.Unmarshal(b, &there); err != nil {
		t.Fatal(err)
	}

	firstUsedFixture(t, time.Hour) // the other computer
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	if s := heldAt(t, "5 hours"); s != 50 {
		t.Fatalf("the other computer before the sync: %d%%", s)
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if s := heldAt(t, "5 hours"); s != 100 {
		t.Errorf("after a sync, the 5 hours held at %d%%", s)
	}

	firstUsedFixture(t, time.Hour)
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	if _, _, err := Restore(there); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthSkipped["new@example.com"].IsZero() {
		t.Errorf("after a restore: %v", s.AccountGrowthSkipped)
	}
	if s := heldAt(t, "5 hours"); s != 100 {
		t.Errorf("after a restore, the 5 hours held at %d%%", s)
	}
}

// A sync onto a computer that has the record already (KeepSecrets' side of
// Mirror) carries the skip too.
func TestSkipGrowthSyncOntoARecord(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	ps, err := Stored()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	var there []Provider
	if err := json.Unmarshal(b, &there); err != nil {
		t.Fatal(err)
	}
	firstUsedFixture(t, time.Hour) // the other computer, with its own record
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	if _, ok := storedPicks("codex"); !ok {
		t.Fatal("the other computer has no record of Codex's")
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if s := heldAt(t, "5 hours"); s != 100 {
		t.Errorf("synced onto a record, the 5 hours held at %d%%", s)
	}
}

// A policy the user picks for the account holds it again: the skip is let
// go of (T-140 review M1), and the account's other settings stay.
func TestSkipGrowthEndsWithAPick(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	freshReadings(t)
	growthDefault(t, settings.GrowthStandard)
	if err := SkipGrowth("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountGrowthPolicy("codex", "new@example.com", settings.GrowthStandard); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthSkipped != nil {
		t.Fatalf("picked a policy, the skip stayed: %v", s.AccountGrowthSkipped)
	}
	if s := heldAt(t, "5 hours"); s != 50 {
		t.Errorf("picked a policy, the 5 hours held at %d%%", s)
	}
}

// A record kept for an account's day and its skip alone goes when the user
// removes the account (forgetAdded's empty record).
func TestSkipGrowthRecordGoesWithIt(t *testing.T) {
	firstUsedFixture(t, time.Hour)
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	f.Providers = append(f.Providers, Provider{ID: "skiponly",
		AccountAdded:         map[string]time.Time{"x@example.com": time.Now().UTC()},
		AccountGrowthSkipped: map[string]time.Time{"x@example.com": time.Now().UTC()}})
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	forgetAdded("skiponly", "x@example.com")
	if _, ok := storedPicks("skiponly"); ok {
		t.Error("a record kept for a day and a skip alone stayed")
	}
}
