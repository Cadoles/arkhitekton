package store

import (
	"context"
	"errors"
	"testing"
)

// Régénérer est légitime : les actions manuelles s'écrivent au fil de la
// livraison, et un bon de livraison produit trop tôt ne porte pas ce qui a été
// fait après. Chaque génération est donc une ligne — les précédentes restent
// lisibles, ce qui compte pour un document déjà transmis.
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
		t.Errorf("%d artefact(s), attendu 2", len(artifacts))
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
		t.Fatalf("lecture: %v", err)
	}
	// Sans la release, le téléchargement ne pourrait pas vérifier que l'artefact
	// demandé est bien celui du chemin.
	if artifact.ReleaseID != releaseID {
		t.Errorf("release = %d, attendu %d", artifact.ReleaseID, releaseID)
	}

	if _, err := db.GetArtifact(ctx, 99999); !errors.Is(err, ErrArtifactNotFound) {
		t.Errorf("erreur = %v, attendu ErrArtifactNotFound", err)
	}
}

// Le générateur part d'un job, qui porte l'identifiant de la release et non son
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
	if err := db.SetManualActions(ctx, "1.8.0", "- bascule du DNS", "vcarroy"); err != nil {
		t.Fatalf("actions: %v", err)
	}

	release, err := db.GetReleaseByID(ctx, releaseID)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if release.Tag != "1.8.0" || release.ManualActions != "- bascule du DNS" {
		t.Errorf("release = %+v", release)
	}

	name, err := db.VersionName(ctx, versionID)
	if err != nil || name != "1.8.0" {
		t.Errorf("version = %q, err = %v", name, err)
	}
}
