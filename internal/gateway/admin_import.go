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
	adminImportTime     = 3 * time.Minute
	adminTooLarge       = "too_large"
	adminTooManyImports = "too_many_imports"
)

// importAgents are the built-in subscriptions whose credentials an area
// imports.
var importAgents = []string{"codex", "claude", "antigravity", "gemini"}

// keyImports are the plugin subscriptions whose keys an area imports, by
// the finder of their keys in the files: Factory's fk- keys, as the
// window's import finds them.
var keyImports = map[string]func([]string) []string{"factory": provider.FactoryKeysIn}

// areaImports are the areas with an import running: one each at a time.
var areaImports struct {
	sync.Mutex
	m map[string]bool
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
	importRanOut        = "the import ran out of time: import the rest again"
)

func (s *Server) adminImport(w http.ResponseWriter, r *http.Request, c adminCall) {
	if s.heldAnswered(w, c, time.Now()) {
		return
	}
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
	areaImports.Lock()
	if areaImports.m[c.area.ID] {
		areaImports.Unlock()
		w.Header().Set("Retry-After", "10")
		adminError(w, http.StatusTooManyRequests, adminTooManyImports, "this area has an import running: try again once it ends")
		return
	}
	if areaImports.m == nil {
		areaImports.m = map[string]bool{}
	}
	areaImports.m[c.area.ID] = true
	areaImports.Unlock()
	defer func() {
		areaImports.Lock()
		delete(areaImports.m, c.area.ID)
		areaImports.Unlock()
	}()

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
	log.Printf("admin: area %s (%s) imported %s: added %d, conflict %d, failed %d", c.area.ID, c.area.Note, in.Agent, out.Added, out.Conflict, out.Failed)
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
	mu         sync.Mutex
	moved      *sync.Cond // an account read ended (on mu)
	owned      int        // the area's accounts, and those being read
	reading    int        // those being read
}

// admit is the import's ImportOptions.Admit: none once the area is held
// for its conflicts or full, the account counted as the area's while it
// is read. Near the line it waits for those being read, each of which
// may be a conflict, so an import never tells the area of more conflicts
// than the line lets through.
func (im *areaImport) admit() (func(provider.ImportedAccount) provider.ImportedAccount, string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.moved == nil {
		im.moved = sync.NewCond(&im.mu)
	}
	for {
		n := im.s.conflicts.count(im.c.area.ID, time.Now())
		switch {
		case n >= conflictMost:
			return nil, importConflictsHeld
		case im.owned >= adminAccountsLimit && im.reading == 0:
			return nil, area.ErrFull.Error()
		case n+im.reading < conflictMost && im.owned < adminAccountsLimit:
			im.owned++
			im.reading++
			return im.done, ""
		}
		im.moved.Wait()
	}
}

// done is told how an admitted account went: one not added is the area's
// no more, a conflict is counted, and one added gets its opening date
// and the proxy picked.
func (im *areaImport) done(r provider.ImportedAccount) provider.ImportedAccount {
	if r.Status == provider.ImportConflict {
		// counted before it is no longer being read: admit judges both
		im.s.countConflict(im.c.area.ID)
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
	im.mu.Lock()
	if !added {
		im.owned--
	}
	im.reading--
	im.moved.Broadcast()
	im.mu.Unlock()
}

func (im *areaImport) ref(user string) string {
	return im.pid + "/" + provider.LoginID(im.agent, user)
}

// claim makes a new account the area's before anything of it is written
// (ImportOptions.Claim): one another area has reads as one magpie has.
func (im *areaImport) claim(agent, user string) error {
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
