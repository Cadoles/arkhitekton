package flux

import (
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
)

func realPolicies() []ImagePolicy {
	// The four policies as they are declared on the development cluster.
	return []ImagePolicy{
		{Name: "portal-unstable", Pattern: `^(?P<semver>.+)_unstable$`, Range: ">=0.0.0-0"},
		{Name: "portal-testing", Pattern: `^(?P<semver>.+)_testing$`, Range: ">=0.0.0-0"},
		{Name: "portal-preprod", Pattern: `^(?P<semver>.+)_preprod$`, Range: ">=0.0.0-0"},
		{Name: "portal-prod", Pattern: `^(?P<semver>.+)_prod$`, Range: ">=0.0.0"},
	}
}

func realEnvironments() []Environment {
	return []Environment{
		{ID: "unstable", PromotionSuffix: "unstable", AcceptsPrerelease: true, Observable: true},
		// The trap: qualif is named `testing` by Flux.
		{ID: "qualif", PromotionSuffix: "testing", AcceptsPrerelease: true, Observable: true},
		{ID: "preprod", PromotionSuffix: "preprod", AcceptsPrerelease: true, Observable: true},
		{ID: "prod", PromotionSuffix: "prod", AcceptsPrerelease: false, Observable: true},
	}
}

func TestGuardAcceptsTheDocumentedConfiguration(t *testing.T) {
	if err := CheckPromotionTargets(realEnvironments(), realPolicies()); err != nil {
		t.Errorf("the real configuration is refused: %v", err)
	}
}

// The failure mode the whole guard rail exists to prevent: a suffix no policy keeps produces a
// valid tag, pushed with no error, which Flux ignores. The promotion looks successful and
// nothing is deployed.
func TestGuardDetectsOrphanSuffix(t *testing.T) {
	environments := realEnvironments()
	// The most likely mistake in practice: writing `qualif` where Flux expects `testing`.
	environments[1].PromotionSuffix = "qualif"

	err := CheckPromotionTargets(environments, realPolicies())
	if err == nil {
		t.Fatal("an orphan suffix must be detected")
	}
	if !strings.Contains(err.Error(), "qualif") {
		t.Errorf("the message does not name the offending suffix: %v", err)
	}
	// The message lists what exists: without that, fixing it means going and querying the cluster by
	// hand.
	if !strings.Contains(err.Error(), "testing") {
		t.Errorf("the message does not list the available suffixes: %v", err)
	}
}

// A table declaring that it accepts RCs where the range excludes them makes a promotion be
// offered that Flux will ignore. The reverse makes a promotion be refused that would have
// worked.
func TestGuardDetectsPrereleaseDisagreement(t *testing.T) {
	environments := realEnvironments()
	environments[3].AcceptsPrerelease = true // prod, whose range is >=0.0.0

	err := CheckPromotionTargets(environments, realPolicies())
	if err == nil {
		t.Fatal("a disagreement about pre-releases must be detected")
	}
	for _, expected := range []string{"prod", "portal-prod", ">=0.0.0"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("the message does not contain %q: %v", expected, err)
		}
	}
}

// Production lives in an independent cluster: its ImagePolicies are not readable from here, and
// requiring them would fail the start-up over a normal absence.
func TestGuardSkipsUnobservableEnvironments(t *testing.T) {
	environments := realEnvironments()
	environments[3].Observable = false
	environments[3].PromotionSuffix = "suffixe-invisible"

	policies := realPolicies()[:3] // no production policy here

	if err := CheckPromotionTargets(environments, policies); err != nil {
		t.Errorf("an unobservable environment was checked: %v", err)
	}
}

// The SemVer transition case: the production policies still use the mktools channel, from which
// no suffix can be extracted. The guard rail must say so clearly rather than let one believe in a
// match.
func TestGuardReportsUnrecognisedPatterns(t *testing.T) {
	legacy := []ImagePolicy{
		{Name: "portal-unstable", Pattern: `^.*-k8sunstable\..*$`, Range: ">=0.0.0-0"},
	}
	environments := []Environment{
		{ID: "unstable", PromotionSuffix: "unstable", AcceptsPrerelease: true, Observable: true},
	}

	err := CheckPromotionTargets(environments, legacy)
	if err == nil {
		t.Fatal("an unrecognised pattern must produce a gap")
	}
	if !strings.Contains(err.Error(), "suffixe non reconnu") {
		t.Errorf("the message does not explain that the pattern is unusable: %v", err)
	}
}

// The rule taken back from the catalogue: two environments cannot share a suffix.
//
// It lived in the catalogue's validation, where it imposed a Flux concept on every catalogue —
// including that of a project with no deployer, which could then declare no environment. It
// belongs to the deployer that selects by suffix, and to it alone.
func TestTwoEnvironmentsCannotShareASuffix(t *testing.T) {
	environments := []Environment{
		{ID: "qualif", PromotionSuffix: "testing", AcceptsPrerelease: true, Observable: true},
		{ID: "preprod", PromotionSuffix: "testing", AcceptsPrerelease: true, Observable: true},
	}

	err := CheckPromotionTargets(environments, nil)
	if err == nil {
		t.Fatal("two environments with the same suffix: one would never be deployed")
	}
	if !strings.Contains(err.Error(), "share the suffix") {
		t.Errorf("message: %v — it must name the conflict", err)
	}
}

// An environment with no suffix cannot be reached by **this** deployer: it selects by SemVer
// metadata and would not know what to push. Refusing it here rather than in the catalogue is
// what lets another deployer accept the same environment.
func TestAnEnvironmentWithNoSuffixIsRefusedByThisDeployer(t *testing.T) {
	err := CheckPromotionTargets([]Environment{{ID: "dev", Observable: true}}, nil)
	if err == nil {
		t.Fatal("an environment with no suffix must be refused by a deployer that needs one")
	}
}

// The conflict shows even on unobservable environments: two environments of the same name tread
// on each other independently of our ability to read their policies.
func TestTheSuffixConflictShowsOutOfReach(t *testing.T) {
	environments := []Environment{
		{ID: "prod", PromotionSuffix: "prod", Observable: false},
		{ID: "prod-bis", PromotionSuffix: "prod", Observable: false},
	}

	if err := CheckPromotionTargets(environments, nil); err == nil {
		t.Error("the conflict does not depend on observability")
	}
}

// The scheme × deployer coupling, refused rather than discovered.
//
// This deployer promotes by writing metadata into the tag, which an ImagePolicy's range keeps. A
// scheme that does not know where to put it makes promotion impossible — and the failure would
// be the most expensive there is: the tag would go, the registry would accept it, Flux would
// ignore it, and the promotion would look successful.
func TestAnOpaqueSchemeIsRefusedByThisDeployer(t *testing.T) {
	err := NewDeployer(nil, DefaultPromotionKind, "").CheckScheme(host.Opaque{})
	if err == nil {
		t.Fatal("a scheme with no marker must be refused: the promotion would have no effect")
	}
	// The message must say *why*, otherwise one will hunt on the registry's or the policies' side.
	if !strings.Contains(err.Error(), "marker") {
		t.Errorf("message %q — it must name what is missing", err)
	}
}

// SemVer fits: it is the grammar the ImagePolicies select by.
func TestASchemeThatCanMarkIsAccepted(t *testing.T) {
	if err := NewDeployer(nil, DefaultPromotionKind, "").CheckScheme(host.SemVer{}); err != nil {
		t.Errorf("SemVer scheme refused: %v", err)
	}
}

// The marker is set with an underscore: the grammar of OCI tags excludes the `+` that SemVer
// reserves for build metadata.
func TestTheMarkerIsSetWithAnUnderscore(t *testing.T) {
	got, err := Mark("1.8.0-rc1", "testing")
	if err != nil {
		t.Fatalf("marking: %v", err)
	}
	if got != "1.8.0-rc1_testing" {
		t.Errorf("marked tag = %q", got)
	}
	// With no marker, the tag comes out unchanged rather than suffixed with an orphan underscore,
	// which the registry would accept with nothing selecting it.
	if got, _ := Mark("1.8.0", ""); got != "1.8.0" {
		t.Errorf("tag with no marker = %q", got)
	}
}
