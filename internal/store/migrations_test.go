package store

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var migrationName = regexp.MustCompile(`^\d{4}_[a-z0-9_]+\.sql$`)

func TestMigrationsAreEmbedded(t *testing.T) {
	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("lecture des migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("aucune migration embarquée: `migrate` ne ferait rien depuis l'image")
	}
}

// Une numérotation dupliquée ferait appliquer les migrations dans un ordre
// dépendant du système de fichiers — reproductible en local, aléatoire ailleurs.
func TestMigrationNumbersAreUniqueAndWellFormed(t *testing.T) {
	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("lecture des migrations: %v", err)
	}

	seen := map[string]string{}
	for _, name := range names {
		if !migrationName.MatchString(name) {
			t.Errorf("%s ne respecte pas le format NNNN_nom.sql", name)
			continue
		}
		prefix := name[:4]
		if previous, exists := seen[prefix]; exists {
			t.Errorf("numéro %s dupliqué: %s et %s", prefix, previous, name)
		}
		seen[prefix] = name
	}
}

// goose distingue les sens par ces annotations. Une migration sans `Down` ne peut
// pas être annulée, ce qui se découvre au pire moment.
func TestEveryMigrationDeclaresBothDirections(t *testing.T) {
	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("lecture des migrations: %v", err)
	}

	for _, name := range names {
		content, err := fs.ReadFile(Migrations(), "migrations/"+name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body := string(content)
		if !strings.Contains(body, "-- +goose Up") {
			t.Errorf("%s: annotation `-- +goose Up` absente", name)
		}
		if !strings.Contains(body, "-- +goose Down") {
			t.Errorf("%s: annotation `-- +goose Down` absente", name)
		}
	}
}
