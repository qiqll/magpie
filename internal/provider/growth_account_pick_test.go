package provider

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// accountPickFixture is growthPicksFixture with a second Codex account of
// the same day, other@example.com, and new@example.com's own pick.
func accountPickFixture(t *testing.T, sub, account string) {
	t.Helper()
	growthPicksFixture(t, sub)
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	c := &f.Providers[0]
	c.AccountAdded["other@example.com"] = c.AccountAdded["new@example.com"]
	if account != "" {
		c.AccountGrowthPolicy = map[string]string{"new@example.com": account}
	}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
}

// An account's own pick (T-133) comes before its subscription's, which
// comes before the default, in the gateway's holds (CapsOf), switching
// (AccountCapsOf) and the account's row (AccountGrowthOf) alike. On day 1
// Gentle holds at 74%, Cautious at 32% and Standard, the default, at 53%.
// Each level taken away hands the account to the next; the other account
// of the subscription keeps the subscription's all along; a pick no policy
// has is its subscription's, not the default's.
func TestGrowthPolicyByAccount(t *testing.T) {
	accountPickFixture(t, settings.GrowthCautious, settings.GrowthGentle)
	row := func(want string) {
		t.Helper()
		if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g == nil || g.Policy != want {
			t.Errorf("the row says %+v, want %s", g, want)
		}
	}
	if f, w := pickedShare(t, "codex", "new@example.com"); f != 74 || w != 74 {
		t.Fatalf("Gentle picked for the account, Cautious for Codex: 5 hours %d%%, week %d%%", f, w)
	}
	row(settings.GrowthGentle)
	if f, _ := pickedShare(t, "codex", "other@example.com"); f != 32 {
		t.Errorf("the other account, following Codex's Cautious: %d%%", f)
	}

	set := func(sub string, account map[string]string) {
		t.Helper()
		f, err := read()
		if err != nil {
			t.Fatal(err)
		}
		f.Providers[0].GrowthPolicy, f.Providers[0].AccountGrowthPolicy = sub, account
		if err := store(f); err != nil {
			t.Fatal(err)
		}
	}
	set(settings.GrowthCautious, map[string]string{"new@example.com": "gone"})
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 32 {
		t.Errorf("the account's pick gone: held by Codex's Cautious, got %d%%", f)
	}
	row(settings.GrowthCautious)
	set(settings.GrowthCautious, nil)
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 32 {
		t.Errorf("no pick of its own: Codex's Cautious, got %d%%", f)
	}
	set("", nil)
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 53 {
		t.Errorf("no pick at all: the default Standard, got %d%%", f)
	}
	row(settings.GrowthStandard)
	set("", map[string]string{"new@example.com": settings.GrowthNone})
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 0 {
		t.Errorf("Off picked for the account alone: held at %d%%", f)
	}
	if g := AccountGrowthOf("codex", "new@example.com", time.Now()); g != nil {
		t.Errorf("Off picked, the row still says %+v", g)
	}
	if f, _ := pickedShare(t, "codex", "other@example.com"); f != 53 {
		t.Errorf("the other account moved with new@example.com's Off: %d%%", f)
	}
}

// SetAccountGrowthPolicy writes the account's pick on the record as it is
// under the lock, by the account's name in lower case, so a setting set
// since stays; a policy there is none of, or an account the subscription
// hasn't, is refused and changes nothing. The editor's Save keeps the
// picks the file has, and "" has the account follow its subscription.
func TestSetAccountGrowthPolicy(t *testing.T) {
	firstUsedFixture(t, 36*time.Hour)
	picks := func() map[string]string {
		t.Helper()
		s, ok := storedPicks("codex")
		if !ok {
			t.Fatal("no record of Codex's")
		}
		return s.AccountGrowthPolicy
	}
	if err := SetAccountGrowthPolicy("codex", "NEW@example.com ", " cautious "); err != nil {
		t.Fatal(err)
	}
	if err := SetCapStop("codex", true); err != nil {
		t.Fatal(err)
	}
	if err := SetGrowthPolicy("codex", settings.GrowthGentle); err != nil {
		t.Fatal(err)
	}
	if got := picks(); len(got) != 1 || got["new@example.com"] != settings.GrowthCautious {
		t.Fatalf("after the account's pick and two more settings: %v", got)
	}
	if s, _ := storedPicks("codex"); !s.CapStop || s.GrowthPolicy != settings.GrowthGentle {
		t.Fatalf("the settings set after it: %+v", s)
	}
	if err := SetAccountGrowthPolicy("codex", "new@example.com", "nope"); err == nil {
		t.Fatal("took a policy there is none of")
	}
	if err := SetAccountGrowthPolicy("codex", "nobody@example.com", settings.GrowthGentle); err == nil {
		t.Fatal("took an account Codex hasn't")
	}
	if got := picks(); len(got) != 1 || got["new@example.com"] != settings.GrowthCautious {
		t.Fatalf("a refused pick changed them: %v", got)
	}
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 32 {
		t.Errorf("picked Cautious, under Codex's Gentle: %d%%", f)
	}

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	q := *p
	q.AccountGrowthPolicy = map[string]string{"me@example.com": settings.GrowthNone}
	if err := Save(q); err != nil {
		t.Fatal(err)
	}
	if got := picks(); len(got) != 1 || got["new@example.com"] != settings.GrowthCautious {
		t.Fatalf("Save of a form carrying other picks: %v", got)
	}
	if err := SetAccountGrowthPolicy("codex", "new@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if got := picks(); got != nil {
		t.Fatalf("following its subscription again: %v", got)
	}
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 74 {
		t.Errorf("following Codex's Gentle again: %d%%", f)
	}
}

// An account's pick moves with it to its new name (moveAccountSettings,
// as AccountAdded does), and goes when the user removes the account
// (forgetAdded, through ForgetLogin): signed in again, it follows its
// subscription. A record left with nothing else goes too.
func TestAccountGrowthPolicyMovesAndGoes(t *testing.T) {
	accountPickFixture(t, "", settings.GrowthCautious)
	if err := moveAccountSettings("codex", map[string]string{"new@example.com": "Renamed@example.com"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthPolicy["renamed@example.com"] != settings.GrowthCautious || len(s.AccountGrowthPolicy) != 1 {
		t.Fatalf("after the rename: %v", s.AccountGrowthPolicy)
	}
	if f, _ := pickedShare(t, "codex", "renamed@example.com"); f != 32 {
		t.Errorf("held under its new name at %d%%", f)
	}

	// a pick on an account with no day kept goes with it all the same
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	f.Providers[0].AccountGrowthPolicy["nobody@example.com"] = settings.GrowthGentle
	f.Providers = append(f.Providers, Provider{ID: "pickonly", AccountGrowthPolicy: map[string]string{"x@example.com": settings.GrowthGentle}})
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	forgetAdded("codex", "NOBODY@example.com")
	if s, _ := storedPicks("codex"); s.AccountGrowthPolicy["nobody@example.com"] != "" || s.AccountGrowthPolicy["renamed@example.com"] != settings.GrowthCautious {
		t.Fatalf("after removing nobody@example.com: %v", s.AccountGrowthPolicy)
	}
	forgetAdded("pickonly", "x@example.com")
	if _, ok := storedPicks("pickonly"); ok {
		t.Error("a record kept for its pick alone stayed once the pick went")
	}

	firstUsedFixture(t, 36*time.Hour)
	if err := SetAccountGrowthPolicy("codex", "new@example.com", settings.GrowthCautious); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthPolicy != nil {
		t.Fatalf("removed by the user, its pick stayed: %v", s.AccountGrowthPolicy)
	}
}

// An account's pick goes with providers.json to another computer (sync's
// Stored and Mirror) and with a backup (Restore), so the account there is
// held by the same policy; the field is accountGrowthPolicy.
func TestAccountGrowthPolicyThroughASync(t *testing.T) {
	accountPickFixture(t, "", settings.GrowthCautious)
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
	if m, _ := raw[0]["accountGrowthPolicy"].(map[string]any); m["new@example.com"] != settings.GrowthCautious {
		t.Fatalf("written as %v", raw[0])
	}

	accountPickFixture(t, "", "") // the other computer
	var there []Provider
	if err := json.Unmarshal(b, &there); err != nil {
		t.Fatal(err)
	}
	if err := Mirror(there, nil); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthPolicy["new@example.com"] != settings.GrowthCautious {
		t.Fatalf("after a sync: %v", s.AccountGrowthPolicy)
	}
	if f, _ := pickedShare(t, "codex", "new@example.com"); f != 32 {
		t.Errorf("held after a sync at %d%%", f)
	}

	accountPickFixture(t, "", "")
	if _, _, err := Restore(there); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); s.AccountGrowthPolicy["new@example.com"] != settings.GrowthCautious {
		t.Fatalf("after a restore: %v", s.AccountGrowthPolicy)
	}
}

// A policy an account picked for itself marks it on its chart and lists
// its subscription in its In use, with how many of its accounts picked it,
// though the subscription follows another (AccountsOnly); the policy the
// subscription follows no longer marks it. GrowthPicks names the account,
// so the page's delete refuses the policy.
func TestGrowthUsesCountAnAccountsPick(t *testing.T) {
	firstUsedFixture(t, 3*24*time.Hour+time.Hour) // the default is "before"
	has := func(as []GrowthAccount, user string) bool {
		return slices.ContainsFunc(as, func(a GrowthAccount) bool { return a.Provider == "codex" && a.Account == user })
	}
	now := time.Now()
	if err := SetAccountGrowthPolicy("codex", "new@example.com", settings.GrowthCautious); err != nil {
		t.Fatal(err)
	}
	if !has(GrowthAccountsOf(settings.GrowthCautious, now), "new@example.com") || has(GrowthAccountsOf("before", now), "new@example.com") {
		t.Fatalf("picked Cautious: its chart %+v, the default's %+v", GrowthAccountsOf(settings.GrowthCautious, now), GrowthAccountsOf("before", now))
	}
	us := GrowthUses(settings.GrowthCautious, now)
	i := slices.IndexFunc(us, func(u GrowthUse) bool { return u.Provider == "codex" })
	if i < 0 || !us[i].AccountsOnly || us[i].Picked || us[i].AccountsPicked != 1 || us[i].Accounts != 1 {
		t.Fatalf("Cautious's In use: %+v", us)
	}
	us = GrowthUses("before", now)
	if i := slices.IndexFunc(us, func(u GrowthUse) bool { return u.Provider == "codex" }); i < 0 || us[i].AccountsOnly || us[i].AccountsPicked != 0 || us[i].Accounts != 0 {
		t.Fatalf("the default's In use, Codex following it with its account picked away: %+v", us)
	}
	picks, err := GrowthPicks()
	if err != nil || !slices.Equal(picks, []GrowthPick{{Provider: "codex", Account: "new@example.com", Policy: settings.GrowthCautious}}) {
		t.Fatalf("picks: %v %v", picks, err)
	}
}

// A pick left by an account gone from outside magpie (its sign-in deleted
// by hand, never removed through magpie) can be cleared by its name, so
// the policy it names can be deleted; a policy can't be picked for it.
func TestAccountGrowthPolicyOfAGoneAccountClears(t *testing.T) {
	accountPickFixture(t, "", "")
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	f.Providers[0].AccountGrowthPolicy = map[string]string{"gone@example.com": settings.GrowthCautious}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountGrowthPolicy("codex", "gone@example.com", settings.GrowthGentle); err == nil {
		t.Fatal("picked a policy for an account Codex hasn't")
	}
	if err := SetAccountGrowthPolicy("codex", "Gone@example.com", ""); err != nil {
		t.Fatalf("clearing a gone account's pick: %v", err)
	}
	if picks, err := GrowthPicks(); err != nil || len(picks) != 0 {
		t.Fatalf("picks after clearing it: %v %v", picks, err)
	}
}

// Every pick GrowthPicks counts, and the page's delete is refused by, is
// in the policy's In use (T-145): a subscription turned off that picked
// it, an account gone from the list that picked it (Gone, by name), and
// a record the Providers page has no row of (Unlisted). Each one is
// cleared by its setter's "", the record unlisted too, and then neither
// lists it nor refuses the delete. One turned off that follows the policy
// as the default holds nothing and isn't listed.
func TestGrowthUsesListWhatRefusesTheDelete(t *testing.T) {
	firstUsedFixture(t, 3*24*time.Hour+time.Hour) // the default is "before"
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == "codex" })
	f.Providers[i].GrowthPolicy = settings.GrowthCautious
	f.Providers[i].AccountGrowthPolicy = map[string]string{"gone@example.com": settings.GrowthCautious}
	f.Providers = append(f.Providers, Provider{ID: "claude", GrowthPolicy: settings.GrowthCautious, AccountGrowthPolicy: map[string]string{"old@example.com": settings.GrowthCautious}})
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(sharedAll(), func(p Provider) bool { return p.ID == "claude" }) {
		t.Fatal("the fixture's Claude is listed: it is meant to have no row")
	}
	if err := SetOff("codex", true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	use := func(sub string) *GrowthUse {
		us := GrowthUses(settings.GrowthCautious, now)
		i := slices.IndexFunc(us, func(u GrowthUse) bool { return u.Provider == sub })
		if i < 0 {
			return nil
		}
		return &us[i]
	}
	picks, err := GrowthPicks()
	if err != nil || len(picks) != 4 {
		t.Fatalf("picks: %v %v", picks, err)
	}
	if u := use("codex"); u == nil || !u.Off || !u.Picked || u.AccountsOnly || u.Accounts != 0 || u.AccountsPicked != 1 || !slices.Equal(u.Gone, []string{"gone@example.com"}) {
		t.Fatalf("Codex, turned off, in Cautious's In use: %+v", u)
	}
	if u := use("claude"); u == nil || !u.Unlisted || !u.Picked || u.AccountsPicked != 1 || !slices.Equal(u.Gone, []string{"old@example.com"}) {
		t.Fatalf("Claude, with no row, in Cautious's In use: %+v", u)
	}
	if us := GrowthUses("before", now); slices.ContainsFunc(us, func(u GrowthUse) bool { return u.Provider == "codex" }) {
		t.Fatalf("Codex, turned off, listed under the default it holds nothing by: %+v", us)
	}

	for _, clear := range []func() error{
		func() error { return SetAccountGrowthPolicy("codex", "gone@example.com", "") },
		func() error { return SetGrowthPolicy("codex", "") },
		func() error { return SetAccountGrowthPolicy("claude", "Old@example.com", "") },
		func() error { return SetGrowthPolicy("claude", "") },
	} {
		if err := clear(); err != nil {
			t.Fatal(err)
		}
	}
	if picks, err := GrowthPicks(); err != nil || len(picks) != 0 {
		t.Fatalf("picks after clearing each: %v %v", picks, err)
	}
	if us := GrowthUses(settings.GrowthCautious, now); len(us) != 0 {
		t.Fatalf("Cautious's In use after clearing each: %+v", us)
	}
	// a record of none, or one with nothing left to clear, is Find's error
	if err := SetGrowthPolicy("claude", ""); err == nil {
		t.Fatal("cleared Claude's pick twice")
	}
	if err := SetGrowthPolicy("nobody", ""); err == nil {
		t.Fatal("cleared the pick of a provider there is no record of")
	}
	if f, err := read(); err != nil || slices.ContainsFunc(f.Providers, func(p Provider) bool { return p.ID == "nobody" }) {
		t.Fatalf("a record made for nobody: %v", err)
	}
}

// A clear meant for a record the Providers page has no row of goes to that
// record, never to a listed provider Find reaches by its name or a former
// id (review H1): a provider of the user's own named Claude, which refused
// the clear as one of keys. A key or
// an id written by hand in another case is its account's or its
// provider's: In use lists Codex once, the account listed isn't Gone, and
// one clear takes the pick away (review M2, M3).
func TestGrowthClearsTheRecordItNames(t *testing.T) {
	firstUsedFixture(t, 3*24*time.Hour+time.Hour)
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == "codex" })
	f.Providers[i].ID = " Codex"
	f.Providers[i].GrowthPolicy = settings.GrowthCautious
	f.Providers[i].AccountGrowthPolicy = map[string]string{"New@Example.com": settings.GrowthCautious, "Gone@Example.com": settings.GrowthCautious}
	f.Providers = append(f.Providers, Provider{ID: "mine", Name: "Claude", Chat: "https://relay.example.com/v1", Key: "sk-test"},
		Provider{ID: "claude", GrowthPolicy: settings.GrowthCautious, AccountGrowthPolicy: map[string]string{"old@example.com": settings.GrowthCautious}})
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("claude"); err != nil || p.ID != "mine" {
		t.Fatalf("the fixture's Find(claude) is meant to reach the user's own Claude by its name: %v %v", p, err)
	}
	now := time.Now()
	us := GrowthUses(settings.GrowthCautious, now)
	if n := len(slices.DeleteFunc(slices.Clone(us), func(u GrowthUse) bool { return u.Provider != "codex" })); n != 1 {
		t.Fatalf("Codex listed %d times: %+v", n, us)
	}
	if i := slices.IndexFunc(us, func(u GrowthUse) bool { return u.Provider == "codex" }); i < 0 || us[i].Unlisted || us[i].AccountsPicked != 2 || !slices.Equal(us[i].Gone, []string{"gone@example.com"}) {
		t.Fatalf("Codex in Cautious's In use: %+v", us)
	}

	if err := SetGrowthPolicy("claude", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountGrowthPolicy("claude", "old@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountGrowthPolicy("codex", "gone@example.com", ""); err != nil {
		t.Fatal(err)
	}
	picks, err := GrowthPicks()
	want := []GrowthPick{{Provider: " Codex", Policy: settings.GrowthCautious}, {Provider: " Codex", Account: "new@example.com", Policy: settings.GrowthCautious}}
	if err != nil || !slices.Equal(picks, want) {
		t.Fatalf("picks after clearing Claude's and Codex's gone account's: %v %v", picks, err)
	}
}

// A provider of keys whose record kept a pick (written before, by hand or
// a sync) is in the policy's In use (T-152), Keyed, its accounts' picks
// Gone: GrowthPicks counts them, so they refuse the delete. Each is
// cleared by its setter's "" by the provider's id, and then neither lists
// it nor refuses the delete; a pick of a policy is still refused, as one
// of keys has no accounts to hold, and so is a clear asked by its name.
func TestGrowthUsesListAKeyedProvidersPick(t *testing.T) {
	firstUsedFixture(t, 3*24*time.Hour+time.Hour)
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	f.Providers = append(f.Providers, Provider{ID: "relay", Name: "My relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		GrowthPolicy: settings.GrowthCautious, AccountGrowthPolicy: map[string]string{"old@example.com": settings.GrowthCautious}})
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("relay"); err != nil || p.Account != nil || !slices.ContainsFunc(sharedAll(), func(q Provider) bool { return q.ID == "relay" }) {
		t.Fatalf("the fixture's relay is meant to be a listed provider of keys: %v %v", p, err)
	}
	now := time.Now()
	us := GrowthUses(settings.GrowthCautious, now)
	i := slices.IndexFunc(us, func(u GrowthUse) bool { return u.Provider == "relay" })
	if i < 0 || !us[i].Keyed || !us[i].Picked || us[i].AccountsOnly || us[i].AccountsPicked != 1 || !slices.Equal(us[i].Gone, []string{"old@example.com"}) || us[i].Name != "My relay" {
		t.Fatalf("the relay in Cautious's In use: %+v", us)
	}
	if picks, err := GrowthPicks(); err != nil || len(picks) != 2 {
		t.Fatalf("picks: %v %v", picks, err)
	}
	if err := SetGrowthPolicy("relay", settings.GrowthGentle); err == nil {
		t.Fatal("picked a policy for a provider of keys")
	}
	if err := SetGrowthPolicy("My relay", ""); err == nil {
		t.Fatal("cleared a pick asked by the provider's name")
	}
	if err := SetAccountGrowthPolicy("relay", "Old@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if us := GrowthUses(settings.GrowthCautious, now); !slices.ContainsFunc(us, func(u GrowthUse) bool { return u.Provider == "relay" && u.Picked && len(u.Gone) == 0 }) {
		t.Fatalf("after its account's clear: %+v", us)
	}
	if err := SetGrowthPolicy("relay", ""); err != nil {
		t.Fatal(err)
	}
	if picks, err := GrowthPicks(); err != nil || len(picks) != 0 {
		t.Fatalf("picks after clearing each: %v %v", picks, err)
	}
	if us := GrowthUses(settings.GrowthCautious, now); len(us) != 0 {
		t.Fatalf("Cautious's In use after clearing each: %+v", us)
	}
	if err := SetGrowthPolicy("relay", ""); err == nil {
		t.Fatal("cleared the relay's pick twice")
	}
	if p, err := Find("relay"); err != nil || p.Key != "sk-test" || p.Chat != "https://relay.example.com/v1" {
		t.Fatalf("the clear changed the relay: %v %v", p, err)
	}
}
