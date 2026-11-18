package provider

// An area's import (T-211, T-220, docs/subsystems/areas.md): the window's
// readers, ImportLoginsWith and ImportGoogleAccountsWith, run in a mode
// that brings in new accounts alone. An account magpie has, the owner's or
// another area's, is "conflict": nothing of it is written or renewed, and
// its name isn't told. A new one is claimed (ImportOptions.Claim) under
// the logins' lock, before anything of it is written, so it is never
// magpie's without being the area's.

import (
	"errors"
	"sync"
)

// ErrKnownAccount is an account an area's import found magpie has.
var ErrKnownAccount = errors.New("this account is in magpie already")

// ImportConflict is the status of an account ErrKnownAccount was found for.
const ImportConflict = "conflict"

// ImportOptions are an import's choices beside its files. The window's
// import takes none.
type ImportOptions struct {
	// NewOnly brings in new accounts alone (above). A ChatGPT account is
	// named by the id_token ChatGPT gives, a Google one by Google's
	// answer, a Claude one by Anthropic's (claude_profile.go): never by
	// what the file says, which also names nothing the tool it came from
	// called it.
	NewOnly bool
	// Admit, when set, is asked for each account read, in the files'
	// order, before it is judged or asked of its vendor, and may wait for
	// those admitted before: why it is not to be (an area at its 500
	// accounts, or held for its conflicts), or "" and done, told how it
	// went once it has (from another goroutine), which says how it reads.
	Admit func() (done func(ImportedAccount) ImportedAccount, why string)
	// Claim takes a new account (NewOnly), by its agent and the name it
	// is kept by, under the logins' lock before anything of it is
	// written: an error keeps it out (ErrKnownAccount, wrapped or not,
	// reads "conflict"). Unclaim lets go of one claimed whose save then
	// failed.
	Claim   func(agent, user string) error
	Unclaim func(agent, user string)
}

// admit asks o's Admit for one account named name: the func its row goes
// through, or false with *row failed, saying why.
func (o ImportOptions) admit(row *ImportedAccount, name string) (func(ImportedAccount) ImportedAccount, bool) {
	if o.Admit == nil {
		return func(r ImportedAccount) ImportedAccount { return r }, true
	}
	done, why := o.Admit()
	if why != "" {
		*row = ImportedAccount{User: name, Status: "failed", Error: why}
		return nil, false
	}
	return done, true
}

// known is r, an account magpie has ("exists", "updated"), as o tells it:
// for NewOnly a conflict with no name.
func (o ImportOptions) known(r ImportedAccount) ImportedAccount {
	if !o.NewOnly {
		return r
	}
	return ImportedAccount{Status: ImportConflict, Error: ErrKnownAccount.Error()}
}

// claimer is o's Claim for agent as the save paths take it, and undo,
// which lets go of what it took.
func (o ImportOptions) claimer(agent string) (claim func(string) error, undo func()) {
	var mu sync.Mutex
	took := ""
	claim = func(user string) error {
		if o.Claim == nil {
			return nil
		}
		if err := o.Claim(agent, user); err != nil {
			return err
		}
		mu.Lock()
		took = user
		mu.Unlock()
		return nil
	}
	undo = func() {
		mu.Lock()
		u := took
		mu.Unlock()
		if u != "" && o.Unclaim != nil {
			o.Unclaim(agent, u)
		}
	}
	return claim, undo
}

// saved is how saving a new account named user went, err from its save.
func (o ImportOptions) saved(user, plan, name string, err error) ImportedAccount {
	switch {
	case errors.Is(err, ErrKnownAccount):
		return o.known(ImportedAccount{Status: "exists"})
	case err != nil:
		return ImportedAccount{User: name, Status: "failed", Error: err.Error()}
	}
	return ImportedAccount{User: user, Status: "added", Plan: plan}
}

// save keeps l, a new ChatGPT or Claude account (NewOnly), as an area's
// sign-in keeps one: magpie's alone, the agent here left as it is.
func (o ImportOptions) save(l savedLogin, name string) ImportedAccount {
	claim, undo := o.claimer(l.Agent)
	_, user, err := addLoginWith(l, true, claim)
	if err != nil && !errors.Is(err, ErrKnownAccount) {
		undo()
	}
	return o.saved(user, l.Plan, name, err)
}
