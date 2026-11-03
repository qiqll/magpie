package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// compactFrom is how large a conversation's prompt, as its run last asked
// it, makes it worth compacting before it moves to another account
// (compactAway): below it, writing the whole conversation to the new
// account's cache costs about what the /compact costs on the old one, and
// the turn waits 30 to 90s more for it (the owner, 2026-11-03).
const compactFrom = 100_000

// compactLongest is how long a turn waits for its conversation's /compact
// on the old account: the helper's own wait (claudetui's compactWait),
// and a moment for its result to come back.
var compactLongest = 5*time.Minute + 30*time.Second

// compactAway is, for a turn routing moved off the account its
// conversation's run is on because that account reached the usage cap the
// user set on it in magpie, the run's session compacted there and copied
// to the account the turn is on now (configDir, "" for Claude Code's own
// sign-in), for a new Claude Code there to go on from (--resume), typed
// the turn alone. Told whole (replaySession), the new account wrote the
// whole conversation to its cache on the turn's first request (T-075:
// ~19.6M tokens written so, 15% of every cache write). The old account is
// asked this one request past its cap, with its cache warm; never when
// Anthropic itself said it is used up or refused it (it rests then).
//
// nil when there is no such run, its last prompt was under compactFrom,
// or the /compact failed: the turn then goes on as before. It runs only
// Claude Code's own /compact, with no words of magpie's.
func (b *subscriptionBridge) compactAway(ctx context.Context, req *Request, owner string, p provider.Provider, model, configDir string) *savedSession {
	if req.clientCache != nil || p.Account == nil || !claudeTUIOn() {
		return nil
	}
	_, since := nextTurn(req, owner)
	if since == nil {
		return nil
	}
	var run *subscriptionRun
	b.mu.Lock()
	for key, r := range b.idle {
		o := r.owned()
		if o == owner || !strings.HasPrefix(o, p.ID+"\x00") {
			continue
		}
		if k, _ := nextTurn(req, o); k != key {
			continue
		}
		r.mu.Lock()
		ok := r.tui && !r.closed && !r.clientMarks && r.lean == req.leanPrompt && r.sessionID != "" && len(r.sessions) > 0 && r.promptTokens >= compactFrom
		r.mu.Unlock()
		if ok && atCap(p, o, model) {
			run = r
			delete(b.idle, key)
			r.idleKey = ""
			break
		}
	}
	b.mu.Unlock()
	if run == nil {
		return nil
	}
	user, _ := ownerAccount(run.owned())
	run.mu.Lock()
	tokens := run.promptTokens
	run.mu.Unlock()
	log.Printf("claude: a conversation of %d tokens leaves %s at its usage cap: compacted there before it goes on, on another account", tokens, user)
	began := time.Now()
	if err := run.compact(ctx); err != nil {
		log.Printf("claude: the conversation's /compact on %s failed (%v); the next account is told it whole", user, err)
		run.abortFor("its /compact failed: " + err.Error())
		return nil
	}
	run.mu.Lock()
	sid, from := run.sessionID, run.sessions
	run.mu.Unlock()
	to := movedSessions(from, configDir)
	id, err := copySession(from, sid, to[len(to)-1])
	run.abortFor("its conversation was compacted and went on on another account")
	if err != nil {
		log.Printf("claude: the compacted session couldn't be copied (%v); the next account is told the conversation whole", err)
		return nil
	}
	log.Printf("claude: the conversation was compacted on %s in %s; the next account goes on from it", user, time.Since(began).Round(time.Second))
	return &savedSession{session: id, sessions: to, owner: owner, at: time.Now(), since: since, tui: true}
}

// atCap says the account owner names is held at the usage cap the user
// set on it in magpie, and nothing more: not used up as Anthropic said
// it (100%), resting for a refusal, or held as its usage can't be told.
func atCap(p provider.Provider, owner, model string) bool {
	user, _ := ownerAccount(owner)
	if user == "" {
		return false
	}
	acct := provider.Provider{ID: p.ID, Account: &provider.Account{Agent: p.Account.Agent, User: user}}
	h := capHeld(p, acct, model, time.Now())
	if h == nil || h.noCredits || h.unread != "" {
		return false
	}
	if used, _ := allowances(acct.Account.UsageAgent())[user].For(model, time.Now()); used >= 100 {
		return false
	}
	c := candidate{p: acct, model: model, rest: p.ID + "@" + user}
	return !resting(c)
}

// compact has the run's Claude Code run /compact, and waits for it to
// end. The run is the caller's alone: taken off b.idle.
func (r *subscriptionRun) compact(ctx context.Context) error {
	done := make(chan error, 1)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("the run ended")
	}
	r.compacted = done
	exited := r.exited
	r.mu.Unlock()
	r.endIn(compactLongest)
	if err := r.tell([]byte(`{"type":"command","command":"/compact"}`)); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-exited:
		return r.whyEnded()
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(compactLongest):
		return errors.New("it didn't end in " + compactLongest.String())
	}
}

// movedSessions is the project folders a run in configDir ("" for Claude
// Code's own) keeps the sessions of the same work folder in as dirs, a
// run's in another account's.
func movedSessions(dirs []string, configDir string) []string {
	if configDir == "" {
		configDir = claudeConfigDir()
	}
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = filepath.Join(configDir, "projects", filepath.Base(d))
	}
	return out
}

// copySession copies session sid, from whichever of dirs has it, to a
// session of a new id in dir, and says the id: each line as written, its
// sessionId the copy's.
func copySession(dirs []string, sid, dir string) (string, error) {
	if sid == "" || strings.ContainsAny(sid, `/\`) || strings.Contains(sid, "..") {
		return "", errors.New("no session to copy")
	}
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, sid+".jsonl"))
		if err != nil {
			continue
		}
		id := newUUID()
		old, _ := json.Marshal(sid)
		now, _ := json.Marshal(id)
		lines := bytes.ReplaceAll(b, []byte(`"sessionId":`+string(old)), []byte(`"sessionId":`+string(now)))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		path := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(path, lines, 0o600); err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return id, nil
	}
	return "", errors.New("session " + sid + " isn't there")
}
