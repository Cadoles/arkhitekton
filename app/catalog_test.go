package app

import (
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
)

// The tests of the catalogue actually shipped. They live with the assembly: the catalogue binds
// the built-in plugins' capabilities, and only the binary knows which plugins it carries.

// The catalogue actually shipped, set against what the chaining launches.
//
// A verification kind that declares no artifact runs, fails or succeeds, and the dashboard
// knows nothing of it: its signal stays "not measured" while ten scanners or three hundred
// tests run. That was the case of `security-audit` and of `integration-tests`, which published
// into the pipelines' bucket — where the reader does not look.
//
// The rule holds for everything that is chained: launching a verification whose verdict will
// not be read amounts to not launching it, only more expensively.
func TestEverythingChainedReturnsAReadableResult(t *testing.T) {
	pipelines := shippedCatalogue(t)

	kinds := pipelines.VerificationKinds()
	if len(kinds) == 0 {
		t.Fatal("the shipped catalogue chains no verification: a successful build would conclude on nothing")
	}

	// The rule "a verification declares an artifact" is now held by the catalogue's validation,
	// which `Load` applies: this test would fail earlier, at loading, if the file shipped broke it.
	// Checking it a second time here would measure the same code twice.

	// The point the validation cannot settle on its own: what builds or re-tags must not be chained.
	// A build that chained itself would loop endlessly, publishing an image at each round.
	for _, kind := range []string{catalog.KindBuild, catalog.KindRetag} {
		if pipelines.IsVerification(kind) {
			t.Errorf("%s is declared a verification: a build would chain itself", kind)
		}
	}
}

// And the counterpart: a declared artifact must name a parameter and an object name, without
// which the URL is minted with nobody knowing where to send it — the scheduler then refuses the
// run, and the release stays waiting for a test that will not start.
func TestEachArtifactDeclaresItsParameterAndItsName(t *testing.T) {
	pipelines := shippedCatalogue(t)

	for _, pipeline := range pipelines.Pipelines {
		for name, upload := range pipeline.Uploads {
			if upload.Param == "" || upload.Artifact == "" {
				t.Errorf("%s/%s: param=%q artifact=%q — both are required",
					pipeline.Kind, name, upload.Param, upload.Artifact)
			}
			// The object's extension must say its content: it is what decides, at reading, whether an
			// archive must be unpacked.
			archive := strings.HasSuffix(upload.Artifact, ".tar.gz") ||
				strings.HasSuffix(upload.Artifact, ".tgz")
			if (upload.Format == "archive") != archive {
				t.Errorf("%s/%s: format=%q but artifact=%q — the reader will unpack or not, wrongly",
					pipeline.Kind, name, upload.Format, upload.Artifact)
			}
		}
	}
}

// The automatic chaining and the re-run of a single kind bear on the same kinds, and the
// catalogue is now their **single** source. What follows therefore no longer compares two lists
// — it checks that the file shipped does name those expected, the declaration being now what
// one can get wrong in writing.
func TestTheShippedCatalogueChainsTheExpectedKinds(t *testing.T) {
	pipelines := shippedCatalogue(t)

	want := []string{"security/security-audit", "php/static-tests", "php/unit-tests"}
	got := pipelines.VerificationKinds()

	if len(got) != len(want) {
		t.Fatalf("families chained = %v, expected %v", got, want)
	}
	for index, kind := range want {
		// The order is that of declaration, and it becomes that of queueing: runs changing rank at every
		// build make it impossible to compare two releases at a glance.
		if got[index] != kind {
			t.Errorf("family %d = %q, expected %q", index, got[index], kind)
		}
	}
}

// The catalogue actually deployed must load.
//
// It is edited by hand, in a long file, and a misspelled `suggest` has no visible
// consequence before the binary starts in the cluster. Reading it here fails in
// the right place.
func TestTheDeployedCatalogueLoads(t *testing.T) {
	for _, pipeline := range shippedCatalogue(t).Pipelines {
		for _, input := range pipeline.Inputs() {
			// A typed parameter with no declared nature is not a mistake, but it is one more
			// empty field in the form. Reporting it keeps the question open.
			if input.Suggest == "" {
				t.Logf("%s/%s: no suggestion declared", pipeline.Kind, input.Name)
			}
		}
	}
}

// The shipped catalogue must stay parseable: it is mounted as a ConfigMap, and a
// typo would only show when a pod starts.
func TestShippedCatalogParses(t *testing.T) {
	shipped := shippedCatalogue(t)
	if len(shipped.Pipelines) == 0 {
		t.Error("no pipeline declared")
	}
}

// The pipeline a signal re-runs, found through its group, is the one the score used to name.
func TestEachSignalReRunsItsPipeline(t *testing.T) {
	shipped := shippedCatalogue(t)
	for group, kind := range map[string]string{
		"unit": "php/unit-tests", "static": "php/static-tests", "security": "security/security-audit",
		// No pipeline of the shipped catalogue shows its results there: nothing to re-run.
		"integration": "", "e2e": "",
	} {
		if got := shipped.KindOfGroup(group); got != kind {
			t.Errorf("%s: %q, expected %q", group, got, kind)
		}
	}
}
