package gateway

// A caller that is neither another magpie nor this computer's owner (a
// gateway key's holder, a user of --bind=user, anyone from another
// machine) reads no "magpie" in an error the gateway answers it with. The
// owner's agents and another magpie keep every word as it was, to the
// byte: their replies are passed on untouched.
//
// It is one exit: brandWriter, which the gateway's listener (Front, the
// page port's client routes too) puts first on every request, so every
// error leaves through it, whoever wrote it (writeError,
// http.Error, a guard's own JSON, an encoder's error event, a vendor's
// body passed on as it came, a recorder's copy). withCaller and lanGuard
// tell it who the caller is (markCaller), once the address and the key are
// known. Only an error's own places are
// said again (unbrandReply): every string value of a failure reply's body
// (a status of 400 or more, no model output in it), and in a reply that
// succeeded, the error of an error event ("type":"error", a top-level
// "error", response.failed's response.error) in its stream or as its
// whole body. A key, the model's text and every header are left as they
// came. A Responses socket's own error frames and close reasons are said
// the same way (responsesSocket.say).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/access"
)

// keepsBrand is whether r's caller reads magpie's errors as they are: another
// magpie (magpieAsks), or this computer's owner (a local request with no
// gateway key). A local request that carries a key's form (managedKey) is
// the key's holder, the one refused too: its key isn't known as a KeyID.
func keepsBrand(r *http.Request) bool {
	return magpieAsks(r) || local(r) && access.Caller(r.Context()).KeyID == "" && !managedKey(r)
}

// brandWriter is the client's writer, marked with who the caller is.
type brandWriter struct {
	w http.ResponseWriter

	mu   sync.Mutex
	keep bool // the caller reads the errors as they are
	mode int  // what is done with the reply, once it begins
	code int  // the status held while the body is gathered
	buf  bytes.Buffer
	// sent: the reply's headers have gone to w
	sent bool
}

const (
	brandUndecided = iota
	brandPass      // as it is
	brandWhole     // gathered, then said again whole
	brandLines     // a stream, each error event said again
	brandSniff     // a reply that succeeded: held until its first bytes say what it is
)

// brandWholeMost is the most of a failure reply gathered before it is said;
// past it, what came is said and the rest passed as it is.
const brandWholeMost = 4 << 20

// brandLineMost is the most of a stream's unfinished line held back to be
// read whole; a longer one is passed as it is.
const brandLineMost = 64 << 10

// markCaller tells w's brandWriter who r's caller is. Called before
// anything is written; once the reply has begun it changes nothing.
func markCaller(w http.ResponseWriter, r *http.Request) {
	b := brandOf(w)
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.mode == brandUndecided {
		b.keep = keepsBrand(r)
	}
	b.mu.Unlock()
}

// brandOf is the brandWriter w writes through, nil for none.
func brandOf(w http.ResponseWriter) *brandWriter {
	for w != nil {
		if b, ok := w.(*brandWriter); ok {
			return b
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

// keepsBrandOn is whether the caller w answers reads magpie's errors as they
// are: true when w goes through no brandWriter (Handler called without
// Front, as a test does).
func keepsBrandOn(w http.ResponseWriter) bool {
	b := brandOf(w)
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.keep
}

// branded is next with each reply's writer marked: the gateway's own
// listener (Front), first after socketEntry, so a Responses socket's every
// turn is marked again on its own writer.
func branded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if brandOf(w) != nil {
			next.ServeHTTP(w, r)
			return
		}
		b := &brandWriter{w: w, keep: keepsBrand(r)}
		next.ServeHTTP(b, r)
		b.finish()
	})
}

func (b *brandWriter) Header() http.Header { return b.w.Header() }

// Unwrap is for http.ResponseController and a WebSocket's Hijack.
func (b *brandWriter) Unwrap() http.ResponseWriter { return b.w }

func (b *brandWriter) WriteHeader(code int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writeHeader(code)
}

// writeHeader decides the reply's mode by its status and type. Called with
// mu held.
func (b *brandWriter) writeHeader(code int) {
	if b.mode != brandUndecided {
		if b.mode == brandPass || b.mode == brandLines {
			b.w.WriteHeader(code) // net/http says a second one is superfluous
		}
		return
	}
	if code < 200 {
		b.w.WriteHeader(code) // an informational status, the reply still to come
		return
	}
	h := b.w.Header()
	ct := strings.ToLower(h.Get("Content-Type"))
	streams := strings.Contains(ct, "event-stream") || strings.Contains(ct, "ndjson")
	switch {
	case b.keep:
		b.mode = brandPass
	case code >= 400:
		b.mode, b.code = brandWhole, code
		return
	case streams && h.Get("Content-Length") == "":
		b.mode = brandLines
	case streams:
		// a stream of a known length, played back whole
		b.mode, b.code = brandWhole, code
		return
	default:
		b.mode, b.code = brandSniff, code
		return
	}
	b.code = code
	b.sendHeader()
}

func (b *brandWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mode == brandUndecided {
		b.writeHeader(http.StatusOK)
	}
	switch b.mode {
	case brandSniff:
		if len(bytes.TrimLeft(p, " \t\r\n")) == 0 {
			// a reply kept alive with blank lines: they go now, the body
			// after them is still to be told
			b.sendHeader()
			return b.w.Write(p)
		}
		if !errorShaped(p) {
			b.mode = brandPass
			b.sendHeader()
			return b.w.Write(p)
		}
		b.mode = brandWhole
		fallthrough
	case brandWhole:
		b.buf.Write(p)
		if b.buf.Len() >= brandWholeMost || !b.isStream() && json.Valid(b.buf.Bytes()) {
			// a JSON error comes in one write; the client has it now, as a
			// reply magpie answers while the handler is still held must
			if err := b.emit(); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	case brandLines:
		b.buf.Write(p)
		if err := b.lines(false); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return b.w.Write(p)
}

// sendHeader sends the status held, once. Called with mu held.
func (b *brandWriter) sendHeader() {
	if !b.sent {
		b.sent = true
		b.w.WriteHeader(b.code)
	}
}

func (b *brandWriter) Flush() { _ = b.FlushError() }

// FlushError is http.ResponseController's Flush, which looks for it before
// it unwraps: a reply gathered whole is said at a flush, as it stands, and
// a held status goes, so a flush never holds back what it was asked to send.
func (b *brandWriter) FlushError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.mode {
	case brandWhole:
		if err := b.emit(); err != nil {
			return err
		}
	case brandSniff:
		b.sendHeader()
	}
	return http.NewResponseController(b.w).Flush()
}

// finish is the handler done: whatever is held goes out.
func (b *brandWriter) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.mode {
	case brandWhole:
		b.emit()
	case brandSniff:
		b.mode = brandPass
		b.sendHeader()
	case brandLines:
		b.lines(true)
	}
}

func (b *brandWriter) isStream() bool {
	ct := strings.ToLower(b.w.Header().Get("Content-Type"))
	return strings.Contains(ct, "event-stream") || strings.Contains(ct, "ndjson")
}

// emit says the gathered body again and sends it, its length set to what
// is sent; what comes after is passed as it is. A body whose length went
// out already in a status sent at a flush is sent as it came. Called with
// mu held.
func (b *brandWriter) emit() error {
	body := b.buf.Bytes()
	out := body
	switch {
	case b.isStream():
		out = unbrandLines(body, b.code >= 400)
	case b.code >= 400:
		out = unbrandReply(body)
	default:
		out = unbrandErrorAt(body)
	}
	if h := b.w.Header(); h.Get("Content-Length") != "" {
		if b.sent {
			out = body
		} else {
			h.Set("Content-Length", strconv.Itoa(len(out)))
		}
	}
	b.mode = brandPass
	b.sendHeader()
	_, err := b.w.Write(out)
	b.buf.Reset()
	return err
}

// lines sends a stream's whole lines, each error event's said again, and
// holds back an unfinished one, unless last or it is too long to hold.
// Called with mu held.
func (b *brandWriter) lines(last bool) error {
	data := b.buf.Bytes()
	cut := bytes.LastIndexByte(data, '\n') + 1
	if last || len(data)-cut > brandLineMost {
		cut = len(data)
	}
	if cut == 0 {
		return nil
	}
	out := unbrandLines(data[:cut], false)
	_, err := b.w.Write(out)
	rest := append([]byte(nil), data[cut:]...)
	b.buf.Reset()
	b.buf.Write(rest)
	return err
}

// errorShaped is whether the first bytes of a reply that succeeded are an
// error's body: an object that opens with "error", or with "type":"error"
// (json.Marshal puts a map's keys in order, so writeError's comes first).
// A JSON-RPC error opens with "jsonrpc" and is passed as it came.
func errorShaped(p []byte) bool {
	p = bytes.TrimLeft(p, " \t\r\n")
	if len(p) == 0 || p[0] != '{' {
		return false
	}
	p = bytes.TrimLeft(p[1:], " \t\r\n")
	return bytes.HasPrefix(p, []byte(`"error"`)) || bytes.HasPrefix(p, []byte(`"type":"error"`)) || bytes.HasPrefix(p, []byte(`"type": "error"`))
}

// hasMagpie is whether b says magpie, in any case.
func hasMagpie(b []byte) bool {
	for i := 0; i+6 <= len(b); i++ {
		if c := b[i] | 0x20; c == 'm' && bytes.EqualFold(b[i:i+6], []byte("magpie")) {
			return true
		}
	}
	return false
}

// unbrandLines says again the error events of a stream's whole lines (an
// SSE data line, or an NDJSON line, whose JSON is an error event), every
// other line as it came. With all, the stream is a failure reply's and
// every data line's strings are said again.
func unbrandLines(b []byte, all bool) []byte {
	if !hasMagpie(b) {
		return b
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	for i, ln := range lines {
		if !hasMagpie(ln) {
			continue
		}
		body := bytes.TrimRight(ln, "\r\n")
		end := ln[len(body):]
		prefix := []byte(nil)
		if d, ok := bytes.CutPrefix(body, []byte("data:")); ok {
			prefix = body[:len(body)-len(d)]
			body = d
		}
		var said []byte
		switch {
		case !bytes.HasPrefix(bytes.TrimLeft(body, " "), []byte("{")):
			if all && prefix != nil {
				said = []byte(unbrand(string(body)))
			} else {
				continue // an event's name, a comment, a line of text
			}
		case all:
			said = unbrandReply(body)
		default:
			said = unbrandErrorAt(body)
		}
		lines[i] = append(append(append([]byte(nil), prefix...), said...), end...)
	}
	return bytes.Join(lines, nil)
}

// unbrandErrorAt says again the error in an event or a reply that
// succeeded, by where its protocol puts one: a "type":"error" event's
// error and message, response.failed's response.error, a top-level error
// (Chat's and Gemini's, a JSON-RPC error, Ollama's). Anything else in it,
// the model's output among it, is left as it came.
func unbrandErrorAt(b []byte) []byte {
	if !hasMagpie(b) || !gjson.ValidBytes(b) {
		return b
	}
	var at []string
	switch gjson.GetBytes(b, "type").String() {
	case "error":
		at = []string{"error", "message"}
	case "response.failed":
		at = []string{"response.error"}
	default:
		at = []string{"error"}
	}
	for _, path := range at {
		v := gjson.GetBytes(b, path)
		if !v.Exists() || v.Index <= 0 || !hasMagpie([]byte(v.Raw)) {
			continue
		}
		said := unbrandReply([]byte(v.Raw))
		b = append(append(append([]byte(nil), b[:v.Index]...), said...), b[v.Index+len(v.Raw):]...)
	}
	return b
}

// unbrandReply says again a failure reply's body: every string value of
// its JSON, the keys as they are; text that isn't JSON, whole.
func unbrandReply(b []byte) []byte {
	if !hasMagpie(b) {
		return b
	}
	if !json.Valid(b) {
		// text, or JSON cut short: whole, as words (what is said in a
		// name's place has no quote or backslash, so JSON stays as it was)
		return []byte(unbrand(string(b)))
	}
	var out []byte
	last := 0
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		j := i + 1
		for j < len(b) && b[j] != '"' {
			if b[j] == '\\' {
				j++
			}
			j++
		}
		tok := b[i : j+1]
		k := j + 1
		for k < len(b) && (b[k] == ' ' || b[k] == '\t' || b[k] == '\r' || b[k] == '\n') {
			k++
		}
		isKey := k < len(b) && b[k] == ':'
		if !isKey && hasMagpie(tok) {
			var s string
			if json.Unmarshal(tok, &s) == nil {
				out = append(out, b[last:i]...)
				out = append(out, jsonString(unbrand(s))...)
				last = j + 1
			}
		}
		i = j
	}
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

// jsonString is s as a JSON string, its <, > and & as they are (#260).
func jsonString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

var magpieWord = regexp.MustCompile(`(?i)magpie`)

// articles are the words before magpie that "gateway" takes as they are:
// "a magpie gateway key", "another magpie", "this computer's magpie".
var articles = map[string]bool{"a": true, "an": true, "the": true, "this": true, "that": true, "another": true, "other": true,
	"your": true, "each": true, "every": true, "one": true, "no": true, "any": true, "computer's": true, "own": true,
	"remote": true} // a Remote magpie provider is a Remote gateway provider

// unbrand is msg with magpie said as the gateway: "magpie's quotas" as
// "the gateway's quotas", "another magpie" as "another gateway", "an
// enabled magpie gateway key" as "an enabled gateway key". A name (a
// route, a header, a key's form, a variable: /v1/magpie/usage,
// X-Magpie-Session, sk-magpie-, MAGPIE_ADDR) is the interface the caller
// calls, and is left as it is (T-113's D3: the interface doesn't change).
func unbrand(msg string) string {
	idx := magpieWord.FindAllStringIndex(msg, -1)
	if idx == nil {
		return msg
	}
	var out strings.Builder
	last := 0
	for _, m := range idx {
		i, j := m[0], m[1]
		if i < last {
			continue
		}
		word := msg[i:j]
		var prev, next byte
		if i > 0 {
			prev = msg[i-1]
		}
		if j < len(msg) {
			next = msg[j]
		}
		out.WriteString(msg[last:i])
		last = j
		plural := next == 's' && !isWordByte(byteAt(msg, j+1))
		inName := strings.IndexByte("/_.-", prev) >= 0 || strings.IndexByte("/_-", next) >= 0 ||
			isWordByte(prev) || isWordByte(next) && !plural
		if inName {
			// a route, a header, a key's form or a tool's id is the
			// interface the caller calls, kept as it is spelled
			out.WriteString(word)
			continue
		}
		if plural {
			last++
		}
		// "magpie gateway key" is a gateway key, "magpie's gateway limit"
		// the gateway's limit
		if rest := msg[last:]; len(rest) >= 8 && rest[0] == ' ' && strings.EqualFold(rest[1:8], "gateway") && !isWordByte(byteAt(rest, 8)) {
			last++
			continue
		} else if len(rest) >= 10 && rest[:2] == "'s" && strings.EqualFold(rest[2:10], " gateway") && !isWordByte(byteAt(rest, 10)) {
			last += 10
			plural = false
			out.WriteString(theAt(msg[:i]) + "gateway's")
			continue
		}
		g := "gateway"
		if plural {
			g = "gateways"
		}
		switch strings.ToLower(prevWord(msg[:i])) {
		case "run", "with":
			// a command's hint, "run magpie heap": the gateway's command
			if byteAt(msg, last) == ' ' && !plural {
				out.WriteString("the gateway's command")
				continue
			}
		}
		if articles[strings.ToLower(prevWord(msg[:i]))] {
			out.WriteString(g)
			continue
		}
		out.WriteString(theAt(msg[:i]) + g)
	}
	out.WriteString(msg[last:])
	return out.String()
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func byteAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// prevWord is the word s ends with, past the spaces after it.
func prevWord(s string) string {
	s = strings.TrimRight(s, " ")
	k := strings.LastIndexAny(s, " \t\n(\"“:,;")
	return strings.TrimLeft(s[k+1:], "'‘")
}

// theAt is "the " after s, "The " where it begins a sentence.
func theAt(s string) string {
	s = strings.TrimRight(s, " \"'“(")
	if s == "" || strings.ContainsAny(s[len(s)-1:], ".!?\n") {
		return "The "
	}
	return "the "
}
