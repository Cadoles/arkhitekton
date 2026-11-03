package http

import (
	"context"
	"errors"
	"fmt"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// Dossier composes what a release's screen shows, for a plugin that produces a deliverable from
// it: the note, the artifacts and their digests, the results and the score, by the same paths as
// the screen. Two compositions would have ended up attesting to something the screen does not say.
//
// The artifacts' divergence from a promotion is left to the plugin that promotes: it keeps the
// promotions, and asking it back from within its own call would make the Host call the plugin.
func (h *ReleasesHandler) Dossier(ctx context.Context, projectID int64, tag string) (native.Dossier, error) {
	release, err := h.store.GetRelease(ctx, projectID, tag)
	if err != nil {
		return native.Dossier{}, err
	}
	who := h.actorsFor(ctx)

	dossier := native.Dossier{}
	if name, err := h.store.VersionName(ctx, release.VersionID); err == nil {
		dossier.Version = name
	}

	if release.ManualActions != "" || release.ManualActionsUpdatedAt != nil {
		dossier.ManualActions = &native.DossierActions{
			Markdown: release.ManualActions, UpdatedBy: who.name(release.ManualActionsUpdatedBy),
			UpdatedAt: release.ManualActionsUpdatedAt,
		}
	}

	entries, err := h.store.ListNoteEntries(ctx, release.ID)
	if err != nil {
		return native.Dossier{}, fmt.Errorf("the note of %s: %w", tag, err)
	}
	tracker := trackerOf(ctx, h.trackers, projectID)
	for _, entry := range entries {
		dossier.Notes = append(dossier.Notes, native.DossierNote{
			ExternalID: entry.ExternalID, Subject: entry.Subject, Tracker: entry.Tracker,
			Status: entry.StatusName, Anomaly: entry.Anomaly, Known: entry.Known,
			PullRequest: entry.PullRequest, URL: tracker.issueURL(entry.ExternalID),
		})
	}

	artifacts := h.releaseArtifacts(ctx, release.ID, nil)
	for _, artifact := range artifacts {
		dossier.Artifacts = append(dossier.Artifacts, native.DossierArtifact{
			Geste: artifact.Geste, Name: artifact.Name, Kind: artifact.Kind,
			Reference: artifact.Reference, Digest: artifact.Digest,
		})
	}
	if h.images == nil && len(artifacts) > 0 {
		dossier.ArtifactsNote = "The registry is not configured: the digests cannot be read."
	}

	if dossier.Results, err = h.store.ListReleaseTestResults(ctx, release.ID); err != nil {
		return native.Dossier{}, fmt.Errorf("the results of %s: %w", tag, err)
	}
	if dossier.Score, err = computeScore(ctx, h.store, release.ID, dossier.Results, catalog.Of(h.scoreCatalog)); err != nil {
		return native.Dossier{}, fmt.Errorf("the score of %s: %w", tag, err)
	}

	switch audit, err := h.store.LatestSecurityAudit(ctx, release.ID); {
	case err == nil:
		dossier.Audit = &audit
	case !errors.Is(err, store.ErrAuditNotFound):
		return native.Dossier{}, fmt.Errorf("the audit of %s: %w", tag, err)
	}
	return dossier, nil
}
