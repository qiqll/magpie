package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// main's GLM Coding Plan search (10f1e978) asks the plan's search MCP
// server with each of the provider's plan keys in turn until one answers.
// Bound by user, a search made for a user goes on the user's own key
// alone (usersSearchKey), as a Kimi Code plan's and WorkBuddy's do: when
// it is refused, the provider's other key, another user's, is never tried
// in its place.
func TestGLMSearchOnTheUsersKeyAlone(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		if r.URL.Path != provider.ZhipuSearchPath {
			http.NotFound(w, r)
			return
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		tried = append(tried, key)
		mu.Unlock()
		http.Error(w, `{"error":{"message":"search quota used up"}}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(up.Close)
	glm := provider.Provider{ID: "glm", Name: "GLM Coding Plan", Preset: "zhipu", Key: "sk-glm-a", KeyName: "A",
		Keys: []provider.KeyAccount{{Name: "B", Key: "sk-glm-b"}}, Chat: up.URL + "/api/coding/paas/v4", Models: []string{"glm-4.6"}}
	if err := provider.Save(glm); err != nil {
		t.Fatal(err)
	}
	if !glmSearches(glm) {
		t.Fatal("the plan doesn't search")
	}
	keys, _ := newCaller(t, "Full")
	now := time.Now().UTC().Truncate(time.Second)
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"glm": {
			"alice": {Who: "glm#" + provider.KeyID("sk-glm-b"), Account: "B", At: now},
			// carol on the provider's first key: usersSearchKey answers
			// the provider whole, every key still in it
			"carol": {Who: "glm#" + provider.KeyID("sk-glm-a"), Account: "A", At: now},
		}},
	}})
	s := byUserUpTo(t, New(), 1)
	for _, c := range []struct{ user, own, other string }{{"alice", "sk-glm-b", "sk-glm-a"}, {"carol", "sk-glm-a", "sk-glm-b"}} {
		mu.Lock()
		tried = nil
		mu.Unlock()
		ctx := context.WithValue(context.Background(), askedForKey{}, askedFor{key: keys[0].ID, user: c.user})
		ctx = context.WithValue(ctx, searchModelKey{}, "glm-4.6")
		p, err := provider.Find("glm")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.ownSearch(ctx, *p, "latest go"); err == nil {
			t.Fatalf("%s: searched, want the user's key's refusal", c.user)
		}
		mu.Lock()
		if len(tried) == 0 || tried[0] != c.own || slices.Contains(tried, c.other) {
			t.Errorf("%s: keys asked %q, want %s alone", c.user, tried, c.own)
		}
		mu.Unlock()
	}
}

// main's plugin search that signs in as its provider (083821dc) walks
// every account of the provider that is on. Bound by user, a search made
// for a user takes the accounts the user is on alone, as its group shares
// them; one for no user, or for a user on none, is refused, never sent
// on another user's account.
func TestPluginSearchSignedInOnTheUsersAccounts(t *testing.T) {
	fresh(t)
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "plugin:seekco", Name: "Seek", Account: &provider.Account{Agent: "plugin:seekco", User: user}}}
	}
	cs := []candidate{acct("a@seek"), acct("b@seek"), acct("c@seek")}
	keys, _ := newCaller(t, "Full")
	now := time.Now().UTC().Truncate(time.Second)
	writeUsers(t, userFile{Keys: map[string]map[string]map[string]userBinding{
		keys[0].ID: {"plugin:seekco": {
			"alice": {Who: cs[1].who(), Account: "b@seek", At: now, More: []userSeat{{Who: cs[2].who(), Account: "c@seek", At: now}}},
			"bob":   {Who: cs[0].who(), Account: "a@seek", At: now},
		}},
	}})
	users := func(cs []candidate) (out []string) {
		for _, c := range cs {
			out = append(out, c.p.Account.User)
		}
		return out
	}
	for _, c := range []struct {
		user string
		want []string
	}{{"alice", []string{"b@seek", "c@seek"}}, {"bob", []string{"a@seek"}}, {"carol", nil}, {"", nil}} {
		ctx := context.Background()
		if c.user != "" {
			ctx = context.WithValue(ctx, askedForKey{}, askedFor{key: keys[0].ID, user: c.user})
		}
		got, err := usersSearchAccounts(ctx, "Seek", cs)
		if c.want == nil {
			if err == nil {
				t.Errorf("%q: searched on %v, want refused", c.user, users(got))
			}
			continue
		}
		if err != nil || !slices.Equal(users(got), c.want) {
			t.Errorf("%q: accounts %v %v, want %v", c.user, users(got), err, c.want)
		}
	}
}
