package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeExportStore struct {
	release  store.Release
	recorded []store.Artifact
}

func (f *fakeExportStore) GetReleaseByID(_ context.Context, id int64) (store.Release, error) {
	if id != f.release.ID {
		return store.Release{}, store.ErrReleaseNotFound
	}
	return f.release, nil
}

func (f *fakeExportStore) VersionName(context.Context, int64) (string, error) { return "1.8.0", nil }

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
		return "", errors.New("manifest missing")
	}
	return digest, nil
}

func newExportFixture() (*fakeExportStore, fakeStorage, *Exporter) {
	releases := &fakeExportStore{
		release: store.Release{
			ID: 1, VersionID: 5, Tag: "1.8.0", Kind: "final", State: "ready",
			ImageVersion: "1.8.0-rc3", GitRef: "abc1234", CreatedBy: "vcarroy",
			ManualActions: "- cache purge", ManualActionsUpdatedBy: "vcarroy",
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

	// The key is computed before writing, never discovered: that is what makes it findable
	// without sweeping a prefix.
	const key = "artifacts/releases/1.8.0/delivery-note.pdf"
	content, written := storage[key]
	if !written {
		t.Fatalf("keys written = %v", keysOf(storage))
	}
	if !bytes.HasPrefix(content, []byte("%PDF-")) {
		t.Error("the content is not a PDF")
	}

	if len(releases.recorded) != 1 {
		t.Fatalf("%d artifact(s) recorded", len(releases.recorded))
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
		t.Fatalf("keys written = %v", keysOf(storage))
	}
	text := string(content)
	// An integrator must read that the format is not final.
	if !strings.Contains(text, `"provisional": true`) {
		t.Error("the provisional flag is missing")
	}
	// The image's version, distinct from the tag, is what will be observed on the environments.
	if !strings.Contains(text, `"image_version": "1.8.0-rc3"`) {
		t.Error("the image version is missing")
	}
}

// An unpushed tag has no digest, and the document must say so: without a digest, it does not
// attest that the image delivered is the one that was validated.
func TestExporterNamesUnverifiedImages(t *testing.T) {
	releases, storage, _ := newExportFixture()
	// The release's tag is not pushed yet: it has no digest.
	exporter := NewExporter(releases, storage, fakeDigests{"1.8.0-rc3": "sha256:aaa"},
		"portal", slog.New(slog.DiscardHandler))

	if err := exporter.Run(context.Background(), exportJob(map[string]string{ParamExportFormat: "json"}, 1)); err != nil {
		t.Fatalf("export: %v", err)
	}

	text := string(storage["artifacts/releases/1.8.0/delivery-note.json"])
	// Promotions no longer appear in the delivery note.
	if strings.Contains(text, "promotions") {
		t.Error("the document must no longer carry promotions")
	}
	if !strings.Contains(text, "Digests not checked") {
		t.Error("the document must name what it could not check")
	}
}

// Producing an empty document that would look like it attests to something is worse than
// refusing.
func TestExporterRefusesReportsWhoseDataIsMissing(t *testing.T) {
	_, storage, exporter := newExportFixture()

	err := exporter.Run(context.Background(),
		exportJob(map[string]string{ParamExportKind: store.ArtifactTestReport}, 1))
	if err == nil {
		t.Error("a test report with no results must be refused")
	}
	if len(storage) != 0 {
		t.Errorf("%d object(s) written despite the refusal", len(storage))
	}
}

func TestExporterRefusesAnUnknownFormat(t *testing.T) {
	_, _, exporter := newExportFixture()

	if err := exporter.Run(context.Background(),
		exportJob(map[string]string{ParamExportFormat: "docx"}, 1)); err == nil {
		t.Error("unknown format accepted")
	}
}

func TestExporterRefusesAJobWithoutRelease(t *testing.T) {
	_, _, exporter := newExportFixture()

	if err := exporter.Run(context.Background(), store.Job{ID: 1, Kind: KindExport}); err == nil {
		t.Error("an export with no release names nothing to produce")
	}
}

// The registry is optional: a delivery note stays producible without digests, provided it
// says what it could not check.
func TestExporterWorksWithoutARegistry(t *testing.T) {
	releases, storage, _ := newExportFixture()
	exporter := NewExporter(releases, storage, nil, "portal", slog.New(slog.DiscardHandler))

	if err := exporter.Run(context.Background(), exportJob(map[string]string{ParamExportFormat: "json"}, 1)); err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(string(storage["artifacts/releases/1.8.0/delivery-note.json"]), "Digests not checked") {
		t.Error("the missing digest must be reported")
	}
}

func keysOf(storage fakeStorage) []string {
	var keys []string
	for key := range storage {
		keys = append(keys, key)
	}
	return keys
}

// A project that switched its deliverables off after asking gets none.
func TestTheExporterRefusesWhenDeliverablesAreOff(t *testing.T) {
	project, release := int64(3), int64(1)
	exporter := NewExporter(nil, nil, nil, "portal", slog.New(slog.DiscardHandler)).
		WithGate(func(context.Context, int64) bool { return false })

	err := exporter.Run(context.Background(), store.Job{ID: 1, ProjectID: &project, ReleaseID: &release})
	if err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("%v", err)
	}
}
