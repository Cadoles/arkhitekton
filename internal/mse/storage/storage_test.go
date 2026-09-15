package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les tables du plugin, sur une vraie base.
 *
 * Pilotés par TEST_DATABASE_URL, comme ceux du noyau : les requêtes portent des
 * jointures et des contraintes qu'un faux ne vérifierait pas.
 */

const testProjectID = 1

func testConfig(t *testing.T) config.DatabaseConfig {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL non défini: test d'intégration ignoré")
	}
	return config.DatabaseConfig{DSN: dsn, MaxConns: 4, ConnMaxLifetime: time.Minute}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// bareSchema rend une base vide, sans aucune migration.
func bareSchema(t *testing.T) (config.DatabaseConfig, *store.DB) {
	t.Helper()
	cfg := testConfig(t)
	ctx := context.Background()

	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Pool().Exec(ctx, "drop schema public cascade; create schema public;"); err != nil {
		t.Fatalf("réinitialisation du schéma: %v", err)
	}
	return cfg, db
}

// migrated rend une base au schéma du noyau puis du plugin, avec un projet.
func migrated(t *testing.T) (*store.DB, *Store) {
	t.Helper()
	cfg, db := bareSchema(t)
	ctx := context.Background()

	if err := store.Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("migrations du noyau: %v", err)
	}
	if err := Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("migrations du plugin: %v", err)
	}
	if _, err := db.Pool().Exec(ctx,
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context,
		                       tracker_project, created_by)
		 values ('test', 'vcarroy/test', 'Test', 'https://forge.example', 'Dockerfile', '.',
		         'projet-suivi', 'test')`); err != nil {
		t.Fatalf("projet de test: %v", err)
	}
	return db, New(db.Pool())
}

func releaseFor(t *testing.T, db *store.DB, tag, imageVersion string) int64 {
	t.Helper()
	ctx := context.Background()
	versionID, err := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	id, err := db.CreateRelease(ctx, store.Release{
		ProjectID: testProjectID, VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: imageVersion, CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release %s: %v", tag, err)
	}
	return id
}

func environment(id string, rank int, observable bool) Environment {
	return Environment{
		ID: id, Label: id, Rank: rank, Namespace: "mse-" + id, Observable: observable, Visible: true,
		DeployerSettings: map[string]string{"promotionSuffix": id},
	}
}

/*
 * Les migrations.
 */

// Sur une base dont les tables existent déjà — celles qu'avaient créées les
// migrations du noyau avant le transfert —, la migration du plugin ne fait rien
// et enregistre sa version.
func TestLaMigrationDuPluginReprendDesTablesExistantes(t *testing.T) {
	cfg, db := bareSchema(t)
	ctx := context.Background()

	if err := store.Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("migrations du noyau: %v", err)
	}
	if err := Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("première migration: %v", err)
	}
	// La base d'avant le transfert : les tables sont là, l'historique du plugin non.
	if _, err := db.Pool().Exec(ctx, "drop table "+VersionTable); err != nil {
		t.Fatalf("retrait de l'historique: %v", err)
	}
	if err := Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("reprise de tables existantes: %v", err)
	}
	if pending, err := PendingMigrations(ctx, cfg); err != nil || len(pending) != 0 {
		t.Errorf("en attente après reprise: %v (%v)", pending, err)
	}
	if err := CheckSchema(db.Pool())(ctx); err != nil {
		t.Errorf("sonde du schéma: %v", err)
	}
}

// Le noyau seul ne crée aucune des tables du plugin, et `jobs` ne les référence
// plus.
func TestLeNoyauSeulNeCreePasLesTablesDuPlugin(t *testing.T) {
	cfg, db := bareSchema(t)
	ctx := context.Background()

	if err := store.Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("migrations du noyau: %v", err)
	}
	for _, table := range []string{"environments", "image_promotions", "environment_observations", "release_signoffs"} {
		var exists bool
		if err := db.Pool().QueryRow(ctx, "select to_regclass($1) is not null", "public."+table).Scan(&exists); err != nil {
			t.Fatalf("catalogue: %v", err)
		}
		if exists {
			t.Errorf("table %s créée par le noyau", table)
		}
	}
	if err := CheckSchema(db.Pool())(ctx); err == nil {
		t.Error("la sonde doit signaler un schéma du plugin absent")
	}
}

// Le plugin s'annule sans laisser ses tables, et le noyau reste en place.
func TestLesMigrationsDuPluginSAnnulent(t *testing.T) {
	db, _ := migrated(t)
	cfg := testConfig(t)
	ctx := context.Background()

	if err := Rollback(ctx, cfg); err != nil {
		t.Fatalf("annulation: %v", err)
	}
	var exists bool
	if err := db.Pool().QueryRow(ctx, "select to_regclass('public.environments') is not null").Scan(&exists); err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	if exists {
		t.Error("environments subsiste après annulation")
	}
	if err := db.Pool().QueryRow(ctx, "select to_regclass('public.releases') is not null").Scan(&exists); err != nil || !exists {
		t.Errorf("les tables du noyau ont été touchées (%v)", err)
	}
}

/*
 * Les environnements et leurs relevés.
 */

// La jointure se fait sur `image_version` et jamais sur le tag (B8, B17).
func TestDeployedReleaseJoinsOnImageVersion(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()

	if err := environments.UpsertEnvironment(ctx, environment("prod", 4, true)); err != nil {
		t.Fatalf("environnement: %v", err)
	}
	releaseFor(t, db, "1.8.0", "1.8.0-rc3")
	if err := environments.SaveObservation(ctx, Observation{
		EnvironmentID: "prod", PortalVersion: "1.8.0-rc3", MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}

	deployed, err := environments.DeployedRelease(ctx, "prod")
	if err != nil {
		t.Fatalf("rapprochement: %v", err)
	}
	if deployed.Tag != "1.8.0" || deployed.ImageVersion != "1.8.0-rc3" {
		t.Errorf("release déployée = %+v", deployed)
	}
	if byRelease, err := environments.DeployedByRelease(ctx); err != nil || len(byRelease["1.8.0"]) != 1 {
		t.Errorf("pastilles = %v (%v)", byRelease, err)
	}
}

// Le re-tag rend l'ambiguïté normale : c'est la promotion qui tranche.
func TestPromotionResolvesTheAmbiguity(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()

	if err := environments.UpsertEnvironment(ctx, environment("prod", 4, true)); err != nil {
		t.Fatalf("environnement: %v", err)
	}
	releaseFor(t, db, "1.8.0-rc3", "1.8.0-rc3")
	finalID := releaseFor(t, db, "1.8.0", "1.8.0-rc3")
	if err := environments.SaveObservation(ctx, Observation{
		EnvironmentID: "prod", PortalVersion: "1.8.0-rc3", MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}

	if deployed, err := environments.DeployedRelease(ctx, "prod"); err != nil || !deployed.Ambiguous {
		t.Errorf("sans promotion: %+v (%v) — l'ambiguïté doit être signalée", deployed, err)
	}

	if err := environments.RecordPromotion(ctx, Promotion{
		ReleaseID: finalID, EnvironmentID: "prod", ImageName: "portal", Tag: "1.8.0_prod",
	}); err != nil {
		t.Fatalf("promotion: %v", err)
	}
	deployed, err := environments.DeployedRelease(ctx, "prod")
	if err != nil {
		t.Fatalf("rapprochement: %v", err)
	}
	if deployed.Tag != "1.8.0" || deployed.Ambiguous || deployed.PromotedTag != "1.8.0_prod" {
		t.Errorf("release déployée = %+v, attendu la finale promue", deployed)
	}
}

func TestAnUnknownDeployedVersionIsReported(t *testing.T) {
	_, environments := migrated(t)
	ctx := context.Background()

	if err := environments.UpsertEnvironment(ctx, environment("prod", 4, true)); err != nil {
		t.Fatalf("environnement: %v", err)
	}
	if err := environments.SaveObservation(ctx, Observation{
		EnvironmentID: "prod", PortalVersion: "2024.3.1-legacy", MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}
	if _, err := environments.DeployedRelease(ctx, "prod"); !errors.Is(err, ErrNoDeployedRelease) {
		t.Errorf("erreur = %v, attendu ErrNoDeployedRelease", err)
	}
}

// Les réglages du déployeur font l'aller-retour sans être lus, clé inconnue comprise.
func TestLesReglagesDuDeployeurFontLAllerRetourTelsQuels(t *testing.T) {
	_, environments := migrated(t)
	ctx := context.Background()

	settings := map[string]string{
		"promotionSuffix": "testing", "acceptsPrerelease": "true",
		"kustomization": "portal-testing", "inconnueDuDashboard": "valeur",
	}
	qualif := environment("qualif", 2, true)
	qualif.DeployerSettings = settings
	if err := environments.UpsertEnvironment(ctx, qualif); err != nil {
		t.Fatalf("environnement: %v", err)
	}

	listed, err := environments.ListEnvironments(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("relecture: %v (%v)", listed, err)
	}
	if !maps.Equal(listed[0].DeployerSettings, settings) {
		t.Errorf("réglages relus %v, déposés %v", listed[0].DeployerSettings, settings)
	}
}

// La carte ne porte que des chaînes : la contrainte refuse le reste à l'écriture.
func TestLaCarteDeReglagesNAccepteQueDesChaines(t *testing.T) {
	db, _ := migrated(t)

	if _, err := db.Pool().Exec(context.Background(),
		`insert into environments (id, label, rank, namespace, deployer_settings, observable, visible)
		 values ('dev', 'Dev', 1, 'mse-dev', '{"acceptsPrerelease": true}'::jsonb, true, true)`,
	); err == nil {
		t.Error("un booléen JSON devrait être refusé par la contrainte")
	}
}

/*
 * Les promotions.
 */

// Une promotion enregistre ce qui a été **demandé**, et l'historique le suit.
func TestLHistoriqueSuitLesPromotionsDemandees(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()

	if err := environments.UpsertEnvironment(ctx, environment("qualif", 2, true)); err != nil {
		t.Fatalf("environnement: %v", err)
	}
	var last int64
	for _, tag := range []string{"1.8.0-rc1", "1.8.0-rc2"} {
		last = releaseFor(t, db, tag, tag)
		if err := environments.RecordPromotion(ctx, Promotion{
			ReleaseID: last, EnvironmentID: "qualif", ImageName: "portal", Tag: tag + "_testing",
		}); err != nil {
			t.Fatalf("promotion %s: %v", tag, err)
		}
	}

	promotions, err := environments.ListPromotions(ctx, last)
	if err != nil || len(promotions) != 1 || promotions[0].Tag != "1.8.0-rc2_testing" {
		t.Errorf("promotions = %+v (%v)", promotions, err)
	}
	history, err := environments.ListEnvironmentHistory(ctx, "qualif", 0)
	if err != nil || len(history) != 2 || history[0].ReleaseTag != "1.8.0-rc2" {
		t.Errorf("historique = %+v (%v)", history, err)
	}
	latest, err := environments.LatestPromotions(ctx)
	if err != nil || latest["qualif"].ReleaseTag != "1.8.0-rc2" {
		t.Errorf("dernière promotion = %+v (%v)", latest, err)
	}
}

/*
 * Les validations de recette.
 */

func flow(t *testing.T, environments *Store) {
	t.Helper()
	for rank, id := range []string{"unstable", "qualif", "preprod", "prod"} {
		if err := environments.UpsertEnvironment(context.Background(), environment(id, rank+1, true)); err != nil {
			t.Fatalf("environnement %s: %v", id, err)
		}
	}
}

// Une release validée en qualif ne l'est ni en préproduction ni pour une autre
// release : la clé est le couple.
func TestUneValidationEstPropreAuCoupleReleaseEnvironnement(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)
	release := releaseFor(t, db, "1.8.0-rc1", "1.8.0-rc1")
	autre := releaseFor(t, db, "1.8.0-rc2", "1.8.0-rc2")

	if err := environments.SetSignoff(ctx, release, "qualif", "Client", "pass", "", "vcarroy"); err != nil {
		t.Fatalf("validation: %v", err)
	}
	if got, _ := environments.EnvironmentSignoffs(ctx, release, "preprod"); len(got) != 0 {
		t.Errorf("la validation de qualif a fui en preprod: %+v", got)
	}
	if got, _ := environments.EnvironmentSignoffs(ctx, autre, "qualif"); len(got) != 0 {
		t.Errorf("la validation a fui sur une autre release: %+v", got)
	}
}

// Se prononcer à nouveau remplace son avis ; deux rôles cohabitent.
func TestUnAvisSeRemplaceEtLesRolesCohabitent(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)
	release := releaseFor(t, db, "1.8.0-rc1", "1.8.0-rc1")

	for _, decision := range [][3]string{
		{"Client", "fail", "panier cassé"}, {"Client", "pass", ""}, {"Recette", "warn", "lenteur"},
	} {
		if err := environments.SetSignoff(ctx, release, "qualif", decision[0], decision[1], decision[2], "vcarroy"); err != nil {
			t.Fatalf("avis %v: %v", decision, err)
		}
	}
	signoffs, err := environments.EnvironmentSignoffs(ctx, release, "qualif")
	if err != nil || len(signoffs) != 2 {
		t.Fatalf("avis = %+v (%v)", signoffs, err)
	}
	if signoffs[0].Role != "Client" || signoffs[0].Status != "pass" || signoffs[0].Detail != "" {
		t.Errorf("avis du client = %+v", signoffs[0])
	}
}

// Retirer n'est pas refuser ; retirer un avis absent est une erreur nommée.
func TestRetirerUnAvis(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)
	release := releaseFor(t, db, "1.8.0-rc1", "1.8.0-rc1")

	if err := environments.SetSignoff(ctx, release, "qualif", "Client", "fail", "erreur", "vcarroy"); err != nil {
		t.Fatalf("refus: %v", err)
	}
	if err := environments.DeleteSignoff(ctx, release, "qualif", "Client"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	if got, _ := environments.EnvironmentSignoffs(ctx, release, "qualif"); len(got) != 0 {
		t.Errorf("%d avis subsistent après retrait", len(got))
	}
	if err := environments.DeleteSignoff(ctx, release, "qualif", "Personne"); !errors.Is(err, ErrSignoffNotFound) {
		t.Errorf("erreur %v, attendu ErrSignoffNotFound", err)
	}
}

// L'ordre de lecture est celui du flux de promotion, et les avis partent avec la
// release.
func TestLesValidationsSuiventLeFluxEtPartentAvecLaRelease(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)
	release := releaseFor(t, db, "1.8.0-rc1", "1.8.0-rc1")

	for _, id := range []string{"prod", "qualif", "unstable"} {
		if err := environments.SetSignoff(ctx, release, id, "Client", "pass", "", "vcarroy"); err != nil {
			t.Fatalf("validation %s: %v", id, err)
		}
	}
	signoffs, err := environments.ListSignoffs(ctx, release)
	if err != nil || len(signoffs) != 3 || signoffs[0].EnvironmentID != "unstable" || signoffs[2].EnvironmentID != "prod" {
		t.Errorf("avis = %+v (%v)", signoffs, err)
	}

	if _, err := db.Pool().Exec(ctx, `delete from releases where id = $1`, release); err != nil {
		t.Fatalf("suppression de la release: %v", err)
	}
	var remaining int
	if err := db.Pool().QueryRow(ctx, `select count(*) from release_signoffs`).Scan(&remaining); err != nil || remaining != 0 {
		t.Errorf("%d validation(s) orpheline(s) (%v)", remaining, err)
	}
}

func TestUnEnvironnementInconnuEstDetecte(t *testing.T) {
	_, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)

	if exists, err := environments.SignoffEnvironmentExists(ctx, "qualif"); err != nil || !exists {
		t.Errorf("qualif: %v (%v)", exists, err)
	}
	if exists, err := environments.SignoffEnvironmentExists(ctx, "nexiste-pas"); err != nil || exists {
		t.Errorf("environnement inconnu reconnu: %v (%v)", exists, err)
	}
}
