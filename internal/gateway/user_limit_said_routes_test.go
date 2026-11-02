package gateway

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// T-069: bound by user, a vendor's 429 on a route that isn't a turn — a
// drawing and an edit, the image MCP server's generate_image, an
// embedding, a rerank, audio, a video and the asks after it — is told
// only userLimitSaid, as a turn's is (T-067). Before, the vendor's words
// and its host went to the caller (saidOf of the vendor's error).

// limitRoutesUp is a vendor whose every API answers how: "quota" a 429
// out of its allowance, Retry-After 30; "refused" a 429 refusing the
// prompt; "bad" a 400; "ok" each route's success. Its model list names
// vid-1 a video model.
type limitRoutesUp struct {
	*httptest.Server
	mu  sync.Mutex
	how string
}

func newLimitRoutesUp(t *testing.T, how string) *limitRoutesUp {
	t.Helper()
	u := &limitRoutesUp{how: how}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		u.mu.Lock()
		how := u.how
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"object":"list","data":[{"id":"vid-1","object":"model","kind":"video"}]}`)
			return
		}
		switch how {
		case "quota":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"VENDOR-QUOTA You exceeded your current quota, please check your plan and billing details"}}`)
		case "refused":
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"invalid_request","message":"VENDOR-REFUSED unapproved channel"}}`)
		case "bad":
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"VENDOR-BAD Unknown parameter: foo"}}`)
		default:
			switch {
			case strings.HasPrefix(r.URL.Path, "/v1/images/"):
				io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+b64+`"}]}`)
			case r.URL.Path == "/v1/videos":
				io.WriteString(w, `{"id":"video_1","object":"video","created_at":1790000000,"status":"queued","progress":0,"model":"vid-1"}`)
			default:
				w.WriteHeader(404)
				io.WriteString(w, `{"error":{"message":"no route"}}`)
			}
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *limitRoutesUp) set(how string) {
	u.mu.Lock()
	u.how = how
	u.mu.Unlock()
}

// limitRoute is a route that isn't a turn, asked of the providers
// limitRoutesSetup saves.
type limitRoute struct {
	name string
	ask  func(t *testing.T, s *Server, user string) *httptest.ResponseRecorder
}

func limitRoutes(t *testing.T) []limitRoute {
	audio, audioType := audioFormBody(t, "model", "twin/whisper-1")
	edit := `{"model":"twin/gpt-image-1","prompt":"bluer","images":[{"image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngBytes) + `"}]}`
	post := func(path, ctype string, body []byte) func(*testing.T, *Server, string) *httptest.ResponseRecorder {
		return func(_ *testing.T, s *Server, user string) *httptest.ResponseRecorder {
			if user == "" {
				return unboundAt(s, path, ctype, body)
			}
			return userAt(s, path, ctype, body, user)
		}
	}
	video := []byte(`{"model":"studio/vid-1","prompt":"a kite"}`)
	return []limitRoute{
		{name: "images/generations", ask: post("/v1/images/generations", "", []byte(`{"model":"twin/gpt-image-1","prompt":"a magpie"}`))},
		{name: "images/edits", ask: post("/v1/images/edits", "", []byte(edit))},
		{name: "embeddings", ask: post("/v1/embeddings", "", []byte(`{"model":"twin/embed-1","input":"a magpie"}`))},
		{name: "rerank", ask: post("/v1/rerank", "", []byte(`{"model":"twin/rerank-1","query":"birds","documents":["a magpie"]}`))},
		{name: "audio/transcriptions", ask: post("/v1/audio/transcriptions", audioType, audio)},
		{name: "audio/translations", ask: post("/v1/audio/translations", audioType, audio)},
		{name: "audio/speech", ask: post("/v1/audio/speech", "", []byte(`{"model":"twin/tts-1","input":"hi","voice":"coral"}`))},
		{name: "videos", ask: post("/v1/videos", "", video)},
		{name: "videos/get", ask: videoAsk(video, "")},
		{name: "videos/content", ask: videoAsk(video, "/content")},
	}
}

// videoAsk starts a video as user while the vendor answers, then asks
// after it at the tail ("" for how it goes) once it answers the 429 the
// test set.
func videoAsk(start []byte, tail string) func(*testing.T, *Server, string) *httptest.ResponseRecorder {
	return func(t *testing.T, s *Server, user string) *httptest.ResponseRecorder {
		t.Helper()
		up := limitUpOf(t)
		how := up.how
		up.set("ok")
		var rec *httptest.ResponseRecorder
		if user == "" {
			rec = unboundAt(s, "/v1/videos", "", start)
		} else {
			rec = userAt(s, "/v1/videos", "", start, user)
		}
		up.set(how)
		if rec.Code != 200 {
			t.Fatalf("video not started: %d %s", rec.Code, rec.Body.String())
		}
		var v struct{ ID string }
		json.Unmarshal(rec.Body.Bytes(), &v)
		req := httptest.NewRequest("GET", "/v1/videos/"+v.ID+tail, nil)
		if user != "" {
			req.Header.Set(UserHeader, user)
		}
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		return out
	}
}

// limitUps holds each test's vendor for videoAsk.
var limitUps sync.Map // *testing.T → *limitRoutesUp

func limitUpOf(t *testing.T) *limitRoutesUp {
	v, ok := limitUps.Load(t)
	if !ok {
		t.Fatal("no vendor")
	}
	return v.(*limitRoutesUp)
}

// limitRoutesSetup saves twin, every model but videos, and studio, the
// video model, each with two keys at a vendor answering how.
func limitRoutesSetup(t *testing.T, how string) *limitRoutesUp {
	t.Helper()
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	u := newLimitRoutesUp(t, how)
	limitUps.Store(t, u)
	t.Cleanup(func() { limitUps.Delete(t) })
	if err := provider.Save(provider.Provider{ID: "twin", Name: "Twin", Chat: u.URL + "/v1", Anthropic: u.URL,
		Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}, Models: []string{"m", "gpt-image-1", "embed-1", "rerank-1", "whisper-1", "tts-1"}}); err != nil {
		t.Fatal(err)
	}
	saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: u.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}})
	return u
}

// unboundAt posts body to path with no user, Content-Type ctype ("" for
// JSON).
func unboundAt(s *Server, path, ctype string, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
	if ctype == "" {
		ctype = "application/json"
	}
	req.Header.Set("Content-Type", ctype)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// errorMessageOf is the message of an OpenAI-shaped error body.
func errorMessageOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("not an error body: %d %s", rec.Code, rec.Body.String())
	}
	return e.Error.Message
}

// limitSaidHeaders are the headers of a 429 the fix leaves as they were.
var limitSaidHeaders = []string{"Retry-After", UserLimitHeader, UserUntilHeader}

// A bound user's vendor 429 on each route that isn't a turn: told at
// 429 only userLimitSaid, none of the vendor's words, its name or its
// host; the headers are as unbound's, the vendor's words the call's
// error. With no user bound the vendor's words are told, as before.
func TestUserLimitSaidOnEveryRoute(t *testing.T) {
	for _, rt := range limitRoutes(t) {
		t.Run(rt.name, func(t *testing.T) {
			up := limitRoutesSetup(t, "quota")
			unbound := rt.ask(t, New(), "")
			if unbound.Code != 429 || !strings.Contains(unbound.Body.String(), "VENDOR-QUOTA") || strings.Contains(unbound.Body.String(), userLimitSaid) {
				t.Fatalf("unbound: %d %s", unbound.Code, unbound.Body.String())
			}
			noRests()
			s := byUserUpTo(t, New(), 1)
			rec := rt.ask(t, s, "alice")
			if rec.Code != 429 {
				t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
			}
			if msg := errorMessageOf(t, rec); msg != userLimitSaid {
				t.Fatalf("told %q, want %q", msg, userLimitSaid)
			}
			body := rec.Body.String()
			for _, w := range []string{"VENDOR-", "quota", "Twin", "Studio", "twin", "studio", strings.TrimPrefix(up.URL, "http://")} {
				if strings.Contains(body, w) {
					t.Fatalf("told %q: %s", w, body)
				}
			}
			for _, h := range limitSaidHeaders {
				if got, was := rec.Header().Get(h), unbound.Header().Get(h); got != was {
					t.Fatalf("%s %q, unbound %q: %v", h, got, was, rec.Header())
				}
			}
			if !strings.HasPrefix(rt.name, "videos/") {
				if c := lastCallOf(t, s); c.Status != 429 || !strings.Contains(c.Error, "VENDOR-QUOTA") {
					t.Fatalf("call %d %q", c.Status, c.Error)
				}
			}
		})
	}
}

// Bound by user, a 429 refusing the prompt (no limit: asked again it is
// refused again) and a 400 keep the vendor's words, as a turn's do.
func TestUserLimitSaidOnlyOfALimitOnEveryRoute(t *testing.T) {
	for _, how := range []string{"refused", "bad"} {
		for _, rt := range limitRoutes(t) {
			t.Run(how+"/"+rt.name, func(t *testing.T) {
				limitRoutesSetup(t, how)
				rec := rt.ask(t, byUserUpTo(t, New(), 1), "alice")
				if rec.Code < 400 || strings.Contains(rec.Body.String(), userLimitSaid) || !strings.Contains(rec.Body.String(), "VENDOR-") {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

// The image MCP server's generate_image of a bound user its vendor
// answers with a 429: the tool's error says userLimitSaid, never the
// vendor's words, its name or its host. With no user bound, the vendor's
// words, as before.
func TestUserLimitSaidOfGenerateImage(t *testing.T) {
	for _, user := range []string{"alice", ""} {
		t.Run(cmp.Or(user, "unbound"), func(t *testing.T) {
			up := limitRoutesSetup(t, "quota")
			if err := catalog.SaveLive("twin", up.URL+"/v1", []catalog.Model{{ID: "m"}}); err != nil {
				t.Fatal(err)
			}
			s := New()
			hdr := []string{}
			if user != "" {
				s, hdr = byUserUpTo(t, s, 1), []string{UserHeader, user}
			}
			m := readImageReply(t, imagePost(lanGuard(s.Handler()), "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie", "model": "twin/gpt-image-1"}), hdr...))
			text := m.Result.Content[0].Text
			if !m.Result.IsError {
				t.Fatalf("drawn: %+v", m)
			}
			if user == "" {
				if !strings.Contains(text, "VENDOR-QUOTA") || strings.Contains(text, userLimitSaid) {
					t.Fatalf("unbound told %q", text)
				}
				return
			}
			if text != "no image: "+userLimitSaid {
				t.Fatalf("told %q", text)
			}
		})
	}
}
