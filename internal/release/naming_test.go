package release

import (
	"testing"

	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

func TestNormalizeVersionName(t *testing.T) {
	cases := []struct {
		name, want, why string
	}{
		{"v3.2.0", "3.2.0", "convention courante"},
		{"V3.2.0", "3.2.0", "majuscule admise"},
		{"3.2.0", "3.2.0", "already normalised"},
		{"  v3.2.0  ", "3.2.0", "espaces autour"},

		// `v` is stripped only before a digit: otherwise a name would be amputated.
		{"validation", "validation", "v not followed by a digit"},
		{"vNext", "vNext", "v not followed by a digit"},
		{"v", "v", "prefix alone"},
		{"", "", "empty"},

		// No other prefix is stripped. Such names exist in the tracker, on closed versions:
		// they stay intact, and therefore attach to no tag.
		{"MSE 3.1.2", "MSE 3.1.2", "deployment prefix, dropped from the rule"},
		{"MSE3-1.5", "MSE3-1.5", "historical convention"},
	}

	for _, c := range cases {
		if got := NormalizeVersionName(c.name); got != c.want {
			t.Errorf("%s: NormalizeVersionName(%q) = %q, expected %q", c.why, c.name, got, c.want)
		}
	}
}

// Normalisation is not injective: any matching must handle that case instead of
// assuming a unique correspondence.
func TestNormalisationIsNotInjective(t *testing.T) {
	if NormalizeVersionName("3.1.2") != NormalizeVersionName("v3.1.2") {
		t.Error("these two names must reduce to the same number")
	}
}

// Strict equality, as originally specified, rejected every real release.
func TestAReleaseAttachesToAPrefixedVersion(t *testing.T) {
	version, err := versionpkg.SemVer{}.Parse("3.2.0-rc1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if err := CheckTrackedVersion(version, "v3.2.0"); err != nil {
		t.Errorf("a candidate 3.2.0-rc1 must attach to version v3.2.0: %v", err)
	}
	// Normalisation loosens the form, not the substance.
	if err := CheckTrackedVersion(version, "v3.3.0"); err == nil {
		t.Error("3.2.0-rc1 must not attach to v3.3.0")
	}
}
