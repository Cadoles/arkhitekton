package store

import (
	"context"
	"errors"
	"testing"
)

// Regenerating is legitimate: manual actions are written as the delivery proceeds, and a
// delivery note produced too early does not carry what was done afterwards. Each
// generation is therefore a row — the previous ones stay readable, which matters for a
// document already sent.
func TestArtifactsKeepEveryGeneration(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	for range 2 {
		if _, err := db.RecordArtifact(ctx, Artifact{
			ReleaseID: releaseID, Kind: ArtifactDeliveryNote, Format: "pdf",
			Object:    "artifacts/releases/1.8.0/delivery-note.pdf",
			SizeBytes: 2048, GeneratedBy: "vcarroy",
		}); err != nil {
			t.Fatalf("enregistrement: %v", err)
		}
	}

	artifacts, err := db.ListArtifacts(ctx, releaseID)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(artifacts) != 2 {
		t.Errorf("%d artifact(s), expected 2", len(artifacts))
	}
}

func TestGetArtifactCarriesItsRelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	releaseID, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0", CreatedBy: "vcarroy",
	})

	id, err := db.RecordArtifact(ctx, Artifact{
		ReleaseID: releaseID, Kind: ArtifactDeliveryNote, Format: "json",
		Object: "artifacts/releases/1.8.0/delivery-note.json", GeneratedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("enregistrement: %v", err)
	}

	artifact, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Without the release, the download could not check that the artifact asked for is the
	// one in the path.
	if artifact.ReleaseID != releaseID {
		t.Errorf("release = %d, expected %d", artifact.ReleaseID, releaseID)
	}

	if _, err := db.GetArtifact(ctx, 99999); !errors.Is(err, ErrArtifactNotFound) {
		t.Errorf("error = %v, expected ErrArtifactNotFound", err)
	}
}

// The generator starts from a job, which carries the release's identifier and not its
// tag.
func TestGetReleaseByIDCarriesTheManualActions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	releaseID, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	})
	if err := db.SetManualActions(ctx, "1.8.0", "- DNS switchover", "vcarroy"); err != nil {
		t.Fatalf("actions: %v", err)
	}

	release, err := db.GetReleaseByID(ctx, releaseID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if release.Tag != "1.8.0" || release.ManualActions != "- DNS switchover" {
		t.Errorf("release = %+v", release)
	}

	name, err := db.VersionName(ctx, versionID)
	if err != nil || name != "1.8.0" {
		t.Errorf("version = %q, err = %v", name, err)
	}
}
