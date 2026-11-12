package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// brandAsk is one ask of h by c, as T-118's sweep makes it.
func brandAsk(h http.Handler, c brandCaller, method, path, body string) (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
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

// magpieUsageKey is a magpie_ key of a reply's JSON, not one the model
// wrote inside a string (escaped there: \"magpie_deferred\").
var magpieUsageKey = regexp.MustCompile(`(^|[^\\])"magpie_[a-z_]+"\s*:`)

// A plugin's usage fields for magpie (the Cursor plugin's magpie_deferred,
// Kiro's and Trae's magpie_cache_unknown) were relayed to the client as the
// plugin wrote them. A caller that isn't magpie's (a key's holder, on this
// computer or another) is told none of them, in a reply whole or streamed,
// Chat's or Messages'; the model's words, magpie and a "magpie_" key in a
// call's arguments among them, are as it wrote them. The owner and another
// magpie get the bytes Front gave before (no brandWriter).
func TestRepliesSayNoMagpieUsageToOtherCallers(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	const text = `the magpie said \"magpie_deferred\"`
	const args = `{\"magpie_cache_unknown\":1}`
	chatUsage := `"usage":{"prompt_tokens":185670,"completion_tokens":2240,"prompt_tokens_details":{"cached_tokens":181266},"magpie_cache_unknown":true,"magpie_deferred":{"prompt_tokens":181270,"completion_tokens":1913,"cached_tokens":177533,"cache_write_tokens":3735}}`
	chatUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"`+text+`"}}]}`+"\n\n")
			// the usage chunk cut across two writes, as a socket may
			io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],`)
			w.(http.Flusher).Flush()
			io.WriteString(w, chatUsage+"}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"`+text+`","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"`+args+`"}}]},"finish_reason":"tool_calls"}],`+chatUsage+`}`)
	}))
	defer chatUp.Close()
	antUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"a1\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"magpie_cache_unknown\":true}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\""+text+"\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3,\"magpie_cache_unknown\":true}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"a1","content":[{"type":"text","text":"`+text+`"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"magpie_cache_unknown":1}}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":3,"magpie_cache_unknown":true}}`)
	}))
	defer antUp.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: chatUp.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "ant", Name: "Ant", Key: "k", Anthropic: antUp.URL, Models: []string{"a1"}}); err != nil {
		t.Fatal(err)
	}
	_, secrets := newCaller(t, "Laptop")
	s := New()
	front := s.Front()
	plain := socketEntry(s.dumpFirst(lanGuard(s.Handler()))) // Front without its brandWriter

	told := 0 // replies that told magpie's usage fields before
	for _, a := range [][2]string{
		{"/v1/chat/completions", `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/chat/completions", `{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"ant/a1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"ant/a1","max_tokens":5,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"plan/m1","max_tokens":5,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"plan/m1","stream":true,"input":"hi"}`},
		{"/v1/chat/completions", `{"model":"ant/a1","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
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
			// the model's call input, a magpie_ key it wrote, is no usage
			const input = `"input":{"magpie_cache_unknown":1}`
			if magpieUsageKey.MatchString(strings.ReplaceAll(ref, input, "")) {
				told++
			}
			if m := magpieUsageKey.FindString(strings.ReplaceAll(got, input, "")); m != "" {
				t.Errorf("%s: told %s: %.800s", name, m, got)
			}
			if strings.Contains(ref, `magpie said`) && !strings.Contains(got, text) {
				t.Errorf("%s: the model's words changed: %.800s", name, got)
			}
			if strings.Contains(ref, args) && !strings.Contains(got, args) || strings.Contains(ref, input) && !strings.Contains(got, input) {
				t.Errorf("%s: the call's arguments changed: %.800s", name, got)
			}
			if !strings.Contains(a[1], `"stream":true`) && !json.Valid([]byte(got)) {
				t.Errorf("%s: not JSON now: %s", name, got)
			}
			if strings.Contains(ref, `"cached_tokens":181266`) && !strings.Contains(got, `"cached_tokens":181266`) {
				t.Errorf("%s: the usage's own counts changed: %.800s", name, got)
			}
		}
	}
	if told < 6 {
		t.Errorf("only %d replies told magpie's usage fields before: the asks reach too few", told)
	}
}

// A turn magpie answers itself (the tool-loop guard's) has an id with no
// magpie in it for a key's holder; the owner's is msg_magpie_ as before.
func TestMadeReplyIDSaysNoMagpie(t *testing.T) {
	v := &countingVendor{}
	toolLoopOn(t, v)
	t.Setenv("MAGPIE_ADDR", "")
	_, secrets := newCaller(t, "Laptop")
	h := New().Front()
	body := toolTurns(provider.Anthropic, toolLoopRepeats, codexRead, false)
	for _, c := range brandCallers(secrets[0]) {
		code, got := brandAsk(h, c, "POST", "/v1/messages", body)
		var reply struct{ ID, Type string }
		if code != 200 || json.Unmarshal([]byte(got), &reply) != nil || reply.Type != "message" {
			t.Fatalf("%s: %d %.400s", c.name, code, got)
		}
		if strings.Contains(reply.ID, "magpie") != c.keeps || !strings.HasPrefix(reply.ID, "msg_") {
			t.Errorf("%s: id %q", c.name, reply.ID)
		}
	}
	if n := v.asked.Load(); n != 0 {
		t.Fatalf("the vendor was asked %d times", n)
	}
}

func TestWithoutMagpieUsage(t *testing.T) {
	for in, want := range map[string]string{
		`{"usage":{"a":1,"magpie_deferred":{"b":2},"c":3}}`:                          `{"usage":{"a":1,"c":3}}`,
		`{"type":"message_start","message":{"usage":{"magpie_cache_unknown":true}}}`: `{"type":"message_start","message":{"usage":{}}}`,
		`{"response":{"usage":{"x":1, "magpie_x" : 2}}}`:                             `{"response":{"usage":{"x":1}}}`,
		`{"choices":[{"delta":{"content":"\"magpie_x\":1"}}]}`:                       `{"choices":[{"delta":{"content":"\"magpie_x\":1"}}]}`,
		`{"usage":{"magpie_x":1`:                                                     `{"usage":{"magpie_x":1`,
		`{"args":{"magpie_x":1},"usage":{"n":1}}`:                                    `{"args":{"magpie_x":1},"usage":{"n":1}}`,
	} {
		if got := string(withoutMagpieUsage([]byte(in))); got != want {
			t.Errorf("withoutMagpieUsage(%s) = %s, want %s", in, got, want)
		}
	}
}

// A stream's error frame naming its status (#4855) fails the try at that
// status for a key's holder too: the writer that takes magpie's usage
// fields out of the reply doesn't hide the holdWriter it is noted on.
func TestKeyHolderStreamErrorFrameSaysItsStatus(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	setup(t, provider.Chat, &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"partial text"}}]}`,
		`data: {"error":{"message":"busy","type":"upstream_error","code":"rate_limit","status":429}}`)})
	_, secrets := newCaller(t, "Laptop")
	s := New()
	code, got := brandAsk(s.Front(), brandCallers(secrets[0])[0], "POST", "/v1/chat/completions",
		`{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(got, `"status":429`) {
		t.Fatalf("the frame was not relayed: %d %s", code, got)
	}
	state := s.Trace(context.Background(), 0, 0)
	if len(state.Routes) != 1 {
		t.Fatalf("routes %d", len(state.Routes))
	}
	var try *Try
	for i := range state.Routes[0].Tries {
		if state.Routes[0].Tries[i].Rest != nil {
			try = &state.Routes[0].Tries[i]
		}
	}
	if try == nil || try.Fail != failRate || try.Rest.Status != 429 {
		t.Fatalf("the try: %+v", state.Routes[0].Tries)
	}
}

// respMagpieUsage is a Responses usage with a plugin's fields for magpie.
const respMagpieUsage = `"usage":{"input_tokens":300,"output_tokens":50,"total_tokens":350,"input_tokens_details":{"cached_tokens":120},"magpie_cache_unknown":true,"magpie_deferred":{"input_tokens":7,"output_tokens":2}}`

// respMagpieUp is a Responses upstream whose reply, whole or streamed,
// carries respMagpieUsage, and whose model wrote a magpie_ key in a call's
// arguments.
func respMagpieUp(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	item := `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"the magpie said \"magpie_deferred\""}]}`
	call := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Bash","arguments":"{\"magpie_cache_unknown\":1}","status":"completed"}`
	done := `{"id":"resp_1","object":"response","model":"m1","status":"completed","output":[` + item + `,` + call + `],` + respMagpieUsage + `}`
	if !strings.Contains(string(b), `"stream":true`) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, done)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":`+call+`}`,
		`data: {"type":"response.completed","response":`+done+`}`))
}

// checkNoMagpieUsage fails a key holder's reply got that tells a magpie_
// usage field, or that lost the model's words or the usage's own counts
// the reply before (ref) had.
func checkNoMagpieUsage(t *testing.T, name, ref, got string) {
	t.Helper()
	const args = `{\"magpie_cache_unknown\":1}`
	if m := magpieUsageKey.FindString(got); m != "" {
		t.Errorf("%s: told %s: %.900s", name, m, got)
	}
	if strings.Contains(ref, `magpie said`) && !strings.Contains(got, `the magpie said \"magpie_deferred\"`) {
		t.Errorf("%s: the model's words changed: %.900s", name, got)
	}
	if strings.Contains(ref, args) && !strings.Contains(got, args) {
		t.Errorf("%s: the call's arguments changed: %.900s", name, got)
	}
	if strings.Contains(ref, `"cached_tokens":120`) && !strings.Contains(got, `"cached_tokens":120`) {
		t.Errorf("%s: the usage's own counts changed: %.900s", name, got)
	}
}

// T-119's guard reached Chat and Messages replies and Responses ones
// translated from Chat. The rest of the ways a successful reply leaves
// (T-123): a native Responses upstream's response.usage, whole and
// streamed, and a group's member answering after another failed, through
// the candidate loop's holdWriter. A key's holder is told no magpie_ usage
// field; the owner and another magpie read the bytes as before.
func TestNativeResponsesAndGroupSayNoMagpieUsage(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	up := httptest.NewServer(http.HandlerFunc(respMagpieUp))
	defer up.Close()
	var downAsked atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downAsked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"down","type":"server_error"}}`)
	}))
	defer down.Close()
	if err := provider.Save(provider.Provider{ID: "rs", Name: "RS", Key: "k", Responses: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "bad", Name: "Bad", Key: "k", Responses: down.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"bad/m1", "rs/m1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	_, secrets := newCaller(t, "Laptop")
	s := New()
	front := s.Front()
	plain := socketEntry(s.dumpFirst(lanGuard(s.Handler()))) // Front without its brandWriter

	told := 0
	for _, a := range [][2]string{
		{"/v1/responses", `{"model":"rs/m1","input":"hi"}`},
		{"/v1/responses", `{"model":"rs/m1","stream":true,"input":"hi"}`},
		{"/v1/responses", `{"model":"group/g","input":"hi"}`},
		{"/v1/responses", `{"model":"group/g","stream":true,"input":"hi"}`},
	} {
		for _, c := range brandCallers(secrets[0]) {
			refCode, ref := brandAsk(plain, c, "POST", a[0], a[1])
			code, got := brandAsk(front, c, "POST", a[0], a[1])
			name := c.name + ": " + a[1]
			if code != 200 || refCode != 200 {
				t.Fatalf("%s: %d (before %d) %.400s", name, code, refCode, got)
			}
			if c.keeps {
				if replyStamps.ReplaceAllString(got, "#") != replyStamps.ReplaceAllString(ref, "#") {
					t.Errorf("%s: answered %.600q, before %.600q", name, got, ref)
				}
				continue
			}
			if magpieUsageKey.MatchString(ref) {
				told++
			}
			checkNoMagpieUsage(t, name, ref, got)
			if !strings.Contains(a[1], `"stream":true`) && !json.Valid([]byte(got)) {
				t.Errorf("%s: not JSON now: %s", name, got)
			}
		}
	}
	if told < 8 {
		t.Errorf("only %d replies told magpie's usage fields before: the asks reach too few", told)
	}
	if downAsked.Load() == 0 {
		t.Error("the group's first member was never asked: no reply went through the candidate loop's hold")
	}
}

// A plugin's account that only streams (the Cursor plugin's), asked for a
// whole Responses reply, is asked streamed and its stream gathered into
// the one reply the client gets (gathers). That reply carries the
// plugin's magpie_deferred to a key's holder no more than a relayed one
// does (T-123); the owner's carries it as before.
func TestGatheredReplySaysNoMagpieUsage(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	up := httptest.NewServer(http.HandlerFunc(respMagpieUp))
	defer up.Close()
	p := provider.PluginForTest("cursor", "cursor")
	p.Name, p.Key, p.Models, p.Responses = "Cursor", "k", []string{"m1"}, up.URL+"/v1"
	p.Account.Stream = true
	const body = `{"model":"m1","input":"hi"}`
	if !gathers(p, provider.Responses, []byte(body)) {
		t.Fatal("the ask isn't gathered")
	}
	s := New()
	var ref string
	for _, keep := range []bool{true, false} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		b := &brandWriter{w: rec, r: r, length: -1, marked: true, keep: keep}
		r = r.WithContext(context.WithValue(r.Context(), brandKey{}, b))
		var u Usage
		status, msg, _ := s.passthrough(b, r, p, provider.Responses, "m1", []byte(body), &u)
		b.finish()
		got := rec.Body.String()
		if status != 200 || rec.Code != 200 || !json.Valid([]byte(got)) {
			t.Fatalf("keep %v: %d %d %s %.600s", keep, status, rec.Code, msg, got)
		}
		if keep {
			if !magpieUsageKey.MatchString(got) {
				t.Fatalf("the owner's gathered reply has no magpie_ field to take out: %.600s", got)
			}
			ref = got
			continue
		}
		checkNoMagpieUsage(t, "a key's holder", ref, got)
	}
}

// A Responses socket's turn (Codex's) is a reply like any other: a key's
// holder reads no magpie_ usage field in its frames, the owner's are as
// the upstream sent them (T-123).
func TestSocketTurnSaysNoMagpieUsage(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	sockUp(t, &respUp{reply: func(w http.ResponseWriter, r *http.Request, n int) { respMagpieUp(w, r) }})
	_, secrets := newCaller(t, "Laptop")
	_, ts := sockServer(t)
	var ref string
	for _, keyed := range []bool{false, true} {
		h := http.Header{}
		if keyed {
			h.Set("Authorization", "Bearer "+secrets[0])
		}
		c, resp, err := dialSock(t, ts, "/v1/responses", h)
		if err != nil {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Fatalf("keyed %v: %v (%d)", keyed, err, code)
		}
		frames := c.turn(create("", userHi))
		if last(frames).Get("type").String() != "response.completed" {
			t.Fatalf("keyed %v: %v", keyed, frames)
		}
		var all []string
		for _, f := range frames {
			all = append(all, f.Raw)
		}
		got := strings.Join(all, "\n")
		if !keyed {
			if !magpieUsageKey.MatchString(got) {
				t.Fatalf("the owner's frames have no magpie_ field to take out: %.900s", got)
			}
			ref = got
			continue
		}
		checkNoMagpieUsage(t, "a key's holder", ref, got)
	}
}
