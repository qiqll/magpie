package netproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/netguard"
)

// Public-only proxies (T-198, docs/subsystems/areas.md "Proxy IPs"). A
// proxy an area's admin added (ips.json, addedBy "area") must be at a
// public address: on this computer, the owner's network or a cloud's
// metadata address it would have magpie connect where the area has no
// business (the owner's own Clash, a router's page, 169.254.169.254). It
// is judged when it is saved (CheckPublic) and again on every connection
// magpie makes to it, a proxy check and an account's real traffic alike,
// on the address the socket connects to, so a name pointed somewhere
// else after it was saved (DNS rebinding) is refused too (guardDial,
// DialSOCKS). A child process given it (Claude Code, a plugin's Bun) is
// given the address checked in place of the name (pinPublic).

// PublicOnly says the proxy at raw (a choice as With takes it, or a
// parsed URL's String) may be connected to at a public address alone.
// internal/renewal sets it from ips.json; nil holds no proxy to it.
var PublicOnly func(raw string) bool

// ErrNotPublic refuses a connection to a public-only proxy at an address
// that isn't public, and ErrUnresolved one whose name has no address.
var (
	ErrNotPublic  = errors.New("the proxy isn't at a public address")
	ErrUnresolved = errors.New("the proxy's name has no address")
)

// LookupIP resolves a public-only proxy's name; a test answers it.
var LookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// DialPublicAddr connects to addr, an address a public-only proxy's name
// was checked to: a test stands a local server in for it.
var DialPublicAddr = func(ctx context.Context, dial DialFunc, network, addr string) (net.Conn, error) {
	return dial(ctx, network, addr)
}

// DialFunc is a transport's DialContext.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// publicOnly is PublicOnly of raw, by its text and as it parses.
func publicOnly(raw string) bool {
	f := PublicOnly
	if f == nil || strings.TrimSpace(raw) == "" || raw == "direct" {
		return false
	}
	if f(strings.TrimSpace(raw)) {
		return true
	}
	u, err := Parse(raw)
	return err == nil && f(u.String())
}

// proxySchemes are the schemes a public-only proxy may have, and the port
// each is dialled at when it names none.
var proxySchemes = map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}

// CheckPublic says whether raw can be kept as a proxy an area added: an
// http, https, socks5 or socks5h URL with its port written, at an address
// that is public (see publicHost), every address its name resolves to
// among them. A name that can't be resolved is ErrUnresolved, an address
// that isn't public ErrNotPublic; a malformed one is neither.
func CheckPublic(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Opaque != "" {
		return errors.New("a proxy is written scheme://host:port, its scheme http, https, socks5 or socks5h")
	}
	if _, ok := proxySchemes[u.Scheme]; !ok {
		return fmt.Errorf("a proxy's scheme is http, https, socks5 or socks5h, not %q", u.Scheme)
	}
	if p, err := strconv.Atoi(u.Port()); err != nil || p < 1 || p > 65535 {
		return errors.New("write the proxy's port: scheme://host:port")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("a proxy is scheme://host:port alone, with no path or query")
	}
	_, err = publicAddrs(ctx, u.Hostname())
	return err
}

// publicAddrs are the addresses host stands for, when every one of them
// is public (netguard.Public: not loopback, private, link-local, CGNAT,
// 0.0.0.0/8, 198.18.0.0/15, multicast, nor an IPv4-mapped, NAT64 or 6to4
// form of one). A name is refused before it is looked up when it is
// localhost or under .localhost, a single label (which a search domain or
// mDNS answers from the owner's network), or an IPv4 address spelled as
// the C library reads one (2130706433, 0x7f.1, 0177.0.0.1, 127.1), which
// it would connect to. A trailing dot is the same name.
func publicAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	h := strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if h == "" {
		return nil, ErrNotPublic
	}
	if a, err := netip.ParseAddr(h); err == nil {
		if a.Zone() != "" || !netguard.Public(a) {
			return nil, ErrNotPublic
		}
		return []netip.Addr{a.Unmap()}, nil
	}
	if strings.Contains(h, "%") || h == "localhost" || strings.HasSuffix(h, ".localhost") || !strings.Contains(h, ".") || numericName(h) {
		return nil, ErrNotPublic
	}
	addrs, err := LookupIP(ctx, h)
	if err != nil || len(addrs) == 0 {
		return nil, ErrUnresolved
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if a.Zone() != "" || !netguard.Public(a) {
			return nil, ErrNotPublic
		}
		out = append(out, a.Unmap())
	}
	return out, nil
}

// numericName says every label of h is a number as inet_aton reads one:
// decimal, octal (a leading 0) or hex (0x…).
func numericName(h string) bool {
	for _, l := range strings.Split(h, ".") {
		if l == "" {
			return false
		}
		digits := l
		if strings.HasPrefix(l, "0x") {
			digits = l[2:]
			if strings.Trim(digits, "0123456789abcdef") != "" {
				return false
			}
			continue
		}
		if strings.Trim(digits, "0123456789") != "" {
			return false
		}
	}
	return true
}

// proxyAddr is the host:port a transport dials for the proxy u.
func proxyAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = proxySchemes[u.Scheme]
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// guardDial is dial, but a connection made for a request whose proxy is
// public-only (the choice its context names, With) goes to a public
// address alone (dialPublic). A transport with a choice dials that proxy,
// or a loopback host direct (Func goes around the proxy for it): a
// loopback address other than the proxy's own is let through, and any
// other address is judged as the proxy's, whatever its spelling.
func guardDial(dial DialFunc) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c := choiceOf(ctx)
		if c == "" || !publicOnly(c) {
			return dial(ctx, network, addr)
		}
		u, err := Parse(c)
		if err != nil {
			return nil, ErrNotPublic
		}
		host, _, _ := net.SplitHostPort(addr)
		if !strings.EqualFold(addr, proxyAddr(u)) && loopback(strings.ToLower(host)) {
			return dial(ctx, network, addr)
		}
		return dialPublic(ctx, dial, network, addr)
	}
}

// GuardDial is guardDial for a transport netproxy didn't make (the
// gateway's image fetch through a provider's proxy).
func GuardDial(dial DialFunc) DialFunc { return guardDial(dial) }

// dialPublic connects to addr when its host is public (publicAddrs), at
// the addresses checked, never by the name again.
func dialPublic(ctx context.Context, dial DialFunc, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, ErrNotPublic
	}
	addrs, err := publicAddrs(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("proxyconnect %s: %w", network, err)
	}
	var last error
	for _, a := range addrs {
		c, err := DialPublicAddr(ctx, dial, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// refusedProxy is the proxy a child process is given in place of a
// public-only one whose address isn't public: under .invalid, a name no
// resolver answers (RFC 6761), so the child's requests fail rather than
// leave by another way.
const refusedProxy = "http://proxy-not-public.invalid:9"

// pinPublic is choice as a child process is given it: when it is
// public-only, with its host checked public and an http:// or SOCKS one's
// host put in its place by the address checked, so a name pointed
// elsewhere after the check reaches nothing new. An https:// proxy keeps
// its name, which its certificate is checked against; it is checked each
// time a child is started with it. Any other choice is as it is.
func pinPublic(choice string) (string, error) {
	if !publicOnly(choice) {
		return choice, nil
	}
	u, err := Parse(choice)
	if err != nil {
		return "", ErrNotPublic
	}
	addrs, err := publicAddrs(context.Background(), u.Hostname())
	if err != nil {
		return "", fmt.Errorf("the proxy %s: %w", Redact(choice), err)
	}
	if u.Scheme == "https" {
		return choice, nil
	}
	_, port, _ := net.SplitHostPort(proxyAddr(u))
	pinned := *u
	pinned.Host = net.JoinHostPort(addrs[0].String(), port)
	return pinned.String(), nil
}

// PinChoice is pinPublic for a child process netproxy doesn't start (the
// plugin host): a public-only proxy whose address isn't public is a proxy
// that reaches nothing (refusedProxy), never "" (the global one).
func PinChoice(choice string) string {
	p, err := pinPublic(choice)
	if err != nil {
		return refusedProxy
	}
	return p
}
