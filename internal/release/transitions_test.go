package release

import "testing"

// The two paths, walked end to end.
//
// The derived path passes through neither `building` nor `verifying`: nothing is
// built, so nothing runs. Modelling a fictional `building` would make the interface
// wait for a pipeline that will never come.
func TestBothPathsCanBeWalked(t *testing.T) {
	routes := map[string][]State{
		"construite": {StateDraft, StateBuilding, StateVerifying, StateReady, StateReleased},
		"derived":    {StateDraft, StateReady, StateReleased},
	}

	for name, route := range routes {
		for i := 0; i < len(route)-1; i++ {
			from, to := route[i], route[i+1]
			if !CanTransition(from, to) {
				t.Errorf("path %s: %s → %s refused", name, from, to)
			}
		}
	}
}

// A shipped release does not go back down to the tests: replaying its approval
// afterwards would rewrite the history of something already delivered.
func TestAShippedReleaseDoesNotGoBackDown(t *testing.T) {
	for _, to := range []State{StateDraft, StateBuilding, StateVerifying, StateReady, StateFailed} {
		if CanTransition(StateReleased, to) {
			t.Errorf("released → %s should be refused", to)
		}
	}
	// It may however be abandoned: a regression found after shipping must be
	// recordable.
	if !CanTransition(StateReleased, StateCancelled) {
		t.Error("released → cancelled should be allowed")
	}
}

// `cancelled` is terminal by design: resuming an abandoned release would hide the
// reason it was abandoned, while creating a new one leaves it readable.
func TestAnAbandonedReleaseIsTerminal(t *testing.T) {
	if !StateCancelled.Terminal() {
		t.Error("cancelled should be terminal")
	}
	for _, to := range []State{StateDraft, StateBuilding, StateVerifying, StateReady, StateReleased, StateFailed} {
		if CanTransition(StateCancelled, to) {
			t.Errorf("cancelled → %s should be refused", to)
		}
	}
	// No other state is terminal: every one must at least be abandonable, or a release
	// would be stuck with no way out.
	for _, state := range []State{StateDraft, StateBuilding, StateVerifying, StateReady, StateReleased, StateFailed} {
		if state.Terminal() {
			t.Errorf("%s should not be terminal", state)
		}
	}
}

// Every non-terminal state must be abandonable: it is the way out of a release
// nobody will ship, and its absence would leave live rows nothing can close.
func TestEveryLiveStateCanBeAbandoned(t *testing.T) {
	for _, state := range []State{
		StateDraft, StateBuilding, StateVerifying, StateReady, StateReleased, StateFailed,
	} {
		if !CanTransition(state, StateCancelled) {
			t.Errorf("%s → cancelled should be allowed", state)
		}
	}
}

// A failure is re-runnable both ways: `verifying` when the images already exist,
// `building` when a rebuild is needed.
func TestAFailureIsReRunnable(t *testing.T) {
	if !CanTransition(StateFailed, StateBuilding) {
		t.Error("failed → building should be allowed")
	}
	if !CanTransition(StateFailed, StateVerifying) {
		t.Error("failed → verifying should be allowed")
	}
	// But a failure does not become shippable without a human decision.
	if CanTransition(StateFailed, StateReleased) {
		t.Error("failed → released should be refused")
	}
}

// `failed → ready` is allowed, and this rule changed.
//
// It was refused on the grounds that "the tests must be run again". That was exactly
// the case it forbade: re-running a verification family is running the tests again,
// and its verdict replaces the previous one. The release therefore stayed failed over
// a stale result, and the "replay" button caught nothing up.
//
// What the rule protected is elsewhere, and still holds: `failed` does not lead to
// `released`. Moving to `ready` ships nothing — it is the "decision pending" state,
// and the decision stays human.
func TestAFailureCaughtUpByAReRunBecomesDecidableAgain(t *testing.T) {
	if !CanTransition(StateFailed, StateReady) {
		t.Error("failed → ready should be allowed: a successful re-run replaces the verdict")
	}
}

// No state transitions to itself: a transition with no effect would hide a caller
// that believes it is advancing something.
func TestNoStateTransitionsToItself(t *testing.T) {
	for _, state := range []State{
		StateDraft, StateBuilding, StateVerifying, StateReady,
		StateReleased, StateFailed, StateCancelled,
	} {
		if CanTransition(state, state) {
			t.Errorf("%s → %s should be refused", state, state)
		}
	}
}

// An unknown state opens nothing: a value read from the database matching no
// declared state must not behave as a wildcard.
func TestAnUnknownStateOpensNothing(t *testing.T) {
	unknown := State("invented")
	for _, to := range []State{StateBuilding, StateReady, StateReleased, StateCancelled} {
		if CanTransition(unknown, to) {
			t.Errorf("invented → %s should be refused", to)
		}
	}
	// And it is seen as terminal, which is the safe behaviour: nothing will advance it
	// until someone fixes the data.
	if !unknown.Terminal() {
		t.Error("an unknown state should be seen as terminal")
	}
}

// Building does not skip the tests on the built path, but `building → ready` stays
// open: not every test family is mandatory, and a release with none goes straight to
// awaiting a decision.
func TestABuildCanConcludeWithoutTests(t *testing.T) {
	if !CanTransition(StateBuilding, StateReady) {
		t.Error("building → ready devrait rester permis")
	}
	if !CanTransition(StateBuilding, StateVerifying) {
		t.Error("building → verifying should be allowed")
	}
}
