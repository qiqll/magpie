package renewal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/appdir"
)

// IP is a proxy exit IP the user pays for by the month (T-155): its
// address, a name of the user's, and when its period was opened, zero for
// not told (never due).
type IP struct {
	IP     string    `json:"ip"`
	Name   string    `json:"name,omitempty"`
	Opened time.Time `json:"opened,omitzero"`
}

// MaxIPs is the most IPs kept.
const MaxIPs = 500

// ipsFile is ips.json beside settings.json: {"ips": [IP…]}. It is this
// computer's own, as Settings › Network's saved proxies are: no sync or
// backup carries it.
type ipsFile struct {
	IPs []IP `json:"ips"`
}

var ipsMu sync.Mutex

func ipsPath() string { return filepath.Join(appdir.Config(), "ips.json") }

// IPs is the IPs as kept, in the user's order; none when there is no
// file. A file that can't be read or parsed is an error, never none, so
// a page never offers to write over what it couldn't read.
func IPs() ([]IP, error) {
	ipsMu.Lock()
	defer ipsMu.Unlock()
	return readIPs()
}

func readIPs() ([]IP, error) {
	b, err := os.ReadFile(ipsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f ipsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("ips.json: %w", err)
	}
	return f.IPs, nil
}

// SetIPs keeps ips as the whole list, checked (CheckIPs). A file there
// that can't be read is left as it is, with its error: the list sent was
// made without it. It is written to a temp file renamed over the old one,
// so a reader never sees half of it.
func SetIPs(ips []IP, now time.Time) error {
	ips = append([]IP(nil), ips...)
	if err := CheckIPs(ips, now); err != nil {
		return err
	}
	ipsMu.Lock()
	defer ipsMu.Unlock()
	if _, err := readIPs(); err != nil {
		return err
	}
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
// name of at most 80 characters, an opening time CheckOpened takes, and
// at most MaxIPs of them.
func CheckIPs(ips []IP, now time.Time) error {
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
		if !x.Opened.IsZero() {
			if err := CheckOpened(x.Opened, now); err != nil {
				return fmt.Errorf("%s: %w", x.IP, err)
			}
			x.Opened = x.Opened.UTC()
		}
	}
	return nil
}
