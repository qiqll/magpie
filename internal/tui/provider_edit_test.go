package tui

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A key on the providers page writes only what it changes, so a
// setting set meanwhile (the GUI, another magpie) stays.
func TestTUIProviderKeysKeepWhatWasSetMeanwhile(t *testing.T) {
	home(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-r", Models: []string{"m", "n"}, Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	n := 0
	t.Cleanup(func() { editProvider = provider.Edit })
	editProvider = func(id string, change func(p *provider.Provider) error) (provider.Provider, provider.Provider, error) {
		return provider.Edit(id, func(p *provider.Provider) error {
			n++
			if err := provider.SetRPM(id, 10+n); err != nil {
				t.Fatal(err)
			}
			return change(p)
		})
	}
	m := press(t, model{w: 140, h: 40}, "2")
	m.reloadProviders()
	i := slices.IndexFunc(m.provs, func(p provider.Provider) bool { return p.ID == "relay" })
	if i < 0 {
		t.Fatal("no provider relay")
	}
	m.prow = i
	m = press(t, m, "u")
	wantFlash(t, m, true, "serves only through routing groups")
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Unlisted {
		t.Fatal("u wasn't written")
	}
	if p.MaxRPM != 11 {
		t.Fatalf("u wrote over the limit a minute set meanwhile: %d", p.MaxRPM)
	}

	// a model turned off in the picker, as the queue is set meanwhile
	m.openProviderModels("relay")
	if _, text, ok := m.pk.toggle("n"); !ok {
		t.Fatalf("toggle: %s", text)
	}
	if p, _ = provider.Find("relay"); !slices.Equal(p.Models, []string{"m"}) || p.MaxRPM != 12 {
		t.Fatalf("after the toggle: models %v, limit a minute %d", p.Models, p.MaxRPM)
	}
}
