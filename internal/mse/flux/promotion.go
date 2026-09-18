package flux

import (
	"fmt"
	"regexp"
	"strings"

	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

/*
 * The rule for forming a promotion tag.
 *
 * It lived in `internal/release`, a core package, although its only caller was this deployer:
 * the per-environment suffix, the underscore and the refusal of pre-releases are conventions of
 * Flux and its ImagePolicies, not of a release's life cycle.
 */

// DefaultTagTemplate composes the promotion tag.
//
// The template is configurable rather than hard-coded: `+` is excluded from the grammar of OCI
// tags (`[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`), hence the underscore; if the convention changes,
// the separator stays a parameter.
const DefaultTagTemplate = "{version}_{suffix}"

// PromotionTarget describes the environment a promotion targets.
type PromotionTarget struct {
	ID string
	// Suffix is the SemVer metadata Flux expects — `testing` for qualification, whose current name
	// Flux does not know.
	Suffix string
	// AcceptsPrerelease mirrors the matching ImagePolicy's SemVer range, checked against the
	// cluster at start-up.
	AcceptsPrerelease bool
}

// ociTag bounds what a registry accepts as a tag.
var ociTag = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

// PromotionTag composes the tag to push in order to reach an environment.
//
// The tag is composed from the **release's tag**, never from its image's version. The two differ
// on every final release produced by re-tag, and the mistake would be fatal there: the image of
// `1.8.0` carries the version `1.8.0-rc3`, and composing from it would give `1.8.0-rc3_prod` — a
// pre-release, which production's SemVer range excludes. The final release would be refused
// production, or worse pushed there with nothing being deployed.
//
// The function refuses a promotion Flux would ignore rather than let it go. It is the most
// important point of the whole promotion arrangement: pushing a tag no `ImagePolicy` keeps
// **produces no error**. The tag is accepted by the registry, the task succeeds, and nothing is
// deployed — the most expensive failure there is, since it looks like a success.
//
// Two refusals, therefore:
//
//  1. **A pre-release towards an environment that accepts none.** Its policy's SemVer range is
//     written without `-0`, which makes an RC structurally ineligible. The dashboard refuses
//     upstream, with an explicit message, rather than relying on silence.
//  2. **A tag outside the OCI grammar.** The registry would refuse it, but later and with a
//     message that speaks of a manifest.
func PromotionTag(releaseTag string, target PromotionTarget, template string) (string, error) {
	version := strings.TrimSpace(releaseTag)
	if version == "" {
		return "", fmt.Errorf("no image version to promote")
	}
	if strings.TrimSpace(target.Suffix) == "" {
		return "", fmt.Errorf("environment %q: no promotion suffix", target.ID)
	}

	parsed, err := versionpkg.SemVer{}.Parse(version)
	if err != nil {
		return "", fmt.Errorf("release tag %q: %w", version, err)
	}

	// `known` is ignored here, and that is right: this path is reached only from a deployer that has
	// already refused the schemes unable to answer ([Deployer.CheckScheme]). A mute scheme does not
	// promote by range.
	prerelease, _ := parsed.Prereleased()
	if prerelease && !target.AcceptsPrerelease {
		return "", fmt.Errorf(
			"the environment %q does not accept pre-releases: promoting %q there would produce a tag "+
				"Flux would ignore, with neither error nor deployment",
			target.ID, version)
	}

	if template == "" {
		template = DefaultTagTemplate
	}
	tag := strings.NewReplacer(
		"{version}", version,
		"{suffix}", target.Suffix,
	).Replace(template)

	if !ociTag.MatchString(tag) {
		return "", fmt.Errorf(
			"the tag %q is not a valid OCI tag: the template %q produces a value the registry would refuse",
			tag, template)
	}

	return tag, nil
}

// PromotionPlan describes what a promotion must do.
type PromotionPlan struct {
	EnvironmentID string
	// OldTag is the tag that already exists in the registry — the one the image was built under,
	// that is, the image's version and not the release's tag. It is where the re-tag starts from.
	OldTag string
	// NewTag is the suffixed tag to add, composed from the release's tag. The digest is the same on
	// both sides: promoting is not rebuilding.
	NewTag string
}

// PlanPromotion prepares the promotion of a release to an environment.
//
// Two tags as input, and that is the whole point: `releaseTag` is the release's identity,
// `imageVersion` is what its image really exists under in the registry. They coincide for a
// pre-release and diverge for any final produced by re-tag. Confusing the two gives either a
// re-tag of a non-existent image, or a promotion tag Flux ignores.
func PlanPromotion(releaseTag, imageVersion string, target PromotionTarget, template string) (PromotionPlan, error) {
	source := strings.TrimSpace(imageVersion)
	if source == "" {
		// With no image in the registry, there is nothing to re-tag. Saying so here avoids a job that
		// will fail on a missing manifest.
		return PromotionPlan{}, fmt.Errorf("release %q carries no image version", releaseTag)
	}

	tag, err := PromotionTag(releaseTag, target, template)
	if err != nil {
		return PromotionPlan{}, err
	}

	return PromotionPlan{
		EnvironmentID: target.ID,
		OldTag:        source,
		NewTag:        tag,
	}, nil
}
