package gateway

// A user's tier, bound by user (--bind=user): how many accounts of each
// provider it may be bound to, its first and its spares (growUser), and
// how many users an account it is on may be bound to (share). The
// caller names it in UserTierHeader, a request without it is of the
// default tier, and user-tiers.json, next to user-accounts.json, says
// each tier's max_accounts, share and credits (kept, not used yet). No file is
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
	// Share is the most users an account a user of it is on may be bound
	// to, that user among them: 1 its own alone, 0 no limit
	Share int `json:"share"`
}

// Tiers is user-tiers.json as it holds: Default names the tier of a
// request without UserTierHeader; Tiers are in the order of their
// max_accounts, then their names. Aliases are other words a caller may
// send in UserTierHeader for a tier (a caller that numbers them, "4" for
// max): alias → the tier's name.
type Tiers struct {
	Default string            `json:"default"`
	Tiers   []Tier            `json:"tiers"`
	Aliases map[string]string `json:"aliases,omitempty"`
}

// find is the tier named name, or one of its aliases, whatever its case.
func (t Tiers) find(name string) (Tier, bool) {
	i := slices.IndexFunc(t.Tiers, func(x Tier) bool { return strings.EqualFold(x.Name, name) })
	if i < 0 {
		for a, n := range t.Aliases {
			if strings.EqualFold(a, name) {
				i = slices.IndexFunc(t.Tiers, func(x Tier) bool { return x.Name == n })
				break
			}
		}
	}
	if i < 0 {
		return Tier{}, false
	}
	return t.Tiers[i], true
}

// aliasesOf is the aliases of the tier named name, in order.
func (t Tiers) aliasesOf(name string) []string {
	var out []string
	for a, n := range t.Aliases {
		if n == name {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// names is each tier's name, in order.
func (t Tiers) names() []string {
	out := make([]string, len(t.Tiers))
	for i, x := range t.Tiers {
		out[i] = x.Name
	}
	return out
}

// said is each tier's name with its aliases, in order, as a refusal
// lists them: "free (1), plus (2)".
func (t Tiers) said() string {
	out := make([]string, len(t.Tiers))
	for i, x := range t.Tiers {
		out[i] = x.Name
		if as := t.aliasesOf(x.Name); len(as) > 0 {
			out[i] += " (" + strings.Join(as, ", ") + ")"
		}
	}
	return strings.Join(out, ", ")
}

// tiersJSON is user-tiers.json on disk: tier name → tier.
type tiersJSON struct {
	Default string `json:"default"`
	Tiers   map[string]struct {
		Credits     int64    `json:"credits"`
		MaxAccounts int      `json:"max_accounts"`
		Share       int      `json:"share"`
		Aliases     []string `json:"aliases"`
	} `json:"tiers"`
}

// defaultTiers are the tiers without a user-tiers.json: free shares an
// account with one other free user at most, the others have theirs alone.
// A caller that numbers them sends 1 to 4.
var defaultTiers = Tiers{Default: "free", Tiers: []Tier{
	{Name: "free", Credits: 1000, MaxAccounts: 2, Share: 2},
	{Name: "plus", Credits: 3000, MaxAccounts: 3, Share: 1},
	{Name: "pro", Credits: 5000, MaxAccounts: 4, Share: 1},
	{Name: "max", Credits: 10000, MaxAccounts: 5, Share: 1},
}, Aliases: map[string]string{"1": "free", "2": "plus", "3": "pro", "4": "max"}}

// shareOf is the share of the tier a binding keeps (name), the default
// tier's for one gone from the file or kept before tiers; 1, the least,
// when not even that is known.
func (t Tiers) shareOf(name string) int {
	x, ok := t.find(name)
	if !ok {
		if x, ok = t.find(t.Default); !ok {
			return 1
		}
	}
	return x.Share
}

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
// number 1 or more, share 0 (no limit, as a file before shares) or more,
// credits 0 or more, and default one of them; each of its aliases taken
// as a name is, and none a tier's name or another alias.
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
		if x.Share < 0 {
			return Tiers{}, fmt.Errorf("tier %q: share is how many users an account may be bound to, 1 or more, or 0 for no limit, not %d", n, x.Share)
		}
		if x.Credits < 0 {
			return Tiers{}, fmt.Errorf("tier %q: credits are 0 or more, not %d", n, x.Credits)
		}
		t.Tiers = append(t.Tiers, Tier{Name: n, Credits: x.Credits, MaxAccounts: x.MaxAccounts, Share: x.Share})
	}
	// each alias another word for one tier: none a tier's name, nor
	// another's alias, whatever its case
	for name, x := range f.Tiers {
		n := strings.TrimSpace(name)
		for _, a := range x.Aliases {
			a = strings.TrimSpace(a)
			if a == "" {
				return Tiers{}, fmt.Errorf("tier %q has an empty alias", n)
			}
			if bad := idBad(fmt.Sprintf("tier %q's alias %q", n, a), a); bad != "" {
				return Tiers{}, errors.New(bad)
			}
			if x, taken := t.find(a); taken {
				return Tiers{}, fmt.Errorf("tier %q's alias %q is already tier %q or its alias (the same whatever the case)", n, a, x.Name)
			}
			if t.Aliases == nil {
				t.Aliases = map[string]string{}
			}
			t.Aliases[a] = n
		}
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
	return Tier{}, fmt.Sprintf("%s: %q is none of user-tiers.json's tiers: %s", UserTierHeader, v, t.said()), nil
}

// tierNamed says h names its user's tier (UserTierHeader). Without it
// the tier is the one the user's binding keeps, the default for a user
// bound to none or to none user-tiers.json has (bindUser).
func tierNamed(h http.Header) bool { return strings.TrimSpace(h.Get(UserTierHeader)) != "" }

// callerOnly says name is a header that tells magpie of the caller's
// user, never sent on: one of userHeaders, or UserTierHeader.
func callerOnly(name string) bool {
	return isUserHeader(name) || strings.EqualFold(name, UserTierHeader)
}
