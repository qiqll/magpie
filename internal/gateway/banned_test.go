package gateway

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// banSaved marks the codex account user banned, or lifts the mark, as the
// Accounts page does.
func banSaved(t *testing.T, user string, banned bool) {
	t.Helper()
	if err := provider.SetLoginBanned("codex", user, banned); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
}

// An account the user marked banned is never asked, though the one left
// is out of its allowance: unlike a lapsed one it isn't tried as a last
// resort. Lifted, it is asked again.
func TestBannedAccountNeverAsked(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, map[string]int{"acct-1": http.StatusTooManyRequests})
	banSaved(t, "spare@example.com", true)
	s := New()
	codexPostTo(s, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if got := asked(true); slices.Contains(got, "acct-2") {
		t.Fatalf("asked %v: the banned account was asked", got)
	}
	// left out by routing itself, not only refused as it is signed
	if r := lastRoute(s); slices.ContainsFunc(append(r.Order, r.Left...), func(w Weighed) bool { return w.Who == "spare@example.com" }) {
		t.Fatalf("the banned account is planned: %+v %+v", r.Order, r.Left)
	}
	banSaved(t, "spare@example.com", false)
	codexPostTo(New(), `{"model":"gpt-5.5","stream":true,"input":"again"}`)
	if got := asked(true); !slices.Contains(got, "acct-2") {
		t.Fatalf("lifted, not asked: %v", got)
	}
}

// With every account banned, the one Codex is signed in to too, nobody
// is asked and the request fails: not routed, and not relayed as it
// came on Codex's own sign-in either.
func TestEveryAccountBannedAsksNobody(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	asked, _ := usersUpstream(t, nil)
	banSaved(t, "spare@example.com", true)
	banSaved(t, "me@example.com", true)
	rec := codexPostTo(New(), `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if got := asked(true); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
	if rec.Code == 200 {
		t.Fatalf("served %s", rec.Body.String())
	}
}

// A compaction moved off Codex's paused sign-in, and a web search on a
// ChatGPT account, leave a banned account out as routing does.
func TestBannedAccountNotCompactedOnOrSearchedWith(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	usersUpstream(t, nil)
	banSaved(t, "spare@example.com", true)
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", CodexPath+"/responses/compact", nil)
	if on := compactOn(r, "gpt-5.5"); on == nil || on.Account.User != "third@example.com" {
		t.Fatalf("compacted on %+v", on)
	}
	ons, _ := chatGPTSearchers(httptest.NewRequest("POST", "/v1/responses", nil), "codex/gpt-5.5")
	for _, o := range ons {
		if o.Account.User == "spare@example.com" {
			t.Fatal("searches with the banned account")
		}
	}
}

// Routing over the codex subscription's accounts (codex/<model>) leaves
// out the one Codex is signed in to once it is marked banned, the spares
// on kept: never a candidate, with or without others.
func TestBannedSignedInAccountNotACandidate(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	banSaved(t, "me@example.com", true)
	p, _, ok := provider.Resolve("codex/gpt-5.5")
	if !ok || p.Account == nil {
		t.Fatal("no codex provider")
	}
	var users []string
	for _, c := range accountsOn(p, "gpt-5.5") {
		users = append(users, c.p.Account.User)
	}
	if !slices.Equal(users, []string{"spare@example.com"}) {
		t.Fatalf("candidates %v", users)
	}
}
