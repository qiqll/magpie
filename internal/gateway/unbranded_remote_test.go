package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/yetone/magpie/internal/provider"
)

// madeIDValue is a made id as an id's value in a reply's JSON.
var madeIDValue = regexp.MustCompile(`id"\s*:\s*"[a-z_-]*_magpie_[0-9]+"`)

// A remote magpie answers this one as another magpie, so a reply it made
// itself carries its made ids: here a real one, the tool-loop guard's turn
// as another magpie is answered it on Responses (resp_msg_magpie_<n>,
// streamed and whole), Chat (chatcmpl-msg_magpie_<n>) and Messages
// (msg_magpie_<n>). Relayed through this one to a key's holder they were
// as the remote wrote them (T-122): now each id is said without magpie, a
// stream's cut across two writes too, and the rest of the reply is as it
// came. The owner and another magpie read every byte as before.
func TestRemoteMagpieMadeIDsSayNoMagpie(t *testing.T) {
	toolLoopOn(t, &countingVendor{})
	t.Setenv("MAGPIE_ADDR", "")
	type recorded struct{ ctype, body string }
	replies := map[string]recorded{}
	real := New().Front()
	for _, c := range []struct {
		path  string
		proto provider.Protocol
		whole bool
	}{
		{"/v1/responses", provider.Responses, false},
		{"/v1/responses", provider.Responses, true},
		{"/v1/chat/completions", provider.Chat, true},
		{"/v1/messages", provider.Anthropic, true},
	} {
		r := httptest.NewRequest("POST", c.path, strings.NewReader(toolTurns(c.proto, toolLoopRepeats, codexRead, !c.whole)))
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", "magpie/0.1.2000")
		w := httptest.NewRecorder()
		real.ServeHTTP(w, r)
		if w.Code != 200 || !madeIDValue.MatchString(w.Body.String()) {
			t.Fatalf("the remote's own %s (whole %v): %d %.600s", c.path, c.whole, w.Code, w.Body.String())
		}
		replies[c.path+strings.Repeat(" whole", map[bool]int{true: 1}[c.whole])] = recorded{w.Header().Get("Content-Type"), w.Body.String()}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "magpie/") {
			t.Errorf("the remote was asked as %q", r.Header.Get("User-Agent"))
		}
		path := strings.TrimPrefix(r.URL.Path, "/v1")
		if path == "/messages" || path == "/chat/completions" || !strings.Contains(string(b), `"stream":true`) {
			path += " whole"
		}
		rec, ok := replies["/v1"+path]
		if !ok {
			t.Errorf("asked %s %s", r.URL.Path, b)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", rec.ctype)
		// the first id cut across two writes, as a socket may
		cut := strings.Index(rec.body, "_magpie_") + 4
		io.WriteString(w, rec.body[:cut])
		w.(http.Flusher).Flush()
		io.WriteString(w, rec.body[cut:])
	}))
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "rk", Models: []string{"m1"},
		Chat: srv.URL + "/v1", Responses: srv.URL + "/v1", Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	_, secrets := newCaller(t, "Laptop")
	s := New()
	front := s.Front()
	plain := socketEntry(s.dumpFirst(lanGuard(s.Handler()))) // Front without its brandWriter

	told := 0 // replies that told the remote's made ids before
	for _, a := range [][2]string{
		{"/v1/responses", `{"model":"office/m1","stream":true,"input":"hi"}`},
		{"/v1/responses", `{"model":"office/m1","input":"hi"}`},
		{"/v1/chat/completions", `{"model":"office/m1","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"office/m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		for _, c := range brandCallers(secrets[0]) {
			refCode, ref := brandAsk(plain, c, "POST", a[0], a[1])
			code, got := brandAsk(front, c, "POST", a[0], a[1])
			name := c.name + ": " + a[0] + " " + a[1]
			if code != 200 || refCode != 200 {
				t.Fatalf("%s: %d (before %d) %.400s", name, code, refCode, got)
			}
			norm := func(s string) string { return replyStamps.ReplaceAllString(s, "#") }
			if c.keeps {
				if norm(got) != norm(ref) {
					t.Errorf("%s: answered %.600q, before %.600q", name, got, ref)
				}
				continue
			}
			if madeIDValue.MatchString(ref) {
				told++
			}
			if m := madeIDValue.FindString(got); m != "" {
				t.Errorf("%s: told %s: %.800s", name, m, got)
			}
			// nothing but the ids changed
			if want := strings.ReplaceAll(ref, "_magpie_", "_"); norm(got) != norm(want) {
				t.Errorf("%s: answered %.800q, want %.800q", name, got, want)
			}
		}
	}
	if told < 8 {
		t.Errorf("only %d replies told the remote's made ids before: the asks reach too few", told)
	}
}

// A turn a remote magpie kept is kept under the remote's own id, which is
// what this one names when the next turn goes as a delta, though the reply
// said it without magpie to a key's holder (T-122): naming the id as said,
// the remote wouldn't find it, and the route would be sent whole for
// everyone for half an hour.
func TestRemoteMagpieKeptTurnUnderItsOwnID(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	freshRemoteTurns(t)
	hop := &hopLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := hop.add(r)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get(KeepTurnHeader) != "" {
			w.Header().Set(KeptTurnHeader, "1")
		}
		// the remote holds the turn under its own id only
		if strings.Contains(string(b), "previous_response_id") && !strings.Contains(string(b), `"resp_magpie_7"`) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Previous response not found.","type":"invalid_request_error","param":"previous_response_id","code":"previous_response_not_found"}}`)
			return
		}
		io.WriteString(w, `{"id":"resp_magpie_7","object":"response","model":"m1","status":"completed","output":[{"type":"message","id":"msg_magpie_7","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "rk", Models: []string{"m1"},
		Chat: srv.URL + "/v1", Responses: srv.URL + "/v1", Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	_, secrets := newCaller(t, "Laptop")
	h := New().Front()
	key := map[string]string{"Authorization": "Bearer " + secrets[0]}
	const ua = "codex_cli_rs/0.50.0"
	rec := askHop(t, h, ua, storeFalseTurn("office/m1", bigImage), key)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"msg_7"`) || strings.Contains(rec.Body.String(), "_magpie_") {
		t.Fatalf("turn 1: %d %.400s", rec.Code, rec.Body.String())
	}
	said := `{"type":"message","id":"msg_7","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`
	if rec = askHop(t, h, ua, storeFalseTurn("office/m1", bigImage, said, userAsks("and now?")), key); rec.Code != 200 {
		t.Fatalf("turn 2: %d %.400s", rec.Code, rec.Body.String())
	}
	if hop.n() != 2 || hop.body(1)["previous_response_id"] != "resp_magpie_7" || strings.Contains(hop.raw(1), "QUFB") {
		t.Fatalf("turn 2 to the remote, %d asks: %.300s", hop.n(), hop.raw(hop.n()-1))
	}
}

// unmadeBody says every made id that is an id's value whole, read in any
// pieces, and leaves everything else as it came; it tells the remote's
// own id for each it said.
func TestUnmadeBodySaysNoMagpie(t *testing.T) {
	for in, want := range map[string]string{
		`{"id":"resp_magpie_1762","item":{"id":"msg_magpie_1"}}`:                        `{"id":"resp_1762","item":{"id":"msg_1"}}`,
		`data: {"id":"cmp_magpie_99"}` + "\n\n":                                         `data: {"id":"cmp_99"}` + "\n\n",
		`{"id":"resp_msg_magpie_7","item_id" : "msg_magpie_7"}`:                         `{"id":"resp_msg_7","item_id" : "msg_7"}`,
		`{"id":"chatcmpl-msg_magpie_7"}`:                                                `{"id":"chatcmpl-msg_7"}`,
		`{"previous_response_id":"resp_magpie_5"}`:                                      `{"previous_response_id":"resp_5"}`,
		`{"text":"see \"msg_magpie_1\" here"}`:                                          `{"text":"see \"msg_magpie_1\" here"}`,
		`{"text":"msg_magpie_1 in words"}`:                                              `{"text":"msg_magpie_1 in words"}`,
		`{"content":"msg_magpie_5"}`:                                                    `{"content":"msg_magpie_5"}`,
		`{"delta":"resp_magpie_5"}`:                                                     `{"delta":"resp_magpie_5"}`,
		`{"id":"msg_magpie_"}`:                                                          `{"id":"msg_magpie_"}`,
		`{"id":"Msg_magpie_1"}`:                                                         `{"id":"Msg_magpie_1"}`,
		`{"a":"x\\","id":"msg_magpie_7"}`:                                               `{"a":"x\\","id":"msg_7"}`,
		`{"id":"msg_magpie_12"`:                                                         `{"id":"msg_12"`,
		`"msg_magpie_12"`:                                                               `"msg_magpie_12"`,
		strings.Repeat("a", 100) + `{"id":"resp_magpie_123456789012345"}`:               strings.Repeat("a", 100) + `{"id":"resp_123456789012345"}`,
		`{"x":"` + strings.Repeat("a", 200) + `","response_id":"resp_magpie_3"}` + "\n": `{"x":"` + strings.Repeat("a", 200) + `","response_id":"resp_3"}` + "\n",
	} {
		for name, r := range map[string]io.Reader{"whole": strings.NewReader(in), "a byte at a time": iotest.OneByteReader(strings.NewReader(in)),
			"halves": io.MultiReader(strings.NewReader(in[:len(in)/2]), strings.NewReader(in[len(in)/2:]))} {
			got, err := io.ReadAll(&unmadeBody{ReadCloser: io.NopCloser(r)})
			if err != nil || string(got) != want {
				t.Errorf("%s: %s read as %s (%v), want %s", name, in, got, err, want)
			}
		}
	}
	// every place a read may end, a made id among a stream's bytes
	in := []byte(`data: {"type":"x","id":"resp_magpie_1762","item":{"id":"resp_msg_magpie_1762"},"t":"\"msg_magpie_1\""}` + "\n\n")
	want := bytes.ReplaceAll(bytes.ReplaceAll(in, []byte(`"resp_magpie_`), []byte(`"resp_`)), []byte(`{"id":"resp_msg_magpie_`), []byte(`{"id":"resp_msg_`))
	for cut := range in {
		u := &unmadeBody{ReadCloser: io.NopCloser(io.MultiReader(bytes.NewReader(in[:cut]), bytes.NewReader(in[cut:])))}
		got, _ := io.ReadAll(u)
		if !bytes.Equal(got, want) {
			t.Errorf("cut at %d: %s", cut, got)
		}
		if o := unmadeOriginal(u, "resp_1762"); o != "resp_magpie_1762" {
			t.Errorf("cut at %d: the remote's own id %q", cut, o)
		}
	}
	if o := unmadeOriginal(io.NopCloser(nil), "resp_1"); o != "resp_1" {
		t.Errorf("another body's id %q", o)
	}
}

// A failed image job's result is read by every call that waits on it: a
// key's holder's reading of it said without magpie is a copy, so calls at
// once don't write one map (a fatal error under -race and without), and
// the owner's next reading of the job still says magpie (T-124).
func TestImageJobKeepsMagpieForTheOwner(t *testing.T) {
	stored := toolResult("Image generation is off in magpie", true)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := brandedAs(httptest.NewRequest("POST", ImageMCPPath, nil), false)
			b, _ := json.Marshal(ownFailure(r.Context(), stored))
			if saysMagpie(string(b)) {
				t.Errorf("said %s", b)
			}
		}()
	}
	wg.Wait()
	if b, _ := json.Marshal(stored); !saysMagpie(string(b)) {
		t.Errorf("the job's result was said again in place: %s", b)
	}
}
