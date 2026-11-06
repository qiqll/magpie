package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// capTable is the usage caps the user set on each account (providers.json:
// accountCaps, a share for every window, and accountWindowCaps, a share for
// a window by its name), by provider and account. A window's span is read
// from quota-history.json by its name, so a cap set on "5 hours" applies
// where an account's fullest window is the five hours.
type capTable struct {
	all     map[string]int            // provider and account → its cap
	windows map[string]map[string]int // provider and account → window name → its cap
	spans   map[string]time.Duration  // provider and window name → its span
	about   string
}

func capKey(provider, user string) string {
	return provider + "\x00" + strings.ToLower(strings.TrimSpace(user))
}

// of is the share at which the account's window holds it: the window's own
// cap, else the account's, 100 for none (a cap of 100 or more is none, as
// provider.WindowCaps.Of has it). short: the window is under a day (the
// five hours), not the week.
func (c capTable) of(provider, user string, short bool) int {
	k := capKey(provider, user)
	share := c.all[k]
	for name, v := range c.windows[k] {
		span := c.spans[provider+"\x00"+strings.ToLower(strings.TrimSpace(name))]
		if span > 0 && (span < 24*time.Hour) == short && v > 0 {
			share = v
		}
	}
	if share <= 0 || share >= 100 {
		return 100
	}
	return share
}

// readCaps reads the caps from path. A file that can't be read is said so
// in about, and every account is taken to have no cap.
func readCaps(path string, spans map[string]time.Duration) capTable {
	c := capTable{all: map[string]int{}, windows: map[string]map[string]int{}, spans: spans}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		c.about = "providers.json not found: no account taken to have a usage cap"
		return c
	}
	var f struct {
		Providers []struct {
			ID                string                    `json:"id"`
			AccountCaps       map[string]int            `json:"accountCaps"`
			AccountWindowCaps map[string]map[string]int `json:"accountWindowCaps"`
		} `json:"providers"`
	}
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		c.about = "providers.json not read (" + err.Error() + "): no account taken to have a usage cap"
		return c
	}
	n := 0
	for _, p := range f.Providers {
		for u, v := range p.AccountCaps {
			if v > 0 && v < 100 {
				c.all[capKey(p.ID, u)] = v
				n++
			}
		}
		for u, ws := range p.AccountWindowCaps {
			if len(ws) > 0 {
				c.windows[capKey(p.ID, u)] = ws
				n++
			}
		}
	}
	c.about = fmt.Sprintf("providers.json: %d account caps set (as set now; a cap changed since applies to all the history)", n)
	return c
}
