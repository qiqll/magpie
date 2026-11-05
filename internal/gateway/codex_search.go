package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// AlphaSearchPath is where Codex's own web search, its web.run tool, goes:
// "alpha/search" under the model provider's base URL, so /v1/alpha/search
// on magpie's provider and CodexPath+"/alpha/search" when Codex is signed in
// to ChatGPT. Codex offers web.run since 0.143 for a provider that sends
// x-openai-actor-authorization, as magpie's does, on a model whose catalog
// entry says use_responses_lite, as a ChatGPT account's do in magpie
// (codexcat copies Codex's own entry). Unanswered, the 404 ended the turn:
// Codex takes any failure of web.run as fatal (anton on Discord).
const AlphaSearchPath = "/alpha/search"

// alphaSearchRequest is the part of Codex's SearchRequest magpie reads; the
// rest goes on to the ChatGPT backend as it came.
type alphaSearchRequest struct {
	Model    string `json:"model"`
	Commands struct {
		SearchQuery []alphaQuery `json:"search_query"`
		ImageQuery  []alphaQuery `json:"image_query"`
	} `json:"commands"`
	Settings struct {
		// Codex's web_search mode: false "cached", true "live"
		ExternalWebAccess json.RawMessage `json:"external_web_access"`
	} `json:"settings"`
}

type alphaQuery struct {
	Q       string   `json:"q"`
	Domains []string `json:"domains"`
}

// alphaSearchReply is Codex's SearchResponse: output is what the model is
// shown as the tool's result.
type alphaSearchReply struct {
	EncryptedOutput *string `json:"encrypted_output"`
	Output          string  `json:"output"`
}

// alphaSearch answers POST /v1/alpha/search. A ChatGPT account's model
// goes to the ChatGPT backend's search, as Codex sends it there itself,
// on the account's sign-in. Any other model's search_query is searched as
// magpie searches for a model that can't (webSearch). What magpie can't
// do is said in the tool's output with a 200, which the model reads; a
// failure status would end the turn.
func (s *Server) alphaSearch(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readRequestBody(w, r, provider.Responses, 0)
	if !ok {
		return
	}
	s.alphaSearchBody(w, r, body)
}

func (s *Server) alphaSearchBody(w http.ResponseWriter, r *http.Request, body []byte) {
	var q alphaSearchRequest
	if err := json.Unmarshal(body, &q); err != nil {
		writeError(w, provider.Responses, http.StatusBadRequest, "magpie couldn't read the web search request: "+err.Error())
		return
	}
	if q.Model == "" {
		writeError(w, provider.Responses, http.StatusBadRequest, "the web search request names no model")
		return
	}
	note := ""
	ons, model := chatGPTSearchers(r, q.Model)
	if len(ons) > 0 {
		// bound by user, only the user's own ChatGPT accounts search
		// for it; with none, magpie searches, on the user's accounts too
		var why string
		if ons, why = s.usersSearchers(r, q.Model, ons, model); why != "" {
			log.Printf("web search on ChatGPT for %s: %s", q.Model, why)
			note = ownWords(r.Context(), "ChatGPT's web search isn't for this user ("+why+"); magpie searched in its place.\n\n")
		}
	}
	if len(ons) > 0 {
		answered, why, told := s.alphaSearchOn(w, r, withModel(body, model), model, ons)
		if answered {
			return
		}
		// a failure would end the turn: magpie searches in its place
		log.Printf("web search on ChatGPT for %s: %s", q.Model, why)
		if told == "" {
			// every account failed as one to be signed in again, or,
			// bound by user, as anything but a limit: that is the
			// log's alone
			note = ownWords(r.Context(), "ChatGPT's web search failed; magpie searched in its place.\n\n")
		} else {
			note = ownWords(r.Context(), "ChatGPT's web search failed ("+told+"); magpie searched in its place.\n\n")
		}
	}
	s.alphaSearchHere(w, r, q, note)
}

// chatGPTSearchers is the ChatGPT accounts a web search for model may go
// to, the one it is served on first and those resting last, with the
// model as the backend knows it: none when model isn't a ChatGPT
// account's, or the calling key may use none of them. One whose sign-in
// has to be made again (provider.SignInLapsed) is left out while another
// can search, as routing leaves it (leaveLapsed), else asked last.
func chatGPTSearchers(r *http.Request, model string) ([]provider.Provider, string) {
	p, bare, ok := provider.Resolve(model)
	if !ok || p.Account == nil || p.Account.Agent != "codex" {
		return nil, ""
	}
	who, keyHeld := keyHolds(r)
	if keyHeld && !modelAllowed(who, p, bare) {
		return nil, ""
	}
	var all []provider.Provider
	if !p.OwnPaused() {
		all = append(all, p)
	}
	all = append(all, p.AlsoOn()...)
	var ready, later, lapsed []provider.Provider
	for _, o := range all {
		c := candidate{p: o, model: bare, rest: o.ID}
		if !accountAllowed(who, c) {
			continue
		}
		if provider.SignInLapsed(o.Account) != "" {
			lapsed = append(lapsed, o)
			continue
		}
		if resting(c) {
			later = append(later, o)
			continue
		}
		ready = append(ready, o)
	}
	if len(ready)+len(later) == 0 {
		return lapsed, bare
	}
	return append(ready, later...), bare
}

// usersSearchers is ons, the ChatGPT accounts a search for id (bare as
// model) may go to, bound by user less those r's user isn't on
// (usersOwn), and why, when none is left. Bound by user, an id that no
// longer resolves (its provider turned off since chatGPTSearchers looked)
// leaves none: ons aren't narrowed to the user, so some may be another's.
func (s *Server) usersSearchers(r *http.Request, id string, ons []provider.Provider, model string) ([]provider.Provider, string) {
	p, _, ok := provider.Resolve(id)
	if !ok {
		if s.bind.byUser() {
			return nil, "magpie no longer finds " + id + "'s provider, so it can't tell the user's accounts"
		}
		return ons, ""
	}
	own, bound, err := s.usersOwn(s.forUser(r.Context(), r), p, model, provider.Responses)
	if !bound {
		return ons, ""
	}
	if err != nil {
		return nil, err.Error()
	}
	var out []provider.Provider
	for _, c := range own {
		for _, o := range ons {
			if strings.EqualFold(candidate{p: o, model: model, rest: o.ID}.who(), c.who()) {
				out = append(out, o)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, "none of the user's accounts can search now"
	}
	return out, ""
}

// alphaSearchOn sends the search to the ChatGPT backend on each account in
// turn, the next asked when one is refused, limited or fails, and relays
// the backend's answer as it is. It says whether one answered: when none
// did, nothing is written, and the search is magpie's (alphaSearchHere),
// with why returned, and told, of why, the failures of accounts that
// needn't be signed in again, for the caller's output (bound by user,
// userLimitSaid for a limit alone).
func (s *Server) alphaSearchOn(w http.ResponseWriter, r *http.Request, body []byte, model string, ons []provider.Provider) (answered bool, why, told string) {
	start := time.Now()
	// as written only when every account it may be asked on is set to go
	// unmasked (provider.SkipsRedaction, isee1_1 on Discord); magpie's own
	// search in its place masks its query itself (maskedQuery)
	if len(ons) > 0 && !slices.ContainsFunc(ons, func(p provider.Provider) bool { return !p.SkipsRedaction() }) {
		return s.alphaSearchAsked(w, r, body, model, ons, start)
	}
	w, body, unmask, hits, err := redactedHits(r.Context(), w, body)
	defer unmask()
	if err != nil {
		return false, err.Error(), err.Error()
	}
	noteMasked(r, "search", hits, sentOf(hits, body), 0, "openai", model)
	return s.alphaSearchAsked(w, r, body, model, ons, start)
}

// alphaSearchAsked is alphaSearchOn once body is as it goes.
func (s *Server) alphaSearchAsked(w http.ResponseWriter, r *http.Request, body []byte, model string, ons []provider.Provider, start time.Time) (answered bool, why, told string) {
	var errs, said []string
	bound := s.bind.byUser()
	for _, on := range ons {
		who := "OpenAI (" + on.Account.User + "): "
		// each failure is the log's; the caller's output has those of
		// accounts that needn't be signed in again (the owner's
		// decision, 2026-10-29), as any lapse is the operator's. Bound
		// by user, the output names neither the account nor the
		// vendor's words, as a bound user's 429 doesn't (T-067): a
		// limit (a 429 that isn't a prompt refused) is userLimitSaid,
		// anything else only that the search failed (T-071)
		fail := func(code int, msg string) {
			errs = append(errs, who+msg)
			switch {
			case provider.SignInLapsed(on.Account) != "" || provider.SignInRevoked(http.StatusUnauthorized, []byte(msg)):
				noteLapseRefused(candidate{p: on, rest: on.ID}.who(), provider.SignInLapsed(on.Account))
			case !bound:
				said = append(said, who+msg)
			case code == http.StatusTooManyRequests && !promptRefused(code, []byte(msg)) && !slices.Contains(said, userLimitSaid):
				said = append(said, userLimitSaid)
			}
		}
		ctx := on.Via(r.Context())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.CodexBase+AlphaSearchPath, bytes.NewReader(body))
		if err != nil {
			fail(0, err.Error())
			continue
		}
		for k, vs := range r.Header {
			if codexHeader(k) {
				req.Header[k] = vs
			}
		}
		req.Header.Set("Content-Type", "application/json")
		// a nil body: the search is answered in JSON, not streamed
		if err := on.Sign(ctx, req, provider.Responses, nil); err != nil {
			fail(0, err.Error())
			continue
		}
		// a sign-in its vendor revoked isn't sent again (#1793)
		if why, revoked := provider.RevokedSignIn(on.Account, req); revoked {
			fail(0, why)
			continue
		}
		res, err := s.client.Do(req)
		if err != nil {
			if r.Context().Err() != nil {
				return true, "", ""
			}
			fail(0, err.Error())
			continue
		}
		noteSignIn(ctx, on.Account, req, res)
		c := Call{Time: start, From: provider.Responses, To: provider.Responses, Model: model, Provider: on.ID,
			Agent: callerOf(r).agent, Via: callerOf(r).via, Kind: "web_search", Status: res.StatusCode}
		if res.StatusCode < 200 || res.StatusCode > 299 {
			msg, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
			res.Body.Close()
			fail(res.StatusCode, res.Status+": "+strings.TrimSpace(string(msg)))
			c.Millis, c.Error = time.Since(start).Milliseconds(), res.Status
			s.record(c)
			continue
		}
		for k, vs := range res.Header {
			if !hopHeader(k) {
				w.Header()[k] = vs
			}
		}
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
		res.Body.Close()
		c.Millis = time.Since(start).Milliseconds()
		s.record(c)
		return true, "", ""
	}
	return false, strings.Join(errs, "; "), strings.Join(said, "; ")
}

// alphaSearchHere answers a search for a model no ChatGPT account serves:
// its search_query and image_query searched as magpie searches for a
// model that can't, the findings the tool's output. Opening, clicking or
// finding in a page is the ChatGPT backend's alone, and said so.
func (s *Server) alphaSearchHere(w http.ResponseWriter, r *http.Request, q alphaSearchRequest, note string) {
	var queries []string
	for _, x := range append(q.Commands.SearchQuery, q.Commands.ImageQuery...) {
		text := strings.TrimSpace(x.Q)
		if text == "" {
			continue
		}
		for _, d := range x.Domains {
			if d = strings.TrimSpace(d); d != "" {
				text += " site:" + d
			}
		}
		queries = append(queries, text)
	}
	if len(queries) == 0 {
		writeJSON(w, http.StatusOK, alphaSearchReply{Output: note + ownWords(r.Context(), "magpie can only search the web for ") + q.Model +
			ownWords(r.Context(), ": give web.run a search_query. Opening, clicking or finding in a page goes to the ChatGPT backend, for a ChatGPT account's model in magpie.")})
		return
	}
	ctx := withWebAccess(context.WithValue(s.forUser(r.Context(), r), searchForKey{}, &CallFor{Agent: callerOf(r).agent, Model: q.Model, User: callerUser(r)}), q.Settings.ExternalWebAccess)
	var said []string
	for _, query := range queries {
		found, _, err := s.webSearch(ctx, query)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			log.Printf("web search for %s: %v", q.Model, err)
			// magpie's words as the caller reads them (ownWords); the
			// query is as it was asked
			msg := ownWords(r.Context(), "magpie couldn't search the web for ") + query
			if why := s.searchFailSaid(err); why != "" {
				msg += ownWords(r.Context(), ": "+why)
			}
			if errors.Is(err, errNoSearcher) {
				msg += ownWords(r.Context(), ": add a search API in magpie's Settings → Models → Web search (or magpie search add <api> <key>), or a provider that can search")
			}
			said = append(said, msg)
			continue
		}
		if len(queries) > 1 {
			found = "Search: " + query + "\n" + found
		}
		said = append(said, found)
	}
	writeJSON(w, http.StatusOK, alphaSearchReply{Output: note + strings.Join(said, "\n\n")})
}

// searchFailSaid is why magpie's own search failed (err), as web.run's
// output tells it (sideSaid). Bound by user, a searcher's own words (a
// provider's or a search API's) aren't told, as ChatGPT's aren't
// (alphaSearchAsked, T-071): a limit is userLimitSaid, which a bound
// user's 429 through serve already says, anything else nothing; magpie's
// own (errNoSearcher, a key's refusal) is told as it is. err is the log's.
func (s *Server) searchFailSaid(err error) string {
	if !s.bind.byUser() || errors.Is(err, errNoSearcher) || errors.As(err, new(sideHeld)) {
		return sideSaid(err)
	}
	if strings.Contains(err.Error(), userLimitSaid) {
		return userLimitSaid
	}
	return ""
}
