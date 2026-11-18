// Package area keeps the areas the owner hands out (docs/subsystems/areas.md):
// a note such as 学习 or 工作, the provider accounts it owns, and what its
// admin may add. An area decides who may manage an account and see what it
// spent, never where a request goes: routing reads no area.
//
// It is a leaf: access and provider both read it, so it imports neither.
package area

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filelock"
)

// Area is one area as areas.json holds it.
type Area struct {
	ID      string    `json:"id"`
	Note    string    `json:"note"`
	Off     bool      `json:"off,omitempty"`
	Created time.Time `json:"created"`
	// Providers are the providers whose API keys the area's admin may
	// add; SignIns the subscriptions it may sign in to. Both are the
	// owner's to set.
	Providers []string `json:"providers,omitempty"`
	SignIns   []string `json:"signIns,omitempty"`
	// Accounts are the accounts it owns, as access.Key.Accounts writes
	// them: "<provider>/<login id>" or "<provider>/<key fingerprint>".
	Accounts []Account `json:"accounts,omitempty"`
	// CallerSalt is the area's own random key (hex) that names a gateway
	// key to it opaquely (the admin routes' callerKey): the same caller
	// reads alike within the area and apart across two. Made with the
	// area, or on first use for one made before; never told.
	CallerSalt string `json:"callerSalt,omitempty"`
}

// Account is an account an area owns, and since when: what it spent
// counts from then. Previous are the refs it had before a key's value was
// replaced in place (Rename), so what it spent under them stays its own.
//
// Proxy is the area's proxy its admin put on it (T-198), nil for none.
type Account struct {
	Ref      string        `json:"ref"`
	Added    time.Time     `json:"added"`
	Previous []string      `json:"previous,omitempty"`
	Proxy    *AccountProxy `json:"proxy,omitempty"`
}

// AccountProxy is a proxy of one of the area's IPs its admin put on an
// account: the IP, the proxy's id (renewal.ProxyID) and the account's key
// in its provider's AccountProxies (a key's fingerprint, an account's
// login in lower case), where the proxy's address is written. The
// address itself is not kept here. Never told as it is.
//
// Direct is the admin's "direct" on the account (T-213): no IP and no
// proxy, "direct" written as its own proxy, so it goes through none.
type AccountProxy struct {
	IP     string `json:"ip,omitempty"`
	ID     string `json:"id,omitempty"`
	Key    string `json:"key"`
	Direct bool   `json:"direct,omitempty"`
}

// Released is told each account an area let go of (Release,
// ReleaseProvider) with the proxy its admin had put on it, once the
// change is written and outside the lock. internal/provider takes the
// proxy off the account, where it is still the area's.
var Released func(Account)

// Has says whether the area owns ref.
func (a Area) Has(ref string) bool {
	return slices.ContainsFunc(a.Accounts, func(c Account) bool { return c.Ref == ref })
}

type file struct {
	Version int    `json:"version"`
	Areas   []Area `json:"areas"`
}

var (
	ErrNotFound   = errors.New("no such area")
	ErrNotEmpty   = errors.New("the area still owns accounts: delete them, or take them back, first")
	ErrTaken      = errors.New("the account is already in magpie")
	ErrNoteInUse  = errors.New("another area has this note")
	ErrUnreadable = errors.New("areas.json can't be read")
	ErrFull       = errors.New("an area has at most 500 accounts")
)

// Path is areas.json, beside settings.json.
func Path() string { return filepath.Join(appdir.Config(), "areas.json") }

var mu = filelock.Mutex{Path: func() string { return Path() + ".lock" }}

// last is the last good read, by the file's size and time: a read that
// fails keeps it (unknown is never taken for "no areas"), and a reader
// on every request doesn't parse the file each time.
var last struct {
	sync.Mutex
	size, mod int64
	read      bool
	areas     []Area
}

// List is every area. A file that can't be read or parsed gives the last
// good read, and ErrUnreadable with none since magpie started.
func List() ([]Area, error) {
	fi, err := os.Stat(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	last.Lock()
	defer last.Unlock()
	if err == nil && last.read && last.size == fi.Size() && last.mod == fi.ModTime().UnixNano() {
		return clone(last.areas), nil
	}
	areas, rerr := load()
	if rerr != nil || err != nil {
		if last.read {
			return clone(last.areas), nil
		}
		return nil, ErrUnreadable
	}
	last.size, last.mod, last.read, last.areas = fi.Size(), fi.ModTime().UnixNano(), true, areas
	return clone(areas), nil
}

// Get is the area id names.
func Get(id string) (Area, error) {
	areas, err := List()
	if err != nil {
		return Area{}, err
	}
	for _, a := range areas {
		if a.ID == id {
			return a, nil
		}
	}
	return Area{}, ErrNotFound
}

// Owner is the id of the area that owns ref, "" for none. A ref two areas
// claim (areas.json written by hand) is owned by neither, and contested
// says so: no admin manages it until the owner settles it.
func Owner(ref string) (id string, contested bool, err error) {
	areas, err := List()
	if err != nil {
		return "", false, err
	}
	for _, a := range areas {
		if a.Has(ref) {
			if id != "" {
				return "", true, nil
			}
			id = a.ID
		}
	}
	return id, false, nil
}

// Spec is what the owner sets when making or changing an area.
type Spec struct {
	Note      *string
	Off       *bool
	Providers *[]string
	SignIns   *[]string
}

// Create makes an area.
func Create(note string, providers, signIns []string) (Area, error) {
	note, err := checkNote(note)
	if err != nil {
		return Area{}, err
	}
	var made Area
	err = change(func(f *file) error {
		if noteInUse(f.Areas, note, "") {
			return ErrNoteInUse
		}
		made = Area{ID: newID(), Note: note, Created: time.Now().UTC(), Providers: tidy(providers), SignIns: tidy(signIns), CallerSalt: newSalt()}
		f.Areas = append(f.Areas, made)
		return nil
	})
	return made, err
}

// Update changes what s sets of the area id.
func Update(id string, s Spec) (Area, error) {
	var note string
	if s.Note != nil {
		n, err := checkNote(*s.Note)
		if err != nil {
			return Area{}, err
		}
		note = n
	}
	var got Area
	err := change(func(f *file) error {
		i := index(f.Areas, id)
		if i < 0 {
			return ErrNotFound
		}
		a := &f.Areas[i]
		if s.Note != nil {
			if noteInUse(f.Areas, note, id) {
				return ErrNoteInUse
			}
			a.Note = note
		}
		if s.Off != nil {
			a.Off = *s.Off
		}
		if s.Providers != nil {
			a.Providers = tidy(*s.Providers)
		}
		if s.SignIns != nil {
			a.SignIns = tidy(*s.SignIns)
		}
		got = *a
		return nil
	})
	return got, err
}

// Salt is the area id's CallerSalt, made and kept first for an area
// that has none (made before it was).
func Salt(id string) (string, error) {
	a, err := Get(id)
	if err != nil {
		return "", err
	}
	if a.CallerSalt != "" {
		return a.CallerSalt, nil
	}
	var salt string
	err = change(func(f *file) error {
		i := index(f.Areas, id)
		if i < 0 {
			return ErrNotFound
		}
		if f.Areas[i].CallerSalt == "" {
			f.Areas[i].CallerSalt = newSalt()
		}
		salt = f.Areas[i].CallerSalt
		return nil
	})
	return salt, err
}

// Delete removes an area that owns no account.
func Delete(id string) error {
	return change(func(f *file) error {
		i := index(f.Areas, id)
		if i < 0 {
			return ErrNotFound
		}
		if len(f.Areas[i].Accounts) > 0 {
			return ErrNotEmpty
		}
		f.Areas = slices.Delete(f.Areas, i, i+1)
		return nil
	})
}

// Claim gives ref to the area id. An account any area owns already is
// ErrTaken, whichever area: who owns it is not said.
func Claim(id, ref string) error { return ClaimUpTo(id, ref, 0) }

// ClaimUpTo is Claim of an area that owns fewer than most accounts (0:
// any number), judged under the lock: else ErrFull, so requests side by
// side never take an area past it.
func ClaimUpTo(id, ref string, most int) error {
	return change(func(f *file) error {
		i := index(f.Areas, id)
		if i < 0 {
			return ErrNotFound
		}
		if most > 0 && len(f.Areas[i].Accounts) >= most {
			return ErrFull
		}
		for _, a := range f.Areas {
			if a.Has(ref) {
				return ErrTaken
			}
		}
		f.Areas[i].Accounts = append(f.Areas[i].Accounts, Account{Ref: ref, Added: time.Now().UTC()})
		return nil
	})
}

// Release takes ref from whichever area owns it; one no area owns is no
// error, so the owner's removal of any account may call it.
func Release(ref string) error {
	return releaseWhere(func(c Account) bool { return c.Ref == ref })
}

// releaseWhere takes every account gone says from the areas, then tells
// Released of those with a proxy.
func releaseWhere(gone func(Account) bool) error {
	var let []Account
	err := change(func(f *file) error {
		let = nil
		for i := range f.Areas {
			f.Areas[i].Accounts = slices.DeleteFunc(f.Areas[i].Accounts, func(c Account) bool {
				if gone(c) {
					if c.Proxy != nil {
						let = append(let, c)
					}
					return true
				}
				return false
			})
		}
		return nil
	})
	if err == nil && Released != nil {
		for _, c := range let {
			Released(c)
		}
	}
	return err
}

// ErrNotOwned: the area doesn't own the account alone.
var ErrNotOwned = errors.New("the area doesn't own this account")

// SetProxy records p (nil: none) as the proxy the area id's admin put on
// its account ref, which the area must own alone (no other area claims
// it): else ErrNotOwned. The proxy itself is written to the account's
// provider by internal/provider.
func SetProxy(id, ref string, p *AccountProxy) error {
	return change(func(f *file) error {
		i := index(f.Areas, id)
		if i < 0 {
			return ErrNotFound
		}
		for _, a := range f.Areas {
			if a.ID != id && a.Has(ref) {
				return ErrNotOwned
			}
		}
		j := slices.IndexFunc(f.Areas[i].Accounts, func(c Account) bool { return c.Ref == ref })
		if j < 0 {
			return ErrNotOwned
		}
		if p != nil {
			p = &AccountProxy{IP: p.IP, ID: p.ID, Key: p.Key, Direct: p.Direct}
		}
		f.Areas[i].Accounts[j].Proxy = p
		return nil
	})
}

// MoveProxy has each of the area id's accounts on the proxy proxyID of
// its IP from say the IP to instead (T-214: the proxy's exit was read
// again and it moved, renewal.MoveAreaProxy). The address written to the
// account is the proxy's, and stays. ErrNotFound for no such area.
func MoveProxy(id, from, proxyID, to string) error {
	return change(func(f *file) error {
		i := index(f.Areas, id)
		if i < 0 {
			return ErrNotFound
		}
		for j := range f.Areas[i].Accounts {
			if p := f.Areas[i].Accounts[j].Proxy; p != nil && !p.Direct && p.IP == from && p.ID == proxyID {
				p.IP = to
			}
		}
		return nil
	})
}

// ReleaseProvider takes every account of provider pid from the areas
// that own them: the provider was deleted, its keys with it. A provider
// of the same id added later doesn't bring them back to an area.
func ReleaseProvider(pid string) error {
	return releaseWhere(func(c Account) bool { return strings.HasPrefix(c.Ref, pid+"/") })
}

// Rename moves ownership of from to to (a key's value replaced in place
// gets a new fingerprint), in whichever area owns it. The account keeps
// from in Previous: the calls made under it are still the area's.
func Rename(from, to string) error {
	if from == to {
		return nil
	}
	return change(func(f *file) error {
		for i := range f.Areas {
			for j := range f.Areas[i].Accounts {
				if c := &f.Areas[i].Accounts[j]; c.Ref == from {
					c.Ref = to
					// the proxy moved with the key (provider.ReplaceKey)
					if _, fp, _ := strings.Cut(from, "/"); c.Proxy != nil && c.Proxy.Key == fp {
						_, c.Proxy.Key, _ = strings.Cut(to, "/")
					}
					c.Previous = slices.DeleteFunc(c.Previous, func(r string) bool { return r == from || r == to })
					c.Previous = append(c.Previous, from)
				}
			}
		}
		return nil
	})
}

// change reads areas.json afresh under the lock, applies fn and writes it
// whole, through a temp file renamed over it. A file that can't be read is
// never written over: what it held would be lost.
func change(fn func(*file) error) error {
	mu.Lock()
	defer mu.Unlock()
	areas, err := load()
	if err != nil {
		return err
	}
	f := file{Version: 1, Areas: areas}
	if err := fn(&f); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	if err := edit.WriteAtomic(Path(), append(b, '\n')); err != nil {
		return err
	}
	last.Lock()
	last.read = false // read afresh next time
	last.Unlock()
	return nil
}

func load() ([]Area, error) {
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	return f.Areas, nil
}

// checkNote is note trimmed, or why it can't be one: 1 to 40 characters,
// none of them a control character.
func checkNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if n := utf8.RuneCountInString(note); n < 1 || n > 40 {
		return "", errors.New("an area's note is 1 to 40 characters")
	}
	if strings.ContainsFunc(note, unicode.IsControl) {
		return "", errors.New("an area's note can't hold control characters")
	}
	return note, nil
}

func noteInUse(areas []Area, note, except string) bool {
	return slices.ContainsFunc(areas, func(a Area) bool { return a.ID != except && strings.EqualFold(a.Note, note) })
}

func index(areas []Area, id string) int {
	return slices.IndexFunc(areas, func(a Area) bool { return a.ID == id })
}

func tidy(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "ar_" + hex.EncodeToString(b)
}

func newSalt() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func clone(areas []Area) []Area {
	out := slices.Clone(areas)
	for i := range out {
		out[i].Providers = slices.Clone(out[i].Providers)
		out[i].SignIns = slices.Clone(out[i].SignIns)
		out[i].Accounts = slices.Clone(out[i].Accounts)
		for j := range out[i].Accounts {
			out[i].Accounts[j].Previous = slices.Clone(out[i].Accounts[j].Previous)
			if p := out[i].Accounts[j].Proxy; p != nil {
				c := *p
				out[i].Accounts[j].Proxy = &c
			}
		}
	}
	return out
}
