// Package release carries a release's life cycle and the version invariant.
//
// The invariant in one sentence: a version is a string, present identically in four
// places — the tracker version's name, the git tag, the image tag, and the version
// baked into the image that the application reports back.
//
// This is not naming convenience: it is what makes the join possible. If those
// values diverge, every feature of the dashboard degrades differently — an
// environment wrongly shown as divergent, an empty version screen, a release note
// comparing tag ranges unrelated to the images promoted.
//
// This package is pure: no database, no cluster, no network. The decisions live
// here, and they must be checkable without standing anything up.
package release

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

// State is a release's state.
type State string

const (
	// StateDraft: created in the dashboard, git tag not yet set.
	StateDraft State = "draft"
	// StateBuilding : tag posé, `build-image` en cours.
	StateBuilding State = "building"
	// StateVerifying: images available, the test campaign under way or partial.
	//
	// `verifying` rather than `testing`: `testing` is an environment's other name in
	// existing code, and two meanings for one word in the same system is gratuitous
	// confusion.
	StateVerifying State = "verifying"
	// StateReady: every mandatory test finished, human decision pending. Not yet
	// promotable — `released` is what allows that.
	StateReady State = "ready"
	// StateReleased: approved, promotable to higher-ranked environments.
	StateReleased State = "released"
	// StateFailed: a mandatory pipeline failed. Re-runnable, which returns to
	// `verifying` when the images already exist.
	StateFailed State = "failed"
	// StateCancelled: abandoned. Terminal — resuming an abandoned release would hide
	// the reason it was abandoned, while creating a new one leaves it readable.
	StateCancelled State = "cancelled"
)

// transitions describes the allowed passages.
//
// An explicit table rather than scattered ifs: the day a transition is missing, it
// is missing in one place, and reading it is enough to know what is possible.
//
// Two paths coexist, deliberately:
//
//	built (pre, patch)
//	  draft ──▶ building ──▶ verifying ──▶ ready ──▶ released
//
//	derived (final, a re-tag of an approved candidate)
//	  draft ──▶ ready ──▶ released
//
// The derived path passes through neither `building` nor `verifying`: nothing is
// built, so nothing runs. Modelling a fictional `building` would make the interface
// wait for a pipeline that will never come.
var transitions = map[State][]State{
	// `draft → ready` is the gate of the derived path; `draft → building` that of the
	// built path.
	StateDraft:    {StateBuilding, StateReady, StateFailed, StateCancelled},
	StateBuilding: {StateVerifying, StateReady, StateFailed, StateCancelled},
	// `verifying → building`: rebuilding from the same reference stays allowed, since a
	// build can fail for a reason outside the release.
	StateVerifying: {StateReady, StateBuilding, StateFailed, StateCancelled},
	// From `ready`, the human decision settles it: ship, or send back to the tests
	// because a family is to be re-run.
	StateReady: {StateReleased, StateVerifying, StateBuilding, StateFailed, StateCancelled},
	// `released` is not terminal: a shipped release may be abandoned — withdrawn after a
	// regression — but does not go back down to the tests. Replaying its approval
	// afterwards would rewrite the history of something already delivered.
	StateReleased: {StateCancelled},
	// A failure is re-runnable: `verifying` when the images already exist, `building`
	// when a rebuild is needed.
	//
	// `failed → ready` is added, and it is not a shortcut: a re-run verification family
	// may succeed, and the last verdict is then the one that counts. Without this
	// passage a release stayed failed over a result the next attempt had replaced, and
	// the "replay" button caught nothing up. The decision to ship stays human — `ready`
	// does not mean shipped.
	StateFailed: {StateBuilding, StateVerifying, StateReady, StateCancelled},
	// `cancelled` is terminal, by design: see above.
	StateCancelled: {},
}

// Terminal says whether a state leads nowhere.
func (s State) Terminal() bool { return len(transitions[s]) == 0 }

// CanTransition says whether the passage is allowed.
func CanTransition(from, to State) bool {
	return slices.Contains(transitions[from], to)
}

// ErrInvalidTransition signale un passage refusé.
type ErrInvalidTransition struct {
	From State
	To   State
}

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("transition refused: %s → %s", e.From, e.To)
}

// CheckTrackedVersion checks the join with the tracker.
//
// `release tag = tracker version name [+ pre-release suffix]`. A candidate
// `3.2.0-rc1` therefore attaches to version `3.2.0`, which is what links a
// pre-release to its functional scope.
//
// The comparison is normalised ([NormalizeVersionName]) and not strict. Exact
// equality rejected every real release: the instance's versions are called `v3.2.0`,
// never `3.2.0`.
func CheckTrackedVersion(version versionpkg.Version, redmineName string) error {
	if !SameVersion(version, redmineName) {
		return fmt.Errorf(
			"tag %q attaches to version %q, while the tracker version aimed at is %q",
			version.Raw, version.Base, strings.TrimSpace(redmineName))
	}
	return nil
}

// ErrSourceRequired reports a final release with no source candidate.
var ErrSourceRequired = errors.New("a final release by re-tag requires a source pre-release")

// Source describes the pre-release a final release reuses.
type Source struct {
	Tag          string
	ImageVersion string
	State        State
	// TestsGreen is false when the candidate's results are not green. The release stays
	// possible — it is a human decision — but it must be flagged.
	TestsGreen bool
}

// FinalFromSource composes a final release's image from its candidate.
//
// Candidate to final happens by re-tag: what goes to production is
func FinalFromSource(source Source) (imageVersion string, warnings []string, err error) {
	if strings.TrimSpace(source.Tag) == "" {
		return "", nil, ErrSourceRequired
	}
	// The source candidate must be approved, not merely ready. The distinction carries
	// the whole meaning of the re-tag: it carries the candidate's approval over to the
	// final release, so there is nothing to carry while nobody has approved. Accepting a
	// `ready` candidate would ship on the strength of green tests, where the cycle
	// reserves that decision to a human.
	if source.State != StateReleased {
		return "", nil, fmt.Errorf(
			"pre-release %q is in state %q: only a %q release can be re-tagged",
			source.Tag, source.State, StateReleased)
	}

	imageVersion = source.ImageVersion
	if imageVersion == "" {
		// A candidate whose image is not identified cannot be re-tagged: there is nothing to
		// promote.
		return "", nil, fmt.Errorf("pre-release %q carries no image version", source.Tag)
	}

	if !source.TestsGreen {
		// A warning and not a refusal: shipping a candidate with a red test is a decision
		// that belongs to the deployment. Staying silent about it, however, would be a
		// fault.
		warnings = append(warnings, fmt.Sprintf(
			"the results of pre-release %q are not green", source.Tag))
	}

	return imageVersion, warnings, nil
}
