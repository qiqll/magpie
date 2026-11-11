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
// has none served; one that can't be opened or read to its end is an
// error, never "none" (T-149: a read that stopped partway left out the
// rows after it).
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
	ledger := readLogSnapshot()
	if ledger.err != nil {
		return nil, ledger.err
	}
	ledger.visit(since, func(r Record) {
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

// ServedDays is provider.ServedDays over usage.jsonl (T-134): for each
// account of want (a provider's id and an account's name in lower case),
// the days since the time want gives it (its first use), each 24 hours
// from it, that a served row of it (ServedOn) falls in: how many, and when
// the latest starts. An account whose first day has no row isn't in it:
// its first use is a served row, so the ledger doesn't reach back to it
// (trimmed, or the first use another computer's), and its days aren't
// known, never one. A ledger that isn't there has none; one that can't be
// opened or read to its end is an error (T-149). Read once at a start
// (provider.BackfillUsedDays), never on a request.
func ServedDays(want map[[2]string]time.Time) (map[[2]string]provider.UsedDays, error) {
	out := map[[2]string]provider.UsedDays{}
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
	ledger := readLogSnapshot()
	if ledger.err != nil {
		return nil, ledger.err
	}
	days := map[[2]string]map[int64]bool{}
	ledger.visit(since, func(r Record) {
		if !r.ServedOn() {
			return
		}
		k := [2]string{renames.Of(r.Provider, r.Time), strings.ToLower(strings.TrimSpace(r.ProviderAccount))}
		from, ok := want[k]
		if !ok || r.Time.Before(from) {
			return
		}
		if days[k] == nil {
			days[k] = map[int64]bool{}
		}
		days[k][int64(r.Time.Sub(from)/(24*time.Hour))] = true
	})
	for k, ds := range days {
		if !ds[0] {
			continue // the ledger doesn't reach its first use
		}
		// each day named (T-148), so a sync takes the union
		named := make([]int, 0, len(ds))
		for d := range ds {
			named = append(named, int(d))
		}
		out[k] = provider.UsedDaysOf(want[k], named)
	}
	return out, nil
}
