package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // enregistre le pilote "pgx"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// VersionTable is the plugin's version table.
//
// Distinct from the core's: the two histories advance each at its own pace, and a number of the
// plugin must never read as a core migration already applied.
const VersionTable = "goose_mse_db_version"

// withProvider opens a connection for the duration of one migration operation.
//
// A provider and not goose's global API: the core uses it for its own migrations, and changing
// the global table or file system along the way would migrate one with the other's history.
func withProvider(cfg config.DatabaseConfig, run func(*goose.Provider) error) error {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return fmt.Errorf("DSN PostgreSQL invalide: %w", store.RedactError(err, cfg.DSN))
	}
	defer db.Close()

	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	versions, err := database.NewStore(database.DialectPostgres, VersionTable)
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectCustom, db, files,
		goose.WithStore(versions), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("MSE plugin migrations: %w", err)
	}
	return run(provider)
}

// Migrate applies the plugin's migrations.
//
// To be run **after** the core's: its tables reference `releases` and `jobs`.
func Migrate(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) error {
	return withProvider(cfg, func(provider *goose.Provider) error {
		results, err := provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("applying the MSE plugin's migrations: %w", store.RedactError(err, cfg.DSN))
		}
		for _, result := range results {
			logger.Info("MSE plugin migration", "file", result.Source.Path, "duration", result.Duration)
		}
		return nil
	})
}

// Rollback undoes the plugin's last migration. Reserved for tests.
func Rollback(ctx context.Context, cfg config.DatabaseConfig) error {
	return withProvider(cfg, func(provider *goose.Provider) error {
		if _, err := provider.Down(ctx); err != nil {
			return fmt.Errorf("rolling back an MSE plugin migration: %w", store.RedactError(err, cfg.DSN))
		}
		return nil
	})
}

// PendingMigrations returns the plugin's migrations the database does not have yet.
func PendingMigrations(ctx context.Context, cfg config.DatabaseConfig) ([]string, error) {
	var pending []string
	err := withProvider(cfg, func(provider *goose.Provider) error {
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("state of the MSE plugin's migrations: %w", store.RedactError(err, cfg.DSN))
		}
		for _, status := range statuses {
			if status.State == goose.StatePending {
				pending = append(pending, status.Source.Path)
			}
		}
		return nil
	})
	return pending, err
}

// WaitForSchema waits until the database carries the plugin's migrations.
//
// The same reason as [store.WaitForSchema]: the migration Job and the pods start together, and
// the worker synchronises the environments as soon as it starts. The plugin migrates after the
// core, so waiting for the core alone would leave a window in which `environments` does not
// exist yet.
func WaitForSchema(
	ctx context.Context, cfg config.DatabaseConfig, timeout time.Duration, logger *slog.Logger,
) error {
	deadline := time.Now().Add(timeout)
	for attempt := 1; ; attempt++ {
		pending, err := PendingMigrations(ctx, cfg)
		if err == nil && len(pending) == 0 {
			if attempt > 1 {
				logger.Info("MSE plugin schema up to date", "tentatives", attempt)
			}
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("state of the MSE plugin's schema unreadable after %s: %w", timeout, err)
			}
			return fmt.Errorf("%w: MSE plugin, %d migration(s) pending after %s",
				store.ErrSchemaOutdated, len(pending), timeout)
		}
		logger.Info("MSE plugin schema pending", "tentative", attempt, "en_attente", len(pending))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ExpectedVersion returns the number of the last embedded migration.
func ExpectedVersion() (int64, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return 0, err
	}
	var highest int64
	for _, entry := range entries {
		prefix, _, found := strings.Cut(entry.Name(), "_")
		if !found {
			return 0, fmt.Errorf("migration %q: name with no version prefix", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q: unreadable prefix: %w", entry.Name(), err)
		}
		highest = max(highest, version)
	}
	if highest == 0 {
		return 0, errors.New("no MSE plugin migration embedded")
	}
	return highest, nil
}

// CheckSchema returns the readiness probe of the plugin's schema.
//
// Through the pool rather than through goose: the probe runs every few seconds, and opening a
// connection at each pass is not justified.
func CheckSchema(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error {
		expected, err := ExpectedVersion()
		if err != nil {
			return err
		}
		var applied int64
		if err := pool.QueryRow(ctx,
			`select coalesce(max(version_id), 0) from `+VersionTable+` where is_applied`,
		).Scan(&applied); err != nil {
			return fmt.Errorf("reading the MSE plugin's schema version: %w", err)
		}
		if applied < expected {
			return fmt.Errorf("%w: MSE plugin, version %d applied, %d expected",
				store.ErrSchemaOutdated, applied, expected)
		}
		return nil
	}
}
