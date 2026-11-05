package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/yetone/magpie/internal/provider"
)

// brandCaller is one kind of caller a reply is judged for.
type brandCaller struct {
	name, from, secret, ua string
	keeps                  bool // reads magpie's errors as they are
}

// magpieAsWord matches magpie said as a word, not in a name the caller calls
// (a route, a header, a key's form: /v1/magpie/usage, X-Magpie-Session,
// sk-magpie-), which stays as it is spelled.
var magpieAsWord = regexp.MustCompile(`(?i)(^|[^a-z0-9/_.\-])magpies?($|[^a-z0-9/_\-])`)

func saysMagpie(b string) bool { return magpieAsWord.MatchString(b) }

// replyStamps are the times and ids two replies to one ask differ by.
var replyStamps = regexp.MustCompile(`[0-9a-f]{6,}|[0-9]+`)

func brandCallers(full string) []brandCaller {
	return []brandCaller{
		{name: "a key from another machine", from: "192.168.1.9:5000", secret: full},
		{name: "a key on this computer", from: "127.0.0.1:5000", secret: full},
		{name: "this computer's owner", from: "127.0.0.1:5000", keeps: true},
		{name: "another magpie", from: "192.168.1.9:5000", secret: full, ua: "magpie/0.1.2000", keeps: true},
	}
}

// brandUpstream is a vendor whose errors and model output say magpie: an
// error it sends is said again for a caller that isn't magpie's, its
// model's words never are.
func brandUpstream(t *testing.T) *httptest.Server {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(b), `"stream":true`):
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"the magpie flew\"}}]}\n\n")
			io.WriteString(w, "data: {\"error\":{\"message\":\"Magpie relay overloaded mid-stream\",\"type\":\"server_error\"}}\n\n")
		case strings.Contains(string(b), "plan/m1"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"the magpie flew"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"this magpie key's model is unknown","type":"invalid_request_error"}}`)
		}
	}))
	t.Cleanup(up.Close)
	return up
}

// A caller that is neither another magpie nor this computer's owner reads
// no "magpie" (but in a route's or a header's name) in any error the gateway answers on any of its routes (the
// router's every pattern, a bad body on each), whoever wrote it: magpie's
// own words, a guard's http.Error, a vendor's error passed on. The owner
// and another magpie are answered byte for byte what they were before
// (Front without its brandWriter).
func TestErrorsSayNoMagpieToOtherCallers(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	up := brandUpstream(t)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	_, secrets := newCaller(t, "Laptop")
	full := secrets[0]

	s := New()
	front := s.Front()
	plain := socketEntry(s.dumpFirst(lanGuard(s.Handler()))) // Front without its brandWriter
	do := func(h http.Handler, c brandCaller, method, path, body string) (int, string) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		// an endless stream (the route events) ends with the request
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, path, rd).WithContext(ctx)
		r.RemoteAddr = c.from
		r.Header.Set("Content-Type", "application/json")
		if c.secret != "" {
			r.Header.Set("Authorization", "Bearer "+c.secret)
		}
		if c.ua != "" {
			r.Header.Set("User-Agent", c.ua)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}

	var asks [][3]string // method, path, body
	for _, p := range s.routes().patterns {
		if p == CodexPath+"/" {
			continue // a relay to ChatGPT's own backend: a test never reaches it
		}
		for _, mp := range requestsFor(p) {
			body := ""
			if mp[0] == "POST" || mp[0] == "PUT" || mp[0] == "PATCH" {
				body = "{" // not JSON: each route's own refusal
			}
			asks = append(asks, [3]string{mp[0], mp[1], body})
		}
	}
	// a vendor's error passed on, and one a route says of a model
	asks = append(asks,
		[3]string{"POST", "/v1/chat/completions", `{"model":"plan/m2","messages":[{"role":"user","content":"hi"}]}`},
		[3]string{"POST", "/v1/messages", `{"model":"plan/m2","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`},
		[3]string{"POST", "/v1/responses", `{"model":"plan/m2","input":"hi"}`},
		[3]string{"POST", "/v1/chat/completions", `{"model":"nobody/none","messages":[{"role":"user","content":"hi"}]}`},
		[3]string{"GET", "/v1/responses", ""},
	)

	said := 0 // failures whose words said magpie before
	for _, a := range asks {
		for _, c := range brandCallers(full) {
			refCode, ref := do(plain, c, a[0], a[1], a[2])
			code, got := do(front, c, a[0], a[1], a[2])
			if c.keeps {
				if code != refCode || replyStamps.ReplaceAllString(got, "#") != replyStamps.ReplaceAllString(ref, "#") {
					t.Errorf("%s: %s %s: answered %d %.300q, before %d %.300q", c.name, a[0], a[1], code, got, refCode, ref)
				}
				continue
			}
			if code < 400 {
				continue
			}
			if saysMagpie(ref) {
				said++
			}
			if code != refCode {
				t.Errorf("%s: %s %s: status %d, before %d", c.name, a[0], a[1], code, refCode)
			}
			if saysMagpie(got) {
				t.Errorf("%s: %s %s: %d says magpie: %.400s", c.name, a[0], a[1], code, got)
			}
		}
	}
	if said < 20 {
		t.Errorf("only %d failures said magpie before: the sweep reaches too few of magpie's own words", said)
	}
}

// A stream's error event, a vendor's, is said again for a key's holder;
// the model's words before it stay as the model wrote them.
func TestStreamErrorSaysNoMagpie(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	up := brandUpstream(t)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	_, secrets := newCaller(t, "Laptop")
	h := New().Front()
	for _, c := range brandCallers(secrets[0]) {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		r.RemoteAddr = c.from
		r.Header.Set("Content-Type", "application/json")
		if c.secret != "" {
			r.Header.Set("Authorization", "Bearer "+c.secret)
		}
		if c.ua != "" {
			r.Header.Set("User-Agent", c.ua)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		b := w.Body.String()
		if !strings.Contains(b, "the magpie flew") {
			t.Errorf("%s: the model's own words changed: %.600s", c.name, b)
		}
		if !strings.Contains(b, "overloaded mid-stream") {
			t.Errorf("%s: the stream's error is gone: %.600s", c.name, b)
		}
		if strings.Contains(b, "Magpie relay") == !c.keeps {
			t.Errorf("%s: the error event: %.600s", c.name, b)
		}
	}
}

// A Responses socket's own error frame says no magpie to a key's holder,
// and is as it was to the owner.
func TestSocketErrorSaysNoMagpie(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	_, secrets := newCaller(t, "Laptop")
	srv := httptest.NewServer(New().Front())
	defer srv.Close()
	for _, keyed := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		hdr := http.Header{}
		if keyed {
			hdr.Set("Authorization", "Bearer "+secrets[0])
		}
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: hdr})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"bogus"}`)); err != nil {
			t.Fatal(err)
		}
		_, frame, err := c.Read(ctx)
		c.CloseNow()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(frame), "unsupported_message") {
			t.Fatalf("keyed %v: no error frame: %s", keyed, frame)
		}
		if saysMagpie(string(frame)) == keyed {
			t.Errorf("keyed %v: %s", keyed, frame)
		}
	}
}

func TestUnbrandWords(t *testing.T) {
	for in, want := range map[string]string{
		"magpie's quotas are told to another machine": "The gateway's quotas are told to another machine",
		"Magpie isn't shared on the local network":    "The gateway isn't shared on the local network",
		"send an enabled magpie gateway key":          "send an enabled gateway key",
		"went round magpies that are each other's":    "went round the gateways that are each other's",
		"is told to this computer's magpie only":      "is told to this computer's gateway only",
		"GET /v1/magpie/usage or X-Magpie-Session":    "GET /v1/magpie/usage or X-Magpie-Session",
		"no MAGPIE_ADDR, send sk-magpie-…":            "no MAGPIE_ADDR, send sk-magpie-…",
		"turn off the Remote magpie provider":         "turn off the Remote gateway provider",
		"over loopback: run magpie heap there":        "over loopback: run the gateway's command heap there",
		"couldn't read magpie.json":                   "couldn't read magpie.json",
		"nothing of it":                               "nothing of it",
	} {
		if got := unbrand(in); got != want {
			t.Errorf("unbrand(%q) = %q, want %q", in, got, want)
		}
	}
	// a key's name is a key's, the value said again in JSON stays JSON
	b := unbrandReply([]byte(`{"error":{"message":"magpie said \"no\" <here>","magpie_via":"x"}}`))
	if string(b) != `{"error":{"message":"The gateway said \"no\" <here>","magpie_via":"x"}}` {
		t.Errorf("unbrandReply: %s", b)
	}
	// a reply that succeeded keeps its output; only its error is said again
	b = unbrandErrorAt([]byte(`{"type":"response.failed","response":{"output":[{"text":"magpie"}],"error":{"message":"magpie failed"}}}`))
	if string(b) != `{"type":"response.failed","response":{"output":[{"text":"magpie"}],"error":{"message":"The gateway failed"}}}` {
		t.Errorf("unbrandErrorAt: %s", b)
	}
}

// Who keeps magpie's words: the owner on loopback with no key, and another
// magpie; a key's holder on loopback is not the owner, before its key is
// judged too (a refused one never gets a KeyID).
func TestKeepsBrandCallers(t *testing.T) {
	fresh(t)
	for _, c := range []struct {
		from, auth, ua string
		keeps          bool
	}{
		{"127.0.0.1:5000", "", "", true},
		{"127.0.0.1:5000", "Bearer " + "sk-magpie-0123456789abcdef", "", false},
		{"192.168.1.9:5000", "", "", false},
		{"192.168.1.9:5000", "", "magpie/0.1.2000", true},
		{"127.0.0.1:5000", "", "Claude-Code/2.0", true},
	} {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		r.RemoteAddr = c.from
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		if c.ua != "" {
			r.Header.Set("User-Agent", c.ua)
		}
		if got := keepsBrand(r); got != c.keeps {
			t.Errorf("keepsBrand(%s, %q, %q) = %v, want %v", c.from, c.auth, c.ua, got, c.keeps)
		}
	}
}

// brandWriter's own framing, under a recorder: the length it sends is the
// body's, a body cut by a flush goes as it came, a failure's stream is
// said again event by event, the model's words in a stream never are.
func TestBrandWriterFraming(t *testing.T) {
	const said = `{"error":{"message":"magpie refused it","type":"invalid_request_error"}}` + "\n"
	serve := func(from string, h http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		r.RemoteAddr = from
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }()
			branded(h).ServeHTTP(w, r)
		}()
		return w
	}
	const other, owner = "192.168.1.9:5000", "127.0.0.1:5000"
	lengthOf := func(w *httptest.ResponseRecorder) string { return w.Header().Get("Content-Length") }

	// a failure of a set length in two writes: said again, its length too
	w := serve(other, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(said)))
		w.WriteHeader(400)
		io.WriteString(w, said[:20])
		io.WriteString(w, said[20:])
	})
	if b := w.Body.String(); saysMagpie(b) || !json.Valid([]byte(b)) || lengthOf(w) != strconv.Itoa(len(b)) {
		t.Errorf("a set length in two writes: %q, Content-Length %s", b, lengthOf(w))
	}
	// cut by a flush: as it came, its length as the handler set it
	w = serve(other, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(said)))
		w.WriteHeader(400)
		io.WriteString(w, said[:20])
		w.(http.Flusher).Flush()
		io.WriteString(w, said[20:])
	})
	if b := w.Body.String(); b != said || lengthOf(w) != strconv.Itoa(len(said)) {
		t.Errorf("a set length cut by a flush: %q, Content-Length %s", b, lengthOf(w))
	}
	// a HEAD's failure: no body, its length as set
	w = serve(other, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "50")
		w.WriteHeader(404)
	})
	if lengthOf(w) != "50" || w.Code != 404 {
		t.Errorf("a HEAD's failure: %d, Content-Length %s", w.Code, lengthOf(w))
	}
	// an error body of a reply that succeeded, in pieces
	w = serve(other, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":{"message":"magpie`)
		io.WriteString(w, ` stopped"}}`)
	})
	if b := w.Body.String(); b != `{"error":{"message":"The gateway stopped"}}` || w.Code != 200 {
		t.Errorf("a 200's error in pieces: %d %q", w.Code, b)
	}
	// a failure's stream, flushed event by event: each said again
	w = serve(other, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(503)
		for range 2 {
			io.WriteString(w, "data: {\"message\":\"magpie is busy\"}\n\n")
			w.(http.Flusher).Flush()
		}
	})
	if b := w.Body.String(); saysMagpie(b) || strings.Count(b, "The gateway is busy") != 2 {
		t.Errorf("a failure's stream: %q", b)
	}
	// a stream that succeeded: an error event cut across writes is said
	// again, the model's words as written
	stream := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a mag")
		io.WriteString(w, "pie sings\"}}]}\n\ndata: {\"error\":{\"message\":\"Mag")
		w.(http.Flusher).Flush()
		io.WriteString(w, "pie relay gone\"}}\n\n")
	}
	w = serve(other, stream)
	if b := w.Body.String(); w.Code != 200 || !strings.Contains(b, "a magpie sings") || !strings.Contains(b, `"The gateway relay gone"`) {
		t.Errorf("a stream: %d %q", w.Code, b)
	}
	// the owner: every byte as written
	w = serve(owner, stream)
	if b := w.Body.String(); !strings.Contains(b, "Magpie relay gone") {
		t.Errorf("the owner's stream: %q", b)
	}
	w = serve(owner, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(said)))
		w.WriteHeader(400)
		io.WriteString(w, said)
	})
	if b := w.Body.String(); b != said || lengthOf(w) != strconv.Itoa(len(said)) {
		t.Errorf("the owner's failure: %q", b)
	}
	// a handler that panics after its failure: what it wrote goes out
	w = serve(other, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, "magpie broke")
		panic("boom")
	})
	if b := w.Body.String(); b != "The gateway broke" {
		t.Errorf("after a panic: %q", b)
	}
}
