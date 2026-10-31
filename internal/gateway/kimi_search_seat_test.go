package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Bound by user, a Kimi Code plan's search is made on the key its user
// is on already, out of its allowance for hours or not (the service isn't
// the model), and no seat is bound for it: searchBy and ownSearch on
// another's key alike (T-029: searchBy took usersOwn's first, which for a
// key out a while is a spare bound for one search).
func TestKimiSearchKeepsTheUsersSeat(t *testing.T) {
	searchSandbox(t)
	noRests()
	t.Cleanup(noRests)
	var mu sync.Mutex
	var keys []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		keys = append(keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		io.WriteString(w, `{"search_results":[{"site_name":"Go","title":"Go downloads","url":"https://go.dev/dl/","snippet":"go1.27.1 is the latest release.","date":"2026-09-30"}]}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi Code", Preset: "kimi-code-cn", Key: "sk-a", Keys: []provider.KeyAccount{{Key: "sk-b"}},
		Chat: up.URL + "/coding/v1", Models: []string{"kimi-for-coding"}}); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.Searcher = "kimi"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("kimi")
	if err != nil {
		t.Fatal(err)
	}
	// two accounts each: a spare is the user's to have
	s := byUserUpTo(t, New(), 2)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set(UserHeader, "alice")
	ctx := context.WithValue(s.forUser(context.Background(), r), searchForKey{}, &CallFor{Model: "kimi-for-coding"})
	searched := func() string {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(keys) == 0 {
			t.Fatal("nothing searched")
		}
		return keys[len(keys)-1]
	}
	seats := func() []userSeat {
		t.Helper()
		userBinds.Lock()
		defer userBinds.Unlock()
		if err := loadUsers(); err != nil {
			t.Fatal(err)
		}
		return userBinds.m[""]["kimi"]["alice"].seats()
	}
	accounts := perKey(*p, "kimi-for-coding", provider.Anthropic)
	if len(accounts) != 2 {
		t.Fatalf("%d keys, want two: the seat and a spare", len(accounts))
	}
	// on the provider's first key and on the other: the searcher is the
	// provider whole, whose key is the first
	for i, seat := range accounts {
		other := accounts[1-i]
		t.Run(seat.p.Key, func(t *testing.T) {
			noRests()
			now := time.Now().UTC()
			writeUsers(t, userFile{Users: map[string]map[string]userBinding{"kimi": {
				"alice": {Who: seat.who(), Account: seat.p.Key, At: now, Used: now},
			}}})
			// alice's key out of its allowance for hours: a turn would be
			// given the spare, a search keeps to the key
			restSeat(seat.restKey(), 3*time.Hour)
			for _, search := range []func() error{
				func() error { _, _, err := s.searchBy(ctx, *p, "kimi-for-coding", "latest go", ""); return err },
				func() error { _, _, err := s.ownSearch(ctx, other.p, "latest go"); return err },
			} {
				if err := search(); err != nil {
					t.Fatal(err)
				}
				if got := searched(); got != seat.p.Key {
					t.Fatalf("searched with %q, want alice's %q", got, seat.p.Key)
				}
				if got := seats(); len(got) != 1 || !strings.EqualFold(got[0].Who, seat.who()) {
					t.Fatalf("a search bound alice to %+v", got)
				}
			}
		})
	}
}
