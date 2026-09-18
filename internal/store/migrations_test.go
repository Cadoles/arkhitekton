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
		t.Fatalf("reading the migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migration: `migrate` would do nothing from the image")
	}
}

// Duplicate numbering would apply the migrations in an order depending on the file system —
// reproducible locally, random elsewhere.
func TestMigrationNumbersAreUniqueAndWellFormed(t *testing.T) {
	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("reading the migrations: %v", err)
	}

	seen := map[string]string{}
	for _, name := range names {
		if !migrationName.MatchString(name) {
			t.Errorf("%s does not follow the NNNN_name.sql format", name)
			continue
		}
		prefix := name[:4]
		if previous, exists := seen[prefix]; exists {
			t.Errorf("number %s duplicated: %s and %s", prefix, previous, name)
		}
		seen[prefix] = name
	}
}

// goose tells the directions apart by these annotations. A migration with no `Down` cannot
// be undone, which is discovered at the worst moment.
func TestEveryMigrationDeclaresBothDirections(t *testing.T) {
	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("reading the migrations: %v", err)
	}

	for _, name := range names {
		content, err := fs.ReadFile(Migrations(), "migrations/"+name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body := string(content)
		if !strings.Contains(body, "-- +goose Up") {
			t.Errorf("%s: missing `-- +goose Up` annotation", name)
		}
		if !strings.Contains(body, "-- +goose Down") {
			t.Errorf("%s: missing `-- +goose Down` annotation", name)
		}
	}
}
