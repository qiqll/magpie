package access

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// Admin keys (docs/subsystems/areas.md): an area's admin manages the
// accounts its area owns and reads what they spent; the console's key
// reads every area and makes, changes and deletes them. Both reach
// /v1/magpie/admin/* alone (the gateway's adminGuard); what an area is
// lives in internal/area.
const (
	AdminArea    = "area"
	AdminConsole = "console"
)

// adminHeld is what an admin key is held to on disk, besides its Admin:
// read-only, to an account no provider has, and to no MCP server. A
// magpie from before admin keys reads it as any other key, so it answers
// it the stats routes alone, and of those only what a key held to
// accounts it has none of may read.
var adminHeld = []string{"-/-"}

// adminActions are what Update does to an admin key: what it may do and
// still be an admin key held as adminHeld holds it.
var adminActions = map[string]bool{
	"rotate-key": true, "rename-key": true, "on-key": true, "off-key": true,
	"remove-key": true, "copy-key": true,
}

// firstAdmin is where keys' admin keys start: they are kept last, so a
// list of the gateway keys alone (the window's) has each where the store
// has it, and a move or a new key goes before them.
func firstAdmin(keys []Key) int {
	if i := slices.IndexFunc(keys, func(k Key) bool { return k.Admin != "" }); i >= 0 {
		return i
	}
	return len(keys)
}

// ErrConsoleKey: the console has a key already; rotate or remove it.
var ErrConsoleKey = errors.New("The console has a key already: rotate or remove that one")

// AddAdmin makes an admin key of role, for area when role is AdminArea,
// and gives its secret: the only time it is told, but for copy-key. The
// console has one key at most.
func AddAdmin(role, area, name string) (Key, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 120 {
		return Key{}, "", errors.New("Use a name between 1 and 120 characters")
	}
	switch role {
	case AdminArea:
		if area == "" {
			return Key{}, "", errors.New("An area admin's key needs its area")
		}
	case AdminConsole:
		area = ""
	default:
		return Key{}, "", errors.New("unknown admin role " + role)
	}
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return Key{}, "", err
	}
	if role == AdminConsole && slices.ContainsFunc(keys, func(k Key) bool { return k.Admin == AdminConsole }) {
		return Key{}, "", ErrConsoleKey
	}
	id, err := random(12)
	if err != nil {
		return Key{}, "", err
	}
	token, err := random(24)
	if err != nil {
		return Key{}, "", err
	}
	k := Key{ID: id, Name: name, Secret: Prefix + token, Admin: role, Area: area, ReadOnly: true, Accounts: slices.Clone(adminHeld), MCP: []string{NoMCP}}
	keys = append(keys, k)
	if err := save(keys); err != nil {
		return Key{}, "", err
	}
	secret := k.Secret
	k.Secret = ""
	return k, secret, nil
}

// AreaKey is the admin key of area, if it has one.
func AreaKey(area string) (Key, bool, error) {
	keys, err := List()
	if err != nil {
		return Key{}, false, err
	}
	i := slices.IndexFunc(keys, func(k Key) bool { return k.Admin == AdminArea && k.Area == area })
	if i < 0 {
		return Key{}, false, nil
	}
	return keys[i], true, nil
}

// RemoveArea removes the admin keys of area.
func RemoveArea(area string) error {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return err
	}
	n := len(keys)
	keys = slices.DeleteFunc(keys, func(k Key) bool { return k.Admin == AdminArea && k.Area == area })
	if len(keys) == n {
		return nil
	}
	return save(keys)
}
