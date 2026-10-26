package gateway

// A user's tier, bound by user (--bind=user): how many accounts of each
// provider it may be bound to, its first and its spares (growUser). The
// caller names it in UserTierHeader, a request without it is of the
// default tier, and user-tiers.json, next to user-accounts.json, says
// each tier's max_accounts and credits (kept, not used yet). No file is
// defaultTiers, and none is written: the file is the user's to make. One
// edited is read again. One that doesn't read, or holds a value it
// can't, is unknown: the tiers read before still hold (logged once a
// change), and with none read before, a user's request is refused, as
// `magpie serve --bind=user` refuses to start on it (CheckUserTiers).
// Bound by session, neither the header nor the file is read.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// UserTierHeader names the tier of the request's user (UserHeader). It
// goes no further than magpie.
const UserTierHeader = "X-Magpie-User-Tier"

// Tier is one tier of user-tiers.json.
type Tier struct {
	Name string `json:"name"` // as the file names it
	// Credits are the tier's credits, kept for an allowance by use, not
	// used yet
	Credits     int64 `json:"credits"`
	MaxAccounts int   `json:"max_accounts"` // the most accounts of a provider a user of it has
}

// Tiers is user-tiers.json as it holds: Default names the tier of a
// request without UserTierHeader; Tiers are in the order of their
// max_accounts, then their names.
type Tiers struct {
	Default string `json:"default"`
	Tiers   []Tier `json:"tiers"`
}

// find is the tier named name, whatever its case.
func (t Tiers) find(name string) (Tier, bool) {
	i := slices.IndexFunc(t.Tiers, func(x Tier) bool { return strings.EqualFold(x.Name, name) })
	if i < 0 {
		return Tier{}, false
	}
	return t.Tiers[i], true
}

// names is each tier's name, in order.
func (t Tiers) names() []string {
	out := make([]string, len(t.Tiers))
	for i, x := range t.Tiers {
		out[i] = x.Name
	}
	return out
}

// tiersJSON is user-tiers.json on disk: tier name → tier.
type tiersJSON struct {
	Default string `json:"default"`
	Tiers   map[string]struct {
		Credits     int64 `json:"credits"`
		MaxAccounts int   `json:"max_accounts"`
	} `json:"tiers"`
}

// defaultTiers are the tiers without a user-tiers.json.
var defaultTiers = Tiers{Default: "free", Tiers: []Tier{
	{Name: "free", Credits: 1000, MaxAccounts: 2},
	{Name: "plus", Credits: 3000, MaxAccounts: 3},
	{Name: "pro", Credits: 5000, MaxAccounts: 4},
	{Name: "max", Credits: 10000, MaxAccounts: 5},
}}

// idBad is why v, a user's or a tier's name sent in or read for name,
// can't be taken: longer than userMax, or holding a control or format
// character; "" when it can.
func idBad(name, v string) string {
	switch {
	case len(v) > userMax:
		return fmt.Sprintf("%s is at most %d bytes", name, userMax)
	case strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0:
		return name + " can't hold control or format characters"
	}
	return ""
}

// parseTiers is the tiers b says, or why they can't be taken: each name
// one idBad takes and no two alike but for case, max_accounts a whole
// number 1 or more, credits 0 or more, and default one of them.
func parseTiers(b []byte) (Tiers, error) {
	var f tiersJSON
	if err := json.Unmarshal(b, &f); err != nil {
		return Tiers{}, err
	}
	if len(f.Tiers) == 0 {
		return Tiers{}, errors.New(`it names no tiers ("tiers")`)
	}
	var t Tiers
	for name, x := range f.Tiers {
		n := strings.TrimSpace(name)
		if n == "" {
			return Tiers{}, errors.New("a tier has no name")
		}
		if bad := idBad(fmt.Sprintf("tier %q", n), n); bad != "" {
			return Tiers{}, errors.New(bad)
		}
		if _, twice := t.find(n); twice {
			return Tiers{}, fmt.Errorf("tier %q is named twice (names are the same whatever their case)", n)
		}
		if x.MaxAccounts < 1 {
			return Tiers{}, fmt.Errorf("tier %q: max_accounts is a whole number of accounts, 1 or more, not %d", n, x.MaxAccounts)
		}
		if x.Credits < 0 {
			return Tiers{}, fmt.Errorf("tier %q: credits are 0 or more, not %d", n, x.Credits)
		}
		t.Tiers = append(t.Tiers, Tier{Name: n, Credits: x.Credits, MaxAccounts: x.MaxAccounts})
	}
	slices.SortFunc(t.Tiers, func(a, b Tier) int {
		return cmp.Or(cmp.Compare(a.MaxAccounts, b.MaxAccounts), strings.Compare(a.Name, b.Name))
	})
	d, ok := t.find(strings.TrimSpace(f.Default))
	if !ok {
		return Tiers{}, fmt.Errorf("default %q is none of its tiers (%s)", f.Default, strings.Join(t.names(), ", "))
	}
	t.Default = d.Name
	return t, nil
}

func tiersPath() string {
	return filepath.Join(filepath.Dir(usersPath()), "user-tiers.json")
}

var userTiers = struct {
	sync.Mutex
	t Tiers
	// the file as last read, read again when it changed; mod zero for no
	// file (defaultTiers)
	path string
	mod  time.Time
	size int64
	read bool // t is the file's, or defaultTiers with none
	// the file as last found not to parse, not read again until it
	// changes; one that couldn't be read at all is tried again each time
	badMod  time.Time
	badSize int64
	badErr  error
}{}

var tierFileSaid sync.Map // a user-tiers.json that doesn't read, logged once a change

// readTiers is the tiers in the file at path, defaultTiers when there is
// none (exists false), or why it can't be read.
func readTiers(path string) (t Tiers, exists bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultTiers, false, nil
	}
	if err != nil {
		return Tiers{}, true, err
	}
	t, err = parseTiers(b)
	return t, true, err
}

// loadTiers is the tiers as user-tiers.json says now, read again when it
// changed. One that doesn't read leaves the tiers read before, and is an
// error only when none were.
func loadTiers() (Tiers, error) {
	userTiers.Lock()
	defer userTiers.Unlock()
	path := tiersPath()
	if path != userTiers.path {
		userTiers.t, userTiers.path, userTiers.mod, userTiers.size, userTiers.read = Tiers{}, path, time.Time{}, 0, false
		userTiers.badMod, userTiers.badSize, userTiers.badErr = time.Time{}, 0, nil
	}
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		userTiers.t, userTiers.mod, userTiers.size, userTiers.read = defaultTiers, time.Time{}, 0, true
		return userTiers.t, nil
	}
	if err == nil && userTiers.read && !userTiers.mod.IsZero() && fi.ModTime().Equal(userTiers.mod) && fi.Size() == userTiers.size {
		return userTiers.t, nil
	}
	// the same file that didn't parse: as it was, not read again
	if err == nil && userTiers.badErr != nil && fi.ModTime().Equal(userTiers.badMod) && fi.Size() == userTiers.badSize {
		if userTiers.read {
			return userTiers.t, nil
		}
		return Tiers{}, userTiers.badErr
	}
	var t Tiers
	var b []byte
	parsed := false
	if err == nil {
		if b, err = os.ReadFile(path); err == nil {
			t, err = parseTiers(b)
			parsed = true
		}
	}
	if err != nil {
		if _, said := tierFileSaid.LoadOrStore(fmt.Sprint(path, err), true); !said {
			log.Printf("user tiers: %s can't be read (%v): the tiers read before still hold, and the file is left as it is", path, err)
		}
		if parsed {
			userTiers.badMod, userTiers.badSize, userTiers.badErr = fi.ModTime(), fi.Size(), err
		}
		if !userTiers.read {
			return Tiers{}, err
		}
		return userTiers.t, nil
	}
	userTiers.t, userTiers.mod, userTiers.size, userTiers.read = t, fi.ModTime(), fi.Size(), true
	userTiers.badMod, userTiers.badSize, userTiers.badErr = time.Time{}, 0, nil
	return t, nil
}

// UserTiers is the tiers the gateway holds to, as user-tiers.json says
// (defaultTiers without one), for the Gateway page; an error when the
// file doesn't read and none were read before in this process.
func UserTiers() (Tiers, error) { return loadTiers() }

// CheckUserTiers is why user-tiers.json can't be taken, nil when it can
// or there is none: `magpie serve --bind=user` refuses to start on it
// rather than bind users to it unknown. Taken, it is what this process
// holds to (loadTiers), should it break before the first request.
func CheckUserTiers() error {
	path := tiersPath()
	if _, _, err := readTiers(path); err != nil {
		return fmt.Errorf("%s can't be read: %v", path, err)
	}
	loadTiers()
	return nil
}

// tierOf is the tier of a request's user: UserTierHeader's, the default
// without it. bad is why the header's value can't be taken (a 400); err
// that the tiers aren't known (user-tiers.json doesn't read, and never
// did).
func tierOf(h http.Header) (tier Tier, bad string, err error) {
	t, err := loadTiers()
	if err != nil {
		return Tier{}, "", err
	}
	v := strings.TrimSpace(h.Get(UserTierHeader))
	if v == "" {
		tier, _ = t.find(t.Default)
		return tier, "", nil
	}
	if bad := idBad(UserTierHeader, v); bad != "" {
		return Tier{}, bad, nil
	}
	if tier, ok := t.find(v); ok {
		return tier, "", nil
	}
	return Tier{}, fmt.Sprintf("%s: %q is none of user-tiers.json's tiers: %s", UserTierHeader, v, strings.Join(t.names(), ", ")), nil
}

// callerOnly says name is a header that tells magpie of the caller's
// user, never sent on: one of userHeaders, or UserTierHeader.
func callerOnly(name string) bool {
	return isUserHeader(name) || strings.EqualFold(name, UserTierHeader)
}
