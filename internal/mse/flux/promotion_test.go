package flux

import (
	"strings"
	"testing"
)

func targets() (unstable, qualif, prod PromotionTarget) {
	return PromotionTarget{ID: "unstable", Suffix: "unstable", AcceptsPrerelease: true},
		// The trap: Flux names qualification by its old name.
		PromotionTarget{ID: "qualif", Suffix: "testing", AcceptsPrerelease: true},
		PromotionTarget{ID: "prod", Suffix: "prod", AcceptsPrerelease: false}
}

func TestPromotionTagUsesTheFluxSuffixNotTheEnvironmentName(t *testing.T) {
	_, qualif, _ := targets()

	tag, err := PromotionTag("1.8.0-rc1", qualif, "")
	if err != nil {
		t.Fatalf("promotion refused: %v", err)
	}
	if tag != "1.8.0-rc1_testing" {
		t.Errorf("tag = %q, expected 1.8.0-rc1_testing", tag)
	}
}

// The refusal that counts most: pushing a tag no ImagePolicy keeps produces no error. The
// registry accepts it, the task succeeds, nothing is deployed — a failure that looks like a
// success.
func TestPromotionRefusesPrereleaseWhereFluxWouldIgnoreIt(t *testing.T) {
	_, _, prod := targets()

	_, err := PromotionTag("1.8.0-rc1", prod, "")
	if err == nil {
		t.Fatal("a pre-release towards production must be refused")
	}
	for _, expected := range []string{"prod", "1.8.0-rc1", "would ignore"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("the message does not contain %q: %v", expected, err)
		}
	}
}

func TestPromotionAcceptsFinalReleaseInProduction(t *testing.T) {
	_, _, prod := targets()

	tag, err := PromotionTag("1.8.0", prod, "")
	if err != nil {
		t.Fatalf("promotion refused: %v", err)
	}
	if tag != "1.8.0_prod" {
		t.Errorf("tag = %q", tag)
	}
}

// The separator stays a parameter: `+` is excluded from the grammar of OCI tags, and if the
// convention changes the template follows with no code change.
func TestPromotionTagHonoursTheTemplate(t *testing.T) {
	unstable, _, _ := targets()

	tag, err := PromotionTag("3.1.0", unstable, "{suffix}-{version}")
	if err != nil {
		t.Fatalf("template refused: %v", err)
	}
	if tag != "unstable-3.1.0" {
		t.Errorf("tag = %q", tag)
	}
}

// A template that would produce a tag outside the OCI grammar must be refused here: the registry
// would refuse it too, but later and with a message that speaks of a manifest.
func TestPromotionTagRejectsInvalidOCITags(t *testing.T) {
	unstable, _, _ := targets()

	if _, err := PromotionTag("3.1.0", unstable, "{version}+{suffix}"); err == nil {
		t.Error("a tag carrying a + must be refused")
	}
	if _, err := PromotionTag("3.1.0", unstable, "{version}/{suffix}"); err == nil {
		t.Error("a tag carrying a slash must be refused")
	}
}

func TestPromotionRefusesUnusableInputs(t *testing.T) {
	unstable, _, _ := targets()

	if _, err := PromotionTag("", unstable, ""); err == nil {
		t.Error("an empty version must be refused")
	}
	if _, err := PromotionTag("1.8.0", PromotionTarget{ID: "no-suffix"}, ""); err == nil {
		t.Error("an environment with no suffix must be refused")
	}
}

// The old mktools tag scheme **is** valid SemVer: `2026.8.3` with the pre-release
// `k8sunstable.1249.520c9a020`. This function therefore accepts it, and that is right — it judges
// only the shape.
//
// The test exists to fix the limit of its responsibility. The guard rail against that scheme is
// elsewhere, when a release is created, where the tag is confronted with the Redmine version
// name. And the consequence, were such a tag promoted: its major being a year, it would take
// precedence over any SemVer version on the environments that accept pre-releases.
func TestPromotionTagJudgesFormOnly(t *testing.T) {
	unstable, _, _ := targets()

	tag, err := PromotionTag("2026.8.3-k8sunstable.1249.520c9a020", unstable, "")
	if err != nil {
		t.Fatalf("a valid SemVer tag must not be refused here: %v", err)
	}
	if tag != "2026.8.3-k8sunstable.1249.520c9a020_unstable" {
		t.Errorf("tag = %q", tag)
	}
}

// Promoting is not rebuilding: the plan carries the old and the new tag, and the digest stays the
// same.
func TestPlanPromotionCarriesBothTags(t *testing.T) {
	_, qualif, _ := targets()

	plan, err := PlanPromotion("1.8.0-rc3", "1.8.0-rc3", qualif, "")
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if plan.OldTag != "1.8.0-rc3" || plan.NewTag != "1.8.0-rc3_testing" {
		t.Errorf("plan = %+v", plan)
	}
	if plan.EnvironmentID != "qualif" {
		t.Errorf("environment = %q", plan.EnvironmentID)
	}
}

// The case that justifies the two parameters: a final release produced by re-tag. Its image still
// carries the RC's version, and composing the promotion tag from that version would forbid it
// production — production's SemVer range excluding pre-releases.
func TestPlanPromotionComposesFromTheReleaseTagAndRetagsFromTheImage(t *testing.T) {
	prod := PromotionTarget{ID: "prod", Suffix: "prod", AcceptsPrerelease: false}

	plan, err := PlanPromotion("1.8.0", "1.8.0-rc3", prod, "")
	if err != nil {
		t.Fatalf("a final release must be able to reach production: %v", err)
	}
	if plan.NewTag != "1.8.0_prod" {
		t.Errorf("new tag = %q, expected 1.8.0_prod", plan.NewTag)
	}
	// The re-tag starts from what exists in the registry, and that is the RC's image.
	if plan.OldTag != "1.8.0-rc3" {
		t.Errorf("old tag = %q, expected that of the image built", plan.OldTag)
	}
}

func TestPlanPromotionRefusesAReleaseWithoutImage(t *testing.T) {
	if _, err := PlanPromotion("1.8.0", "", PromotionTarget{ID: "unstable", Suffix: "unstable"}, ""); err == nil {
		t.Error("a release with no image cannot be promoted")
	}
}
