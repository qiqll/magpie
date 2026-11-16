package provider

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func areaFlow(id, agent string) *signInFlow {
	return &signInFlow{st: SignInState{ID: id, Agent: agent, State: "waiting"}, done: make(chan struct{})}
}

func forgetFlows(t *testing.T, flows ...*signInFlow) {
	t.Cleanup(func() {
		signIns.Lock()
		for _, f := range flows {
			delete(signIns.m, f.st.ID)
			delete(signIns.starting, f)
		}
		signIns.Unlock()
	})
}

func state(s *signInFlow) string { return s.status().State }

// An area's sign-in starts only while none to its agent is under way, the
// owner's started or still starting included, and never cancels one; the
// owner's always starts and replaces an area's: an area can't keep the
// owner from signing in.
func TestAreaSignInsNeverRunOver(t *testing.T) {
	const agent = "area-test"
	owner, area, owner2, area2 := areaFlow("o1", agent), areaFlow("a1", agent), areaFlow("o2", agent), areaFlow("a2", agent)
	forgetFlows(t, owner, area, owner2, area2)
	// the owner's reserved, its CLI still launching: not yet listed
	if err := reserveSignIn(owner, SignInOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := reserveSignIn(area, SignInOptions{Area: true}); !errors.Is(err, ErrSignInBusy) {
		t.Fatal("an area's started beside the owner's still starting:", err)
	}
	replaceSignIns(owner)
	if err := reserveSignIn(area, SignInOptions{Area: true}); !errors.Is(err, ErrSignInBusy) {
		t.Fatal("an area's started beside the owner's:", err)
	}
	owner.finish(SignInState{State: "canceled"})
	if err := reserveSignIn(area, SignInOptions{Area: true}); err != nil {
		t.Fatal("an area's after the owner's ended:", err)
	}
	replaceSignIns(area)
	if !SignInBusy(agent) {
		t.Fatal("not busy while an area's runs")
	}
	// another area's is refused; the owner's starts and replaces it
	if err := reserveSignIn(area2, SignInOptions{Area: true}); !errors.Is(err, ErrSignInBusy) {
		t.Fatal("a second area's beside the first:", err)
	}
	if err := reserveSignIn(owner2, SignInOptions{}); err != nil {
		t.Fatal("the owner's refused beside an area's:", err)
	}
	replaceSignIns(owner2)
	if state(area) != "canceled" || state(owner2) != "waiting" {
		t.Fatal("after the owner's started:", state(area), state(owner2))
	}
	owner2.finish(SignInState{State: "canceled"})
	if SignInBusy(agent) {
		t.Fatal("busy with none under way")
	}
	// an ended area's isn't kept once another of its agent starts
	signIns.Lock()
	_, kept := signIns.m[area.st.ID]
	signIns.Unlock()
	if kept {
		t.Fatal("an ended area's sign-in kept")
	}
}

// An area's reserved while none ran, listed after an owner's started in
// between, gives way: it is canceled, and the owner's goes on.
func TestAreaSignInGivesWayToOneStartedBeside(t *testing.T) {
	const agent = "area-beside"
	area, owner := areaFlow("a1", agent), areaFlow("o1", agent)
	forgetFlows(t, area, owner)
	if err := reserveSignIn(area, SignInOptions{Area: true}); err != nil {
		t.Fatal(err)
	}
	if err := reserveSignIn(owner, SignInOptions{}); err != nil {
		t.Fatal(err)
	}
	replaceSignIns(owner)
	replaceSignIns(area)
	if state(area) != "canceled" || state(owner) != "waiting" {
		t.Fatal(state(area), state(owner))
	}
	owner.finish(SignInState{State: "canceled"})
}

// Areas starting at once: one starts.
func TestAreaSignInsStartOneAtATime(t *testing.T) {
	const agent = "area-race"
	var flows []*signInFlow
	for _, id := range []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"} {
		flows = append(flows, areaFlow(id, agent))
	}
	forgetFlows(t, flows...)
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for _, f := range flows {
		wg.Add(1)
		go func(f *signInFlow) {
			defer wg.Done()
			if reserveSignIn(f, SignInOptions{Area: true}) == nil {
				replaceSignIns(f)
				mu.Lock()
				started++
				mu.Unlock()
			}
		}(f)
	}
	wg.Wait()
	if started != 1 {
		t.Fatal("started:", started)
	}
}

// An area's sign-in to an agent signed in to nothing here leaves it so:
// the account is magpie's alone. The owner's signs the agent in to it,
// as before.
func TestAreaSignInLeavesTheAgentAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	using, saved, err := addLoginOf(codexTestLogin(t, "area@example.com"), true)
	if err != nil || saved != "area@example.com" {
		t.Fatal(saved, err)
	}
	if using {
		t.Fatal("Codex signed in to the area's account")
	}
	if _, signedIn := liveLogin("codex"); signedIn {
		t.Fatal("Codex is signed in after an area's sign-in")
	}
	using, _, err = addLoginOf(codexTestLogin(t, "owner@example.com"), false)
	if err != nil || !using {
		t.Fatal("the owner's sign-in:", using, err)
	}
}

func codexTestLogin(t *testing.T, email string) savedLogin {
	t.Helper()
	exp := float64(time.Now().Add(time.Hour).Unix())
	l, err := codexLogin(fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
		fakeJWT(map[string]any{"exp": exp, "n": email}), "r-"+email, "acct-"+email)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// A second Team seat of one email is kept by a name of its own
// (codexName), and the sign-in says that name: an area that signs in to
// it claims that seat, never the owner's of the same email.
func TestSecondSeatSavedUnderItsOwnName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	seat := func(account string) savedLogin {
		exp := float64(time.Now().Add(time.Hour).Unix())
		l, err := codexLogin(fakeJWT(map[string]any{"email": "alice@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "team", "chatgpt_account_id": account}}),
			fakeJWT(map[string]any{"exp": exp, "n": account}), "r-"+account, account)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	_, first, err := addLoginOf(seat("ws-owner"), false)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := addLoginOf(seat("ws-area"), true)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || LoginID("codex", first) == LoginID("codex", second) {
		t.Fatalf("two seats, one account: %q and %q", first, second)
	}
}
