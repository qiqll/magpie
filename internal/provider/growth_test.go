package provider

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// noGrowth saves a growth policy of no stages in the test's home, for a
// test of switching that signs in accounts one after another, each new to
// magpie and so held by the default policy at 50% of its five hours: its
// premise is an account used to its vendor's 100%, or to a cap it sets.
func noGrowth(t *testing.T) {
	t.Helper()
	s := settings.Load()
	s.Growth = &[]settings.GrowthStage{}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// stagesBefore makes the stages a user who set none was held by before
// policies were kept (settings.GrowthStagesBefore) the default policy, for
// a test of how an account's days are counted, whose numbers are those
// stages' (T-129 made Standard the default).
func stagesBefore(t *testing.T) {
	t.Helper()
	err := settings.EditGrowth(func([]settings.GrowthPolicy, string) ([]settings.GrowthPolicy, string, error) {
		return []settings.GrowthPolicy{{ID: "before", Name: "Before", Kind: settings.GrowthStaged, Stages: settings.GrowthStagesBefore()}}, "before", nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// addedAgo moves when the account user of id was added to ago before now,
// as days going by would.
func addedAgo(t *testing.T, id, user string, ago time.Duration) {
	t.Helper()
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i].AccountAdded[accountKey(user)] = time.Now().Add(-ago).UTC()
			if err := store(f); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no %s in providers.json", id)
}

// growthReading is a reading of an account's five hours at used, renewing in
// three hours, and its week at week.
func growthReading(used, week float64) SubscriptionQuota {
	five, seven := time.Now().Add(3*time.Hour), time.Now().Add(4*24*time.Hour)
	return SubscriptionQuota{Windows: []QuotaWindow{
		{Name: "5 hours", Used: used, ResetsAt: &five, Span: 5 * time.Hour},
		{Name: "Weekly", Used: week, ResetsAt: &seven, Span: 7 * 24 * time.Hour},
	}}
}

// An account added to magpie is held by the growth policy by its days: on
// its second day the five hours at 50%, on its fourth the week at 80% and
// the five hours still at 50%, on its eighth the five hours at 90% and the
// week still at 80% (T-128: a window a later stage leaves out keeps its
// share). The account magpie found as it started keeping accounts has
// no day and is never held.
func TestGrowthHoldsANewAccountByItsDays(t *testing.T) {
	home := signIn(t) // me@example.com on codex, the first write
	stagesBefore(t)
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
	if at := accountAdded("codex", "new@example.com"); time.Since(at) > time.Minute {
		t.Fatalf("a new account's day kept as %v", at)
	}
	if at := accountAdded("codex", "me@example.com"); !at.IsZero() {
		t.Fatalf("the account from before given a day: %v", at)
	}
	if c := AccountCapsOf("codex", "me@example.com"); c.Holds() {
		t.Fatalf("the account from before held: %+v", c)
	}
	now := time.Now()

	addedAgo(t, "codex", "new@example.com", 36*time.Hour) // its second day
	c := AccountCapsOf("codex", "new@example.com")
	if s, _ := c.Of("5 hours"); s != 50 {
		t.Fatalf("the second day's five hours at %d%%", s)
	}
	if s, _ := c.Of("Weekly"); s != 0 {
		t.Fatalf("the second day's week held at %d%%", s)
	}
	if !capReached(growthReading(50, 10), c, now) {
		t.Fatal("at 50% of its five hours on its second day, not held")
	}
	if capReached(growthReading(45, 10), c, now) {
		t.Fatal("held below 50% on its second day")
	}

	addedAgo(t, "codex", "new@example.com", 4*24*time.Hour) // its fourth
	c = AccountCapsOf("codex", "new@example.com")
	for _, w := range []string{"Weekly", "7 days", "7 days · Opus", "7 days · Sonnet", "7 days · Fable"} {
		if s, _ := c.Of(w); s != 80 {
			t.Errorf("the fourth day's %q at %d%%", w, s)
		}
	}
	if s, _ := c.Of("5 hours"); s != 50 {
		t.Fatalf("the fourth day's five hours at %d%%: let go by the week's stage", s)
	}

	addedAgo(t, "codex", "new@example.com", 7*24*time.Hour+12*time.Hour) // its eighth
	c = AccountCapsOf("codex", "new@example.com")
	if s, _ := c.Of("Weekly"); s != 80 {
		t.Fatalf("the eighth day's week at %d%%: let go by the five hours' stage", s)
	}
	if !capReached(growthReading(80, 85), c, now) {
		t.Fatal("at 85% of its week on its eighth day, not held")
	}
	if capReached(growthReading(80, 75), c, now) {
		t.Fatal("held below 80% of its week and 90% of its five hours on its eighth day")
	}
	if s, _ := c.Of("5 hours"); s != 90 {
		t.Fatalf("the eighth day's five hours at %d%%", s)
	}

	addedAgo(t, "codex", "new@example.com", 30*24*time.Hour)
	c = AccountCapsOf("codex", "new@example.com")
	if s, _ := c.Of("5 hours"); s != 95 {
		t.Fatalf("after 14 days the five hours at %d%%", s)
	}
	if s, _ := c.Of("Weekly"); s != 80 {
		t.Fatalf("after 14 days the week at %d%%", s)
	}
}

// What the user set on an account comes first: a window's own share (100,
// none on it, among them) is kept, and an account's cap holds every
// window at it; the policy fills in only the windows left alone.
func TestGrowthLeavesTheUsersCaps(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
	addedAgo(t, "codex", "new@example.com", 36*time.Hour)
	if err := SetWindowCap("codex", "new@example.com", "5 hours", 100); err != nil {
		t.Fatal(err)
	}
	c := AccountCapsOf("codex", "new@example.com")
	if s, _ := c.Of("5 hours"); s != 0 {
		t.Fatalf("no cap on the five hours, set by the user, read as %d%%", s)
	}
	if capReached(growthReading(70, 10), c, time.Now()) {
		t.Fatal("held at 70% of a window the user set no cap on")
	}
	if err := SetWindowCap("codex", "new@example.com", "5 hours", 70); err != nil {
		t.Fatal(err)
	}
	if s, _ := AccountCapsOf("codex", "new@example.com").Of("5 hours"); s != 70 {
		t.Fatalf("the user's 70%% read as %d%%", s)
	}
	if err := SetWindowCap("codex", "new@example.com", "5 hours", 0); err != nil {
		t.Fatal(err)
	}
	addedAgo(t, "codex", "new@example.com", 4*24*time.Hour)
	if err := SetAccountCap("codex", "new@example.com", 90); err != nil {
		t.Fatal(err)
	}
	c = AccountCapsOf("codex", "new@example.com")
	if s, _ := c.Of("Weekly"); s != 90 {
		t.Fatalf("the account's 90%% cap read on the week as %d%%", s)
	}
	// and the provider's own read (HoldCaps, the gateway's) says the same
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := p.CapsOf("new@example.com").Of("Weekly"); s != 90 {
		t.Fatalf("CapsOf read the week as %d%%", s)
	}
	// the editor's Save keeps when the accounts were added
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if at := accountAdded("codex", "new@example.com"); at.IsZero() {
		t.Fatal("the editor's Save dropped the account's day")
	}
}

// The policy is the user's once set: their stages hold as they say, and
// none saved is no hold at all.
func TestGrowthStagesSet(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
	addedAgo(t, "codex", "new@example.com", 36*time.Hour)
	s := settings.Load()
	s.Growth = &[]settings.GrowthStage{{From: 1, Window: settings.GrowthWeek, Cap: 60}, {From: 1, Window: settings.GrowthFiveHours, Cap: 40}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	c := AccountCapsOf("codex", "new@example.com")
	if a, _ := c.Of("5 hours"); a != 40 {
		t.Fatalf("five hours %d%%", a)
	}
	if a, _ := c.Of("weekly"); a != 60 {
		t.Fatalf("week %d%%", a)
	}
	s.Growth = &[]settings.GrowthStage{}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if c := AccountCapsOf("codex", "new@example.com"); c.Holds() {
		t.Fatalf("no stages, held: %+v", c)
	}
	s.Growth = &[]settings.GrowthStage{{From: 0, Window: "1h", Cap: 50}}
	if err := settings.Save(s); err == nil {
		t.Fatal("a window the policy has no such took")
	}
}

// Codex signed in to a new account on its second day, at 55% of its five
// hours: the policy holds it there, and Codex is signed in to the other.
func TestGrowthSwitchesCodexOnItsSecondDay(t *testing.T) {
	home := signIn(t) // me@example.com, acct-1
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new") // signed in to now
	rememberLogins(true)
	addedAgo(t, "codex", "new@example.com", 36*time.Hour)
	used := map[string]float64{"acct-new@example.com": 55, "acct-1": 20}
	reset := time.Now().Add(3 * time.Hour).Unix()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000, "reset_at": reset}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	to, err := SwitchWhenSpent(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if to != "me@example.com" {
		t.Fatalf("at 55%% on its second day, switched to %q", to)
	}
}

// What a write of logins.json adds: an account had by neither its id nor
// its name; one renamed keeps its day; one removed and added again is new.
func TestAddedIn(t *testing.T) {
	signIn(t) // a home of its own: a plugin's provider id is read from it
	a := savedLogin{ID: "a1", Agent: "codex", User: "a@example.com", Auth: json.RawMessage(`{"tokens":{}}`)}
	b := savedLogin{ID: "b1", Agent: "plugin:acme", User: "B"}
	added, renamed := addedIn([]savedLogin{a}, []savedLogin{a, b})
	if len(added) != 1 || added[0] != (loginAccount{labelProvider("plugin:acme"), "b"}) || renamed != nil {
		t.Fatalf("added %v renamed %v", added, renamed)
	}
	moved := a
	moved.User = "A@example.com (Team)"
	added, renamed = addedIn([]savedLogin{a}, []savedLogin{moved})
	if len(added) != 0 || renamed[loginAccount{"codex", "a@example.com"}] != (loginAccount{"codex", "a@example.com (team)"}) {
		t.Fatalf("renamed: added %v renamed %v", added, renamed)
	}
	if added, _ = addedIn([]savedLogin{b}, []savedLogin{a}); len(added) != 1 {
		t.Fatalf("added again after it was removed: %v", added)
	} // the agent's own row written over with another account the agent
	// signed in to (keepSideLogins): that one is new, not a rename
	own := savedLogin{ID: "c1", Agent: "copilot", User: "octocat"}
	other := own
	other.User = "hubot"
	added, renamed = addedIn([]savedLogin{own}, []savedLogin{other})
	if len(added) != 1 || added[0] != (loginAccount{"copilot", "hubot"}) || renamed != nil {
		t.Fatalf("another account on the own row: added %v renamed %v", added, renamed)
	}
}

// Each window is held at the share of the latest started stage that lists
// it: a window a later stage leaves out keeps its share (T-128), checked
// day by day through the default's stages. None started, or a day not
// known, holds nothing.
func TestGrowthCaps(t *testing.T) {
	now := time.Now()
	stages := settings.GrowthStagesBefore()
	at := func(days float64) map[string]int {
		return growthCaps(stages, now.Add(-time.Duration(days*24*float64(time.Hour))), now)
	}
	for _, c := range []struct {
		days       float64
		five, week int
	}{{0.5, 50, 0}, {2.9, 50, 0}, {3, 50, 80}, {6.9, 50, 80}, {7.5, 90, 80}, {13, 90, 80}, {14, 95, 80}, {40, 95, 80}} {
		g := at(c.days)
		if g["5 hours"] != c.five || g["weekly"] != c.week || g["7 days · opus"] != c.week {
			t.Errorf("day %v: %v", c.days, g)
		}
	}
	// every day from 0 to 20, each window on its own
	for d := 0; d <= 20; d++ {
		five, week := 50, 0
		switch {
		case d >= 14:
			five = 95
		case d >= 7:
			five = 90
		}
		if d >= 3 {
			week = 80
		}
		g := at(float64(d) + 0.5)
		for _, w := range growthWindows[settings.GrowthFiveHours] {
			if g[w] != five {
				t.Errorf("day %d: %q at %d%%, not %d%%", d, w, g[w], five)
			}
		}
		for _, w := range growthWindows[settings.GrowthWeek] {
			if g[w] != week {
				t.Errorf("day %d: %q at %d%%, not %d%%", d, w, g[w], week)
			}
		}
	}
	// a user's own stages carry the same way: the week set on day 2 is
	// kept past the 5 hours' stage on day 5, and changed on day 9
	own := []settings.GrowthStage{
		{From: 0, Window: settings.GrowthFiveHours, Cap: 30},
		{From: 2, Window: settings.GrowthWeek, Cap: 60},
		{From: 5, Window: settings.GrowthFiveHours, Cap: 70},
		{From: 9, Window: settings.GrowthWeek, Cap: 90},
	}
	for _, c := range []struct {
		days       float64
		five, week int
	}{{1, 30, 0}, {2, 30, 60}, {5.5, 70, 60}, {9, 70, 90}, {100, 70, 90}} {
		g := growthCaps(own, now.Add(-time.Duration(c.days*24*float64(time.Hour))), now)
		if g["5 hours"] != c.five || g["7 days"] != c.week {
			t.Errorf("own stages, day %v: %v", c.days, g)
		}
	}
	if g := growthCaps(stages, time.Time{}, now); g != nil {
		t.Errorf("no day known: %v", g)
	}
	if g := growthCaps([]settings.GrowthStage{{From: 2, Window: "5h", Cap: 50}}, now.Add(-time.Hour), now); g != nil {
		t.Errorf("before the first stage: %v", g)
	}
}

// A plugin's account (the subscriptions moved onto plugins among them) is
// stamped as it first appears: pluginLogins writes logins.json with its
// row, and the account is held by the policy like a built-in's. One the
// plugin had when magpie first wrote logins.json is from before, and isn't.
func TestGrowthStampsAPluginAccount(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r", "access": "a", "expires": 9e15, "accountId": "old-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	id := PluginID("fakeco")
	pluginAccounts() // logins.json written for the first time: from before
	added := func() map[string]time.Time {
		t.Helper()
		s, _ := storedPicks(id)
		return s.AccountAdded
	}
	if a := added(); len(a) != 0 {
		t.Fatalf("the account from before stamped: %v", a)
	}
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r2", "access": "a2", "expires": 9e15, "accountId": "new-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pluginAccounts() // the new one's row written
	a := added()
	if len(a) != 1 {
		t.Fatalf("one new plugin account, stamped: %v", a)
	}
	for user, at := range a {
		if time.Since(at) > time.Minute {
			t.Fatalf("%s stamped at %v", user, at)
		}
		if s, _ := AccountCapsOf(id, user).Of("5 hours"); s != 50 {
			t.Fatalf("the new plugin account's five hours at %d%% on its first day", s)
		}
	}
}

// An account's day stays while it is gone from logins.json and back, as a
// plugin's is when its sign-ins aren't listed a while: only the user's
// removal lets it go, and the account signed in to again is new again.
func TestGrowthKeepsADayUntilRemoved(t *testing.T) {
	home := signIn(t) // me@example.com on codex, the first write
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
	addedAgo(t, "codex", "new@example.com", 4*24*time.Hour)
	was := accountAdded("codex", "new@example.com")
	loginsMu.Lock()
	all := readLogins()
	gone := slices.DeleteFunc(slices.Clone(all), func(l savedLogin) bool { return l.User == "new@example.com" })
	if len(gone) == len(all) {
		loginsMu.Unlock()
		t.Fatal("no row for new@example.com")
	}
	if err := writeLogins(gone); err != nil {
		loginsMu.Unlock()
		t.Fatal(err)
	}
	if err := writeLogins(all); err != nil {
		loginsMu.Unlock()
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if at := accountAdded("codex", "new@example.com"); !at.Equal(was) {
		t.Fatalf("back in the list, its day went from %v to %v", was, at)
	}
	if err := ForgetLogin("codex", "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if at := accountAdded("codex", "new@example.com"); !at.IsZero() {
		t.Fatalf("removed by the user, its day kept: %v", at)
	}
	codexSignIn(t, home, "new@example.com", "r-new2")
	rememberLogins(true)
	if at := accountAdded("codex", "new@example.com"); time.Since(at) > time.Minute {
		t.Fatalf("signed in to again after its removal, its day is %v", at)
	}
}

// The agent's own account removed in magpie is only hidden; shown again
// as the agent signs in to it anew, it is added.
func TestAddedInShownAgain(t *testing.T) {
	signIn(t)
	own := savedLogin{ID: "c1", Agent: "kimi", User: "me", Hidden: hiddenNoMark}
	shown := own
	shown.Hidden = ""
	if added, _ := addedIn([]savedLogin{own}, []savedLogin{shown}); len(added) != 1 || added[0] != (loginAccount{"kimi", "me"}) {
		t.Fatalf("shown again: added %v", added)
	}
	if added, _ := addedIn([]savedLogin{shown}, []savedLogin{own}); len(added) != 0 {
		t.Fatalf("hidden: added %v", added)
	}
}

// A plugin's account that falls out of its list (its sign-in not read a
// while) keeps its day when it is back; removed by the user it goes.
func TestGrowthKeepsAPluginAccountsDay(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	stagesBefore(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r", "access": "a", "expires": 9e15, "accountId": "old-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	id := PluginID("fakeco")
	pluginAccounts() // from before
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r2", "access": "a2", "expires": 9e15, "accountId": "new-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pluginAccounts()
	s, _ := storedPicks(id)
	if len(s.AccountAdded) != 1 {
		t.Fatalf("one new plugin account, stamped: %v", s.AccountAdded)
	}
	var user string
	for u := range s.AccountAdded {
		user = u
	}
	addedAgo(t, id, user, 4*24*time.Hour)
	was := accountAdded(id, user)
	pp, ok := PluginOf(id)
	if !ok {
		t.Fatal("no fakeco plugin")
	}
	// its sign-in not listed: its row leaves logins.json
	short := pp
	short.Accounts = slices.DeleteFunc(slices.Clone(pp.Accounts), func(a plugin.Account) bool {
		return accountKey(PluginAccountUser(pp.ID, a.Key)) == user
	})
	if len(short.Accounts) == len(pp.Accounts) {
		t.Fatalf("no plugin account named %q in %+v", user, pp.Accounts)
	}
	pluginLogins(short)
	if slices.ContainsFunc(sharedLogins(), func(l savedLogin) bool { return accountKey(l.User) == user }) {
		t.Fatal("the account not listed kept its row")
	}
	pluginLogins(pp) // listed again
	if at := accountAdded(id, user); !at.Equal(was) {
		t.Fatalf("back in the plugin's list, its day went from %v to %v", was, at)
	}
	if s, _ := AccountCapsOf(id, user).Of("Weekly"); s != 80 {
		t.Fatalf("back on its fourth day, the week at %d%%", s)
	}
	if err := ForgetLogin(pluginAgent(pp), user); err != nil {
		t.Fatal(err)
	}
	if at := accountAdded(id, user); !at.IsZero() {
		t.Fatalf("removed by the user, its day kept: %v", at)
	}
}

// An account from before magpie kept when each was added is never held:
// gone from the list a while and back, it is marked from before, not
// taken for new. Removed by the user and signed in to again, it is new.
func TestGrowthFromBeforeBackNotHeld(t *testing.T) {
	home := signIn(t) // me@example.com on codex, the first write: from before
	rememberLogins(true)
	if at := accountAdded("codex", "me@example.com"); !at.IsZero() {
		t.Fatalf("the account from before has a day: %v", at)
	}
	loginsMu.Lock()
	all := readLogins()
	gone := slices.DeleteFunc(slices.Clone(all), func(l savedLogin) bool { return l.User == "me@example.com" })
	if len(gone) == len(all) {
		loginsMu.Unlock()
		t.Fatal("no row for me@example.com")
	}
	if err := writeLogins(gone); err != nil {
		loginsMu.Unlock()
		t.Fatal(err)
	}
	if err := writeLogins(all); err != nil {
		loginsMu.Unlock()
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if at := accountAdded("codex", "me@example.com"); !at.IsZero() {
		t.Fatalf("back in the list, the account from before was given a day: %v", at)
	}
	if s, _ := AccountCapsOf("codex", "me@example.com").Of("5 hours"); s != 0 {
		t.Fatalf("back in the list, the account from before is held at %d%% of its five hours", s)
	}
	if s, _ := storedPicks("codex"); !s.AccountAdded["me@example.com"].Equal(addedBefore) {
		t.Fatalf("the account from before isn't marked so: %v", s.AccountAdded)
	}
	codexSignIn(t, home, "new@example.com", "r-new")
	rememberLogins(true)
	if err := ForgetLogin("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks("codex"); slices.Contains(slices.Collect(maps.Keys(s.AccountAdded)), "me@example.com") {
		t.Fatalf("removed by the user, the mark kept: %v", s.AccountAdded)
	}
	codexSignIn(t, home, "me@example.com", "r-me2")
	rememberLogins(true)
	if at := accountAdded("codex", "me@example.com"); time.Since(at) > time.Minute {
		t.Fatalf("signed in to again after its removal, its day is %v", at)
	}
}

// A plugin removed and installed again keeps its accounts' days: one
// added lately is held by its day, not its first, and one from before is
// held by none, its sign-ins not listed a while as the host starts or not.
func TestGrowthPluginInstalledAgainKeepsDays(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	stagesBefore(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	e, err := plugin.Add(ctx, abs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r", "access": "a", "expires": 9e15, "accountId": "old-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	id := PluginID("fakeco")
	pluginAccounts() // from before
	pp, ok := PluginOf(id)
	if !ok || len(pp.Accounts) != 1 {
		t.Fatalf("fakeco's account from before: %+v", pp.Accounts)
	}
	old := accountKey(PluginAccountUser(pp.ID, pp.Accounts[0].Key))
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r2", "access": "a2", "expires": 9e15, "accountId": "new-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pluginAccounts()
	s, _ := storedPicks(id)
	if len(s.AccountAdded) != 1 {
		t.Fatalf("one new plugin account, stamped: %v", s.AccountAdded)
	}
	var user string
	for u := range s.AccountAdded {
		user = u
	}
	addedAgo(t, id, user, 4*24*time.Hour)
	was := accountAdded(id, user)

	if err := RemovePlugin(ctx, e.Spec); err != nil {
		t.Fatal(err)
	}
	if _, err := AddPlugin(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pp, ok = PluginOf(id)
	if !ok {
		t.Fatal("no fakeco plugin once installed again")
	}
	// installed again, its sign-ins not listed yet, then listed
	none := pp
	none.Accounts = nil
	pluginLogins(none)
	pluginLogins(pp)
	pluginAccounts()
	if at := accountAdded(id, user); !at.Equal(was) {
		t.Fatalf("installed again, its day went from %v to %v", was, at)
	}
	if s, _ := AccountCapsOf(id, user).Of("Weekly"); s != 80 {
		t.Fatalf("installed again on its fourth day, the week at %d%%", s)
	}
	if at := accountAdded(id, old); !at.IsZero() {
		t.Fatalf("installed again, the account from before was given a day: %v", at)
	}
	if s, _ := AccountCapsOf(id, old).Of("5 hours"); s != 0 {
		t.Fatalf("installed again, the account from before is held at %d%% of its five hours", s)
	}
	// every account signed out by the user (the editor's Remove account):
	// each signed in to again is new
	if err := ForgetAccount(id); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks(id); len(s.AccountAdded) != 0 {
		t.Fatalf("signed out by the user, the days kept: %v", s.AccountAdded)
	}
}

// growthPlugin installs the fake plugin with an account from before and
// one added four days ago: its provider's id, the new account and the one
// from before.
func growthPlugin(t *testing.T, ctx context.Context) (id, added, before string) {
	t.Helper()
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := AddPlugin(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r", "access": "a", "expires": 9e15, "accountId": "old-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	id = PluginID("fakeco")
	pluginAccounts() // from before
	pp, ok := PluginOf(id)
	if !ok || len(pp.Accounts) != 1 {
		t.Fatalf("fakeco's account from before: %+v", pp.Accounts)
	}
	before = accountKey(PluginAccountUser(pp.ID, pp.Accounts[0].Key))
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r2", "access": "a2", "expires": 9e15, "accountId": "new-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pluginAccounts()
	s, _ := storedPicks(id)
	if len(s.AccountAdded) != 1 {
		t.Fatalf("one new plugin account, stamped: %v", s.AccountAdded)
	}
	for u := range s.AccountAdded {
		added = u
	}
	addedAgo(t, id, added, 4*24*time.Hour)
	return id, added, before
}

// Removing a plugin's accounts while its sign-ins aren't listed (its host
// just started) signs out every one, listed or not, and each one's day
// goes with it, the mark of one from before too.
func TestGrowthForgetUnlistedPluginAccounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	id, _, _ := growthPlugin(t, ctx)
	pp, ok := PluginOf(id)
	if !ok {
		t.Fatal("no fakeco plugin")
	}
	none := pp
	none.Accounts = nil
	if err := forgetPluginAccounts(none); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks(id); len(s.AccountAdded) != 0 {
		t.Fatalf("signed out by the user while not listed, the days kept: %v", s.AccountAdded)
	}
	if f, err := read(); err != nil || slices.ContainsFunc(f.Providers, func(p Provider) bool { return p.ID == id }) {
		t.Fatalf("the record kept for the days alone outlived them: %v", err)
	}
}

// Removing a plugin's listed accounts drops each one's day, and leaves no
// mark of one from before, which its row leaving would write.
func TestGrowthForgetListedPluginAccounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	id, _, _ := growthPlugin(t, ctx)
	if err := ForgetAccount(id); err != nil {
		t.Fatal(err)
	}
	if s, _ := storedPicks(id); len(s.AccountAdded) != 0 {
		t.Fatalf("signed out by the user, the days kept: %v", s.AccountAdded)
	}
}

// `magpie plugin logout` signs every sign-in of the plugin out: their rows
// leave logins.json with their days, so one signed in to again is new.
func TestGrowthSignedOutOfPlugin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	id, added, _ := growthPlugin(t, ctx)
	pp, ok := PluginOf(id)
	if !ok {
		t.Fatal("no fakeco plugin")
	}
	if err := plugin.SignOut(ctx, pp.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := SignedOutOfPlugin(pp); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(sharedLogins(), func(l savedLogin) bool { return l.Agent == pluginAgent(pp) }) {
		t.Fatal("signed out, the plugin's rows kept")
	}
	if s, _ := storedPicks(id); len(s.AccountAdded) != 0 {
		t.Fatalf("signed out, the days kept: %v", s.AccountAdded)
	}
	if _, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r3", "access": "a3", "expires": 9e15, "accountId": "new-acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pluginAccounts()
	if at := accountAdded(id, added); time.Since(at) > time.Minute {
		t.Fatalf("signed in to again after the sign-out, its day is %v", at)
	}
}
