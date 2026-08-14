package store

import (
	"context"
	"errors"
	"testing"
)

func TestEnsureVersionCreatesThenReuses(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// Sans la synchronisation Redmine, la table est vide et aucune release ne
	// pourrait être créée alors que tout le reste de la chaîne fonctionne.
	first, err := db.EnsureVersion(ctx, "1.8.0")
	if err != nil {
		t.Fatalf("création: %v", err)
	}

	second, err := db.EnsureVersion(ctx, "1.8.0")
	if err != nil {
		t.Fatalf("réutilisation: %v", err)
	}
	if first != second {
		t.Errorf("deux versions créées pour un même nom: %d puis %d", first, second)
	}

	// La ligne provisoire doit être reconnaissable, pour que la synchronisation
	// sache laquelle remplacer.
	var redmineID int64
	var status string
	if err := db.Pool().QueryRow(ctx,
		`select redmine_id, status from versions where id = $1`, first).Scan(&redmineID, &status); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if redmineID >= 0 {
		t.Errorf("redmine_id = %d, attendu négatif pour une version provisoire", redmineID)
	}
	if status != "en-attente-de-synchronisation" {
		t.Errorf("statut = %q", status)
	}
}

func TestCreateAndGetRelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, err := db.EnsureVersion(ctx, "1.8.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	id, err := db.CreateRelease(ctx, Release{
		VersionID: versionID, Tag: "1.8.0-rc1", Kind: "pre", State: "draft",
		GitRef: "abc1234", ImageVersion: "1.8.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("création: %v", err)
	}

	release, err := db.GetRelease(ctx, "1.8.0-rc1")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if release.ID != id || release.Kind != "pre" || release.ImageVersion != "1.8.0-rc1" {
		t.Errorf("release = %+v", release)
	}
}

func TestGetReleaseDistinguishesAbsence(t *testing.T) {
	db := migrated(t)

	if _, err := db.GetRelease(context.Background(), "9.9.9"); !errors.Is(err, ErrReleaseNotFound) {
		t.Errorf("erreur = %v, attendu ErrReleaseNotFound", err)
	}
}

// Le re-tag : la finale porte son propre tag mais l'image de sa RC. C'est cet
// écart que `image_version` absorbe, et sans lequel toute comparaison à
// PortalData échouerait sur une release finale.
func TestFinalReleaseKeepsTheSourceImageVersion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, "1.8.0")

	sourceID, err := db.CreateRelease(ctx, Release{
		VersionID: versionID, Tag: "1.8.0-rc3", Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("RC: %v", err)
	}

	if _, err := db.CreateRelease(ctx, Release{
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", SourceReleaseID: &sourceID,
		CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("finale: %v", err)
	}

	final, err := db.GetRelease(ctx, "1.8.0")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if final.ImageVersion != "1.8.0-rc3" {
		t.Errorf("version d'image = %q, attendu celle de la RC", final.ImageVersion)
	}
	if final.SourceReleaseID == nil || *final.SourceReleaseID != sourceID {
		t.Errorf("release source = %v, attendu %d", final.SourceReleaseID, sourceID)
	}
}

// Une promotion enregistre ce qui a été **demandé**. Pousser le tag ne déploie
// rien : c'est ce qui permettra de distinguer « promu mais pas déployé » de
// « jamais promu ».
func TestRecordPromotion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, "1.8.0")
	releaseID, _ := db.CreateRelease(ctx, Release{
		VersionID: versionID, Tag: "1.8.0-rc1", Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc1", CreatedBy: "vcarroy",
	})

	if err := db.UpsertEnvironment(ctx, Environment{
		ID: "qualif", Label: "Qualification", PromotionSuffix: "testing",
		AcceptsPrerelease: true, Rank: 2, Namespace: "mse-dev", Observable: true, Visible: true,
	}); err != nil {
		t.Fatalf("environnement: %v", err)
	}

	if err := db.RecordPromotion(ctx, Promotion{
		ReleaseID: releaseID, EnvironmentID: "qualif", ImageName: "portal",
		Tag: "1.8.0-rc1_testing", Digest: "sha256:abc",
	}); err != nil {
		t.Fatalf("promotion: %v", err)
	}

	promotions, err := db.ListPromotions(ctx, releaseID)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(promotions) != 1 || promotions[0].Tag != "1.8.0-rc1_testing" {
		t.Errorf("promotions = %+v", promotions)
	}
}
