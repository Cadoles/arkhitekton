package native

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
)

// Dossier is what the dashboard records about a release, composed as its screen composes it: a
// deliverable a plugin produces attests to the same notes, digests and score the screen shows.
type Dossier struct {
	Version       string
	ManualActions *DossierActions
	Notes         []DossierNote
	Artifacts     []DossierArtifact
	ArtifactsNote string
	Results       []store.TestResult
	Score         results.Breakdown
	// Audit is nil when no security audit ran on the release.
	Audit *store.SecurityAudit
}

// DossierActions are the delivery's manual actions, their author named.
type DossierActions struct {
	Markdown  string
	UpdatedBy string
	UpdatedAt *time.Time
}

// DossierNote is a line of the release note, its author and its tracker link resolved.
type DossierNote struct {
	ExternalID, Subject, Tracker, Status, PullRequest, URL string
	Anomaly, Known                                         bool
}

// DossierArtifact is what the automation produced, its digest read from the registry.
type DossierArtifact struct {
	Geste, Name, Kind, Reference, Digest string
}

// DossierSource composes a release's dossier.
type DossierSource func(ctx context.Context, projectID int64, tag string) (Dossier, error)

func (h *host) GetReleaseDossier(ctx context.Context, request *pluginv1.ReleaseByTag) (*pluginv1.ReleaseDossier, error) {
	if h.deps.Dossier == nil {
		return nil, status.Error(codes.Unimplemented, "this role composes no dossier")
	}
	dossier, err := h.deps.Dossier(ctx, request.GetProjectId(), request.GetTag())
	if err != nil {
		return nil, storeStatus(err)
	}
	return DossierTo(dossier), nil
}

// DossierTo is a dossier as a plugin receives it.
func DossierTo(dossier Dossier) *pluginv1.ReleaseDossier {
	converted := &pluginv1.ReleaseDossier{
		Version: dossier.Version, ArtifactsNote: dossier.ArtifactsNote,
		Score: scoreTo(dossier.Score),
	}
	if actions := dossier.ManualActions; actions != nil {
		converted.ManualActions = &pluginv1.ManualActions{Markdown: actions.Markdown, UpdatedBy: actions.UpdatedBy}
		if actions.UpdatedAt != nil {
			converted.ManualActions.UpdatedAt = formatTime(*actions.UpdatedAt)
		}
	}
	for _, note := range dossier.Notes {
		converted.Notes = append(converted.Notes, &pluginv1.DossierNote{
			ExternalId: note.ExternalID, Subject: note.Subject, Tracker: note.Tracker, Status: note.Status,
			Anomaly: note.Anomaly, Known: note.Known, PullRequest: note.PullRequest, Url: note.URL,
		})
	}
	for _, artifact := range dossier.Artifacts {
		converted.Artifacts = append(converted.Artifacts, &pluginv1.DossierArtifact{
			Geste: artifact.Geste, Name: artifact.Name, Kind: artifact.Kind,
			Reference: artifact.Reference, Digest: artifact.Digest,
		})
	}
	for _, result := range dossier.Results {
		converted.Results = append(converted.Results, &pluginv1.DossierResult{
			Group: result.Group, Shape: result.Shape, Tool: result.Tool, Status: result.Status,
			Total: int32(result.Total), Passed: int32(result.Passed), Failed: int32(result.Failed),
			Skipped: int32(result.Skipped), Flaky: int32(result.Flaky), DurationMs: result.DurationMS,
			MeasuredAt: formatTime(result.CreatedAt), RunName: result.RunName,
		})
	}
	if audit := dossier.Audit; audit != nil {
		converted.Audit = &pluginv1.DossierAudit{
			Measured: audit.Measured(), FailedScanners: audit.FailedScanners,
			Total: int32(audit.Total), Critical: int32(audit.Critical), High: int32(audit.High),
			Medium: int32(audit.Medium), Low: int32(audit.Low), Suppressed: int32(audit.Suppressed),
			DeltaNew: int32(audit.DeltaNew), DeltaExisting: int32(audit.DeltaExisting),
			DeltaResolved: int32(audit.DeltaResolved), GeneratedAt: audit.GeneratedAt, RunName: audit.RunName,
		}
	}
	return converted
}

// scoreTo carries the verdicts the screen shows, computed here: a plugin deriving them would be a
// second opinion, diverging at the first threshold change.
func scoreTo(breakdown results.Breakdown) *pluginv1.DossierScore {
	score := &pluginv1.DossierScore{
		Value: int32(breakdown.Score), Measured: breakdown.Measured,
		Health: results.Health(breakdown), Redistributed: breakdown.Redistributed,
	}
	for _, signal := range breakdown.Signals {
		score.Signals = append(score.Signals, &pluginv1.DossierSignal{
			Key: signal.Key, Label: signal.Label, Group: string(signal.Group),
			Value: signal.Value, Weight: signal.Weight, Measured: signal.Measured,
			Health: results.SignalHealth(signal), Detail: signal.Detail,
		})
	}
	return score
}
