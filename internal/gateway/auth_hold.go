package gateway

// Holding a caller after authentication_failed (需求方 2026-11-09). Anthropic
// answering a Claude account's request authentication_failed is how a
// banned account answers, and a ban may follow the user or what they send:
// in the server's ledger, every authentication_failed of the last 30 days was
// on an account later confirmed banned, and the same caller's next request
// went on to the next account, banned in turn. So the caller is held: every
// request of theirs through the gateway is answered 429, its reset the
// hold's end, and nothing is sent upstream, for settings.AuthHold (12 hours
// unless the user set another; 0 holds nobody). The request that met it is
// answered the same 429, the hold starting then, so it and every one after
// read one reset. Its row keeps what the vendor answered (status, the kind,
// the account) and says the client was told 429 held (Call.Told).
//
// Who is held is who the request is of (authHolder): its user, the one its
// usage row records; else the calling gateway key; else its session; a
// request with none of them is only walled off (claudeAuthWalled), as no
// one is there to hold.
//
// Claude Code gives a plain refused sign-in the same kind ("Please run
// /login", "Login expired"), so a user can be held for an account's lapse:
// the 需求方's accepted cost. Holds are listed and released in Models ›
// Accounts, `magpie users held|release`, and GET /api/auth-holds, POST
// /api/auth-holds/release.
//
// The holds are kept in auth-holds.json, written through a temp file
// renamed over it, so a restart keeps them. One that can't be read is
// unknown: its last good generation (.bak) is read instead, and with none,
// every request is refused 503 naming the file, as a list that guards
// access fails closed (code standards): releasing every held caller could
// take the next accounts down, and that is the side kept safe. Nobody is
// held or released silently: the log says which.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/usage"
)

// AuthHold is one caller held: who (its kind, "user", "key" or "session",
// and the value), when it started and ends, and the account whose
// authentication_failed started it.
type AuthHold struct {
	Kind     string    `json:"kind"`
	Who      string    `json:"who"`
	Name     string    `json:"name,omitempty"` // a key's name, to show
	At       time.Time `json:"at"`
	Until    time.Time `json:"until"`
	Provider string    `json:"provider,omitempty"`
	Account  string    `json:"account,omitempty"`
}

// holderOf is h's key in the file and the in-memory set: "kind:who".
func (h AuthHold) holderOf() string { return h.Kind + ":" + h.Who }

// authHoldKinds are the kinds a hold is of, in the order a request's
// holder is taken.
var authHoldKinds = []string{"user", "key", "session"}

// authHoldClock is the hold's clock; tests hold it.
var authHoldClock = time.Now

func authHoldsPath() string {
	return filepath.Join(filepath.Dir(provider.Path()), "auth-holds.json")
}

// authHoldsMu is held for each change of auth-holds.json, in this magpie
// and across magpies (the command line releasing one while the gateway
// runs).
var authHoldsMu = filelock.Mutex{Path: func() string { return authHoldsPath() + ".lock" }}

// authHolds is the file as last read: when it changed since, it is read
// again (a stat a request).
var authHolds struct {
	sync.Mutex
	stamp string // its size and modification time when read
	holds []AuthHold
	err   error // the read failed and no last good generation stood in
}

// errHoldsUnread is the holds not being known.
var errHoldsUnread = errors.New("auth-holds.json can't be read")

// validHolds says b reads as holds.
func validHolds(b []byte) bool {
	var f struct{ Holds []AuthHold }
	return json.Unmarshal(b, &f) == nil
}

// readAuthHolds is the holds in force at now, read afresh when the file
// changed; an error when they can't be known.
func readAuthHolds(now time.Time) ([]AuthHold, error) {
	path := authHoldsPath()
	authHolds.Lock()
	defer authHolds.Unlock()
	fi, err := os.Stat(path)
	stamp := ""
	switch {
	case errors.Is(err, fs.ErrNotExist):
		stamp = "none"
	case err == nil:
		stamp = strconv.FormatInt(fi.Size(), 10) + "/" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
	}
	if stamp == "" || stamp != authHolds.stamp {
		// read once a change of the file: a file that doesn't read is
		// read (and logged) again only once it changes
		authHolds.holds, authHolds.err = parseAuthHolds(path)
		authHolds.stamp = stamp
	}
	if authHolds.err != nil {
		return nil, authHolds.err
	}
	var out []AuthHold
	for _, h := range authHolds.holds {
		if now.Before(h.Until) {
			out = append(out, h)
		}
	}
	return out, nil
}

// parseAuthHolds reads path: none for a file not there; its last good
// generation for one that can't be read; errHoldsUnread for neither.
func parseAuthHolds(path string) ([]AuthHold, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !validHolds(b) {
		good, ferr := lastgood.Fallback(path, b, validHolds)
		if ferr != nil {
			log.Printf("auth-holds.json: %v; %v: every request is refused until it reads", err, ferr)
			return nil, fmt.Errorf("%w: %v", errHoldsUnread, ferr)
		}
		log.Printf("auth-holds.json doesn't read (%v); its last good generation stands in", err)
		b = good
	}
	var f struct{ Holds []AuthHold }
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", errHoldsUnread, err)
	}
	return f.Holds, nil
}

// editAuthHolds changes the holds kept by edit, under authHoldsMu, and
// writes them through a temp file renamed over the old (steady.WriteFile),
// its last good generation kept beside it. Holds ended are let go.
func editAuthHolds(edit func([]AuthHold) []AuthHold) error {
	authHoldsMu.Lock()
	defer authHoldsMu.Unlock()
	path := authHoldsPath()
	holds, err := parseAuthHolds(path)
	if err != nil {
		return err
	}
	now := authHoldClock()
	holds = slices.DeleteFunc(edit(holds), func(h AuthHold) bool { return !now.Before(h.Until) })
	b, err := json.MarshalIndent(map[string]any{"holds": holds}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := lastgood.Keep(path, validHolds, 0o600); err != nil {
		return err
	}
	if err := steady.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	authHolds.Lock()
	authHolds.stamp = "" // read afresh
	authHolds.Unlock()
	return nil
}

// AuthHolds are the callers held now, for the GUI and the command line.
func AuthHolds() ([]AuthHold, error) { return readAuthHolds(authHoldClock()) }

// ReleaseAuthHold lets the caller holder ("kind:who", or the bare value,
// matched against every kind) go; an error when none is held so.
func ReleaseAuthHold(holder string) error {
	holder = strings.TrimSpace(holder)
	if holder == "" {
		return errors.New("name the held caller to release")
	}
	found := false
	err := editAuthHolds(func(hs []AuthHold) []AuthHold {
		return slices.DeleteFunc(hs, func(h AuthHold) bool {
			if h.holderOf() == holder || h.Who == holder {
				found = true
				return true
			}
			return false
		})
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%q isn't held", holder)
	}
	log.Printf("auth hold: %s released by the user", holder)
	return nil
}

// authHolder is who r is of, for a hold: its user (the usage row's), else
// the calling gateway key, else its session; ok false for none.
// DefaultUser is no user here: authHeld reads r before noUser names it
// (userDefaulted), so its hold is the key's or the session's, the same
// read before and after, and one key's callers that name no user aren't
// held for another key's (T-157).
func authHolder(r *http.Request) (AuthHold, bool) {
	if u := callerUser(r); u != "" && u != DefaultUser {
		return AuthHold{Kind: "user", Who: u}, true
	}
	if id, name, _ := rowCaller(r.Context()); id != "" {
		return AuthHold{Kind: "key", Who: id, Name: name}, true
	}
	if s := sessionOf(r.Header); s != "" {
		return AuthHold{Kind: "session", Who: s}, true
	}
	return AuthHold{}, false
}

// authHeldOf is the hold r's caller is under now, ok false for none.
func authHeldOf(r *http.Request, now time.Time) (AuthHold, bool, error) {
	who, ok := authHolder(r)
	if !ok {
		return AuthHold{}, false, nil
	}
	holds, err := readAuthHolds(now)
	if err != nil {
		return AuthHold{}, false, err
	}
	for _, h := range holds {
		if h.holderOf() == who.holderOf() {
			return h, true, nil
		}
	}
	return AuthHold{}, false, nil
}

// startAuthHold holds r's caller from now for the hold's length, after
// Anthropic answered account of provider pid authentication_failed, and
// rests the account until the user acts (provider.SetLoginAuthFailed). ok
// false when nobody is held: the hold is off (0), or r is of no one. A
// hold already running keeps its end, so each request reads one reset.
func startAuthHold(r *http.Request, pid, account, agent string) (AuthHold, bool) {
	now := authHoldClock()
	if agent != "" && account != "" {
		if err := provider.SetLoginAuthFailed(agent, account, now); err != nil {
			log.Printf("auth hold: %s@%s isn't rested: %v", pid, account, err)
		}
	}
	d := settings.Shared().AuthHold()
	who, ok := authHolder(r)
	if d <= 0 || !ok {
		return AuthHold{}, false
	}
	if h, held, err := authHeldOf(r, now); err == nil && held {
		return h, true
	}
	h := who
	h.At, h.Until, h.Provider, h.Account = now, now.Add(d), pid, account
	if err := editAuthHolds(func(hs []AuthHold) []AuthHold {
		// another request of the holder's that met it at the same time
		// started it already: that one stands, one reset for both
		if i := slices.IndexFunc(hs, func(o AuthHold) bool { return o.holderOf() == h.holderOf() && now.Before(o.Until) }); i >= 0 {
			h = hs[i]
			return hs
		}
		hs = slices.DeleteFunc(hs, func(o AuthHold) bool { return o.holderOf() == h.holderOf() })
		return append(hs, h)
	}); err != nil {
		// kept for this request alone: the next one isn't held, and the log says why
		log.Printf("auth hold: %s held for this request only, the hold not kept: %v", h.holderOf(), err)
	} else {
		log.Printf("auth hold: %s held until %s after %s@%s answered authentication_failed", h.holderOf(), h.Until.Local().Format(time.RFC3339), pid, account)
	}
	return h, true
}

// authHeldSaid is what a held caller is told: the usage limit reached, and
// when it resets, on this computer's clock.
func authHeldSaid(h AuthHold) string {
	return "You've reached your usage limit. It resets at " + h.Until.Local().Format("2006-01-02 15:04 MST") + "."
}

// heldErrType is a held request's row's ErrType.
const heldErrType = "user_held"

// writeAuthHeld answers w as a held caller is told: 429 in the client's own
// shape (Anthropic's rate_limit_error, OpenAI's rate_limit_exceeded),
// Retry-After the seconds left and the user's limit headers, with the
// reset in its words. No SDK retries it before then (X-Should-Retry).
func writeAuthHeld(w http.ResponseWriter, proto provider.Protocol, h AuthHold, now time.Time) {
	authHeldHeaders(w.Header(), h, now)
	writeErrorSaid(w, proto, http.StatusTooManyRequests, authHeldSaid(h), "rate_limit_exceeded", nil)
}

// authHeldHeaders are a held answer's headers, also the ones a stream's held
// error goes after when they can still be set.
func authHeldHeaders(hd http.Header, h AuthHold, now time.Time) {
	secs := int(h.Until.Sub(now).Seconds() + 0.999)
	hd.Set("Retry-After", strconv.Itoa(max(secs, 1)))
	hd.Set(UserLimitHeader, "held")
	hd.Set(UserUntilHeader, h.Until.UTC().Format(time.RFC3339))
	hd.Set("X-Should-Retry", "false")
}

// authHeld answers a request of a held caller 429 before anything is
// asked, and refuses every request 503 when the holds can't be known.
// What costs nothing passes, as keyLimited lets it: reads, magpie's own
// paths, MCP calls and counting tokens.
func authHeld(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || strings.HasPrefix(r.URL.Path, "/_magpie/") || strings.HasPrefix(r.URL.Path, "/mcp/") || strings.HasSuffix(r.URL.Path, "/count_tokens") || r.URL.Path == KeysPath {
			next.ServeHTTP(w, r)
			return
		}
		now := authHoldClock()
		h, held, err := authHeldOf(r, now)
		proto := protoOfPath(r.URL.Path)
		switch {
		case err != nil:
			w.Header().Set("X-Should-Retry", "false")
			writeError(w, proto, http.StatusServiceUnavailable, "magpie can't read who is held after authentication_failed ("+authHoldsPath()+"): no request is sent until it reads, or is removed")
			authHeldRow(r, http.StatusServiceUnavailable, err.Error(), "auth_holds_unread", now)
		case held:
			writeAuthHeld(w, proto, h, now)
			authHeldRow(r, http.StatusTooManyRequests, authHeldSaid(h)+" (held after "+h.Provider+"@"+h.Account+" answered authentication_failed)", heldErrType, now)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// authHeldRow is the usage row of a request authHeld turned away.
func authHeldRow(r *http.Request, status int, msg, errType string, now time.Time) {
	c := callerOf(r)
	appendUsage(r, usage.Record{Time: now, Agent: c.agent, Via: c.via, Status: status, Rejected: true,
		Error: msg, ErrType: errType, Endpoint: r.URL.Path, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header)})
}

// streamHoldKey carries, on a Claude subscription's request, what holds its
// caller when its stream, already begun, is answered authentication_failed.
type streamHoldKey struct{}

// toldHeldKey carries, on a request, where the hold told it in its stream
// is noted for its row (Call.Told).
type toldHeldKey struct{}

// toldHeld notes on r's row, when one is kept for it, that the client was
// told h's 429 in its stream.
func toldHeld(r *http.Request, h AuthHold) {
	if told, ok := r.Context().Value(toldHeldKey{}).(*string); ok {
		*told = "429 held until " + h.Until.UTC().Format(time.RFC3339)
	}
}
