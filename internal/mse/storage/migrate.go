package storage

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// VersionTable is the plugin's version table.
const VersionTable = "goose_mse_db_version"

// Migrations are the plugin's migrations, applied **after** the core's: its tables reference
// `releases` and `jobs`.
var Migrations = host.MigrationSet{Name: "MSE plugin", Table: VersionTable, Files: migrationsDir()}

func migrationsDir() fs.FS {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return files
}

// Migrate applies the plugin's migrations.
func Migrate(ctx context.Context, cfg host.DatabaseConfig, logger *slog.Logger) error {
	return Migrations.Migrate(ctx, cfg, logger)
}

// Rollback undoes the plugin's last migration. Reserved for tests.
func Rollback(ctx context.Context, cfg host.DatabaseConfig) error {
	return Migrations.Rollback(ctx, cfg)
}

// PendingMigrations returns the plugin's migrations the database does not have yet.
func PendingMigrations(ctx context.Context, cfg host.DatabaseConfig) ([]string, error) {
	return Migrations.Pending(ctx, cfg)
}

// WaitForSchema waits until the database carries the plugin's migrations: the worker synchronises
// the environments as soon as it starts.
func WaitForSchema(ctx context.Context, cfg host.DatabaseConfig, timeout time.Duration, logger *slog.Logger) error {
	return Migrations.WaitForSchema(ctx, cfg, timeout, logger)
}

// ExpectedVersion returns the number of the last embedded migration.
func ExpectedVersion() (int64, error) { return Migrations.ExpectedVersion() }

// CheckSchema returns the readiness probe of the plugin's schema.
func CheckSchema(pool *pgxpool.Pool) func(context.Context) error { return Migrations.CheckSchema(pool) }
