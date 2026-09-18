// Package store porte l'accès à PostgreSQL et les migrations de schéma.
package store

import (
	"embed"
	"io/fs"
	"sort"
)

// migrationFiles embarque les migrations dans le binaire : `arkhitekton migrate`
// doit pouvoir s'exécuter depuis l'image, sans volume ni fichier annexe.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations expose le système de fichiers des migrations, destiné au moteur de
// migration (goose) branché à l'implémentation.
func Migrations() fs.FS {
	return migrationFiles
}

// MigrationNames liste les migrations embarquées, triées. Sert au diagnostic —
// `migrate --list` — et au test de non-régression ci-contre.
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
