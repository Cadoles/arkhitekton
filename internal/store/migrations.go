// Package store carries PostgreSQL access and the schema migrations. The schema is
// described in docs/08-data-model.md.
package store

import (
	"embed"
	"io/fs"
	"sort"
)

// migrationFiles embeds the migrations in the binary: `arkhitekton migrate` must be able to
// run from the image, with no volume and no side file.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations exposes the migrations' file system, for the migration engine wired into the
// implementation.
func Migrations() fs.FS {
	return migrationFiles
}

// MigrationNames lists the embedded migrations, sorted. It serves diagnosis —
// `migrate --list` — and the regression test alongside.
func MigrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
