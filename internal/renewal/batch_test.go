package renewal

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// batchLine is a line of testdata/batch_lines.json, which
// internal/gui/tests/proxy-ips-batch-lines.test.cjs holds the window's
// ipsBatchParse to as well: what a line reads as, or why not.
type batchLine struct {
	Line string      `json:"line"`
	Type string      `json:"type"`
	Want *BatchProxy `json:"want"`
	Err  string      `json:"err"`
}

func readBatchLines(t *testing.T) []batchLine {
	t.Helper()
	b, err := os.ReadFile("testdata/batch_lines.json")
	if err != nil {
		t.Fatal(err)
	}
	var lines []batchLine
	if err := json.Unmarshal(b, &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) < 40 {
		t.Fatalf("only %d lines read", len(lines))
	}
	return lines
}

// TestAreaBatchLinesReadAsTheWindowReads: each shared line reads in Go as the
// window's Batch add reads it, the same proxy or the same reason.
func TestAreaBatchLinesReadAsTheWindowReads(t *testing.T) {
	for _, l := range readBatchLines(t) {
		got, err := ParseBatchProxy(l.Line, l.Type)
		switch {
		case l.Want == nil && err == nil:
			t.Errorf("%q read as %+v, want %q", l.Line, got, l.Err)
		case l.Want == nil && err.Error() != l.Err:
			t.Errorf("%q: %q, want %q", l.Line, err, l.Err)
		case l.Want != nil && err != nil:
			t.Errorf("%q: %v, want %+v", l.Line, err, *l.Want)
		case l.Want != nil && got != *l.Want:
			t.Errorf("%q read as\n %+v, want\n %+v", l.Line, got, *l.Want)
		}
	}
}

// TestAreaReadBatchSkipsCommentsAndTwice: blank and # lines say nothing, each
// line keeps its number, and one read twice is said so.
func TestAreaReadBatchSkipsCommentsAndTwice(t *testing.T) {
	text := strings.Join([]string{
		"# my proxies",
		"203.0.113.7:1080",
		"",
		"   ",
		"socks5://203.0.113.7:1080",
		"http://203.0.113.7:1080",
		"not a proxy",
	}, "\r\n")
	got := ReadBatch(text, "socks5")
	if len(got) != 4 {
		t.Fatalf("%d lines, want 4: %+v", len(got), got)
	}
	want := []struct {
		n   int
		url string
		err string
	}{
		{2, "socks5://203.0.113.7:1080", ""},
		{5, "", "The same proxy as a line above"},
		{6, "http://203.0.113.7:1080", ""},
		{7, "", "No host and port found in it"},
	}
	for i, w := range want {
		g := got[i]
		if g.N != w.n || g.Proxy.URL != w.url || (g.Err == nil) != (w.err == "") || (g.Err != nil && g.Err.Error() != w.err) {
			t.Errorf("line %d: %+v, want %+v", i, g, w)
		}
	}
}

// TestAreaProxyKeyFolds: a proxy's scheme and host in either case, and a bare
// one as http, are the same proxy; another login is another.
func TestAreaProxyKeyFolds(t *testing.T) {
	if ProxyKey("SOCKS5://u:p@Host.Example:1080") != ProxyKey("socks5://u:p@host.example:1080") {
		t.Error("case tells two proxies apart")
	}
	if ProxyKey("203.0.113.7:3128") != ProxyKey("http://203.0.113.7:3128") {
		t.Error("a bare proxy isn't http")
	}
	if ProxyKey("socks5://u:p@h.example:1") == ProxyKey("socks5://u:q@h.example:1") {
		t.Error("two logins are one proxy")
	}
}
