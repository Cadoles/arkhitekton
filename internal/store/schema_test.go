package store

import (
	"context"
	"errors"
	"testing"
)

func TestExpectedSchemaVersionFollowsTheLastMigration(t *testing.T) {
	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("lecture des migrations: %v", err)
	}

	version, err := ExpectedSchemaVersion()
	if err != nil {
		t.Fatalf("version attendue: %v", err)
	}
	if version != int64(len(names)) {
		// Les migrations sont numérotées sans trou depuis 0001 : si un jour ce
		// n'est plus vrai, c'est ce test qui doit être revu, pas la sonde.
		t.Errorf("version attendue = %d, %d migration(s) embarquée(s)", version, len(names))
	}
}

// Le cas qui motive la sonde : la base répond, mais le Job de migration n'a pas
// tourné. Sans distinction, le pod se déclarerait prêt et répondrait 500 sur
// chaque table absente.
func TestCheckSchemaFailsBeforeMigration(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer db.Close()

	if err := db.CheckSchema(ctx); err == nil {
		t.Fatal("un schéma absent doit faire échouer la sonde")
	}
}

func TestCheckSchemaPassesAfterMigration(t *testing.T) {
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

	if err := db.CheckSchema(ctx); err != nil {
		t.Fatalf("sonde en échec après migration complète: %v", err)
	}
}

// Un schéma partiellement appliqué doit être refusé, et le motif doit être
// reconnaissable : c'est ce qui distingue « migrations en retard » d'une panne
// de connexion, deux causes au traitement opposé.
func TestCheckSchemaDetectsPartialMigration(t *testing.T) {
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

	// Simule un binaire plus récent que la base : la dernière migration est
	// marquée non appliquée, sans toucher aux tables.
	if _, err := db.Pool().Exec(ctx,
		"update goose_db_version set is_applied = false where version_id = (select max(version_id) from goose_db_version)"); err != nil {
		t.Fatalf("altération de l'état goose: %v", err)
	}

	err = db.CheckSchema(ctx)
	if !errors.Is(err, ErrSchemaOutdated) {
		t.Fatalf("erreur = %v, attendu ErrSchemaOutdated", err)
	}
}
