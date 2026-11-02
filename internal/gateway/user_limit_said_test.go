package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// T-067: bound by user, a 429 the caller is told says only userLimitSaid,
// as one refused before any account is asked: the vendor's words and name
// went through when the last of the user's accounts ran out mid-request,
// and in the stream's response.failed after a rate limit waited out.

// limitSaidUpstream stands in for the ChatGPT backend, every account
// answering 429 as how says: "quota" out of its allowance, "rate" rate
// limited for after seconds. Asks counts the requests.
func limitSaidUpstream(t *testing.T, how string, after string) (asks *atomic.Int32) {
	t.Helper()
	asks = new(atomic.Int32)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		asks.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch how {
		case "quota":
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"VENDOR-QUOTA The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
		default:
			w.Header().Set("Retry-After", after)
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"rate_limit_exceeded","code":"rate_limit_exceeded","message":"VENDOR-RATE Rate limit reached for gpt-5.5"}}`)
		}
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return asks
}

// limitSaidEntry is a way in to Codex's model: its path and body.
type limitSaidEntry struct {
	name, path, body string
}

func limitSaidEntries(stream bool) []limitSaidEntry {
	st := "false"
	if stream {
		st = "true"
	}
	return []limitSaidEntry{
		{"codex", CodexPath + "/responses", `{"model":"gpt-5.5","stream":` + st + `,"input":"ping"}`},
		{"responses", "/v1/responses", `{"model":"codex/gpt-5.5","stream":` + st + `,"input":"ping"}`},
		{"chat", "/v1/chat/completions", `{"model":"codex/gpt-5.5","stream":` + st + `,"messages":[{"role":"user","content":"ping"}]}`},
		{"messages", "/v1/messages", `{"model":"codex/gpt-5.5","stream":` + st + `,"max_tokens":10,"messages":[{"role":"user","content":"ping"}]}`},
	}
}

// limitSaidPost posts e as user ("" for none).
func limitSaidPost(s *Server, e limitSaidEntry, user string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", e.path, strings.NewReader(e.body))
	if e.name == "codex" {
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
	} else {
		req.Header.Set("Authorization", "Bearer magpie")
	}
	if user != "" {
		req.Header.Set(UserHeader, user)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// lastCallOf is the last call s recorded.
func lastCallOf(t *testing.T, s *Server) Call {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recent) == 0 {
		t.Fatal("no call recorded")
	}
	return s.recent[len(s.recent)-1]
}

// saysOnlyTheLimit fails unless body says userLimitSaid and none of the
// vendor's words nor its name.
func saysOnlyTheLimit(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, userLimitSaid) {
		t.Fatalf("not told %q: %s", userLimitSaid, body)
	}
	for _, w := range []string{"VENDOR-", "usage limit has been reached", "Rate limit reached", "Codex", "OpenAI", "ChatGPT"} {
		if strings.Contains(body, w) {
			t.Fatalf("told %q: %s", w, body)
		}
	}
}

// The last of a bound user's accounts out of its allowance mid-request,
// no spare to add: the caller is told userLimitSaid at 429, its limit and
// when in the headers as before, streamed or not, at every way in. The
// vendor's words are the call's error. With no user bound they are what
// the caller is told, as before.
func TestUserLimitSaidMidRequest(t *testing.T) {
	for _, stream := range []bool{true, false} {
		for _, e := range limitSaidEntries(stream) {
			name := e.name + "/stream"
			if !stream {
				name = e.name + "/whole"
			}
			t.Run(name, func(t *testing.T) {
				s, _, _ := strictUserSetup(t, provider.AffinitySession)
				asks := limitSaidUpstream(t, "quota", "")
				rec := limitSaidPost(s, e, "alice")
				if rec.Code != 429 || asks.Load() != 2 {
					t.Fatalf("%d after %d asks: %v %s", rec.Code, asks.Load(), rec.Header(), rec.Body.String())
				}
				saysOnlyTheLimit(t, rec.Body.String())
				if limit, until := limitOf(t, rec); limit != "accounts" || !near(until, 2*time.Hour) {
					t.Fatalf("limit %q until %v: %v", limit, until, rec.Header())
				}
				if c := lastCallOf(t, s); c.Status != 429 || !strings.Contains(c.Error, "VENDOR-QUOTA") {
					t.Fatalf("call %d %q", c.Status, c.Error)
				}
			})
		}
	}
	t.Run("unbound", func(t *testing.T) {
		strictUserSetup(t, provider.AffinitySession)
		limitSaidUpstream(t, "quota", "")
		s := New()
		for _, e := range limitSaidEntries(false) {
			rec := limitSaidPost(s, e, "")
			if rec.Code != 429 || !strings.Contains(rec.Body.String(), "VENDOR-QUOTA") || strings.Contains(rec.Body.String(), userLimitSaid) {
				t.Fatalf("%s: %d %s", e.name, rec.Code, rec.Body.String())
			}
			noRests()
		}
	})
}

// A bound user's accounts rate limited a moment at a time, each waited
// out with the agent kept alive (its stream's 200 sent), until patience
// is spent: the stream's failure says userLimitSaid, as a rate limit,
// never the vendor's words. With no user bound it says them, as before.
func TestUserLimitSaidInTheStream(t *testing.T) {
	for _, e := range limitSaidEntries(true) {
		t.Run(e.name, func(t *testing.T) {
			s, _, _ := strictUserSetup(t, provider.AffinitySession)
			asks := limitSaidUpstream(t, "rate", "1")
			wasAfter, wasEvery := keepHeldAfter, keepHeldEvery
			keepHeldAfter, keepHeldEvery = 50*time.Millisecond, 100*time.Millisecond
			t.Cleanup(func() { keepHeldAfter, keepHeldEvery = wasAfter, wasEvery })
			rec := limitSaidPost(s, e, "alice")
			if rec.Code != 200 || asks.Load() < 3 {
				t.Fatalf("not kept alive and asked again: %d after %d asks: %s", rec.Code, asks.Load(), rec.Body.String())
			}
			saysOnlyTheLimit(t, rec.Body.String())
			if c := lastCallOf(t, s); !strings.Contains(c.Error, "VENDOR-RATE") {
				t.Fatalf("call %d %q", c.Status, c.Error)
			}
		})
	}
	t.Run("unbound", func(t *testing.T) {
		strictUserSetup(t, provider.AffinitySession)
		limitSaidUpstream(t, "rate", "1")
		wasAfter, wasEvery := keepHeldAfter, keepHeldEvery
		keepHeldAfter, keepHeldEvery = 50*time.Millisecond, 100*time.Millisecond
		t.Cleanup(func() { keepHeldAfter, keepHeldEvery = wasAfter, wasEvery })
		rec := limitSaidPost(New(), limitSaidEntries(true)[0], "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "VENDOR-RATE") || strings.Contains(rec.Body.String(), userLimitSaid) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
}

// A System One decision of a bound user its account answers with a 429:
// told userLimitSaid, the vendor's Retry-After kept. With no user bound,
// the vendor's words, as before.
func TestUserLimitSaidOfADecision(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"type":"rate_limit_exceeded","message":"VENDOR-RATE Rate limit reached for jev-latest"}}`)
	}))
	t.Cleanup(up.Close)
	t.Cleanup(noRests)
	decide := func(bound bool) *httptest.ResponseRecorder {
		t.Helper()
		noRests()
		writeUsers(t, userFile{Users: map[string]map[string]userBinding{"codex": {
			"alice": {Who: "codex@me@example.com", Account: "me@example.com", At: time.Now().UTC()},
		}}})
		s := byUserUpTo(t, New(), 1)
		p, err := provider.Find("codex")
		if err != nil {
			t.Fatal(err)
		}
		var cs []candidate
		var pl planned
		for _, c := range seatCandidates(*p) {
			c.p.Decide, c.model = up.URL+"/v1", "jev-latest"
			cs, pl.order = append(cs, c), append(pl.order, Weighed{Provider: "codex"})
		}
		body := []byte(`{"model":"codex/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`)
		r := httptest.NewRequest("POST", "/v1/systemone", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		if !bound {
			s.serveDecideTries(rec, r, body, "codex/jev-latest", time.Now(), cs, pl, nil, nil)
			return rec
		}
		r.Header.Set(UserHeader, "alice")
		own, opl, u, err := s.usersOn(r, *p, cs, pl)
		if err != nil || u == nil {
			t.Fatalf("not bound: %v", err)
		}
		s.serveDecideTries(rec, r, body, "codex/jev-latest", time.Now(), own, opl, nil, u)
		return rec
	}
	rec := decide(true)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("bound: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	saysOnlyTheLimit(t, rec.Body.String())
	if rec := decide(false); rec.Code != 429 || !strings.Contains(rec.Body.String(), "VENDOR-RATE") {
		t.Fatalf("unbound: %d %s", rec.Code, rec.Body.String())
	}
}
