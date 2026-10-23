package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/host/hosttest"
)

/*
 * The plugin's tables, on a real database.
 *
 * Driven by TEST_DATABASE_URL, like the core's: the queries carry joins and constraints a fake
 * would not check.
 */

const testProjectID = 1

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// bareSchema returns an empty database, with no migration at all.
func bareSchema(t *testing.T) (host.DatabaseConfig, *hosttest.Database) {
	t.Helper()
	db := hosttest.EmptyDatabase(t)
	return db.Config, db
}

// migrated returns a database with the core's schema then the plugin's, with one project.
func migrated(t *testing.T) (*hosttest.Database, *Store) {
	t.Helper()
	cfg, db := bareSchema(t)
	ctx := context.Background()

	db.MigrateCore(t)
	if err := Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("plugin migrations: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context,
		                       tracker_project, created_by)
		 values ('test', 'vcarroy/test', 'Test', 'https://forge.example', 'Dockerfile', '.',
		         'projet-suivi', 'test')`); err != nil {
		t.Fatalf("test project: %v", err)
	}
	return db, New(db.Pool)
}

func releaseFor(t *testing.T, db *hosttest.Database, tag, imageVersion string) int64 {
	t.Helper()
	return db.Release(t, testProjectID, tag, imageVersion)
}

func environment(id string, rank int, observable bool) Environment {
	return Environment{
		ProjectID: testProjectID, ID: id, Label: id, Rank: rank, Namespace: "mse-" + id,
		Observable: observable, Visible: true,
		DeployerSettings: map[string]string{"promotionSuffix": id},
	}
}

/*
 * Les migrations.
 */

// On a database whose tables already exist — those the core's migrations had created before the
// transfer — the plugin's migration does nothing and records its version.
func TestThePluginMigrationTakesOverExistingTables(t *testing.T) {
	cfg, db := bareSchema(t)
	ctx := context.Background()

	db.MigrateCore(t)
	// The schema from before the transfer: the tables as the core created them, that is, the
	// plugin's first migration, and nothing of its history.
	if err := Migrations.UpTo(ctx, cfg, 1); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	// The database from before the transfer: the tables are there, the plugin's history is not.
	if _, err := db.Pool.Exec(ctx, "drop table "+VersionTable); err != nil {
		t.Fatalf("removing the history: %v", err)
	}
	if err := Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("taking over existing tables: %v", err)
	}
	if pending, err := PendingMigrations(ctx, cfg); err != nil || len(pending) != 0 {
		t.Errorf("pending after the takeover: %v (%v)", pending, err)
	}
	if err := CheckSchema(db.Pool)(ctx); err != nil {
		t.Errorf("schema probe: %v", err)
	}
}

// The core alone creates none of the plugin's tables, and `jobs` no longer references them.
func TestTheCoreAloneDoesNotCreateThePluginsTables(t *testing.T) {
	_, db := bareSchema(t)
	ctx := context.Background()

	db.MigrateCore(t)
	for _, table := range []string{"environments", "image_promotions", "environment_observations", "release_signoffs"} {
		var exists bool
		if err := db.Pool.QueryRow(ctx, "select to_regclass($1) is not null", "public."+table).Scan(&exists); err != nil {
			t.Fatalf("catalogue: %v", err)
		}
		if exists {
			t.Errorf("table %s created by the core", table)
		}
	}
	if err := CheckSchema(db.Pool)(ctx); err == nil {
		t.Error("the probe must report a missing plugin schema")
	}
}

// The plugin rolls back without leaving its tables, and the core stays in place.
func TestThePluginsMigrationsRollBack(t *testing.T) {
	db, _ := migrated(t)
	cfg := db.Config
	ctx := context.Background()

	// All of its migrations, and not the last: it is the first that creates the tables.
	count, err := ExpectedVersion()
	if err != nil {
		t.Fatalf("embedded migrations: %v", err)
	}
	for step := range count {
		if err := Rollback(ctx, cfg); err != nil {
			t.Fatalf("rollback %d/%d: %v", step+1, count, err)
		}
	}
	var exists bool
	if err := db.Pool.QueryRow(ctx, "select to_regclass('public.environments') is not null").Scan(&exists); err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	if exists {
		t.Error("environments survives the rollback")
	}
	if err := db.Pool.QueryRow(ctx, "select to_regclass('public.releases') is not null").Scan(&exists); err != nil || !exists {
		t.Errorf("the core's tables were touched (%v)", err)
	}
}

// The data migration switches the plugin off for the projects that are not MSE's, and its
// rollback removes only what it applied.
func TestTheDataMigrationSwitchesThePluginOffOutsideMSE(t *testing.T) {
	cfg, db := bareSchema(t)
	ctx := context.Background()

	db.MigrateCore(t)
	for _, slug := range []string{"mse-test-project", "mse-tekton"} {
		if _, err := db.Pool.Exec(ctx,
			`insert into projects (slug, repository, label, forge_url, dockerfile, build_context, created_by)
			 values ($1, 'vcarroy/'||$1, $1, 'https://forge.example', 'Dockerfile', '.', 'test')`, slug); err != nil {
			t.Fatalf("project %s: %v", slug, err)
		}
	}
	if err := Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("plugin migrations: %v", err)
	}

	decisions := func() map[string]bool {
		rows, err := db.Pool.Query(ctx,
			`select p.slug, pp.enabled from project_plugins pp join projects p on p.id = pp.project_id
			 where pp.plugin_id = 'mse'`)
		if err != nil {
			t.Fatalf("decisions: %v", err)
		}
		defer rows.Close()
		found := map[string]bool{}
		for rows.Next() {
			var slug string
			var enabled bool
			if err := rows.Scan(&slug, &enabled); err != nil {
				t.Fatalf("decision: %v", err)
			}
			found[slug] = enabled
		}
		return found
	}

	got := decisions()
	if enabled, declared := got["mse-tekton"]; !declared || enabled {
		t.Errorf("mse-tekton: %v (declared %v), expected switched off", enabled, declared)
	}
	if _, declared := got["mse-test-project"]; declared {
		t.Error("mse-test-project must inherit, with no decision recorded")
	}

	// Up to the version preceding the data migration.
	if err := Migrations.DownTo(ctx, cfg, 1); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := decisions(); len(got) != 0 {
		t.Errorf("decisions remaining after the rollback: %v", got)
	}
}

// Migration 0003 attaches the existing rows to the MSE project, and one environment identifier
// becomes possible in two projects.
func TestExistingEnvironmentsJoinTheMSEProject(t *testing.T) {
	cfg, db := bareSchema(t)
	ctx := context.Background()

	db.MigrateCore(t)
	if err := Migrations.UpTo(ctx, cfg, 2); err != nil {
		t.Fatalf("plugin migrations up to 2: %v", err)
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	for _, slug := range []string{"other-project", "mse-test-project"} {
		exec(`insert into projects (slug, repository, label, forge_url, dockerfile, build_context, created_by)
		      values ($1, 'vcarroy/'||$1, $1, 'https://forge.example', 'Dockerfile', '.', 'test')`, slug)
	}
	// The instance's model: an environment with no project, its reading, a sign-off carried by a
	// release of the MSE project.
	exec(`insert into environments (id, label, rank, namespace) values ('qualif', 'Qualification', 2, 'mse-dev')`)
	exec(`insert into environment_observations (environment_id, observed_at, portal_version, resolution)
	      values ('qualif', now(), '1.8.0', 'portaldata')`)
	var mseID int64
	if err := db.Pool.QueryRow(ctx, `select id from projects where slug = 'mse-test-project'`).Scan(&mseID); err != nil {
		t.Fatalf("MSE project: %v", err)
	}
	releaseID := db.Release(t, mseID, "1.8.0", "1.8.0")
	exec(`insert into release_signoffs (release_id, environment_id, role, status, decided_by)
	      values ($1, 'qualif', 'Client', 'pass', 'vcarroy')`, releaseID)

	if err := Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("migration 0003: %v", err)
	}

	environments := New(db.Pool)
	if listed, err := environments.ListEnvironments(ctx, mseID); err != nil || len(listed) != 1 {
		t.Fatalf("environments of the MSE project: %+v (%v)", listed, err)
	}
	views, err := environments.ListEnvironmentViews(ctx, mseID)
	if err != nil || len(views) != 1 || views[0].Observation == nil {
		t.Errorf("reading attached: %+v (%v)", views, err)
	}
	if signoffs, err := environments.ListSignoffs(ctx, releaseID); err != nil || len(signoffs) != 1 {
		t.Errorf("sign-off attached: %+v (%v)", signoffs, err)
	}

	// The same identifier in another project: refused by the old key.
	var otherID int64
	if err := db.Pool.QueryRow(ctx, `select id from projects where slug = 'other-project'`).Scan(&otherID); err != nil {
		t.Fatalf("other project: %v", err)
	}
	if err := environments.UpsertEnvironment(ctx, Environment{
		ProjectID: otherID, ID: "qualif", Label: "Qualification", Rank: 2, Namespace: "other", Visible: true,
	}); err != nil {
		t.Errorf("qualif of a second project: %v", err)
	}
	if listed, _ := environments.ListEnvironments(ctx, otherID); len(listed) != 1 {
		t.Errorf("environments of the second project: %+v", listed)
	}
}

/*
 * The environments and their readings.
 */

// The join is done on `image_version` and never on the tag.
func TestDeployedReleaseJoinsOnImageVersion(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()

	if err := environments.UpsertEnvironment(ctx, environment("prod", 4, true)); err != nil {
		t.Fatalf("environment: %v", err)
	}
	releaseFor(t, db, "1.8.0", "1.8.0-rc3")
	if err := environments.SaveObservation(ctx, Observation{
		ProjectID: testProjectID, EnvironmentID: "prod", PortalVersion: "1.8.0-rc3", MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}

	deployed, err := environments.DeployedRelease(ctx, testProjectID, "prod")
	if err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if deployed.Tag != "1.8.0" || deployed.ImageVersion != "1.8.0-rc3" {
		t.Errorf("release deployed = %+v", deployed)
	}
	if byRelease, err := environments.DeployedByRelease(ctx, testProjectID); err != nil || len(byRelease["1.8.0"]) != 1 {
		t.Errorf("badges = %v (%v)", byRelease, err)
	}
}

// The re-tag makes ambiguity normal: it is the promotion that settles it.
func TestPromotionResolvesTheAmbiguity(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()

	if err := environments.UpsertEnvironment(ctx, environment("prod", 4, true)); err != nil {
		t.Fatalf("environment: %v", err)
	}
	releaseFor(t, db, "1.8.0-rc3", "1.8.0-rc3")
	finalID := releaseFor(t, db, "1.8.0", "1.8.0-rc3")
	if err := environments.SaveObservation(ctx, Observation{
		ProjectID: testProjectID, EnvironmentID: "prod", PortalVersion: "1.8.0-rc3", MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}

	if deployed, err := environments.DeployedRelease(ctx, testProjectID, "prod"); err != nil || !deployed.Ambiguous {
		t.Errorf("with no promotion: %+v (%v) — the ambiguity must be reported", deployed, err)
	}

	if err := environments.RecordPromotion(ctx, Promotion{
		ReleaseID: finalID, EnvironmentID: "prod", ImageName: "portal", Tag: "1.8.0_prod",
	}); err != nil {
		t.Fatalf("promotion: %v", err)
	}
	deployed, err := environments.DeployedRelease(ctx, testProjectID, "prod")
	if err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if deployed.Tag != "1.8.0" || deployed.Ambiguous || deployed.PromotedTag != "1.8.0_prod" {
		t.Errorf("release deployed = %+v, expected the promoted final", deployed)
	}
}

func TestAnUnknownDeployedVersionIsReported(t *testing.T) {
	_, environments := migrated(t)
	ctx := context.Background()

	if err := environments.UpsertEnvironment(ctx, environment("prod", 4, true)); err != nil {
		t.Fatalf("environment: %v", err)
	}
	if err := environments.SaveObservation(ctx, Observation{
		ProjectID: testProjectID, EnvironmentID: "prod", PortalVersion: "2024.3.1-legacy", MigrationState: "done", Resolution: "portaldata",
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}
	if _, err := environments.DeployedRelease(ctx, testProjectID, "prod"); !errors.Is(err, ErrNoDeployedRelease) {
		t.Errorf("error = %v, expected ErrNoDeployedRelease", err)
	}
}

// The deployer's settings make the round trip without being read, unknown key included.
func TestTheDeployerSettingsMakeTheRoundTripAsTheyAre(t *testing.T) {
	_, environments := migrated(t)
	ctx := context.Background()

	settings := map[string]string{
		"promotionSuffix": "testing", "acceptsPrerelease": "true",
		"kustomization": "portal-testing", "unknownToTheDashboard": "value",
	}
	qualif := environment("qualif", 2, true)
	qualif.DeployerSettings = settings
	if err := environments.UpsertEnvironment(ctx, qualif); err != nil {
		t.Fatalf("environment: %v", err)
	}

	listed, err := environments.ListEnvironments(ctx, testProjectID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("read back: %v (%v)", listed, err)
	}
	if !maps.Equal(listed[0].DeployerSettings, settings) {
		t.Errorf("settings read back %v, deposited %v", listed[0].DeployerSettings, settings)
	}
}

// The map carries only strings: the constraint refuses the rest at write time.
func TestTheSettingsMapAcceptsOnlyStrings(t *testing.T) {
	db, _ := migrated(t)

	if _, err := db.Pool.Exec(context.Background(),
		`insert into environments (project_id, id, label, rank, namespace, deployer_settings, observable, visible)
		 values (1, 'dev', 'Dev', 1, 'mse-dev', '{"acceptsPrerelease": true}'::jsonb, true, true)`,
	); err == nil {
		t.Error("a JSON boolean should be refused by the constraint")
	}
}

/*
 * Les promotions.
 */

// A promotion records what was **requested**, and the history follows it.
func TestTheHistoryFollowsTheRequestedPromotions(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()

	if err := environments.UpsertEnvironment(ctx, environment("qualif", 2, true)); err != nil {
		t.Fatalf("environment: %v", err)
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
	history, err := environments.ListEnvironmentHistory(ctx, testProjectID, "qualif", 0)
	if err != nil || len(history) != 2 || history[0].ReleaseTag != "1.8.0-rc2" {
		t.Errorf("history = %+v (%v)", history, err)
	}
	latest, err := environments.LatestPromotions(ctx, testProjectID)
	if err != nil || latest["qualif"].ReleaseTag != "1.8.0-rc2" {
		t.Errorf("last promotion = %+v (%v)", latest, err)
	}
}

/*
 * Les validations de recette.
 */

func flow(t *testing.T, environments *Store) {
	t.Helper()
	for rank, id := range []string{"unstable", "qualif", "preprod", "prod"} {
		if err := environments.UpsertEnvironment(context.Background(), environment(id, rank+1, true)); err != nil {
			t.Fatalf("environment %s: %v", id, err)
		}
	}
}

// A release validated in qualif is validated neither in pre-production nor for another release:
// the key is the pair.
func TestASignoffIsSpecificToTheReleaseEnvironmentPair(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)
	release := releaseFor(t, db, "1.8.0-rc1", "1.8.0-rc1")
	other := releaseFor(t, db, "1.8.0-rc2", "1.8.0-rc2")

	if err := environments.SetSignoff(ctx, release, "qualif", "Client", "pass", "", "vcarroy"); err != nil {
		t.Fatalf("sign-off: %v", err)
	}
	if got, _ := environments.EnvironmentSignoffs(ctx, release, "preprod"); len(got) != 0 {
		t.Errorf("the qualif sign-off leaked into preprod: %+v", got)
	}
	if got, _ := environments.EnvironmentSignoffs(ctx, other, "qualif"); len(got) != 0 {
		t.Errorf("the sign-off leaked onto another release: %+v", got)
	}
}

// Giving an opinion again replaces one's own; two roles coexist.
func TestAnOpinionIsReplacedAndTheRolesCoexist(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)
	release := releaseFor(t, db, "1.8.0-rc1", "1.8.0-rc1")

	for _, decision := range [][3]string{
		{"Client", "fail", "broken cart"}, {"Client", "pass", ""}, {"QA", "warn", "slowness"},
	} {
		if err := environments.SetSignoff(ctx, release, "qualif", decision[0], decision[1], decision[2], "vcarroy"); err != nil {
			t.Fatalf("sign-off %v: %v", decision, err)
		}
	}
	signoffs, err := environments.EnvironmentSignoffs(ctx, release, "qualif")
	if err != nil || len(signoffs) != 2 {
		t.Fatalf("sign-off = %+v (%v)", signoffs, err)
	}
	if signoffs[0].Role != "Client" || signoffs[0].Status != "pass" || signoffs[0].Detail != "" {
		t.Errorf("the client's opinion = %+v", signoffs[0])
	}
}

// Removing is not refusing; removing an absent opinion is a named error.
func TestRemovingAnOpinion(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)
	release := releaseFor(t, db, "1.8.0-rc1", "1.8.0-rc1")

	if err := environments.SetSignoff(ctx, release, "qualif", "Client", "fail", "error", "vcarroy"); err != nil {
		t.Fatalf("refusal: %v", err)
	}
	if err := environments.DeleteSignoff(ctx, release, "qualif", "Client"); err != nil {
		t.Fatalf("removal: %v", err)
	}
	if got, _ := environments.EnvironmentSignoffs(ctx, release, "qualif"); len(got) != 0 {
		t.Errorf("%d opinions survive the removal", len(got))
	}
	if err := environments.DeleteSignoff(ctx, release, "qualif", "Nobody"); !errors.Is(err, ErrSignoffNotFound) {
		t.Errorf("error %v, expected ErrSignoffNotFound", err)
	}
}

// The reading order is that of the promotion flow, and the opinions leave with the release.
func TestTheSignoffsFollowTheFlowAndLeaveWithTheRelease(t *testing.T) {
	db, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)
	release := releaseFor(t, db, "1.8.0-rc1", "1.8.0-rc1")

	for _, id := range []string{"prod", "qualif", "unstable"} {
		if err := environments.SetSignoff(ctx, release, id, "Client", "pass", "", "vcarroy"); err != nil {
			t.Fatalf("sign-off %s: %v", id, err)
		}
	}
	signoffs, err := environments.ListSignoffs(ctx, release)
	if err != nil || len(signoffs) != 3 || signoffs[0].EnvironmentID != "unstable" || signoffs[2].EnvironmentID != "prod" {
		t.Errorf("sign-off = %+v (%v)", signoffs, err)
	}

	if _, err := db.Pool.Exec(ctx, `delete from releases where id = $1`, release); err != nil {
		t.Fatalf("deleting the release: %v", err)
	}
	var remaining int
	if err := db.Pool.QueryRow(ctx, `select count(*) from release_signoffs`).Scan(&remaining); err != nil || remaining != 0 {
		t.Errorf("%d validation(s) orpheline(s) (%v)", remaining, err)
	}
}

func TestAnUnknownEnvironmentIsDetected(t *testing.T) {
	_, environments := migrated(t)
	ctx := context.Background()
	flow(t, environments)

	if exists, err := environments.SignoffEnvironmentExists(ctx, testProjectID, "qualif"); err != nil || !exists {
		t.Errorf("qualif: %v (%v)", exists, err)
	}
	if exists, err := environments.SignoffEnvironmentExists(ctx, testProjectID, "does-not-exist"); err != nil || exists {
		t.Errorf("unknown environment recognised: %v (%v)", exists, err)
	}
}
