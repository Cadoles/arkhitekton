package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/report"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// KindExport est le `kind` des jobs de génération de livrable.
//
// Absent du catalogue, et c'est délibéré : le catalogue décrit ce qui devient un
// PipelineRun. Un export ne sort pas du processus — il lit la base, écrit un
// objet, et rend la main en quelques millisecondes. Lui faire traverser Tekton
// coûterait un pod, un workspace et trente secondes pour produire deux pages.
const KindExport = "export"

// Paramètres d'un job d'export.
const (
	ParamExportKind   = "export-kind"
	ParamExportFormat = "export-format"
)

// ExportStore est ce que le générateur attend de la base.
type ExportStore interface {
	GetReleaseByID(ctx context.Context, id int64) (store.Release, error)
	VersionName(ctx context.Context, versionID int64) (string, error)
	ListPromotions(ctx context.Context, releaseID int64) ([]store.Promotion, error)
	RecordArtifact(ctx context.Context, artifact store.Artifact) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// Storage est ce que le générateur attend du stockage objet.
type Storage interface {
	Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
}

// Registry résout les empreintes. Optionnel : un document peut être produit sans,
// à condition de dire ce qu'il n'a pas pu vérifier.
type Registry interface {
	Digest(ctx context.Context, repository, tag string) (string, error)
}

// Exporter produit les livrables d'une release.
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
	if imageName == "" {
		imageName = "portal"
	}
	return &Exporter{
		store: releases, storage: storage, registry: registry,
		imageName: imageName, logger: logger,
	}
}

// Kind rend le `kind` traité par ce générateur.
func (e *Exporter) Kind() string { return KindExport }

// Run produit le livrable demandé par un job.
//
// Exécuté dans la boucle de l'ordonnanceur plutôt que dans une goroutine : la
// génération dure quelques millisecondes, et la sérialiser évite d'avoir à
// raisonner sur deux exports concurrents de la même release — dont le second
// écraserait l'objet du premier tout en enregistrant deux lignes.
func (e *Exporter) Run(ctx context.Context, job store.Job) error {
	release, err := e.store.GetReleaseByID(ctx, derefRelease(job))
	if err != nil {
		return fmt.Errorf("release du job %d: %w", job.ID, err)
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
		// Les rapports de tests et de sécurité attendent les données du lot 4.
		// Refuser explicitement vaut mieux que produire un document vide qui
		// aurait l'air d'attester quelque chose.
		return fmt.Errorf("livrable %q indisponible: les résultats correspondants ne sont pas encore collectés", kind)
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
		return fmt.Errorf("format de livrable inconnu %q", format)
	}
	if err != nil {
		return err
	}

	key, err := e.keys.ReleaseArtifact(release.Tag, filename)
	if err != nil {
		return fmt.Errorf("clé du livrable de %q: %w", release.Tag, err)
	}

	if err := e.storage.Put(ctx, key, bytes.NewReader(content), int64(len(content)), contentType); err != nil {
		return fmt.Errorf("écriture du livrable %q: %w", key, err)
	}

	if _, err := e.store.RecordArtifact(ctx, store.Artifact{
		ReleaseID: release.ID, Kind: kind, Format: format, Object: key,
		SizeBytes: int64(len(content)), GeneratedBy: job.RequestedBy,
	}); err != nil {
		return err
	}

	// Consigné : un bon de livraison est un document contractuel, et savoir qui
	// l'a produit et quand fait partie de ce qui est livré.
	if err := e.store.Audit(ctx, job.RequestedBy, store.AuditArtifactGenerate, release.Tag,
		map[string]any{"kind": kind, "format": format, "object": key}); err != nil {
		e.logger.Error("journal d'audit", "job", job.ID, "error", err)
	}

	e.logger.Info("livrable produit",
		"job", job.ID, "release", release.Tag, "kind", kind, "format", format, "octets", len(content))
	return nil
}

// buildNote rassemble les faits du bon de livraison.
//
// Rien n'y est inventé : chaque champ vient d'un enregistrement ou d'une lecture
// du registre. Ce qui n'a pas pu être établi est nommé dans `Unavailable` plutôt
// qu'omis — dans un document contractuel, une section absente se lit comme une
// affirmation.
func (e *Exporter) buildNote(ctx context.Context, release store.Release, requestedBy string) (report.Note, error) {
	note := report.Note{
		Release:      release.Tag,
		Kind:         release.Kind,
		ImageVersion: release.ImageVersion,
		GitRef:       release.GitRef,
		GeneratedAt:  time.Now(),
		GeneratedBy:  requestedBy,
		Unavailable:  report.KnownUnavailable,
		// La mise en page attend la validation du client (doc 09).
		Provisional: true,
	}

	if name, err := e.store.VersionName(ctx, release.VersionID); err == nil {
		note.Version = name
	}

	if release.ImageVersion != release.Tag {
		note.SourceTag = release.ImageVersion
	}

	promotions, err := e.store.ListPromotions(ctx, release.ID)
	if err != nil {
		return note, err
	}

	// L'ordre importe : l'image construite d'abord, puis le tag de la release,
	// puis les tags de promotion. C'est l'ordre de la chaîne de livraison.
	tags := []string{release.ImageVersion}
	if release.Tag != release.ImageVersion {
		tags = append(tags, release.Tag)
	}
	for _, promotion := range promotions {
		tags = append(tags, promotion.Tag)
		note.Promotions = append(note.Promotions, report.Promotion{
			Environment: promotion.EnvironmentID,
			Tag:         promotion.Tag,
			RequestedAt: promotion.PushedAt,
			Requested:   true,
		})
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
			"Empreintes non vérifiées pour : %s. Sans empreinte, ce document n'atteste pas que l'image "+
				"livrée est celle qui a été validée.", strings.Join(unverified, ", ")))
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
