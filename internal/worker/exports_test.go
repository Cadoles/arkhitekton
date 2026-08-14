package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type fakeExportStore struct {
	release    store.Release
	promotions []store.Promotion
	recorded   []store.Artifact
}

func (f *fakeExportStore) GetReleaseByID(_ context.Context, id int64) (store.Release, error) {
	if id != f.release.ID {
		return store.Release{}, store.ErrReleaseNotFound
	}
	return f.release, nil
}

func (f *fakeExportStore) VersionName(context.Context, int64) (string, error) { return "1.8.0", nil }

func (f *fakeExportStore) ListPromotions(context.Context, int64) ([]store.Promotion, error) {
	return f.promotions, nil
}

func (f *fakeExportStore) RecordArtifact(_ context.Context, artifact store.Artifact) (int64, error) {
	artifact.ID = int64(len(f.recorded) + 1)
	f.recorded = append(f.recorded, artifact)
	return artifact.ID, nil
}

func (f *fakeExportStore) Audit(context.Context, string, string, string, map[string]any) error {
	return nil
}

type fakeStorage map[string][]byte

func (f fakeStorage) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	content, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	f[key] = content
	return nil
}

type fakeDigests map[string]string

func (f fakeDigests) Digest(_ context.Context, _, tag string) (string, error) {
	digest, found := f[tag]
	if !found {
		return "", errors.New("manifeste absent")
	}
	return digest, nil
}

func newExportFixture() (*fakeExportStore, fakeStorage, *Exporter) {
	at := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	releases := &fakeExportStore{
		release: store.Release{
			ID: 1, VersionID: 5, Tag: "1.8.0", Kind: "final", State: "ready",
			ImageVersion: "1.8.0-rc3", GitRef: "abc1234", CreatedBy: "vcarroy",
			ManualActions: "- purge du cache", ManualActionsUpdatedBy: "vcarroy",
		},
		promotions: []store.Promotion{
			{EnvironmentID: "prod", ImageName: "portal", Tag: "1.8.0_prod", PushedAt: at},
		},
	}
	storage := fakeStorage{}
	digests := fakeDigests{"1.8.0-rc3": "sha256:aaa", "1.8.0": "sha256:aaa"}
	return releases, storage,
		NewExporter(releases, storage, digests, "portal", slog.New(slog.DiscardHandler))
}

func exportJob(params map[string]string, releaseID int64) store.Job {
	return store.Job{ID: 42, Kind: KindExport, Params: params, RequestedBy: "vcarroy", ReleaseID: &releaseID}
}

func TestExporterWritesAtTheExpectedKey(t *testing.T) {
	releases, storage, exporter := newExportFixture()

	if err := exporter.Run(context.Background(), exportJob(nil, 1)); err != nil {
		t.Fatalf("export: %v", err)
	}

	// La clé est calculée avant écriture, jamais découverte : c'est ce qui permet
	// de la retrouver sans balayer un préfixe.
	const key = "artifacts/releases/1.8.0/delivery-note.pdf"
	content, written := storage[key]
	if !written {
		t.Fatalf("clés écrites = %v", keysOf(storage))
	}
	if !bytes.HasPrefix(content, []byte("%PDF-")) {
		t.Error("le contenu n'est pas un PDF")
	}

	if len(releases.recorded) != 1 {
		t.Fatalf("%d artefact(s) enregistré(s)", len(releases.recorded))
	}
	artifact := releases.recorded[0]
	if artifact.Object != key || artifact.SizeBytes != int64(len(content)) {
		t.Errorf("artefact = %+v", artifact)
	}
}

func TestExporterRendersJSON(t *testing.T) {
	_, storage, exporter := newExportFixture()

	err := exporter.Run(context.Background(), exportJob(map[string]string{ParamExportFormat: "json"}, 1))
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	content, written := storage["artifacts/releases/1.8.0/delivery-note.json"]
	if !written {
		t.Fatalf("clés écrites = %v", keysOf(storage))
	}
	text := string(content)
	// Un intégrateur doit lire que le format n'est pas définitif.
	if !strings.Contains(text, `"provisional": true`) {
		t.Error("le drapeau provisoire manque")
	}
	// La version de l'image, distincte du tag, est ce qui sera constaté sur les
	// environnements.
	if !strings.Contains(text, `"image_version": "1.8.0-rc3"`) {
		t.Error("la version d'image manque")
	}
}

// Un tag non poussé n'a pas d'empreinte, et le document doit le dire : sans
// empreinte, il n'atteste pas que l'image livrée est celle qui a été validée.
func TestExporterNamesUnverifiedImages(t *testing.T) {
	releases, storage, exporter := newExportFixture()
	_ = releases

	if err := exporter.Run(context.Background(), exportJob(map[string]string{ParamExportFormat: "json"}, 1)); err != nil {
		t.Fatalf("export: %v", err)
	}

	text := string(storage["artifacts/releases/1.8.0/delivery-note.json"])
	if !strings.Contains(text, "1.8.0_prod") {
		t.Fatal("le tag de promotion doit figurer dans le document")
	}
	if !strings.Contains(text, "Empreintes non vérifiées") {
		t.Error("le document doit nommer ce qu'il n'a pas pu vérifier")
	}
}

// Produire un document vide qui aurait l'air d'attester quelque chose est pire
// que refuser.
func TestExporterRefusesReportsWhoseDataIsMissing(t *testing.T) {
	_, storage, exporter := newExportFixture()

	err := exporter.Run(context.Background(),
		exportJob(map[string]string{ParamExportKind: store.ArtifactTestReport}, 1))
	if err == nil {
		t.Error("un rapport de tests sans résultats doit être refusé")
	}
	if len(storage) != 0 {
		t.Errorf("%d objet(s) écrit(s) malgré le refus", len(storage))
	}
}

func TestExporterRefusesAnUnknownFormat(t *testing.T) {
	_, _, exporter := newExportFixture()

	if err := exporter.Run(context.Background(),
		exportJob(map[string]string{ParamExportFormat: "docx"}, 1)); err == nil {
		t.Error("format inconnu accepté")
	}
}

func TestExporterRefusesAJobWithoutRelease(t *testing.T) {
	_, _, exporter := newExportFixture()

	if err := exporter.Run(context.Background(), store.Job{ID: 1, Kind: KindExport}); err == nil {
		t.Error("un export sans release ne désigne rien à produire")
	}
}

// Le registre est optionnel : un bon de livraison reste produisible sans
// empreintes, à condition de dire ce qu'il n'a pas pu vérifier.
func TestExporterWorksWithoutARegistry(t *testing.T) {
	releases, storage, _ := newExportFixture()
	exporter := NewExporter(releases, storage, nil, "portal", slog.New(slog.DiscardHandler))

	if err := exporter.Run(context.Background(), exportJob(map[string]string{ParamExportFormat: "json"}, 1)); err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(string(storage["artifacts/releases/1.8.0/delivery-note.json"]), "Empreintes non vérifiées") {
		t.Error("l'absence d'empreinte doit être signalée")
	}
}

func keysOf(storage fakeStorage) []string {
	var keys []string
	for key := range storage {
		keys = append(keys, key)
	}
	return keys
}
