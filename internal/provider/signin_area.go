package provider

import (
	"context"
	"errors"
)

// An area's admin signs in to a subscription through the gateway's admin
// routes (docs/subsystems/areas.md), from a browser elsewhere. Such a
// sign-in records the account and nothing more: the agent on this
// computer is never signed in to it (addLoginOf). An area's starts only
// while no other sign-in to its agent is under way, and never cancels
// one; the owner's always starts, and replaces every other, an area's
// included, as one of the owner's always replaced another: an area can't
// keep the owner from signing in.
//
// Every sign-in is held in signIns.starting from its reservation until it
// is listed (replaceSignIns) or over, so one being started (a CLI being
// launched) is seen by another starting beside it.

// ErrSignInBusy: another sign-in to the agent is under way.
var ErrSignInBusy = errors.New("another sign-in to this subscription is under way: wait for it to end, or cancel it, and start again")

// reserveSignIn lets s start, or says why it can't: an area's, while
// another sign-in to its agent is under way.
func reserveSignIn(s *signInFlow, o SignInOptions) error {
	s.area, s.onDone = o.Area, o.Done
	signIns.Lock()
	defer signIns.Unlock()
	if s.area && liveBeside(s) {
		return ErrSignInBusy
	}
	signIns.starting[s] = true
	return nil
}

// liveBeside: another sign-in to s's agent, listed or starting, isn't
// over. Called with signIns held.
func liveBeside(s *signInFlow) bool {
	agent := s.st.Agent
	for f := range signIns.starting {
		if f != s && f.st.Agent == agent && !f.over.Load() {
			return true
		}
	}
	for _, f := range signIns.m {
		if f != s && f.st.Agent == agent && !f.over.Load() {
			return true
		}
	}
	return false
}

// unreserveSignIn lets go of s's reservation, once it ended.
func unreserveSignIn(s *signInFlow) {
	signIns.Lock()
	delete(signIns.starting, s)
	signIns.Unlock()
}

// SignInBusy: a sign-in to agent is under way.
func SignInBusy(agent string) bool {
	signIns.Lock()
	defer signIns.Unlock()
	return liveBeside(&signInFlow{st: SignInState{Agent: agent}})
}

// areaSignIn: ctx is an area's sign-in's (via).
func areaSignIn(ctx context.Context) bool {
	s, ok := ctx.Value(signInKey{}).(*signInFlow)
	return ok && s.area
}
