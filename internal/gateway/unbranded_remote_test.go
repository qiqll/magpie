package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/yetone/magpie/internal/provider"
)

// madeIDQuoted is a made id as a JSON string of the reply's own, not one
// the model wrote inside another (escaped there: \"msg_magpie_1\").
var madeIDQuoted = regexp.MustCompile(`(^|[^\\])"[a-z]+_magpie_[0-9]+"`)

// A remote magpie answers this one as another magpie, so a reply it made
// itself (a title, a compaction, the tool-loop guard's) carries its
// resp_magpie_/msg_magpie_ ids. Relayed through this one to a key's
// holder they were as the remote wrote them (T-122): now they are said
// <kind>_<n>, as this one's own are, whole or streamed, a token cut across
// two writes too; the model's words with such an id in them are as it
// wrote them. The owner and another magpie read every byte as before.
func TestRemoteMagpieMadeIDsSayNoMagpie(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	const text = `the id \"msg_magpie_9\" stays`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "magpie/") {
			t.Errorf("the remote was asked as %q", r.Header.Get("User-Agent"))
		}
		stream := strings.Contains(string(b), `"stream":true`)
		switch {
		case strings.HasSuffix(r.URL.Path, "/responses") && stream:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_magpie_1762\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
			// the item's id cut across two writes, as a socket may
			io.WriteString(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_mag")
			w.(http.Flusher).Flush()
			io.WriteString(w, "pie_1762\",\"role\":\"assistant\",\"content\":[]}}\n\n"+
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_magpie_1762\",\"output_index\":0,\"content_index\":0,\"delta\":\""+text+"\"}\n\n"+
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_magpie_1762\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_magpie_1762\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\""+text+"\"}]},{\"type\":\"compaction\",\"id\":\"cmp_magpie_1762\",\"encrypted_content\":\"x\"}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		case strings.HasSuffix(r.URL.Path, "/responses"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"resp_magpie_1762","object":"response","model":"m1","status":"completed","output":[{"type":"message","id":"msg_magpie_1762","role":"assistant","content":[{"type":"output_text","text":"`+text+`"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_magpie_1762","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"`+text+`"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":3}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_magpie_1762","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"`+text+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3}}`)
		}
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
		{"/v1/messages", `{"model":"office/m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/chat/completions", `{"model":"office/m1","messages":[{"role":"user","content":"hi"}]}`},
	} {
		for _, c := range brandCallers(secrets[0]) {
			refCode, ref := brandAsk(plain, c, "POST", a[0], a[1])
			code, got := brandAsk(front, c, "POST", a[0], a[1])
			name := c.name + ": " + a[0] + " " + a[1]
			if code != 200 || refCode != 200 {
				t.Fatalf("%s: %d (before %d) %.400s", name, code, refCode, got)
			}
			if c.keeps {
				if replyStamps.ReplaceAllString(got, "#") != replyStamps.ReplaceAllString(ref, "#") {
					t.Errorf("%s: answered %.600q, before %.600q", name, got, ref)
				}
				continue
			}
			if madeIDQuoted.MatchString(ref) {
				told++
			}
			if m := madeIDQuoted.FindString(got); m != "" {
				t.Errorf("%s: told %s: %.800s", name, m, got)
			}
			if strings.Contains(ref, `_magpie_1762"`) && !strings.Contains(got, `_1762"`) {
				t.Errorf("%s: the id is gone, not said: %.800s", name, got)
			}
			if !strings.Contains(got, text) {
				t.Errorf("%s: the model's words changed: %.800s", name, got)
			}
		}
	}
	if told < 6 {
		t.Errorf("only %d replies told the remote's made ids before: the asks reach too few", told)
	}
}

// unmadeBody says every made id whole, read in any pieces, and leaves
// everything else as it came.
func TestUnmadeBody(t *testing.T) {
	for in, want := range map[string]string{
		`{"id":"resp_magpie_1762","item":{"id":"msg_magpie_1"}}`:   `{"id":"resp_1762","item":{"id":"msg_1"}}`,
		`data: {"id":"cmp_magpie_99"}` + "\n\n":                    `data: {"id":"cmp_99"}` + "\n\n",
		`{"text":"see \"msg_magpie_1\" here"}`:                     `{"text":"see \"msg_magpie_1\" here"}`,
		`{"text":"msg_magpie_1 in words"}`:                         `{"text":"msg_magpie_1 in words"}`,
		`{"id":"msg_magpie_"}`:                                     `{"id":"msg_magpie_"}`,
		`{"id":"Msg_magpie_1"}`:                                    `{"id":"Msg_magpie_1"}`,
		`{"a":"x\\","id":"msg_magpie_7"}`:                          `{"a":"x\\","id":"msg_7"}`,
		`{"id":"msg_magpie_12"`:                                    `{"id":"msg_12"`,
		`"msg_magpie_12"`:                                          `"msg_12"`,
		strings.Repeat("a", 100) + `"resp_magpie_123456789012345"`: strings.Repeat("a", 100) + `"resp_123456789012345"`,
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
	in := []byte(`data: {"type":"x","id":"resp_magpie_1762","item":{"id":"msg_magpie_1762"},"t":"\"msg_magpie_1\""}` + "\n\n")
	want := bytes.ReplaceAll(bytes.ReplaceAll(in, []byte(`"resp_magpie_`), []byte(`"resp_`)), []byte(`{"id":"msg_magpie_`), []byte(`{"id":"msg_`))
	for cut := range in {
		got, _ := io.ReadAll(&unmadeBody{ReadCloser: io.NopCloser(io.MultiReader(bytes.NewReader(in[:cut]), bytes.NewReader(in[cut:])))})
		if !bytes.Equal(got, want) {
			t.Errorf("cut at %d: %s", cut, got)
		}
	}
}
