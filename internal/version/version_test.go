package version

import (
	"errors"
	"strings"
	"testing"
)

// The shape kept, and what it refuses. A malformed tag would break the join with the
// tracker and range selection: better said at creation than discovered by noticing
// an environment never moves.
func TestSemVerDecomposesAndRefuses(t *testing.T) {
	scheme := SemVer{}

	version, err := scheme.Parse("1.8.0-rc1")
	if err != nil {
		t.Fatalf("valid tag refused: %v", err)
	}
	if version.Base != "1.8.0" || version.Prerelease != "rc1" {
		t.Errorf("decomposition: %+v", version)
	}
	prerelease, known := version.Prereleased()
	if !known || !prerelease {
		t.Errorf("pre-release: %v/%v", prerelease, known)
	}

	for _, tag := range []string{"", "v1.8.0", "1.8", "1.8.0+build", "build-4711"} {
		if _, err := scheme.Parse(tag); err == nil {
			t.Errorf("tag %q accepted", tag)
		}
	}
}

// A final version answers "no, and I am sure". That is what the second return
// distinguishes from a scheme that does not know.
func TestASemVerFinalVersionAssertsItIsNotAPrerelease(t *testing.T) {
	version, _ := SemVer{}.Parse("1.8.0")

	prerelease, known := version.Prereleased()
	if prerelease || !known {
		t.Errorf("pre-release: %v/%v — a SemVer final version is a firm answer",
			prerelease, known)
	}
}

// SemVer order, and the two traps a lexical sort misses.
func TestSemVerOrderIsNotLexical(t *testing.T) {
	scheme := SemVer{}

	// `1.10.0` follows `1.9.0`: compared as strings it would precede it.
	if order, err := scheme.Compare("1.10.0", "1.9.0"); err != nil || order <= 0 {
		t.Errorf("1.10.0 vs 1.9.0 = %d (%v) — the comparison must be numeric", order, err)
	}

	// A pre-release precedes its final version: yet `-` sorts after the empty string.
	if order, err := scheme.Compare("1.8.0-rc1", "1.8.0"); err != nil || order >= 0 {
		t.Errorf("1.8.0-rc1 vs 1.8.0 = %d (%v) — a candidate precedes its final version", order, err)
	}

	if order, err := scheme.Compare("1.8.0", "1.8.0"); err != nil || order != 0 {
		t.Errorf("equality = %d (%v)", order, err)
	}
	if order, err := scheme.Compare("1.8.0-rc1", "1.8.0-rc2"); err != nil || order >= 0 {
		t.Errorf("rc1 vs rc2 = %d (%v)", order, err)
	}
}

/*
 * The opaque scheme, and it is what proves the contract.
 *
 * A contract written on the SemVer implementation alone would have taken its shape without anyone
 * noticing: every question would have returned a value, and the absence of an answer would have
 * been indistinguishable from a negative one.
 */

// Every non-empty tag is well formed: with no declared convention nothing allows
// saying otherwise, and refusing it would impose a rule the project does not have.
func TestTheOpaqueSchemeAcceptsAnyNonEmptyTag(t *testing.T) {
	for _, tag := range []string{"build-4711", "2026.7.27-k8sunstable.1651", "v1.8.0", "n'importe quoi"} {
		version, err := Opaque{}.Parse(tag)
		if err != nil {
			t.Errorf("tag %q refused: %v", tag, err)
			continue
		}
		if version.Raw == "" {
			t.Errorf("tag %q decomposed into nothing", tag)
		}
	}

	if _, err := (Opaque{}).Parse("   "); err == nil {
		t.Error("an empty tag is still refused")
	}
}

// The trap the second return of Prereleased exists to avoid: on an opaque scheme,
// false alone would assert that a release is final when nothing says so — and that is
// how a tag nobody approved gets promoted to production.
func TestTheOpaqueSchemeNeverAssertsATagIsFinal(t *testing.T) {
	version, err := Opaque{}.Parse("build-4711")
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}

	prerelease, known := version.Prereleased()
	if known {
		t.Error("the opaque scheme cannot answer this question")
	}
	if prerelease {
		t.Error("and above all it must not answer yes")
	}
	if version.Base != "" {
		t.Errorf("version line %q — it is unknown", version.Base)
	}
}

// A question it refuses to answer. An invented answer — a lexical order — would be
// displayed as though it were the right one.
func TestTheOpaqueSchemeRefusesWhatItDoesNotKnow(t *testing.T) {
	if _, err := (Opaque{}).Compare("build-4711", "build-4712"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("comparison: %v, expected ErrUnsupported", err)
	}
}

// The declaration resolves, and the unknown one names what exists rather than
// leaving someone to search the source.
func TestASchemeDeclarationResolves(t *testing.T) {
	// Empty means SemVer: it is what every declared project uses, and requiring an
	// explicit declaration would have invalidated existing rows.
	scheme, err := For("")
	if err != nil || scheme.ID() != "semver" {
		t.Errorf("default scheme = %v (%v)", scheme, err)
	}

	if scheme, err := For("opaque"); err != nil || scheme.ID() != "opaque" {
		t.Errorf("opaque scheme = %v (%v)", scheme, err)
	}

	_, err = For("calendaire")
	if err == nil {
		t.Fatal("an unknown scheme must be refused")
	}
	for _, name := range []string{"semver", "opaque"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("message %q — it must name the known schemes", err)
		}
	}
}

// The sixth question, the one that was missing.
//
// The interface used to answer it itself, hard-coding `-rc<N>`: one project's
// convention imposed on every project whose pre-releases read otherwise. A wrong
// suggestion is not dramatic — the field stays editable — but it teaches a
// convention, and that is how an ambient assumption reinstalls itself.
func TestTheNextPrereleaseIsDeducedFromExistingOnes(t *testing.T) {
	scheme := SemVer{}

	// None: the first.
	got, err := scheme.NextPrerelease("1.8.0", nil)
	if err != nil || got != "1.8.0-rc1" {
		t.Errorf("first = %q (%v)", got, err)
	}

	// The next one is deduced from the highest rank, not from how many releases exist:
	// proposing `-rc3` because there are three would fail if `rc5` exists.
	existing := []string{"1.8.0-rc1", "1.8.0-rc5", "1.8.0-rc2"}
	if got, err := scheme.NextPrerelease("1.8.0", existing); err != nil || got != "1.8.0-rc6" {
		t.Errorf("suivante = %q (%v)", got, err)
	}

	// Other lines do not count: `1.9.0-rc4` says nothing about `1.8.0`.
	if got, _ := scheme.NextPrerelease("1.8.0", []string{"1.9.0-rc4"}); got != "1.8.0-rc1" {
		t.Errorf("ligne voisine prise en compte: %q", got)
	}

	// A pre-release of another shape is ignored rather than refused: a history may
	// legitimately carry `1.8.0-beta1`.
	if got, _ := scheme.NextPrerelease("1.8.0", []string{"1.8.0-beta1"}); got != "1.8.0-rc1" {
		t.Errorf("foreign shape: %q", got)
	}
}

// The opaque scheme refuses: with no grammar, "the next one" does not exist, and
// proposing `-rc1` would teach a convention the project did not choose.
func TestTheOpaqueSchemeProposesNoNextVersion(t *testing.T) {
	if _, err := (Opaque{}).NextPrerelease("build-4711", nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("next: %v, expected ErrUnsupported", err)
	}
}
