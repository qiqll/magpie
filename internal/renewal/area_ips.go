package renewal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// An area's IPs (T-198, docs/subsystems/areas.md "Proxy IPs"). An area's
// admin adds, changes and removes the IPs its area owns, one at a time,
// never through the owner's whole list; each write reads the file afresh
// under ipsMu and checks again, here, that the IP is the area's, so a
// handler that forgot to is still refused. A file that can't be read is
// an error and is never written over.

var (
	// ErrIPNotFound: no such IP, or not the area's; which is not said.
	ErrIPNotFound = errors.New("no such IP")
	// ErrIPTaken: the address is in magpie already, whoever's it is.
	ErrIPTaken = errors.New("this IP is in magpie already")
	// ErrAreaFull: the area owns MaxAreaIPs IPs already.
	ErrAreaFull = fmt.Errorf("an area keeps at most %d IPs", MaxAreaIPs)
	// ErrIPGiven: the owner gives an IP that is an area's already.
	ErrIPGiven = errors.New("this IP is an area's already: take it back first")
)

// ProxyID is how a proxy is named to an area: px_ and the first 10 hex
// of its address's SHA-256, the same for the same address. The address
// itself, with its login, is never told.
func ProxyID(proxy string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(proxy)))
	return "px_" + hex.EncodeToString(sum[:])[:10]
}

// ProxyOf is the proxy of x whose ProxyID is id.
func (x IP) ProxyOf(id string) (string, bool) {
	for _, p := range x.Proxies {
		if ProxyID(p) == id {
			return p, true
		}
	}
	return "", false
}

// normIP is s as CheckIPs writes an address, "" for none.
func normIP(s string) string {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return ""
	}
	return ip.String()
}

func keptTimes(ips []IP) map[string]time.Time {
	kept := map[string]time.Time{}
	for _, x := range ips {
		kept[x.IP] = x.Opened
	}
	return kept
}

// AddAreaIP keeps x as a new IP of the area areaID, made by its admin
// (AddedByArea), checked as CheckIPs checks the whole list: ErrIPTaken
// when magpie has the address already, ErrAreaFull past MaxAreaIPs.
// Whether its proxies are public is the caller's to check first
// (netproxy.CheckPublic); they are held to it on every dial whatever.
func AddAreaIP(areaID string, x IP, now time.Time) (IP, error) {
	if areaID == "" {
		return IP{}, ErrIPNotFound
	}
	ipsMu.Lock()
	defer ipsMu.Unlock()
	was, _, err := readIPs()
	if err != nil {
		return IP{}, err
	}
	addr := normIP(x.IP)
	if addr == "" {
		return IP{}, fmt.Errorf("%q is not an IP address", strings.TrimSpace(x.IP))
	}
	n := 0
	for _, o := range was {
		if o.IP == addr {
			return IP{}, ErrIPTaken
		}
		if o.Area == areaID {
			n++
		}
	}
	if n >= MaxAreaIPs {
		return IP{}, ErrAreaFull
	}
	x.Proxies = slices.Clone(x.Proxies)
	x.Area, x.AddedBy = areaID, AddedByArea
	all := append(slices.Clone(was), x)
	if err := CheckIPs(all, keptTimes(was), now); err != nil {
		return IP{}, err
	}
	if err := writeIPs(all); err != nil {
		return IP{}, err
	}
	return all[len(all)-1], nil
}

// ChangeAreaIP changes the IP ip of the area areaID by fn, then checks
// the list again (CheckIPs). The address and whose it is stay as they
// were, whatever fn does. ErrIPNotFound when the area has no such IP.
func ChangeAreaIP(areaID, ip string, now time.Time, fn func(*IP) error) (IP, error) {
	ipsMu.Lock()
	defer ipsMu.Unlock()
	ips, _, err := readIPs()
	if err != nil {
		return IP{}, err
	}
	i := areaIndex(ips, areaID, ip)
	if i < 0 {
		return IP{}, ErrIPNotFound
	}
	kept := keptTimes(ips)
	was := ips[i]
	c := was
	c.Proxies = slices.Clone(was.Proxies)
	if err := fn(&c); err != nil {
		return IP{}, err
	}
	c.IP, c.Area, c.AddedBy = was.IP, was.Area, was.AddedBy
	ips[i] = c
	if err := CheckIPs(ips, kept, now); err != nil {
		return IP{}, err
	}
	if err := writeIPs(ips); err != nil {
		return IP{}, err
	}
	return ips[i], nil
}

// RemoveAreaIP removes the IP ip of the area areaID; ErrIPNotFound when
// the area has no such IP. busy, when set, is asked under ipsMu whether
// the IP may go (its proxy on an account, read afresh): an error from it
// is returned and nothing is removed. An attach holds ipsMu while it
// writes (WithAreaProxy), so the two can't pass each other.
func RemoveAreaIP(areaID, ip string, busy func(IP) error) error {
	lockIPs()
	defer ipsMu.Unlock()
	ips, _, err := readIPs()
	if err != nil {
		return err
	}
	i := areaIndex(ips, areaID, ip)
	if i < 0 {
		return ErrIPNotFound
	}
	if busy != nil {
		if err := busy(ips[i]); err != nil {
			return err
		}
	}
	return writeIPs(slices.Delete(ips, i, i+1))
}

// IPsWait, when set (a test), is called by an area's removal that is
// about to wait for ipsMu, so a test knows it is waiting, not done.
var IPsWait func()

func lockIPs() {
	if !ipsMu.TryLock() {
		if IPsWait != nil {
			IPsWait()
		}
		ipsMu.Lock()
	}
}

// WithAreaProxy runs fn with the area areaID's IP ip and the address of
// its proxy proxyID, read afresh, while ipsMu is held, so no removal of
// the IP or the proxy (the area's, or the owner's whole list) comes
// between the check and what fn writes. ErrIPNotFound when the area has
// no such IP or the IP no such proxy.
//
// Lock order: ipsMu, then areas.json's lock, then providers.json's. fn
// takes the other two and does no network work; nothing that holds
// either of them takes ipsMu.
func WithAreaProxy(areaID, ip, proxyID string, fn func(x IP, url string) error) error {
	ipsMu.Lock()
	defer ipsMu.Unlock()
	ips, _, err := readIPs()
	if err != nil {
		return err
	}
	i := areaIndex(ips, areaID, ip)
	if i < 0 {
		return ErrIPNotFound
	}
	url, ok := ips[i].ProxyOf(proxyID)
	if !ok {
		return ErrIPNotFound
	}
	return fn(ips[i], url)
}

// areaIndex is where the area areaID's IP ip is in ips, -1 for none: an
// area of "" owns nothing (the owner's IPs are no area's).
func areaIndex(ips []IP, areaID, ip string) int {
	addr := normIP(ip)
	if areaID == "" || addr == "" {
		return -1
	}
	return slices.IndexFunc(ips, func(x IP) bool { return x.IP == addr && x.Area == areaID })
}

// GiveIP gives the owner's IP ip to the area areaID (Models › Proxy IPs):
// its admin then sees it and may put its proxies on the area's accounts,
// but not check them. One an area owns already is ErrIPGiven.
func GiveIP(ip, areaID string) error {
	if areaID == "" {
		return ErrIPNotFound
	}
	return setArea(ip, func(x *IP, ips []IP) error {
		if x.Area == areaID {
			return nil
		}
		if x.Area != "" {
			return ErrIPGiven
		}
		n := 0
		for _, o := range ips {
			if o.Area == areaID {
				n++
			}
		}
		if n >= MaxAreaIPs {
			return ErrAreaFull
		}
		x.Area = areaID
		return nil
	})
}

// TakeBackIP makes the IP ip the owner's again, whichever area had it.
// One an area made stays AddedByArea: its proxies came from the area's
// admin, and are still connected to at public addresses alone.
func TakeBackIP(ip string) error {
	return setArea(ip, func(x *IP, _ []IP) error {
		x.Area = ""
		return nil
	})
}

func setArea(ip string, fn func(x *IP, ips []IP) error) error {
	ipsMu.Lock()
	defer ipsMu.Unlock()
	ips, _, err := readIPs()
	if err != nil {
		return err
	}
	addr := normIP(ip)
	i := slices.IndexFunc(ips, func(x IP) bool { return addr != "" && x.IP == addr })
	if i < 0 {
		return ErrIPNotFound
	}
	if err := fn(&ips[i], ips); err != nil {
		return err
	}
	return writeIPs(ips)
}

// areaAdded is the proxies of the IPs an area made, as last read well,
// by the file's size, time and this magpie's own writes (writes): a
// write within one tick of the clock is seen too. A read that failed is
// not tried again for areaAddedRetry while the file stays as it was:
// netproxy asks on every dial through a proxy.
var areaAdded struct {
	sync.Mutex
	size, mod, gen int64
	read           bool
	proxies        map[string]bool
	failedAt       time.Time
	failedSize     int64
	failedMod      int64
	failedGen      int64
}

// areaAddedRetry is how long a failed read of ips.json stands.
var areaAddedRetry = 5 * time.Second

// areaAddedReads counts AreaAddedProxy's reads of the file, for the test
// that a failed one isn't made again on every dial.
var areaAddedReads atomic.Int64

// AreaAddedProxy says the proxy raw is one an area's admin added (an IP
// AddedByArea lists it, by its text or as netproxy.Parse writes it),
// which netproxy connects to at public addresses alone. It is read
// without ipsMu (a write renames a whole file over the old one). A file
// that can't be read keeps the last good read. With none since magpie
// started it holds nothing: which proxies are an area's is unknown, and
// the owner's own (a Clash on loopback) are never held for it; a proxy
// already on an area's account is held by areas.json's record instead
// (provider's areaAttachedProxy).
func AreaAddedProxy(raw string) bool {
	fi, err := os.Stat(ipsPath())
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	gen := writes.Load()
	areaAdded.Lock()
	defer areaAdded.Unlock()
	var size, mod int64
	if err == nil {
		size, mod = fi.Size(), fi.ModTime().UnixNano()
	}
	fresh := err == nil && areaAdded.read && areaAdded.size == size && areaAdded.mod == mod && areaAdded.gen == gen
	failedStill := !areaAdded.failedAt.IsZero() && time.Since(areaAdded.failedAt) < areaAddedRetry &&
		areaAdded.failedSize == size && areaAdded.failedMod == mod && areaAdded.failedGen == gen
	if !fresh && !failedStill {
		areaAddedReads.Add(1)
		ips, _, rerr := readIPs()
		if err == nil && rerr == nil {
			set := map[string]bool{}
			for _, x := range ips {
				if x.AddedBy != AddedByArea {
					continue
				}
				for _, p := range x.Proxies {
					set[strings.TrimSpace(p)] = true
					if u, err := netproxy.Parse(p); err == nil {
						set[u.String()] = true
					}
				}
			}
			areaAdded.size, areaAdded.mod, areaAdded.gen, areaAdded.read, areaAdded.proxies = size, mod, gen, true, set
			areaAdded.failedAt = time.Time{}
		} else {
			areaAdded.failedAt, areaAdded.failedSize, areaAdded.failedMod, areaAdded.failedGen = time.Now(), size, mod, gen
		}
	}
	raw = strings.TrimSpace(raw)
	if areaAdded.proxies[raw] {
		return true
	}
	u, err := netproxy.Parse(raw)
	return err == nil && areaAdded.proxies[u.String()]
}
