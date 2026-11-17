package renewal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/settings"
)

// IP is a proxy exit IP the user pays for by the month (T-155): its
// address, a name of the user's, when its period was opened, zero for
// not told (never due), and the proxies whose exit it was read as (T-170:
// from a saved proxy or the accounts' own), by which the page tells the
// accounts that go out through it.
//
// Area is the area that owns it (T-198), "" for the owner's: every record
// from before areas, and one the owner made. AddedBy is "area" when the
// area's admin made it, whose proxies are then connected to at public
// addresses alone (netproxy.PublicOnly); "" when the owner made it and
// may have given it. Neither changes through the owner's whole list
// (SetIPs): GiveIP, TakeBackIP and the area's own writes set them.
type IP struct {
	IP      string    `json:"ip"`
	Name    string    `json:"name,omitempty"`
	Opened  time.Time `json:"opened,omitzero"`
	Proxies []string  `json:"proxies,omitempty"`
	Area    string    `json:"area,omitempty"`
	AddedBy string    `json:"addedBy,omitempty"`
}

// AddedByArea is IP.AddedBy for an IP an area's admin made.
const AddedByArea = "area"

// MaxIPs is the most IPs kept, and MaxIPProxies the most proxies one IP
// is told the exit of.
const (
	MaxIPs       = 500
	MaxIPProxies = 20
	// MaxAreaIPs is the most IPs one area owns.
	MaxAreaIPs = 50
)

// ipsFile is ips.json beside settings.json: {"ips": [IP…]}. It is this
// computer's own, as Settings › Network's saved proxies are: no sync or
// backup carries it.
type ipsFile struct {
	IPs []IP `json:"ips"`
}

var ipsMu sync.Mutex

func ipsPath() string { return filepath.Join(appdir.Config(), "ips.json") }

// ErrIPsChanged refuses a list made from a read the file has changed
// since: another window's edit is never written over (T-155 review M2).
var ErrIPsChanged = errors.New("the IPs were changed elsewhere since they were read")

// IPs is the IPs as kept, in the user's order, and the revision they were
// read at (SetIPs takes it back); none, at "", when there is no file. A
// file that can't be read or parsed is an error, never none, so a page
// never offers to write over what it couldn't read.
func IPs() ([]IP, string, error) {
	ipsMu.Lock()
	defer ipsMu.Unlock()
	return readIPs()
}

func readIPs() ([]IP, string, error) {
	p := ipsPath()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", unreadable{err}
	}
	var f ipsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, "", unreadable{fmt.Errorf("%s can't be read (%w): fix or remove it", p, err)}
	}
	sum := sha256.Sum256(b)
	return f.IPs, hex.EncodeToString(sum[:8]), nil
}

// ErrIPsUnreadable is an ips.json there that can't be read or parsed:
// unknown, never no IPs, and never written over.
var ErrIPsUnreadable = errors.New("ips.json can't be read")

// unreadable is a read's error as it was, that is also ErrIPsUnreadable.
type unreadable struct{ err error }

func (e unreadable) Error() string   { return e.err.Error() }
func (e unreadable) Unwrap() []error { return []error{ErrIPsUnreadable, e.err} }

// SetIPs keeps ips as the whole list, checked (CheckIPs), when the file is
// still at rev, the revision the list was made from (IPs); else
// ErrIPsChanged, and nothing is written. A file there that can't be read
// is left as it is, with its error. It is written to a temp file renamed
// over the old one, so a reader never sees half of it.
func SetIPs(ips []IP, rev string, now time.Time) error {
	ips = append([]IP(nil), ips...)
	ipsMu.Lock()
	defer ipsMu.Unlock()
	was, at, err := readIPs()
	if err != nil {
		return err
	}
	if at != rev {
		return ErrIPsChanged
	}
	kept := map[string]time.Time{}
	owned := map[string]IP{}
	for _, x := range was {
		kept[x.IP] = x.Opened
		owned[x.IP] = x
	}
	if err := CheckIPs(ips, kept, now); err != nil {
		return err
	}
	// whose each is comes from the file, by its address, never from the
	// list sent: a page that leaves the fields out doesn't make an area's
	// IP the owner's, nor one that names an area give it away
	for i := range ips {
		ips[i].Area, ips[i].AddedBy = owned[ips[i].IP].Area, owned[ips[i].IP].AddedBy
	}
	return writeIPs(ips)
}

// writeIPs writes ips as the whole file, through a temp file renamed over
// it; called with ipsMu held.
func writeIPs(ips []IP) error {
	if ips == nil {
		ips = []IP{}
	}
	b, err := json.MarshalIndent(ipsFile{IPs: ips}, "", "  ")
	if err != nil {
		return err
	}
	p := ipsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".ips-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// CheckIPs says whether ips can be kept, normalizing each in place: an
// address IPv4 or IPv6 takes (written as Go writes it), none twice, a
// name of at most 80 characters, an opening time CheckOpened takes, at
// most MaxIPProxies proxies each, one settings.CheckProxy takes and not
// "direct" (each trimmed, one listed twice kept once), and at most MaxIPs
// of them. An IP's time as kept (kept, by its address) is
// not checked again, so one a clock set back since puts out of bounds
// holds up no other edit (T-155 review L2).
func CheckIPs(ips []IP, kept map[string]time.Time, now time.Time) error {
	if len(ips) > MaxIPs {
		return fmt.Errorf("at most %d IPs are kept, not %d", MaxIPs, len(ips))
	}
	seen := map[string]bool{}
	for i := range ips {
		x := &ips[i]
		ip := net.ParseIP(strings.TrimSpace(x.IP))
		if ip == nil {
			return fmt.Errorf("%q is not an IP address", strings.TrimSpace(x.IP))
		}
		x.IP = ip.String()
		if seen[x.IP] {
			return fmt.Errorf("%s is listed twice", x.IP)
		}
		seen[x.IP] = true
		x.Name = strings.TrimSpace(x.Name)
		if utf8.RuneCountInString(x.Name) > 80 {
			return fmt.Errorf("the name of %s is longer than 80 characters", x.IP)
		}
		if was, ok := kept[x.IP]; !x.Opened.IsZero() && !(ok && was.Equal(x.Opened)) {
			if err := CheckOpened(x.Opened, now); err != nil {
				return fmt.Errorf("%s: %w", x.IP, err)
			}
		}
		if !x.Opened.IsZero() {
			x.Opened = x.Opened.UTC()
		}
		// a list of its own: the caller's is never written in
		var proxies []string
		for _, p := range x.Proxies {
			p = strings.TrimSpace(p)
			if p == "" || p == "direct" || len(p) > 2048 || settings.CheckProxy(p) != nil {
				return fmt.Errorf("%s: a proxy it is the exit of must look like http://127.0.0.1:7890 or socks5://127.0.0.1:1080", x.IP)
			}
			if !slices.Contains(proxies, p) {
				proxies = append(proxies, p)
			}
		}
		if len(proxies) > MaxIPProxies {
			return fmt.Errorf("%s: at most %d proxies are kept for one IP, not %d", x.IP, MaxIPProxies, len(proxies))
		}
		x.Proxies = proxies
	}
	return nil
}
