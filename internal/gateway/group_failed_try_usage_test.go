package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Bound by user, a group's turn whose first member's try billed before
// it failed, and whose next member has none of the user's own (groupTurn
// ran out), has that try's tokens in the ledger once: the request's own
// row is that try's, so no handed-on row is written beside it
// (failedRow). Before, the merge of d2a83de6 wrote both, and the 90K
// cache write counted twice.
func TestUserGroupRanOutCountsTheBilledTryOnce(t *testing.T) {
	start := `    echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"claude-sonnet-5","usage":{"input_tokens":3,"cache_creation_input_tokens":90000}}}}'
`
	failed := `    echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"result":"API Error: 529 overloaded"}'
`
	claudeSubscriptionFirstSays(t, start+failed)
	f := &chatFleet{status: map[string]int{}}
	b := f.serve(t)
	if err := provider.Save(provider.Provider{ID: "cb", Name: "CB", Chat: b.URL + "/v1", Key: "kd", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"claude/claude-sonnet-5", "cb/m"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	// alice on the account whose run fails, cb's one key bob's alone
	writeTiers(t, `{"default":"free","tiers":{"free":{"credits":1000,"max_accounts":1,"share":1}}}`)
	writeUsers(t, userFile{Users: map[string]map[string]userBinding{
		"claude": {"alice": {Who: "claude@me@example.com", Account: "me@example.com", At: time.Now().UTC()}},
		"cb":     {"bob": {Who: keyWho("cb", "kd"), Account: "kd", At: time.Now().UTC()}},
	}})
	s := byUser(New())
	body, _ := json.Marshal(map[string]any{"model": "group/pair", "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "ping"}}})
	rec := userAt(s, "/v1/messages", "", body, "alice")
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("answered: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("bob's key asked for alice: %q", got)
	}
	route := lastRoute(s).ID
	var rows []usage.Record
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows = rows[:0]
		for _, u := range usage.Load(time.Time{}) {
			if u.RouteID == route {
				rows = append(rows, u)
			}
		}
		if len(rows) >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// a late handed-on row would land after the request's
	time.Sleep(100 * time.Millisecond)
	rows = rows[:0]
	for _, u := range usage.Load(time.Time{}) {
		if u.RouteID == route {
			rows = append(rows, u)
		}
	}
	cache := 0
	for _, u := range rows {
		cache += u.CacheWrite
	}
	if len(rows) != 1 || cache != 90000 || rows[0].ProviderAccount != "me@example.com" {
		t.Fatalf("rows %d, cache write %d: %+v", len(rows), cache, rows)
	}
}
