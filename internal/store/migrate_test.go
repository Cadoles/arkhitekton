package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
)

// Ces tests exigent un PostgreSQL réel : appliquer un schéma ne se simule pas
// utilement, et c'est justement l'exécution du SQL que l'on veut vérifier.
//
// Ils sont pilotés par TEST_DATABASE_URL plutôt que par testcontainers : la
// dépendance en moins vaut la mise en place manuelle, et la CI fournit déjà un
// service PostgreSQL. Sans la variable, les tests sont ignorés — visiblement,
// pas silencieusement.
func testDB(t *testing.T) config.DatabaseConfig {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL non défini: test d'intégration ignoré")
	}
	return config.DatabaseConfig{DSN: dsn, MaxConns: 4, ConnMaxLifetime: time.Minute}
}

// resetSchema ramène la base à zéro entre deux tests : goose mémorise la version
// appliquée, et un test qui hérite de l'état du précédent ne prouve rien.
func resetSchema(t *testing.T, cfg config.DatabaseConfig) {
	t.Helper()

	ctx := context.Background()
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer db.Close()

	if _, err := db.Pool().Exec(ctx, "drop schema public cascade; create schema public;"); err != nil {
		t.Fatalf("réinitialisation du schéma: %v", err)
	}
}

func TestOpenRejectsUnreachableDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// pgxpool est paresseux : sans le Ping de Open, cette configuration
	// produirait un pool utilisable en apparence et une panne à la première
	// requête, loin de sa cause.
	_, err := Open(ctx, config.DatabaseConfig{
		DSN:      "postgres://nobody:nothing@127.0.0.1:1/absent",
		MaxConns: 1,
	})
	if err == nil {
		t.Fatal("une base injoignable doit faire échouer Open")
	}
}

func TestMigrateAppliesEverySchemaFile(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	state, err := MigrationState(ctx, cfg)
	if err != nil {
		t.Fatalf("état des migrations: %v", err)
	}
	if len(state) == 0 {
		t.Fatal("aucune migration collectée")
	}
	for _, migration := range state {
		if !migration.Applied {
			t.Errorf("%s non appliquée", migration.Name)
		}
	}
}

// Les tables et contraintes qui portent des décisions dures doivent exister
// après migration : les vérifier ici évite qu'une erreur de frappe dans le SQL
// ne se découvre qu'au moment d'écrire la requête qui en dépend.
func TestMigrateCreatesKeyConstraints(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer db.Close()

	for _, table := range []string{
		"environments", "versions", "issues", "releases", "release_images",
		"image_promotions", "jobs", "runs", "task_runs", "deployments",
		"environment_observations", "test_results", "test_cases", "artifacts",
		"user_roles", "sessions", "audit_log", "sync_state",
	} {
		var exists bool
		err := db.Pool().QueryRow(ctx,
			"select exists (select 1 from information_schema.tables where table_name = $1)",
			table).Scan(&exists)
		if err != nil {
			t.Fatalf("interrogation du catalogue: %v", err)
		}
		if !exists {
			t.Errorf("table %s absente après migration", table)
		}
	}

	// Un environnement ne peut avoir qu'un déploiement courant. La contrainte
	// est dans le schéma pour que le code applicatif ne puisse pas la contourner.
	var indexExists bool
	if err := db.Pool().QueryRow(ctx,
		"select exists (select 1 from pg_indexes where indexname = 'deployments_current_idx')").
		Scan(&indexExists); err != nil {
		t.Fatalf("interrogation des index: %v", err)
	}
	if !indexExists {
		t.Error("index unique partiel deployments_current_idx absent")
	}
}

// Une migration qui ne sait pas s'annuler se découvre au pire moment. Le test
// applique puis annule tout, ce qui exerce chaque bloc `-- +goose Down`.
func TestMigrationsRollBackCleanly(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("lecture des migrations: %v", err)
	}
	for i := range names {
		if err := Rollback(ctx, cfg, discardLogger()); err != nil {
			t.Fatalf("annulation %d/%d: %v", i+1, len(names), err)
		}
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer db.Close()

	var remaining int
	if err := db.Pool().QueryRow(ctx,
		"select count(*) from information_schema.tables where table_schema = 'public' and table_name <> 'goose_db_version'").
		Scan(&remaining); err != nil {
		t.Fatalf("interrogation du catalogue: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d table(s) subsistent après annulation complète", remaining)
	}
}

// Le DSN porte le mot de passe et pgx le cite volontiers dans ses erreurs, qui
// finissent en logs, en tickets, en captures d'écran.
func TestRedactErrorHidesPasswordKeepsMessage(t *testing.T) {
	dsn := "postgres://mse:s3cr3t@db.example.fr:5432/mse"
	err := redactError(
		errorString("échec de connexion avec postgres://mse:s3cr3t@db.example.fr:5432/mse"), dsn)

	if got := err.Error(); strings.Contains(got, "s3cr3t") {
		t.Errorf("le mot de passe fuit: %s", got)
	}
	if got := err.Error(); !strings.Contains(got, "db.example.fr") {
		t.Errorf("le message a perdu l'hôte, il n'est plus diagnosticable: %s", got)
	}
}

func TestRedactErrorPassesThroughWhenNoPassword(t *testing.T) {
	original := errorString("connexion refusée")
	if got := redactError(original, "postgres://mse@db:5432/mse"); got != original {
		t.Errorf("erreur inutilement réécrite: %v", got)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
