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
			if signIns.pending[f.st.Agent] == f {
				delete(signIns.pending, f.st.Agent)
			}
		}
		signIns.Unlock()
	})
}

// No two sign-ins to one agent run over each other once an area's is in
// them: an area's waits for none to be under way, and while it runs the
// owner's is refused rather than replacing it. The owner's still
// replace the owner's, as before.
func TestAreaSignInsNeverRunOver(t *testing.T) {
	const agent = "area-test"
	owner, area, owner2 := areaFlow("o1", agent), areaFlow("a1", agent), areaFlow("o2", agent)
	forgetFlows(t, owner, area, owner2)
	if err := reserveSignIn(owner, SignInOptions{}); err != nil {
		t.Fatal(err)
	}
	replaceSignIns(owner)
	if err := reserveSignIn(area, SignInOptions{Area: true}); !errors.Is(err, ErrSignInBusy) {
		t.Fatal("an area's started beside the owner's:", err)
	}
	owner.finish(SignInState{State: "canceled"})
	if err := reserveSignIn(area, SignInOptions{Area: true}); err != nil {
		t.Fatal("an area's after the owner's ended:", err)
	}
	// reserved, not yet listed: another can't slip in between
	if err := reserveSignIn(owner2, SignInOptions{}); !errors.Is(err, ErrSignInBusy) {
		t.Fatal("the owner's beside an area's not yet listed:", err)
	}
	replaceSignIns(area)
	if err := reserveSignIn(owner2, SignInOptions{}); !errors.Is(err, ErrSignInBusy) {
		t.Fatal("the owner's beside an area's:", err)
	}
	if !SignInBusy(agent) {
		t.Fatal("not busy while an area's runs")
	}
	// were one listed beside it anyway, it isn't replaced
	replaceSignIns(owner2)
	if st, _ := SignInStatus(area.st.ID); st.State != "waiting" {
		t.Fatal("the area's replaced:", st.State)
	}
	area.finish(SignInState{State: "canceled"})
	owner2.finish(SignInState{State: "canceled"})
	if SignInBusy(agent) {
		t.Fatal("busy with none under way")
	}
}

// Two areas starting at once: one starts.
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
	l := codexTestLogin(t, "area@example.com")
	using, err := addLoginOf(l, true)
	if err != nil {
		t.Fatal(err)
	}
	if using {
		t.Fatal("Codex signed in to the area's account")
	}
	if _, signedIn := liveLogin("codex"); signedIn {
		t.Fatal("Codex is signed in after an area's sign-in")
	}
	using, err = addLoginOf(codexTestLogin(t, "owner@example.com"), false)
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
