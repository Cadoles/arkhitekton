package release

import (
	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
	"strings"
	"testing"
)

// Shape and kind are now exercised in internal/version and cycle_test.go: that is
// where the scheme and the cycle live. Replaying them here would test the same rule
// twice from the package that no longer owns it.
func TestCheckRedmineVersionTiesPrereleaseToItsScope(t *testing.T) {
	rc, _ := versionpkg.SemVer{}.Parse("1.8.0-rc3")

	if err := CheckTrackedVersion(rc, "1.8.0"); err != nil {
		t.Errorf("a candidate must attach to its base version: %v", err)
	}
	if err := CheckTrackedVersion(rc, "1.7.0"); err == nil {
		t.Error("attaching to the wrong version must be refused")
	}
}

func TestTransitions(t *testing.T) {
	allowed := [][2]State{
		{StateDraft, StateBuilding},
		{StateBuilding, StateReady},
		{StateBuilding, StateFailed},
		// A rebuild stays possible: a build can fail for a reason outside the release.
		{StateFailed, StateBuilding},
		{StateReady, StateBuilding},
		// `draft → ready` is the gate of the derived path: a final release is a re-tag,
		// nothing is built or run, so it goes straight to awaiting a decision. That passage
		// was refused while the code modelled only the built path.
		{StateDraft, StateReady},
		// `ready → failed`: a release awaiting a decision can see a mandatory pipeline fall
		// back into failure.
		{StateReady, StateFailed},
	}
	refused := [][2]State{
		// Nothing returns to `draft`: that state says "not built yet", and going back would
		// erase what has been produced since.
		{StateReady, StateDraft},
		{StateBuilding, StateDraft},
		{StateReleased, StateDraft},
	}

	for _, pair := range allowed {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("%s → %s should be allowed", pair[0], pair[1])
		}
	}
	for _, pair := range refused {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("%s → %s should be refused", pair[0], pair[1])
		}
	}
}

// The re-tag: what goes to production is bit for bit what was approved. The image
// therefore still carries the candidate's version, and `image_version` absorbs the
// difference — comparing the running version with the tag would fail on every final
// release.
func TestFinalFromSourceKeepsTheSourceImageVersion(t *testing.T) {
	imageVersion, warnings, err := FinalFromSource(Source{
		Tag: "1.8.0-rc3", ImageVersion: "1.8.0-rc3", State: StateReleased, TestsGreen: true,
	})
	if err != nil {
		t.Fatalf("re-tag refused: %v", err)
	}
	if imageVersion != "1.8.0-rc3" {
		t.Errorf("image version = %q, expected the candidate's", imageVersion)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

// Shipping a candidate with a red test belongs to the deployment. Staying silent
// about it would be a fault.
func TestFinalFromSourceWarnsOnRedTests(t *testing.T) {
	_, warnings, err := FinalFromSource(Source{
		Tag: "1.8.0-rc3", ImageVersion: "1.8.0-rc3", State: StateReleased, TestsGreen: false,
	})
	if err != nil {
		t.Fatalf("the re-tag must not be refused: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "1.8.0-rc3") {
		t.Errorf("warning = %v", warnings)
	}
}

func TestFinalFromSourceRefusesUnusableSources(t *testing.T) {
	if _, _, err := FinalFromSource(Source{}); err == nil {
		t.Error("a final release with no source must be refused")
	}

	if _, _, err := FinalFromSource(Source{
		Tag: "1.8.0-rc3", ImageVersion: "1.8.0-rc3", State: StateBuilding,
	}); err == nil {
		t.Error("a candidate still building must not be re-tagged")
	}

	if _, _, err := FinalFromSource(Source{
		Tag: "1.8.0-rc3", State: StateReleased,
	}); err == nil {
		t.Error("a candidate with no image version must not be re-tagged")
	}
}

// The source candidate must be approved, not merely ready. That is the whole meaning
// of the re-tag: it carries approval over to the final release, and there is nothing
// to carry while nobody has approved.
func TestAnUnapprovedCandidateCannotBeReTagged(t *testing.T) {
	for _, state := range []State{StateDraft, StateBuilding, StateVerifying, StateReady, StateFailed} {
		_, _, err := FinalFromSource(Source{
			Tag: "1.8.0-rc3", ImageVersion: "1.8.0-rc3", State: state, TestsGreen: true,
		})
		if err == nil {
			t.Errorf("state %q: the re-tag should be refused while the candidate is not approved", state)
		}
	}
}
