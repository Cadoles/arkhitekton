package release

import (
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

// The historical cycle must describe exactly what the database enumeration
// described: a project declared before the change carries on with nobody writing
// anything.
func TestTheDefaultCycleReproducesTheThreeKinds(t *testing.T) {
	cycle := DefaultCycle()

	if err := cycle.Validate(version.SemVer{}); err != nil {
		t.Fatalf("the default cycle must be valid: %v", err)
	}

	// The trait that carried the no-rebuild rule: a final release re-tags, it does not
	// build.
	final, err := cycle.Find("final")
	if err != nil {
		t.Fatalf("nature final: %v", err)
	}
	if final.Mode != ModeRetag || final.Source != "pre" {
		t.Errorf("final: %+v — the rule holds through the mode, not the name", final)
	}

	// And the flaw the separation reveals: two of the historical kinds differ only by
	// the shape of the tag and the permission; their behaviour is the same.
	pre, _ := cycle.Find("pre")
	patch, _ := cycle.Find("patch")
	if pre.Mode != ModeBuild || patch.Mode != ModeBuild {
		t.Errorf("pre=%s patch=%s — both build", pre.Mode, patch.Mode)
	}
	if pre.Tag == patch.Tag && pre.Permission == patch.Permission {
		t.Error("pre et patch seraient alors indiscernables")
	}
}

// An unknown kind is named alongside the ones that exist: searching the source for
// what one might have written is avoidable waste.
func TestAnUnknownKindNamesTheOnesThatExist(t *testing.T) {
	_, err := DefaultCycle().Find("ga")
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("error %v, expected ErrUnknownKind", err)
	}
	for _, name := range []string{"pre", "final", "patch"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("message %q — it must name the declared kinds", err)
		}
	}
}

// The cycle none of the three historical slots could describe, which is the whole
// point of the change.
func TestAnAlphaBetaGaCycleIsDeclarable(t *testing.T) {
	cycle := Cycle{
		{ID: "alpha", Label: "Alpha", Mode: ModeBuild, Tag: TagPrereleaseRequired},
		{ID: "beta", Label: "Beta", Mode: ModeBuild, Tag: TagPrereleaseRequired},
		{ID: "ga", Label: "Livraison", Mode: ModeRetag, Source: "beta",
			Tag: TagPrereleaseForbidden, Permission: "release:create:final"},
	}

	if err := cycle.Validate(version.SemVer{}); err != nil {
		t.Fatalf("cycle refused: %v", err)
	}
}

// A project that distinguishes nothing — a single sort of release — previously had
// to pick one arbitrarily.
func TestASingleKindCycleIsValid(t *testing.T) {
	cycle := Cycle{{ID: "release", Label: "Release", Mode: ModeBuild}}

	if err := cycle.Validate(version.SemVer{}); err != nil {
		t.Errorf("single-kind cycle refused: %v", err)
	}
}

// The kind × scheme coupling, refused at configuration time.
//
// On an opaque scheme, `prerelease-required` can never be checked: a release
// created believing it held would go to an environment that excludes it, with
// nothing saying so.
func TestATagRuleAssumesASchemeThatCanAnswer(t *testing.T) {
	cycle := Cycle{{ID: "rc", Mode: ModeBuild, Tag: TagPrereleaseRequired}}

	err := cycle.Validate(version.Opaque{})
	if err == nil {
		t.Fatal("a rule the scheme cannot check must be refused")
	}
	if !strings.Contains(err.Error(), "pre-release") {
		t.Errorf("message %q — it must say what the scheme does not know", err)
	}

	// With no rule, the same scheme is fine: an opaque project stays usable.
	libre := Cycle{{ID: "release", Mode: ModeBuild}}
	if err := libre.Validate(version.Opaque{}); err != nil {
		t.Errorf("a cycle with no tag rule must suit an opaque scheme: %v", err)
	}
}

// A non-existent source would only be discovered when shipping, which is the worst
// moment.
func TestANonExistentSourceIsRefused(t *testing.T) {
	cycle := Cycle{
		{ID: "rc", Mode: ModeBuild},
		{ID: "ga", Mode: ModeRetag, Source: "beta"},
	}

	err := cycle.Validate(version.SemVer{})
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Errorf("error %v — the missing source must be named", err)
	}
}

// A source declared after the kind that cites it stays valid: the order of the list
// is the display order, not a dependency.
func TestASourceDeclaredLowerDownStaysValid(t *testing.T) {
	cycle := Cycle{
		{ID: "ga", Mode: ModeRetag, Source: "rc"},
		{ID: "rc", Mode: ModeBuild},
	}

	if err := cycle.Validate(version.SemVer{}); err != nil {
		t.Errorf("cycle refused: %v", err)
	}
}

// The structural inconsistencies.
func TestAnInconsistentCycleIsRefused(t *testing.T) {
	cases := map[string]Cycle{
		"empty":           {},
		"without an id":   {{Mode: ModeBuild}},
		"unknown mode":    {{ID: "rc", Mode: "deploy"}},
		"unknown rule":    {{ID: "rc", Mode: ModeBuild, Tag: "prerelease-maybe"}},
		"duplicated kind": {{ID: "rc", Mode: ModeBuild}, {ID: "rc", Mode: ModeRetag}},
	}

	for name, cycle := range cases {
		if err := cycle.Validate(version.SemVer{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Shape checking, and its deliberate silence on a scheme that cannot tell:
// configuration has already forbidden that case, and failing here on a valid cycle
// would block a creation for an unfixable reason.
func TestShapeCheckingStaysSilentWhenTheSchemeCannotTell(t *testing.T) {
	spec := KindSpec{ID: "rc", Tag: TagPrereleaseRequired}

	opaque, _ := version.Opaque{}.Parse("build-4711")
	if err := CheckTag(spec, opaque); err != nil {
		t.Errorf("the scheme cannot answer: %v", err)
	}

	finale, _ := version.SemVer{}.Parse("1.8.0")
	if err := CheckTag(spec, finale); err == nil {
		t.Error("a kind requiring a pre-release must refuse a final version")
	}

	rc, _ := version.SemVer{}.Parse("1.8.0-rc1")
	if err := CheckTag(spec, rc); err != nil {
		t.Errorf("a candidate must be accepted: %v", err)
	}
}

// An empty kind is not a misspelled kind: it is a caller that chose nothing, and the
// message must say so rather than quote empty quotes.
//
// It happened: the creation form submitted its initial state — the empty string —
// rather than the kind it displayed as checked. The error named a value the user had
// never seen.
func TestAnEmptyKindIsToldApartFromAnUnknownOne(t *testing.T) {
	_, err := DefaultCycle().Find("")
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("error %v, expected ErrUnknownKind", err)
	}
	if !strings.Contains(err.Error(), "no kind") {
		t.Errorf("message %q — it must say that nothing was chosen", err)
	}
	// And it still names what exists: that is what allows fixing it.
	if !strings.Contains(err.Error(), "pre") {
		t.Errorf("message %q — it must name the declared kinds", err)
	}
}
