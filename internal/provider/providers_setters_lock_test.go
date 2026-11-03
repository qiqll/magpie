package provider

import "testing"

// T-094: a setter of one setting changes it on the record as it is when it
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
// account's day is noted: all three stay.
func TestProvidersRPMKeepsAQueueAndADaySetMeanwhile(t *testing.T) {
	providersLockHome(t)
	interleave(t, func() {
		if err := SetRPM("codex", 30); err != nil {
			t.Error(err)
		}
	}, func() {
		if err := SetQueue("codex", 5, 60); err != nil {
			t.Error(err)
		}
		noteAdded([]loginAccount{{"codex", "other@example.com"}}, nil, nil)
	})
	s, _ := storedPicks("codex")
	if s.MaxRPM != 30 {
		t.Errorf("the limit a minute read as %d", s.MaxRPM)
	}
	if s.QueueLimit != 5 || s.QueueWait != 60 {
		t.Errorf("the queue read as %d, %ds", s.QueueLimit, s.QueueWait)
	}
	if accountAdded("codex", "other@example.com").IsZero() {
		t.Error("the day noted for other@example.com was written over")
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
		_ = SetRPM("relay", 30)
	})
	if stored("relay") {
		t.Error("the provider removed was saved again")
	}
}
