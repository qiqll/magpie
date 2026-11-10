package provider

import (
	"os"
	"strings"
	"testing"
)

// A setter of one setting changes it on the record as it is when it
// writes, so another setting set between its read of the provider and its
// write stays.

// Two settings of one account set at once: its name and its weight.
func TestProvidersAccountNameAndWeightSetAtOnce(t *testing.T) {
	providersLockHome(t)
	interleave(t, func() {
		if err := SetAccountName("codex", "new@example.com", "Work"); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := SetAccountWeight("codex", "new@example.com", 3); err != nil {
			t.Error(err)
		}
	})
	s, _ := storedPicks("codex")
	if s.AccountNames["new@example.com"] != "Work" {
		t.Errorf("the name given was lost: %+v", s.AccountNames)
	}
	if s.AccountWeights["new@example.com"] != 3 {
		t.Errorf("the weight set was lost: %+v", s.AccountWeights)
	}
}

// A subscription's limit a minute set while its queue is, and while an
// account's cap is set: all three stay.
func TestProvidersRPMKeepsAQueueAndACapSetMeanwhile(t *testing.T) {
	providersLockHome(t)
	interleave(t, func() {
		if err := SetRPM("codex", 30); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := SetQueue("codex", 5, 60); err != nil {
			t.Error(err)
		}
		if err := SetAccountCap("codex", "me@example.com", 70); err != nil {
			t.Error(err)
		}
	})
	s, _ := storedPicks("codex")
	if s.MaxRPM != 30 {
		t.Errorf("the limit a minute read as %d", s.MaxRPM)
	}
	if s.QueueLimit != 5 || s.QueueWait != 60 {
		t.Errorf("the queue read as %d, %ds", s.QueueLimit, s.QueueWait)
	}
	if c, _ := AccountCapsOf("codex", "me@example.com").Of("5 hours"); c != 70 {
		t.Errorf("the cap set meanwhile read as %d%%", c)
	}
}

// A key's weight set while another key is added: the key added stays, and
// the weight is on the key it was set on.
func TestProvidersKeyWeightKeepsAKeyAddedMeanwhile(t *testing.T) {
	providersLockHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
	interleave(t, func() {
		if err := SetKeyWeight("relay", keyID("sk-personal-1111"), 4); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := AddKey("relay", "Team", "sk-team-2222", ""); err != nil {
			t.Error(err)
		}
	})
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasKey("sk-team-2222") {
		t.Errorf("the key added meanwhile was lost: %+v", p.KeyList())
	}
	if p.Key != "sk-personal-1111" || p.KeyWeight != 4 {
		t.Errorf("the first key's weight read as %d on %q", p.KeyWeight, p.Key)
	}
}

// A key's own limit at once set while the provider's routing is: both stay.
func TestProvidersKeyLimitKeepsRoutingSetMeanwhile(t *testing.T) {
	providersLockHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
	two := 2
	interleave(t, func() {
		if err := SetAccountConcurrency("relay", keyID("sk-personal-1111"), &two); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := SetRouting("relay", Rotate); err != nil {
			t.Error(err)
		}
	})
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Routing != Rotate {
		t.Errorf("the routing set meanwhile read as %q", p.Routing)
	}
	if n, ok := p.AccountConcurrencyOf(keyID("sk-personal-1111")); !ok || n != 2 {
		t.Errorf("the key's limit read as %d (%v)", n, ok)
	}
}

// A setter on a provider removed after it was found doesn't make it again.
func TestProvidersSetterDoesNotBringARemovedProviderBack(t *testing.T) {
	providersLockHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
	interleave(t, func() {
		if err := Delete("relay"); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := SetRPM("relay", 30); err == nil || !strings.Contains(err.Error(), "removed meanwhile") {
			t.Errorf("a limit set on a provider removed meanwhile answered %v", err)
		}
	})
	if stored("relay") {
		t.Error("the provider removed was saved again")
	}
}

// A key added to Vertex AI is refused, as Save refuses it, and not written
// to providers.json (where normalize would drop it unused).
func TestVertexAddKeyRefused(t *testing.T) {
	home := vertexHome(t)
	newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex"}}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("vertex-prod", "", "AIzaSyD-stray-1111", ""); err != errVertexKey {
		t.Errorf("AddKey answered %v", err)
	}
	if _, _, err := AddKeys("vertex-prod", []string{"AIzaSyD-stray-2222"}, ""); err != errVertexKey {
		t.Errorf("AddKeys answered %v", err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "AIzaSyD-stray") {
		t.Errorf("a key was written for Vertex AI: %s", b)
	}
}

// A routing or affinity magpie doesn't know is refused, not written.
func TestUnknownRoutingRefused(t *testing.T) {
	providersLockHome(t)
	if err := SetRouting("codex", "bogus"); err == nil {
		t.Error("an unknown routing was taken")
	}
	if err := SetAffinity("codex", "nonsense"); err == nil {
		t.Error("an unknown affinity was taken")
	}
	if err := SetRouting("codex", ""); err != nil {
		t.Errorf("the default routing was refused: %v", err)
	}
	if s, _ := storedPicks("codex"); s.Routing != "" || s.Affinity != "" {
		t.Errorf("stored routing %q, affinity %q", s.Routing, s.Affinity)
	}
}

// A window taken off meanwhile wasn't there to take off: DropContext says so.
func TestDropContextTakenOffMeanwhile(t *testing.T) {
	providersLockHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111", Models: []string{"m"}, Contexts: map[string]int{"m": 1000}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if dropped, err := DropContext(*p, "m"); err != nil || !dropped {
		t.Fatalf("dropped %v, %v", dropped, err)
	}
	if dropped, err := DropContext(*p, "m"); err != nil || dropped {
		t.Errorf("a window taken off already read as dropped again: %v, %v", dropped, err)
	}
}
