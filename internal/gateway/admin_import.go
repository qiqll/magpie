package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
)

// An area's credential import (T-211, docs/subsystems/areas.md "Imports",
// magpie-api.md §13): the files the window's import takes, read by the
// same readers in their new-only mode (provider.ImportOptions, T-220), so
// an account magpie has, the owner's or another area's, is a conflict
// and stays as it was, and a new one is the area's before anything of it
// is written. Factory's fk- keys go through its plugin's own key sign-in
// to a new account only (areaPluginKey), as signin/plugin's api-key does.

const (
	// adminImportMost is the import's body at most, the window's 8 MB;
	// every other admin route keeps adminBodyMost
	adminImportMost = 8 << 20
	// adminImportFiles is the most files one import takes
	adminImportFiles = 50
	// adminImportTime is how long one import runs at most
	adminImportTime = 3 * time.Minute
	// adminImportsMost is the most imports running at once, every area's:
	// each holds up to 8 MB of files
	adminImportsMost = 4
	// adminConsoleImportsMost is the most of them the console's may be,
	// across the areas it acts for, so the rest stay the areas' own (T-235)
	adminConsoleImportsMost = 2
	adminTooLarge           = "too_large"
	adminTooManyImports     = "too_many_imports"
)

// importAgents are the built-in subscriptions whose credentials an area
// imports.
var importAgents = []string{"codex", "claude", "antigravity", "gemini"}

// keyImports are the plugin subscriptions whose keys an area imports, by
// the finder of their keys in the files: Factory's fk- keys, as the
// window's import finds them.
var keyImports = map[string]func([]string) []string{"factory": provider.FactoryKeysIn}

// areaImports are the areas with an import running, each true when the
// console runs it: one each at a time, adminImportsMost in all, of which
// adminConsoleImportsMost the console's.
var areaImports struct {
	sync.Mutex
	m map[string]bool
}

// startImport takes area id's import slot for c, before its body is read,
// or answers 429 too_many_imports; the func it gives lets it go.
func startImport(w http.ResponseWriter, c adminCall) (func(), bool) {
	id := c.area.ID
	areaImports.Lock()
	defer areaImports.Unlock()
	consoles := 0
	for _, console := range areaImports.m {
		if console {
			consoles++
		}
	}
	_, running := areaImports.m[id]
	var msg string
	switch {
	case running:
		msg = "this area has an import running: try again once it ends"
	case len(areaImports.m) >= adminImportsMost:
		msg = "magpie is running as many imports as it takes at once: try again shortly"
	case c.console && consoles >= adminConsoleImportsMost:
		msg = "the console is running as many imports as magpie takes from it at once: try again shortly"
	}
	if msg != "" {
		w.Header().Set("Retry-After", "10")
		adminError(w, http.StatusTooManyRequests, adminTooManyImports, msg)
		return nil, false
	}
	if areaImports.m == nil {
		areaImports.m = map[string]bool{}
	}
	areaImports.m[id] = c.console
	return func() {
		areaImports.Lock()
		delete(areaImports.m, id)
		areaImports.Unlock()
	}, true
}

// importRow is how one account of an import went, in the files' order:
// never a token, nor the name of an account magpie had.
type importRow struct {
	Index   int           `json:"index"`          // from 1
	File    int           `json:"file,omitempty"` // a whole file that couldn't be read, from 1
	Status  string        `json:"status"`         // "added", "conflict" or "failed"
	Account *adminAccount `json:"account,omitempty"`
	Message string        `json:"message,omitempty"`
}

// importResult is an import's answer.
type importResult struct {
	Results  []importRow `json:"results"`
	Added    int         `json:"added"`
	Conflict int         `json:"conflict"`
	Failed   int         `json:"failed"`
}

func (o *importResult) add(r importRow) {
	r.Index = len(o.Results) + 1
	switch r.Status {
	case keyAdded:
		o.Added++
	case keyConflict:
		r.Message = provider.ErrKnownAccount.Error()
		o.Conflict++
	default:
		r.Status = keyFailed
		o.Failed++
	}
	o.Results = append(o.Results, r)
}

// The reasons an import's rows fail for all the rest.
const (
	importConflictsHeld = "too many conflicts; try again later"
	importFailsHeld     = "too many accounts failed; try again later"
	importRanOut        = "the import ran out of time: import the rest again"
)

func (s *Server) adminImport(w http.ResponseWriter, r *http.Request, c adminCall) {
	if s.heldAnswered(w, c, time.Now()) {
		return
	}
	done, ok := startImport(w, c)
	if !ok {
		return
	}
	defer done()
	var in struct {
		Agent      string          `json:"agent"`
		Files      []string        `json:"files"`
		AccessOnly bool            `json:"accessOnly"`
		Proxy      *proxyPick      `json:"proxy"`
		Opened     json.RawMessage `json:"opened"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		var big *http.MaxBytesError
		if errors.As(err, &big) {
			adminError(w, http.StatusRequestEntityTooLarge, adminTooLarge, "an import is 8 MB at most")
			return
		}
		adminError(w, http.StatusBadRequest, adminBadRequest, "the body isn't the JSON this route takes")
		return
	}
	if !slices.Contains(c.area.SignIns, in.Agent) {
		adminNotFoundErr(w)
		return
	}
	keysIn, byKey := keyImports[in.Agent]
	if !slices.Contains(importAgents, in.Agent) && !byKey {
		adminError(w, http.StatusBadRequest, adminBadRequest, "accounts of "+in.Agent+" can't be imported: sign in to it instead")
		return
	}
	var pm adminPluginMethod
	if byKey {
		pp, listed, on := areaPluginOf(in.Agent)
		if !listed {
			adminNotFoundErr(w)
			return
		}
		i := -1
		if on {
			ms := areaMethods(in.Agent, pp)
			if i = slices.IndexFunc(ms, func(m adminPluginMethod) bool { return m.Way == wayAPIKey }); i >= 0 {
				pm = ms[i]
			}
		}
		if i < 0 {
			adminError(w, http.StatusConflict, adminPluginUnavailable, "the "+in.Agent+" plugin is off, not installed or takes no key on this magpie")
			return
		}
	} else if subscriptionOf(in.Agent) == "" || provider.PluginServes(in.Agent) {
		adminNotFoundErr(w)
		return
	}
	if len(in.Files) == 0 || len(in.Files) > adminImportFiles {
		adminError(w, http.StatusBadRequest, adminBadRequest, "send 1 to "+strconv.Itoa(adminImportFiles)+" files")
		return
	}
	if in.AccessOnly && in.Agent != "codex" {
		adminError(w, http.StatusBadRequest, adminBadRequest, "accessOnly is for ChatGPT (codex) accounts alone")
		return
	}
	var opened time.Time
	if in.Opened != nil {
		var day string
		if byKey {
			adminError(w, http.StatusBadRequest, adminBadRequest, "a "+in.Agent+" import adds keys, which have no opening date: leave opened out")
			return
		}
		if json.Unmarshal(in.Opened, &day) != nil {
			adminError(w, http.StatusBadRequest, adminBadRequest, "opened is a date, YYYY-MM-DD")
			return
		}
		var err error
		if opened, err = areaOpened(day); err != nil {
			adminError(w, http.StatusBadRequest, adminBadRequest, err.Error())
			return
		}
	}
	var o provider.SignInOptions
	if in.Proxy != nil {
		url, public, ok := pickedProxy(w, c, in.Proxy)
		if !ok {
			return
		}
		o.Proxy, o.PublicOnly = &url, public
	}
	im := &areaImport{s: s, c: c, agent: in.Agent, pid: subscriptionOf(in.Agent), pick: in.Proxy, opened: opened, owned: len(c.area.Accounts)}
	var out importResult
	var err error
	if byKey {
		out, err = im.pluginKeys(pm, keysIn(in.Files), o)
	} else {
		out, err = im.logins(r.Context(), in.Files, in.AccessOnly, o)
	}
	if err != nil {
		adminError(w, http.StatusBadRequest, adminBadRequest, heldFrom(err.Error(), in.Files))
		return
	}
	for i := range out.Results {
		out.Results[i].Message = heldFrom(out.Results[i].Message, in.Files)
	}
	c.logf("imported %s: added %d, conflict %d, failed %d", in.Agent, out.Added, out.Conflict, out.Failed)
	writeJSON(w, http.StatusOK, out)
}

// areaImport is an import running for c's area: how many accounts the
// area has with those being read, so it is never taken past its 500.
type areaImport struct {
	s          *Server
	c          adminCall
	agent, pid string
	pick       *proxyPick
	opened     time.Time
	owned      int // the area's accounts, and those being read
	reading    int // those being read
	// keptOut is why the import makes no more accounts the area's, once
	// the key it came with, or its area, was found changed (stillActs):
	// every account after is refused for it too, and those refused, or
	// failing as it was found, are no failures the area is counted for
	keptOut atomic.Pointer[error]
}

// acts is stillActs for the import, kept once it says no.
func (im *areaImport) acts() error {
	if err := im.keptOut.Load(); err != nil {
		return *err
	}
	err := stillActs(im.c)
	if err != nil {
		im.keptOut.CompareAndSwap(nil, &err)
		return *im.keptOut.Load()
	}
	return nil
}

// changed is whether the import was kept out (acts).
func (im *areaImport) changed() bool { return im.keptOut.Load() != nil }

// importReads is every import's admit and release, under one lock: the
// console's imports for different areas run side by side and share its
// total (T-222), so each judges the accounts the others are reading.
var importReads struct {
	sync.Mutex
	moved   *sync.Cond // an account read ended (on the Mutex)
	console int        // accounts the console's imports are reading
}

// admit is the import's ImportOptions.Admit: none once the area is held
// for its conflicts or full, the account counted as the area's while it
// is read. Near the line it waits for those being read, each of which
// may be a conflict, so an import never tells the area of more conflicts
// than the line lets through, nor the console, with its other imports'
// reads, more than its total.
func (im *areaImport) admit() (func(provider.ImportedAccount) provider.ImportedAccount, string) {
	for {
		// the key and the area read again before each account (T-260),
		// and after each wait, outside the lock every import shares: it
		// reads files
		if err := im.acts(); err != nil {
			return nil, err.Error()
		}
		if done, why, ok := im.admitted(); ok {
			return done, why
		}
	}
}

// admitted is admit's turn under importReads: the account admitted, or
// why not, or false after a wait for a read to end, to be judged again.
func (im *areaImport) admitted() (func(provider.ImportedAccount) provider.ImportedAccount, string, bool) {
	importReads.Lock()
	defer importReads.Unlock()
	if importReads.moved == nil {
		importReads.moved = sync.NewCond(&importReads.Mutex)
	}
	now := time.Now()
	areaLeft := conflictMost - im.s.conflicts.count(im.c.area.ID, now)
	consoleLeft := consoleConflictMost
	if im.c.console {
		consoleLeft -= im.s.conflicts.count(consoleBudget, now)
	}
	// each account being read may fail as well (T-233)
	areaFails, consoleFails := im.s.importFailsLeft(im.c, now)
	switch {
	case areaLeft <= 0 || consoleLeft <= 0:
		return nil, importConflictsHeld, true
	case areaFails <= 0 || consoleFails <= 0:
		return nil, importFailsHeld, true
	case im.owned >= adminAccountsLimit && im.reading == 0:
		return nil, area.ErrFull.Error(), true
	case im.reading < min(areaLeft, areaFails) && (!im.c.console || importReads.console < min(consoleLeft, consoleFails)) && im.owned < adminAccountsLimit:
		im.owned++
		im.reading++
		if im.c.console {
			importReads.console++
		}
		return im.done, "", true
	}
	importReads.moved.Wait()
	return nil, "", false
}

// done is told how an admitted account went: one not added is the area's
// no more, a conflict or a failure is counted, and one added gets its
// opening date and the proxy picked.
func (im *areaImport) done(r provider.ImportedAccount) provider.ImportedAccount {
	// counted before it is no longer being read: admit judges both
	switch r.Status {
	case provider.ImportConflict:
		im.s.countConflict(im.c)
	case keyAdded:
	default:
		// any other reads as failed (importResult.add), but for one the
		// changed key kept out
		if !im.changed() {
			im.s.importFailed(im.c, time.Now())
		}
	}
	im.release(r.Status == keyAdded)
	switch r.Status {
	case keyAdded:
		if !im.opened.IsZero() {
			if err := provider.SetAccountOpened(im.pid, r.User, im.opened); err != nil {
				log.Printf("admin: area %s's imported %s account, its opening date not kept: %v", im.c.area.ID, im.agent, err)
				r.Error = "added, but its opening date wasn't kept: " + err.Error()
			}
		}
		if im.pick != nil {
			if err := im.pick.mount(im.c.area.ID, im.ref(r.User)); err != nil {
				log.Printf("admin: area %s's imported %s account, %s", im.c.area.ID, im.agent, mountFailed(err))
				r.Error = strings.TrimPrefix(r.Error+"; ", "; ") + "added, but " + mountFailed(err)
			}
		}
	}
	return r
}

// release ends an admitted account's read, added or not, as done does.
func (im *areaImport) release(added bool) {
	importReads.Lock()
	if !added {
		im.owned--
	}
	im.reading--
	if im.c.console {
		importReads.console--
	}
	importReads.moved.Broadcast()
	importReads.Unlock()
}

func (im *areaImport) ref(user string) string {
	return im.pid + "/" + provider.LoginID(im.agent, user)
}

// claim makes a new account the area's before anything of it is written
// (ImportOptions.Claim): one another area has reads as one magpie has.
func (im *areaImport) claim(agent, user string) error {
	// read again here too: the vendor was asked whose it is since admit,
	// and nothing of it is written once the key or the area changed
	// (T-260)
	if err := im.acts(); err != nil {
		return err
	}
	err := area.ClaimUpTo(im.c.area.ID, im.ref(user), adminAccountsLimit)
	if errors.Is(err, area.ErrTaken) {
		return provider.ErrKnownAccount
	}
	return err
}

func (im *areaImport) unclaim(agent, user string) {
	if err := area.Release(im.ref(user)); err != nil {
		log.Printf("admin: area %s's %s import, an account not saved not let go of either: %v", im.c.area.ID, agent, err)
	}
}

// logins imports the built-ins' files, through o's proxy (none picked:
// the subscription's), as the window's readers read them.
func (im *areaImport) logins(rctx context.Context, files []string, accessOnly bool, o provider.SignInOptions) (importResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(rctx), adminImportTime)
	defer cancel()
	if o.Proxy != nil {
		if o.PublicOnly {
			ctx = netproxy.WithPublicOnly(ctx)
		}
		ctx = netproxy.With(ctx, *o.Proxy)
	} else {
		ctx = provider.Via(ctx, im.pid)
	}
	opts := provider.ImportOptions{NewOnly: true, Admit: im.admit, Claim: im.claim, Unclaim: im.unclaim}
	var res []provider.ImportedAccount
	var err error
	switch im.agent {
	case "codex", "claude":
		res, err = provider.ImportLoginsWith(ctx, im.agent, files, accessOnly, opts)
	default:
		res, err = provider.ImportGoogleAccountsWith(ctx, im.agent, files, opts)
	}
	if err != nil {
		return importResult{}, err
	}
	a, _ := area.Get(im.c.area.ID)
	accs := areaAccounts(a)
	out := importResult{Results: []importRow{}}
	for _, x := range res {
		row := importRow{File: x.File, Status: x.Status, Message: x.Error}
		if x.Status == keyAdded {
			ref := im.ref(x.User)
			if i := slices.IndexFunc(accs, func(acc adminAccount) bool { return acc.ID == ref }); i >= 0 {
				row.Account = &accs[i]
			} else {
				row.Account = &adminAccount{ID: ref, Provider: im.pid, Kind: "account", Name: x.User}
			}
		}
		out.add(row)
	}
	return out, nil
}

// pluginKeys adds each key through the plugin's key sign-in, method pm,
// to a new account only, one at a time.
func (im *areaImport) pluginKeys(pm adminPluginMethod, keys []string, o provider.SignInOptions) (importResult, error) {
	if len(keys) == 0 {
		return importResult{}, errors.New("no " + im.agent + " keys in it (Factory's start fk-)")
	}
	if len(keys) > 200 {
		return importResult{}, errors.New(strconv.Itoa(len(keys)) + " keys; at most 200 at a time")
	}
	pp, _, on := areaPluginOf(im.agent)
	out := importResult{Results: []importRow{}}
	until := time.Now().Add(adminImportTime)
	stop := ""
	for _, k := range keys {
		row := importRow{}
		_, why := im.admit()
		switch {
		case stop != "":
			row.Status, row.Message = keyFailed, stop
		case why != "":
			row.Status, row.Message = keyFailed, why
		case !on:
			row.Status, row.Message = keyFailed, "the "+im.agent+" plugin is off or not installed on this magpie"
		case time.Now().After(until):
			row.Status, row.Message = keyFailed, importRanOut
			stop = importRanOut
		default:
			acc, status, code, msg := im.s.areaPluginKey(im.c, pp, pm, nil, k, im.pick, o)
			switch {
			case status == http.StatusCreated:
				row.Status, row.Account = keyAdded, &acc
			case status == http.StatusConflict && code == adminConflict && msg == provider.ErrKnownAccount.Error():
				// counted by areaPluginKey
				row.Status = keyConflict
			case status == http.StatusUnauthorized, status == http.StatusServiceUnavailable:
				row.Status, row.Message = keyFailed, msg
				stop = msg
			default:
				row.Status, row.Message = keyFailed, msg
			}
			if status == http.StatusUnauthorized && code == adminKeyRequired {
				// kept out by stillActs (T-260), or the area deleted:
				// the rest with it
				im.acts()
			}
			if row.Status == keyFailed && !im.changed() {
				// the plugin asked its vendor (T-233)
				im.s.importFailed(im.c, time.Now())
			}
		}
		if why == "" {
			// a conflict was counted by areaPluginKey
			im.release(row.Status == keyAdded)
		}
		out.add(row)
	}
	return out, nil
}

// tokenRun is a run of characters a token is made of, long enough to be
// one's part.
var tokenRun = regexp.MustCompile(`[A-Za-z0-9._~+/=-]{12,}`)

// heldFrom is why, a reason an import's account failed, as the area is
// told it: left out when a run in it of 12 or more characters a token is
// made of, a digit among them, is in the files, as a vendor's answer may
// say back the token it was given. magpie's own reasons hold none (a
// field's name, refresh_token, has no digit).
func heldFrom(why string, files []string) string {
	for _, run := range tokenRun.FindAllString(why, -1) {
		if !strings.ContainsAny(run, "0123456789") {
			continue
		}
		for _, f := range files {
			if strings.Contains(f, run) {
				return "the vendor's answer is left out here, as it may hold the file's token"
			}
		}
	}
	return why
}
