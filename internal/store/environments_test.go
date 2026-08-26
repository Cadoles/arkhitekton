package store

import (
	"context"
	"errors"
	"testing"
)

// La jointure se fait sur `image_version` et jamais sur le tag : ce que
// PortalData rapporte est la version bakée dans l'image, restée celle de la RC
// pour toute release finale produite par re-tag (B8, B17).
func TestDeployedReleaseJoinsOnImageVersion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.UpsertEnvironment(ctx, Environment{
		ID: "prod", Label: "Production", PromotionSuffix: "prod",
		Rank: 4, Namespace: "mse-prod", Observable: true, Visible: true,
	}); err != nil {
		t.Fatalf("environnement: %v", err)
	}

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	// La finale porte le tag `1.8.0` mais l'image de sa RC.
	if _, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("release: %v", err)
	}

	// PortalData rapporte la version de l'image, pas le tag.
	if err := db.SaveObservation(ctx, Observation{
		EnvironmentID: "prod", PortalVersion: "1.8.0-rc3",
		MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}

	deployed, err := db.DeployedRelease(ctx, "prod")
	if err != nil {
		t.Fatalf("rapprochement: %v", err)
	}
	// Une jointure sur le tag n'aurait rien trouvé.
	if deployed.Tag != "1.8.0" || deployed.ImageVersion != "1.8.0-rc3" {
		t.Errorf("release déployée = %+v", deployed)
	}
}

// Le re-tag rend ce cas normal : la RC et la finale partagent la même image.
// C'est la promotion qui tranche — deviner par la date de création choisirait la
// RC, donc systématiquement la mauvaise dès qu'une finale existe.
func TestPromotionResolvesTheAmbiguity(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.UpsertEnvironment(ctx, Environment{
		ID: "prod", Label: "Production", PromotionSuffix: "prod",
		Rank: 4, Namespace: "mse-prod", Observable: true, Visible: true,
	}); err != nil {
		t.Fatalf("environnement: %v", err)
	}

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	rcID, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0-rc3", Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	})
	finalID, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	})
	_ = rcID

	if err := db.SaveObservation(ctx, Observation{
		EnvironmentID: "prod", PortalVersion: "1.8.0-rc3",
		MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}

	// Sans promotion, l'ambiguïté est signalée plutôt que tranchée au hasard.
	deployed, err := db.DeployedRelease(ctx, "prod")
	if err != nil {
		t.Fatalf("rapprochement: %v", err)
	}
	if !deployed.Ambiguous {
		t.Error("deux releases partagent l'image : l'ambiguïté doit être signalée")
	}

	// La promotion de la finale tranche.
	if err := db.RecordPromotion(ctx, Promotion{
		ReleaseID: finalID, EnvironmentID: "prod",
		ImageName: "portal", Tag: "1.8.0_prod",
	}); err != nil {
		t.Fatalf("promotion: %v", err)
	}

	deployed, err = db.DeployedRelease(ctx, "prod")
	if err != nil {
		t.Fatalf("rapprochement: %v", err)
	}
	if deployed.Tag != "1.8.0" {
		t.Errorf("release déployée = %q, attendu la finale promue", deployed.Tag)
	}
	if deployed.Ambiguous {
		t.Error("la promotion tranche : plus d'ambiguïté")
	}
	if deployed.PromotedAt == nil || deployed.PromotedTag != "1.8.0_prod" {
		t.Errorf("promotion = %v / %q", deployed.PromotedAt, deployed.PromotedTag)
	}
}

// L'application est déployée depuis des années : le dashboard ne connaît que les
// releases créées par lui. Le dire plutôt que présenter un écran vide.
func TestAnUnknownDeployedVersionIsReported(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.UpsertEnvironment(ctx, Environment{
		ID: "prod", Label: "Production", PromotionSuffix: "prod",
		Rank: 4, Namespace: "mse-prod", Observable: true, Visible: true,
	}); err != nil {
		t.Fatalf("environnement: %v", err)
	}
	if err := db.SaveObservation(ctx, Observation{
		EnvironmentID: "prod", PortalVersion: "2024.3.1-legacy",
		MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}

	if _, err := db.DeployedRelease(ctx, "prod"); !errors.Is(err, ErrNoDeployedRelease) {
		t.Errorf("erreur = %v, attendu ErrNoDeployedRelease", err)
	}
}

// Un environnement non observé n'a pas de version : rien à rapprocher.
func TestAnUnobservedEnvironmentHasNoDeployedRelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.UpsertEnvironment(ctx, Environment{
		ID: "prod", Label: "Production", PromotionSuffix: "prod",
		Rank: 4, Namespace: "mse-prod", Observable: false, Visible: true,
	}); err != nil {
		t.Fatalf("environnement: %v", err)
	}

	if _, err := db.DeployedRelease(ctx, "prod"); !errors.Is(err, ErrNoDeployedRelease) {
		t.Errorf("erreur = %v, attendu ErrNoDeployedRelease", err)
	}
}
