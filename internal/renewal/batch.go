package renewal

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The batch-proxy reader (T-214, magpie-console's docs/magpie-api.md
// §16.3): the lines of an area's POST /ips/batch, read as the window's
// Batch add reads them (internal/gui/assets/plugins/accounts/ipsbatch.js
// ipsBatchParse). The two share their test lines
// (testdata/batch_lines.json), so a format one reads the other reads the
// same. It only reads: whether the proxy is public, and whose its IP is,
// is the caller's to judge.

// BatchMost is the most lines one batch reads.
const BatchMost = 100

// BatchTypes are the types a line may write, by what it writes ("socks"
// is SOCKS5), as ipsbatch.js' IPS_BATCH_TYPES.
var BatchTypes = map[string]string{"http": "http", "https": "https", "socks5": "socks5", "socks5h": "socks5h", "socks": "socks5"}

// BatchProxy is one line read: its type, host (an IPv6 one without its
// brackets, V6 set), port, login, the IP when the host is one (as
// ipsCanon writes it), and the proxy's address as it is kept.
type BatchProxy struct {
	Type string `json:"type"`
	Host string `json:"host"`
	V6   bool   `json:"v6"`
	Port string `json:"port"`
	User string `json:"user"`
	Pass string `json:"pass"`
	IP   string `json:"ip"`
	URL  string `json:"url"`
}

// BatchError is why a line wasn't read, in the window's words.
type BatchError struct{ Why string }

func (e *BatchError) Error() string { return e.Why }

// jsSpace is what JavaScript's \s and trim() take as white space. The
// patterns below spell out both cases where the window's use /i: Go's
// (?i) folds ſ and the Kelvin sign to s and k, JavaScript's doesn't.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	batchComment = regexp.MustCompile(`[` + jsSpace + `]+#.*$`)
	batchRefresh = regexp.MustCompile(`\[[^\]]*\]$`)
	batchScheme  = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*)://`)
	batchSlashes = regexp.MustCompile(`/+$`)
	batchSpaces  = regexp.MustCompile(`[` + jsSpace + `]+`)
	batchHasWS   = regexp.MustCompile(`[` + jsSpace + `]`)
	batchV6      = regexp.MustCompile(`^\[[0-9a-fA-F:.]+\]$`)
	batchV4      = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)
	batchName    = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)
	batchLetter  = regexp.MustCompile(`[a-zA-Z]`)
	batchPort    = regexp.MustCompile(`^\d{1,5}$`)
	batchSplit   = regexp.MustCompile(`^(\[[^\]]*\]|[^:]*):(.*)$`)
	batchNL      = regexp.MustCompile(`\r?\n`)
)

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return 0x2000 <= r && r <= 0x200a
}

type batchHP struct {
	host, ip, port string
	v6             bool
}

// batchHost is a host as written, bracketed when IPv6, or false.
func batchHost(h string) (batchHP, bool) {
	if batchV6.MatchString(h) {
		// a browser's URL takes no IPv4 address in [ ], Go's ParseIP does
		ip := net.ParseIP(h[1 : len(h)-1])
		if ip == nil || !strings.Contains(h, ":") {
			return batchHP{}, false
		}
		v := whatwgV6(ip)
		return batchHP{host: v, v6: true, ip: canonIP(ip, v)}, true
	}
	if batchV4.MatchString(h) {
		for _, o := range strings.Split(h, ".") {
			if n, _ := strconv.Atoi(o); n > 255 {
				return batchHP{}, false
			}
		}
		return batchHP{host: h, ip: h}, true
	}
	// a name: a dot and a letter in it, so a username is never taken for one
	if batchName.MatchString(h) && batchLetter.MatchString(h) && len(h) <= 253 {
		return batchHP{host: strings.ToLower(h)}, true
	}
	if strings.ToLower(h) == "localhost" {
		return batchHP{host: "localhost"}, true
	}
	return batchHP{}, false
}

func batchHostPort(h, p string) (batchHP, bool) {
	if !batchPort.MatchString(p) {
		return batchHP{}, false
	}
	n, _ := strconv.Atoi(p)
	if n < 1 || n > 65535 {
		return batchHP{}, false
	}
	x, ok := batchHost(h)
	x.port = strconv.Itoa(n)
	return x, ok
}

// whatwgV6 is ip as a browser's URL writes an IPv6 host: as Go writes it,
// but an IPv4-mapped one in hex groups too (::ffff:cb00:7107).
func whatwgV6(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return "::ffff:" + strconv.FormatUint(uint64(v4[0])<<8|uint64(v4[1]), 16) + ":" + strconv.FormatUint(uint64(v4[2])<<8|uint64(v4[3]), 16)
	}
	return ip.String()
}

// canonIP is an IPv6 host's IP as ips.js' ipsCanon writes it: an
// IPv4-mapped one as its IPv4 address.
func canonIP(ip net.IP, v6 string) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return v6
}

// batchColons is s split at each : outside [ ], an empty part kept.
func batchColons(s string) []string {
	var parts []string
	cur := ""
	for i := 0; i < len(s); i++ {
		j := -1
		if s[i] == '[' {
			if k := strings.IndexByte(s[i:], ']'); k >= 0 {
				j = i + k
			}
		}
		switch {
		case j > 0:
			cur += s[i : j+1]
			i = j
		case s[i] == ':':
			parts = append(parts, cur)
			cur = ""
		default:
			cur += s[i : i+1]
		}
	}
	return append(parts, cur)
}

// encodeURIComponent is JavaScript's: all but A-Z a-z 0-9 - _ . ! ~ * ' ( )
// escaped, as UTF-8.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c)|0x100, 16)[1:]))
		}
	}
	return b.String()
}

// decodeURIComponent is JavaScript's, s as it was when it can't be
// decoded (a lone %, or bytes that aren't UTF-8).
func decodeURIComponent(s string) string {
	v, err := url.PathUnescape(s)
	if err != nil || !utf8.ValidString(v) {
		return s
	}
	return v
}

// ParseBatchProxy reads one line as ipsbatch.js' ipsBatchParse does:
// [type://]host:port[:user[:password]], [type://]user:password:host:port,
// [type://]user:password@host:port, host:port@user:password, each with an
// IPv6 host in [ ], the parts split by : or by spaces; a [refresh URL]
// and a " # comment" at the end are read past. typ is the type of a line
// that writes none. A line read as host:port:user:password and as
// user:password:host:port alike takes the one whose host is an IP, and is
// ambiguous when both or neither are.
func ParseBatchProxy(line, typ string) (BatchProxy, error) {
	s := jsTrim(batchRefresh.ReplaceAllString(jsTrim(batchComment.ReplaceAllString(jsTrim(line), "")), ""))
	if m := batchScheme.FindStringSubmatch(s); m != nil {
		t, ok := BatchTypes[strings.ToLower(m[1])]
		if !ok {
			return BatchProxy{}, &BatchError{strings.ToUpper(m[1]) + " proxies aren't supported: use HTTP, HTTPS or SOCKS5"}
		}
		typ = t
		s = s[len(m[0]):]
	}
	s = batchSlashes.ReplaceAllString(s, "")
	var hp batchHP
	found := false
	user, pass := "", ""
	if at := strings.LastIndex(s, "@"); at >= 0 {
		a, b := s[:at], s[at+1:]
		split := func(v string) (string, string) {
			if m := batchSplit.FindStringSubmatch(v); m != nil {
				return m[1], m[2]
			}
			return "", ""
		}
		login := a
		if hp, found = batchHostPort(split(b)); !found {
			hp, found = batchHostPort(split(a))
			login = b
		}
		// a login pasted from a URL is escaped already
		if c := strings.Index(login, ":"); c < 0 {
			user = decodeURIComponent(login)
		} else {
			user, pass = decodeURIComponent(login[:c]), decodeURIComponent(login[c+1:])
		}
	} else {
		// the parts, an empty one kept (::pass is no username)
		var parts []string
		if batchHasWS.MatchString(s) {
			parts = batchSpaces.Split(s, -1)
		} else {
			parts = batchColons(s)
		}
		if len(parts) > 4 {
			return BatchProxy{}, &BatchError{"Too many parts: write it as username:password@host:port"}
		}
		var first, last batchHP
		okFirst := len(parts) >= 2
		if okFirst {
			first, okFirst = batchHostPort(parts[0], parts[1])
		}
		okLast := len(parts) == 4
		if okLast {
			last, okLast = batchHostPort(parts[2], parts[3])
		}
		if okFirst && okLast && (first.ip == "") == (last.ip == "") {
			return BatchProxy{}, &BatchError{"Read either way: write it as username:password@host:port"}
		}
		switch {
		case okFirst && (!okLast || first.ip != ""):
			hp, found = first, true
			if len(parts) > 2 {
				user = parts[2]
			}
			if len(parts) > 3 {
				pass = parts[3]
			}
		case okLast:
			hp, found, user, pass = last, true, parts[0], parts[1]
		}
	}
	if !found {
		return BatchProxy{}, &BatchError{"No host and port found in it"}
	}
	u := typ + "://"
	if user != "" || pass != "" {
		u += encodeURIComponent(user)
		if pass != "" {
			u += ":" + encodeURIComponent(pass)
		}
		u += "@"
	}
	if hp.v6 {
		u += "[" + hp.host + "]"
	} else {
		u += hp.host
	}
	u += ":" + hp.port
	return BatchProxy{Type: typ, Host: hp.host, V6: hp.v6, Port: hp.port, User: user, Pass: pass, IP: hp.ip, URL: u}, nil
}

// BatchLine is one line of a batch that says something: its number from
// 1, and what was read or why not.
type BatchLine struct {
	N     int
	Proxy BatchProxy
	Err   error
}

// ReadBatch is the lines of text that say something (not blank, not a
// # comment), each read as typ unless it writes a type, as ipsbatch.js'
// ipsBatchLines: one read twice is said so.
func ReadBatch(text, typ string) []BatchLine {
	var out []BatchLine
	seen := map[string]bool{}
	for i, l := range batchNL.Split(text, -1) {
		if t := jsTrim(l); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		x := BatchLine{N: i + 1}
		x.Proxy, x.Err = ParseBatchProxy(l, typ)
		if x.Err == nil {
			k := ProxyKey(x.Proxy.URL)
			if seen[k] {
				x.Proxy, x.Err = BatchProxy{}, &BatchError{"The same proxy as a line above"}
			}
			seen[k] = true
		}
		out = append(out, x)
	}
	return out
}

// ProxyKey is a proxy as it is told apart from another, as ipsbatch.js'
// ipsProxyKey: its scheme, its login and its host in lower case, a bare
// one http.
func ProxyKey(u string) string {
	raw := strings.TrimSpace(u)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	x, err := url.Parse(raw)
	if err != nil {
		return u
	}
	pw, _ := x.User.Password()
	return strings.ToLower(x.Scheme) + "//" + x.User.Username() + ":" + pw + "@" + strings.ToLower(x.Host)
}
