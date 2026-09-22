package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/report"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// KindExport is the `kind` of the deliverable-generation jobs.
//
// Absent from the catalogue, deliberately: the catalogue describes what becomes a PipelineRun.
// An export does not leave the process — it reads the database, writes an object, and returns
// in a few milliseconds. Making it cross Tekton would cost a pod, a workspace and thirty
// seconds to produce two pages.
const KindExport = "export"

// Parameters of an export job.
const (
	ParamExportKind   = "export-kind"
	ParamExportFormat = "export-format"
)

// ExportStore is what the generator needs from the database.
type ExportStore interface {
	GetReleaseByID(ctx context.Context, id int64) (store.Release, error)
	VersionName(ctx context.Context, versionID int64) (string, error)
	RecordArtifact(ctx context.Context, artifact store.Artifact) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// Storage is what the generator needs from object storage.
type Storage interface {
	Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
}

// Registry resolves the digests. Optional: a document can be produced without it, provided it
// says what it could not check.
type Registry interface {
	Digest(ctx context.Context, repository, tag string) (string, error)
}

// Exporter produces a release's deliverables.
type Exporter struct {
	store     ExportStore
	storage   Storage
	registry  Registry
	imageName string
	keys      blob.Keys
	logger    *slog.Logger
}

func NewExporter(
	releases ExportStore, storage Storage, registry Registry,
	imageName string, logger *slog.Logger,
) *Exporter {
	return &Exporter{
		store: releases, storage: storage, registry: registry,
		imageName: imageName, logger: logger,
	}
}

// Kind returns the `kind` this generator handles.
func (e *Exporter) Kind() string { return KindExport }

// Run produces the deliverable a job asks for.
//
// Run in the scheduler's loop rather than in a goroutine: generation takes a few
// milliseconds, and serialising it avoids having to reason about two concurrent exports of
// the same release — the second of which would overwrite the first's object while recording
// two rows.
func (e *Exporter) Run(ctx context.Context, job store.Job) error {
	release, err := e.store.GetReleaseByID(ctx, derefRelease(job))
	if err != nil {
		return fmt.Errorf("release of job %d: %w", job.ID, err)
	}

	format := job.Params[ParamExportFormat]
	if format == "" {
		format = "pdf"
	}
	kind := job.Params[ParamExportKind]
	if kind == "" {
		kind = store.ArtifactDeliveryNote
	}
	if kind != store.ArtifactDeliveryNote {
		// The test and security reports await their data. Refusing explicitly is better than
		// producing an empty document that would look like it attests to something.
		return fmt.Errorf("deliverable %q unavailable: the matching results are not collected yet", kind)
	}

	note, err := e.buildNote(ctx, release, job.RequestedBy)
	if err != nil {
		return err
	}

	var content []byte
	var contentType, filename string
	switch format {
	case "pdf":
		content, err = report.RenderPDF(note)
		contentType, filename = "application/pdf", "delivery-note.pdf"
	case "json":
		content, err = report.RenderJSON(note)
		contentType, filename = "application/json", "delivery-note.json"
	default:
		return fmt.Errorf("unknown deliverable format %q", format)
	}
	if err != nil {
		return err
	}

	key, err := e.keys.ReleaseArtifact(release.Tag, filename)
	if err != nil {
		return fmt.Errorf("key of the deliverable of %q: %w", release.Tag, err)
	}

	if err := e.storage.Put(ctx, key, bytes.NewReader(content), int64(len(content)), contentType); err != nil {
		return fmt.Errorf("writing the deliverable %q: %w", key, err)
	}

	if _, err := e.store.RecordArtifact(ctx, store.Artifact{
		ReleaseID: release.ID, Kind: kind, Format: format, Object: key,
		SizeBytes: int64(len(content)), GeneratedBy: job.RequestedBy,
	}); err != nil {
		return err
	}

	// Logged: a delivery note is a contractual document, and knowing who produced it and when is
	// part of what is delivered.
	if err := e.store.Audit(ctx, job.RequestedBy, store.AuditArtifactGenerate, release.Tag,
		map[string]any{"kind": kind, "format": format, "object": key}); err != nil {
		e.logger.Error("journal d'audit", "job", job.ID, "error", err)
	}

	e.logger.Info("livrable produit",
		"job", job.ID, "release", release.Tag, "kind", kind, "format", format, "octets", len(content))
	return nil
}

// buildNote gathers the delivery note's facts.
//
// Nothing in it is invented: every field comes from a record or from a read of the registry.
// What could not be established is named in `Unavailable` rather than omitted — in a
// contractual document, a missing section reads as an assertion.
func (e *Exporter) buildNote(ctx context.Context, release store.Release, requestedBy string) (report.Note, error) {
	note := report.Note{
		Release:      release.Tag,
		Kind:         release.Kind,
		ImageVersion: release.ImageVersion,
		GitRef:       release.GitRef,
		GeneratedAt:  time.Now(),
		GeneratedBy:  requestedBy,
		Unavailable:  report.KnownUnavailable,
		// The layout awaits the client's validation.
		Provisional: true,
	}

	if name, err := e.store.VersionName(ctx, release.VersionID); err == nil {
		note.Version = name
	}

	if release.ImageVersion != release.Tag {
		note.SourceTag = release.ImageVersion
	}

	// The image built, then the release's tag: the order of the delivery chain. The promotion
	// tags no longer appear there: they belong to the MSE plugin, and the delivery note to the
	// core.
	tags := []string{release.ImageVersion}
	if release.Tag != release.ImageVersion {
		tags = append(tags, release.Tag)
	}

	for _, tag := range tags {
		image := report.Image{Repository: e.imageName, Tag: tag}
		if e.registry != nil {
			if digest, err := e.registry.Digest(ctx, e.imageName, tag); err == nil {
				image.Digest = digest
			}
		}
		note.Images = append(note.Images, image)
	}

	if unverified := note.UnverifiedImages(); len(unverified) > 0 {
		note.Unavailable = append(note.Unavailable, fmt.Sprintf(
			"Digests not checked for: %s. Without a digest, this document does not attest that the image "+
				"delivered is the one that was validated.", strings.Join(unverified, ", ")))
	}

	if release.ManualActions != "" {
		note.ManualActions = &report.ManualActions{
			Markdown:  release.ManualActions,
			UpdatedBy: release.ManualActionsUpdatedBy,
			UpdatedAt: release.ManualActionsUpdatedAt,
		}
	}

	return note, nil
}

func derefRelease(job store.Job) int64 {
	if job.ReleaseID == nil {
		return 0
	}
	return *job.ReleaseID
}
