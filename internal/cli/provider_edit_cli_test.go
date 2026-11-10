package cli

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// meanwhile has every change a command makes to a provider see another
// written first, between its read of the provider and its write: the GUI
// or another magpie setting the queue.
func meanwhile(t *testing.T, also func()) {
	t.Helper()
	t.Cleanup(func() { editProvider = provider.Edit })
	editProvider = func(id string, change func(p *provider.Provider) error) (provider.Provider, provider.Provider, error) {
		return provider.Edit(id, func(p *provider.Provider) error {
			also()
			return change(p)
		})
	}
}

// The commands magpie provider set, listed, fallback, key, icon and models write
// only what they change, so a setting set meanwhile stays.
func TestProviderCommandsKeepWhatWasSetMeanwhile(t *testing.T) {
	groupsHome(t)
	n := 0
	meanwhile(t, func() {
		n++
		if err := provider.SetRPM("a", 10+n); err != nil {
			t.Fatal(err)
		}
	})
	for _, args := range [][]string{
		{"provider", "set", "a", "family=relay"},
		{"provider", "listed", "a", "no"},
		{"provider", "fallback", "a", "b/gpt-5.5"},
		{"provider", "key", "a", "ka-2"},
		{"provider", "icon", "a", "openai"},
		{"provider", "models", "a", "m", "only-a"},
	} {
		if _, err := printed(t, func() error { return providerCmd(args) }); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if p := mustFind(t, "a"); p.MaxRPM != 10+n {
			t.Fatalf("%v wrote over the limit a minute set meanwhile: %d, want %d", args, p.MaxRPM, 10+n)
		}
	}
	p := mustFind(t, "a")
	if p.Family != "relay" || !p.Unlisted || !slices.Equal(p.Fallback, []string{"b/gpt-5.5"}) || p.Key != "ka-2" || p.Icon != "openai" || !slices.Equal(p.Models, []string{"m", "only-a"}) {
		t.Fatalf("a command's change wasn't written: %+v", p)
	}
}
