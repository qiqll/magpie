package usage

// When each subscription account was first used, for the growth policy
// (provider/growth.go): a request its vendor served through the gateway.

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// ServedOn says r is a request a subscription account's vendor served
// through the gateway: a row of the gateway's (not one read from an
// agent's session files, nor one an app reported, nor magpie's own Test,
// an MCP tool's or a search API's ask: those have a Source), with an
// account, a 2xx, not turned away by magpie, and not a plugin's own cache
// keep-alive ping. The growth policy counts an account's days from its
// first such row (provider.NoteAccountUsed); a keep-alive, a usage read or a
// warm-up writes no row, so is never one.
func (r Record) ServedOn() bool {
	return r.ProviderAccount != "" && r.Provider != "" && r.Status >= 200 && r.Status < 300 &&
		r.Source == "" && r.Kind != KindKeepalive && !r.IsRejected()
}

// FirstServed is provider.FirstServed over usage.jsonl: for each account
// of want (a provider's id and an account's name in lower case), its
// earliest served row (ServedOn) at or after the time want gives it, the
// provider named by its id now (UsageRenames). A ledger that isn't there
// has none served; one that can't be opened is an error, never "none".
func FirstServed(want map[[2]string]time.Time) (map[[2]string]time.Time, error) {
	out := map[[2]string]time.Time{}
	if len(want) == 0 {
		return out, nil
	}
	f, err := os.Open(Path())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return out, nil
	case err != nil:
		return nil, err
	}
	f.Close()
	since := time.Time{}
	for _, at := range want {
		if since.IsZero() || at.Before(since) {
			since = at
		}
	}
	renames := provider.UsageRenames()
	Visit(since, func(r Record) {
		if !r.ServedOn() {
			return
		}
		k := [2]string{renames.Of(r.Provider, r.Time), strings.ToLower(strings.TrimSpace(r.ProviderAccount))}
		added, ok := want[k]
		if !ok || r.Time.Before(added) {
			return
		}
		if at, had := out[k]; !had || r.Time.Before(at) {
			out[k] = r.Time
		}
	})
	return out, nil
}
