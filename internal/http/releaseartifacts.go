package http

import (
	"context"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * What a release's automation produced.
 *
 * # What this section replaces
 *
 * The screen listed "images": **a single repository**, the one the configuration names, declined
 * into its tags — build, release, promotions. A project publishing three saw only one, and a
 * project producing a binary saw nothing at all.
 *
 * It now lists what the steps actually returned, collected at their conclusion from the Tekton
 * results. Three builds, three rows, named by the step that produced them.
 *
 * # The digest check is kept, and widened
 *
 * The old section proved that a promotion had not rebuilt: the promoted tag and the original tag
 * must carry the **same** digest. That guarantee is not lost — it now applies to every image
 * produced, instead of the configured repository alone.
 */

// planArtifactPayload is a produced artifact, as the screen reads it.
type planArtifactPayload struct {
	// Geste names what produced it, in the words of the declaration.
	Geste string `json:"geste"`
	// Name is the result name the pipeline returned: `image`, `tag`.
	Name string `json:"name"`
	// Kind reuses the catalogue's `produces` vocabulary.
	//
	// Descriptive and deliberately not structural: it is `image` for every result of a pipeline
	// that produces images, including for a `tag`, which is not one. The screen relies on the
	// name, which is right.
	Kind string `json:"kind,omitempty"`
	// Reference names the artifact: for an image, its full reference.
	Reference string `json:"reference"`
	// Digest proves what was published. Empty when the registry cannot be queried, or when the
	// artifact is not an image.
	Digest string `json:"digest,omitempty"`
	// Diverged says a promotion tag carries a different digest from this one — hence that an
	// image was rebuilt between approval and delivery.
	Diverged bool `json:"diverged,omitempty"`
}

// releaseArtifacts composes what the automation produced for this release.
//
// An artifact whose run is no longer readable keeps an empty step name rather than
// disappearing: it is what was published, and losing the frozen plan changes nothing about
// that.
func (h *ReleasesHandler) releaseArtifacts(
	ctx context.Context, releaseID int64, promotions []ReleasePromotion,
) []planArtifactPayload {
	if h.artifactReader == nil {
		return nil
	}

	artifacts, err := h.artifactReader.ReleaseArtifacts(ctx, releaseID)
	if err != nil {
		// Logged and not returned: an unreadable artifact must not make a release's page
		// inaccessible. What remains is displayed.
		h.logger.Error("a release's artifacts", "release", releaseID, "error", err)
		return nil
	}

	// The steps' labels come from the frozen plan of the run that produced the artifact. An
	// automation renamed since therefore does not rewrite history: the row carries the name it
	// had that day.
	labels := map[int64]map[store.GesteRef]string{}

	payload := make([]planArtifactPayload, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, lu := labels[artifact.RunID]; !lu {
			labels[artifact.RunID] = h.stepLabels(ctx, artifact.RunID)
		}
		ref := store.GesteRef{Stage: artifact.Stage, Position: artifact.Position}

		entry := planArtifactPayload{
			Geste: labels[artifact.RunID][ref], Name: artifact.Name,
			Kind: artifact.Kind, Reference: artifact.Reference,
		}
		// Only images have a digest. The name is authoritative and not `kind`, which is `image` for
		// everything an image pipeline returns — the tag included.
		if artifact.Name == "image" {
			entry.Digest, entry.Diverged = h.digestOf(ctx, artifact.Reference, promotions)
		}
		payload = append(payload, entry)
	}
	return payload
}

// stepLabels returns the name of each step of a run's frozen plan.
func (h *ReleasesHandler) stepLabels(
	ctx context.Context, runID int64,
) map[store.GesteRef]string {
	labels := map[store.GesteRef]string{}

	run, err := h.artifactReader.PlanRunByID(ctx, runID)
	if err != nil {
		// The run may have been purged. The artifact stays true and is displayed without the name
		// of what produced it.
		return labels
	}
	for stage, entry := range run.Plan.Stages {
		for position, step := range entry.Gestes {
			labels[store.GesteRef{Stage: stage, Position: position}] = step.Name()
		}
	}
	return labels
}

// digestOf reads a published image's digest, and says whether it diverged.
//
// Divergence is the delivery invariant: a promotion tag must carry the approved image's
// digest. A gap reports an image rebuilt meanwhile, that is, a delivery that is no longer
// the one that was approved.
func (h *ReleasesHandler) digestOf(
	ctx context.Context, reference string, promotions []ReleasePromotion,
) (string, bool) {
	if h.images == nil {
		return "", false
	}
	repository, tag, ok := splitReference(reference)
	if !ok {
		return "", false
	}

	digest, err := h.images.Digest(ctx, repository, tag)
	if err != nil || digest == "" {
		return "", false
	}

	for _, promotion := range promotions {
		promoted, err := h.images.Digest(ctx, repository, promotion.Tag)
		if err != nil || promoted == "" {
			// A tag absent from the registry is not a divergence: the promotion may simply not have
			// completed. Announcing it as a gap would send someone hunting for a rebuild where there is
			// only a wait.
			continue
		}
		if promoted != digest {
			return digest, true
		}
	}
	return digest, false
}

// splitReference separates the short repository and the tag of a full reference.
//
// The short repository because that is what the registry client expects: it already carries
// its organisation.
func splitReference(reference string) (repository, tag string, ok bool) {
	colon := strings.LastIndex(reference, ":")
	slash := strings.LastIndex(reference, "/")
	if colon < 0 || colon < slash {
		return "", "", false
	}
	path, tag := reference[:colon], reference[colon+1:]
	if slash >= 0 {
		path = path[slash+1:]
	}
	if path == "" || tag == "" {
		return "", "", false
	}
	return path, tag, true
}
