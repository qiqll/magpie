package provider

import (
	"context"
	"errors"
)

// An area's admin signs in to a subscription through the gateway's admin
// routes (docs/subsystems/areas.md), from a browser elsewhere. Such a
// sign-in records the account and nothing more: the agent on this
// computer is never signed in to it (addLoginOf), and no two sign-ins
// to one agent run over each other. While one runs, an area's or the
// owner's, an area's is refused; while an area's runs, every other is
// refused rather than replacing it, as one of the owner's replaces
// another of the owner's.

// ErrSignInBusy: another sign-in to the agent is under way.
var ErrSignInBusy = errors.New("another sign-in to this subscription is under way: wait for it to end, or cancel it, and start again")

// reserveSignIn lets s start, or says why it can't: a sign-in to its
// agent under way that it may not replace. An area's is held as pending
// until it is listed (replaceSignIns), so two started at once can't both
// pass.
func reserveSignIn(s *signInFlow, o SignInOptions) error {
	s.area, s.onDone = o.Area, o.Done
	agent := s.st.Agent
	signIns.Lock()
	defer signIns.Unlock()
	if p := signIns.pending[agent]; p != nil && !p.over.Load() {
		return ErrSignInBusy
	}
	for _, f := range signIns.m {
		if f.st.Agent == agent && !f.over.Load() && (f.area || s.area) {
			return ErrSignInBusy
		}
	}
	if s.area {
		signIns.pending[agent] = s
	}
	return nil
}

// unreserveSignIn lets go of s's reservation, once it ended.
func unreserveSignIn(s *signInFlow) {
	signIns.Lock()
	if signIns.pending[s.st.Agent] == s {
		delete(signIns.pending, s.st.Agent)
	}
	signIns.Unlock()
}

// SignInBusy: a sign-in to agent is under way.
func SignInBusy(agent string) bool {
	signIns.Lock()
	defer signIns.Unlock()
	if p := signIns.pending[agent]; p != nil && !p.over.Load() {
		return true
	}
	for _, f := range signIns.m {
		if f.st.Agent == agent && !f.over.Load() {
			return true
		}
	}
	return false
}

// areaSignIn: ctx is an area's sign-in's (via).
func areaSignIn(ctx context.Context) bool {
	s, ok := ctx.Value(signInKey{}).(*signInFlow)
	return ok && s.area
}
