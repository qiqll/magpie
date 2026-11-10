package provider

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// growthPicksFixture stores an account of Codex's and one of Copilot's,
// each added 36 hours ago (day 1), Codex's subscription with the growth
// policy picked, Copilot's following the default.
func growthPicksFixture(t *testing.T, picked string) {
	t.Helper()
	signIn(t)
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	added := time.Now().Add(-36 * time.Hour).UTC().Truncate(time.Second)
	f.Providers = []Provider{
		{ID: "codex", GrowthPolicy: picked, AccountAdded: map[string]time.Time{"new@example.com": added}},
		{ID: "copilot", AccountAdded: map[string]time.Time{"octocat": added}},
	}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
}

// pickedShare is the share of the 5 hours and of the week the account
// user of id is held to, as switching reads it (AccountCapsOf) and as the
// gateway's capHeld does (CapsOf), which must agree.
func pickedShare(t *testing.T, id, user string) (int, int) {
	t.Helper()
	sw := AccountCapsOf(id, user)
	gw := Provider{ID: id}.CapsOf(user)
	f, _ := sw.Of("5 hours")
	w, _ := sw.Of("Weekly")
	if gf, _ := gw.Of("5 hours"); gf != f {
		t.Errorf("%s: the gateway holds the 5 hours at %d%%, switching at %d%%", id, gf, f)
	}
	if gw, _ := gw.Of("Weekly"); gw != w {
		t.Errorf("%s: the gateway holds the week at %d%%, switching at %d%%", id, gw, w)
	}
	return f, w
}

// A subscription that picked Cautious has its account on day 1 held at
// 32% (30+70×1/30), where one that follows the default, Standard, has its
// account of the same day at 53% (50+50×1/14): in the gateway's holds and
// in switching alike, and on the account's row. A pick no policy has (one
// deleted since) is held by the default, as following it is; the default
// changed, both follow it, and a pick made again holds again.
func TestGrowthPolicyBySubscription(t *testing.T) {
	growthPicksFixture(t, settings.GrowthCautious)
	if f, w := pickedShare(t, "codex", "new@example.com"); f != 32 || w != 32 {
		t.Fatalf("Cautious picked, day 1: 5 hours %d%%, week %d%%", f, w)
	}
	if f, w := pickedShare(t, "copilot", "octocat"); f != 53 || w != 53 {
		t.Fatalf("the default, Standard, day 1: 5 hours %d%%, week %d%%", f, w)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.Policy != settings.GrowthCautious || g.FiveHours != 32 || g.Days != 30 {
		t.Errorf("Codex's row: %+v", g)
	}
	if g := AccountGrowthOf("copilot", "octocat", time.Now()); g == nil || g.Policy != settings.GrowthStandard || g.FiveHours != 53 || g.Days != 14 {
		t.Errorf("Copilot's row: %+v", g)
	}

	pick := func(id string) {
		t.Helper()
		f, err := read()
		if err != nil {
			t.Fatal(err)
		}
		f.Providers[0].GrowthPolicy = id
		if err := store(f); err != nil {
			t.Fatal(err)
		}
	}
	pick("gone")
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 53 {
		t.Errorf("a policy gone, held by the default: %d%%", f)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.Policy != settings.GrowthStandard {
		t.Errorf("a policy gone, the row says %+v", g)
	}

	if err := settings.EditGrowth(func(own []settings.GrowthPolicy, _ string) ([]settings.GrowthPolicy, string, error) {
		return own, settings.GrowthGentle, nil
	}); err != nil {
		t.Fatal(err)
	}
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 74 {
		t.Errorf("a policy gone, the default Gentle: %d%%", f)
	}
	if f, _ := pickedShare(t, "copilot", "octocat"); f != 74 {
		t.Errorf("following the default Gentle: %d%%", f)
	}
	pick(settings.GrowthCautious)
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 32 {
		t.Errorf("Cautious picked again: %d%%", f)
	}
	if f, _ := pickedShare(t, "copilot", "octocat"); f != 74 {
		t.Errorf("Copilot moved with Codex's pick: %d%%", f)
	}
	pick(settings.GrowthNone)
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 0 {
		t.Errorf("Off picked: held at %d%%", f)
	}
}

// SetGrowthPolicy writes the pick on the record as it is under the lock,
// so a setting set since the provider was read stays; a policy there is
// none of is refused and changes nothing. The editor's Save keeps the
// pick the file has, whatever the form carries, and "" follows the
// default again.
func TestSetGrowthPolicy(t *testing.T) {
	signIn(t)
	read := func() Provider {
		t.Helper()
		s, ok := storedPicks("codex")
		if !ok {
			t.Fatal("no record of Codex's")
		}
		return s
	}
	if err := SetGrowthPolicy("codex", settings.GrowthCautious); err != nil {
		t.Fatal(err)
	}
	if err := SetCapStop("codex", true); err != nil {
		t.Fatal(err)
	}
	if s := read(); s.GrowthPolicy != settings.GrowthCautious || !s.CapStop {
		t.Fatalf("after a pick and Stop at the caps: %q %v", s.GrowthPolicy, s.CapStop)
	}
	if err := SetGrowthPolicy("codex", " gentle "); err != nil {
		t.Fatal(err)
	}
	if s := read(); s.GrowthPolicy != settings.GrowthGentle || !s.CapStop {
		t.Fatalf("picked again: %q, Stop at the caps %v", s.GrowthPolicy, s.CapStop)
	}
	if err := SetGrowthPolicy("codex", "nope"); err == nil {
		t.Fatal("took a policy there is none of")
	}
	if s := read(); s.GrowthPolicy != settings.GrowthGentle {
		t.Fatalf("a refused pick changed it: %q", s.GrowthPolicy)
	}
	if p, err := Find("codex"); err != nil || p.GrowthPolicy != settings.GrowthGentle {
		t.Fatalf("Find reads %v %+v", err, p)
	}

	for _, carried := range []string{"", settings.GrowthCautious} {
		p, err := Find("codex")
		if err != nil {
			t.Fatal(err)
		}
		q := *p
		q.GrowthPolicy = carried
		if err := Save(q); err != nil {
			t.Fatal(err)
		}
		if s := read(); s.GrowthPolicy != settings.GrowthGentle {
			t.Fatalf("Save of a form carrying %q: %q", carried, s.GrowthPolicy)
		}
	}
	if err := SetGrowthPolicy("codex", ""); err != nil {
		t.Fatal(err)
	}
	if s := read(); s.GrowthPolicy != "" || !s.CapStop {
		t.Fatalf("following the default again: %q %v", s.GrowthPolicy, s.CapStop)
	}
}

// A subscription's pick goes with providers.json to another computer
// (sync's Stored and Mirror) and with a backup (Restore), so the accounts
// there are held by the same policy; the field is growthPolicy.
func TestGrowthPolicyThroughASync(t *testing.T) {
	growthPicksFixture(t, settings.GrowthCautious)
	ps, err := Stored()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw[0]["growthPolicy"] != settings.GrowthCautious {
		t.Fatalf("written as %v", raw[0])
	}

	growthPicksFixture(t, "") // the other computer
	var there []Provider
	if err := json.Unmarshal(b, &there); err != nil {
		t.Fatal(err)
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.GrowthPolicy != settings.GrowthCautious {
		t.Fatalf("after a sync: %q", s.GrowthPolicy)
	}
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 32 {
		t.Errorf("held after a sync at %d%%", f)
	}

	growthPicksFixture(t, "")
	if _, _, err := Restore(there); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.GrowthPolicy != settings.GrowthCautious {
		t.Fatalf("after a restore: %q", s.GrowthPolicy)
	}
}

// Settings › Growth policy marks on a policy's chart only the accounts it
// holds, and lists in its In use the subscriptions it holds: one that
// picked it, or one that follows the default it is. A pick moves the
// subscription from the default's to the policy's, and GrowthPicks names
// it for the page's delete to refuse.
func TestGrowthUsesAndAccountsOf(t *testing.T) {
	firstUsedFixture(t, 3*24*time.Hour+time.Hour) // the default is "before"
	has := func(as []GrowthAccount, sub string) bool {
		return slices.ContainsFunc(as, func(a GrowthAccount) bool { return a.Provider == sub })
	}
	use := func(us []GrowthUse, sub string) *GrowthUse {
		i := slices.IndexFunc(us, func(u GrowthUse) bool { return u.Provider == sub })
		if i < 0 {
			return nil
		}
		return &us[i]
	}
	now := time.Now()
	if !has(GrowthAccountsOf("before", now), "codex") || has(GrowthAccountsOf(settings.GrowthCautious, now), "codex") {
		t.Fatal("following the default, Codex's account is marked on another policy's chart, or not on the default's")
	}
	if u := use(GrowthUses("before", now), "codex"); u == nil || u.Picked || u.Accounts != 1 || u.Name == "" {
		t.Fatalf("the default's In use: %+v", GrowthUses("before", now))
	}
	if picks, err := GrowthPicks(); err != nil || len(picks) != 0 {
		t.Fatalf("picks before any: %v %v", picks, err)
	}

	if err := SetGrowthPolicy("codex", settings.GrowthCautious); err != nil {
		t.Fatal(err)
	}
	as := GrowthAccountsOf(settings.GrowthCautious, now)
	if !has(as, "codex") || has(GrowthAccountsOf("before", now), "codex") {
		t.Fatalf("Cautious picked: its chart %+v", as)
	}
	if u := use(GrowthUses(settings.GrowthCautious, now), "codex"); u == nil || !u.Picked || u.Accounts != 1 {
		t.Fatalf("Cautious's In use: %+v", GrowthUses(settings.GrowthCautious, now))
	}
	if use(GrowthUses("before", now), "codex") != nil {
		t.Fatal("Codex still listed under the default it no longer follows")
	}
	if u := use(GrowthUses("before", now), "copilot"); u == nil || u.Picked {
		t.Errorf("Copilot, following the default: %+v", GrowthUses("before", now))
	}
	if picks, err := GrowthPicks(); err != nil || picks["codex"] != settings.GrowthCautious || len(picks) != 1 {
		t.Fatalf("picks: %v %v", picks, err)
	}
}
