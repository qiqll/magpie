package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// ruleKeys is every conversation a group's rules keep a turn for.
func ruleKeys() []string {
	turnRules.Lock()
	defer turnRules.Unlock()
	var ks []string
	for k := range turnRules.m {
		ks = append(ks, k)
	}
	return ks
}

// Two users open a ruled group's conversation with the same words and no
// session header (T-046): alice's turn asks for reasoning, so the group's
// rule sends it to cb; bob's request, a tool's result within a turn of his
// own that asked for none, isn't held to alice's decision: his turn has
// none kept, so it goes by the group's order, to ca. Before, the rules
// kept both turns under one key, and bob's went to cb as alice's had.
func TestUserSameFirstWordsRuledApart(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	f := &chatFleet{status: map[string]int{}}
	chatGroup(t, f)
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"ca/m", "cb/m"}, Routing: "order",
		Rules: []provider.Rule{{Use: "cb/m", Effort: "on"}}}); err != nil {
		t.Fatal(err)
	}
	s := byUserUpTo(t, New(), 1)
	alice := `{"model":"group/pair","reasoning_effort":"high","messages":[{"role":"user","content":"same words T-046"}]}`
	if rec := userAt(s, "/v1/chat/completions", "", []byte(alice), "alice"); rec.Code != 200 {
		t.Fatalf("alice: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.took(); len(got) != 1 || !strings.HasPrefix(got[0], "kd") {
		t.Fatalf("alice's turn asked %q: want cb's, its rule's", got)
	}
	bob := `{"model":"group/pair","messages":[{"role":"user","content":"same words T-046"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"a b"}]}`
	if rec := userAt(s, "/v1/chat/completions", "", []byte(bob), "bob"); rec.Code != 200 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.took(); len(got) != 1 || !strings.HasPrefix(got[0], "kc") {
		t.Fatalf("bob's turn asked %q: want ca's, the group's first, not alice's rule", got)
	}
	ks := ruleKeys()
	if len(ks) != 1 || !strings.Contains(ks[0], "|user:alice|") {
		t.Fatalf("rule keys %q", ks)
	}
}

// ruleKey with no owner is the key as it was, byte for byte; an owner's
// goes between the group and the conversation.
func TestRuleKeyOwner(t *testing.T) {
	g := provider.Group{ID: "pair"}
	h := http.Header{}
	h.Set("session_id", "thread-1")
	req := &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	if got, want := ruleKey(g, "", h, req), provider.GroupPrefix+"pair|thread-1|"+firstWords(req); got != want {
		t.Fatalf("unbound %q, want %q", got, want)
	}
	if got, want := ruleKey(g, stickOwner("a|b"), h, req), provider.GroupPrefix+"pair|user:a%7Cb|thread-1|"+firstWords(req); got != want {
		t.Fatalf("owned %q, want %q", got, want)
	}
}
