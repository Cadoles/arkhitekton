package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// MigrationSet is a plugin's migrations, kept in a version table of its own.
//
// Distinct from the core's history: the two advance each at its own pace, and a number of the
// plugin must never read as a core migration already applied. A set is applied **after** the
// core's, its tables referring to the core's.
type MigrationSet struct {
	// Name says whose migrations they are, in logs and errors: "MSE plugin".
	Name string
	// Table is the set's version table.
	Table string
	// Files holds the `.sql` migrations at its root, named `<version>_<description>.sql`.
	Files fs.FS
}

// tableName keeps a version table to what can be concatenated into a query.
var tableName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// withProvider opens a connection for the duration of one migration operation.
//
// A provider and not goose's global API: the core uses it for its own migrations, and changing
// the global table or file system along the way would migrate one with the other's history.
func (s MigrationSet) withProvider(cfg config.DatabaseConfig, run func(*goose.Provider) error) error {
	if !tableName.MatchString(s.Table) || s.Table == gooseVersionTable {
		return fmt.Errorf("%s: version table %q, a name of its own", s.Name, s.Table)
	}
	db, err := openSQL(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	versions, err := database.NewStore(database.DialectPostgres, s.Table)
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectCustom, db, s.Files,
		goose.WithStore(versions), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("%s migrations: %w", s.Name, err)
	}
	return run(provider)
}

// Migrate applies the set's migrations.
func (s MigrationSet) Migrate(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) error {
	return s.withProvider(cfg, func(provider *goose.Provider) error {
		results, err := provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("applying the %s migrations: %w", s.Name, RedactError(err, cfg.DSN))
		}
		for _, result := range results {
			logger.Info("migration", "set", s.Name, "file", result.Source.Path, "duration", result.Duration)
		}
		return nil
	})
}

// UpTo applies the set's migrations up to a version. Reserved for tests that build the state of
// an older database.
func (s MigrationSet) UpTo(ctx context.Context, cfg config.DatabaseConfig, version int64) error {
	return s.withProvider(cfg, func(provider *goose.Provider) error {
		if _, err := provider.UpTo(ctx, version); err != nil {
			return fmt.Errorf("applying the %s migrations up to %d: %w", s.Name, version, RedactError(err, cfg.DSN))
		}
		return nil
	})
}

// DownTo undoes the set's migrations down to a version. Reserved for tests.
func (s MigrationSet) DownTo(ctx context.Context, cfg config.DatabaseConfig, version int64) error {
	return s.withProvider(cfg, func(provider *goose.Provider) error {
		if _, err := provider.DownTo(ctx, version); err != nil {
			return fmt.Errorf("rolling back the %s migrations to %d: %w", s.Name, version, RedactError(err, cfg.DSN))
		}
		return nil
	})
}

// Rollback undoes the set's last migration. Reserved for tests.
func (s MigrationSet) Rollback(ctx context.Context, cfg config.DatabaseConfig) error {
	return s.withProvider(cfg, func(provider *goose.Provider) error {
		if _, err := provider.Down(ctx); err != nil {
			return fmt.Errorf("rolling back a %s migration: %w", s.Name, RedactError(err, cfg.DSN))
		}
		return nil
	})
}

// Pending returns the set's migrations the database does not have yet.
func (s MigrationSet) Pending(ctx context.Context, cfg config.DatabaseConfig) ([]string, error) {
	var pending []string
	err := s.withProvider(cfg, func(provider *goose.Provider) error {
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("state of the %s migrations: %w", s.Name, RedactError(err, cfg.DSN))
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

// WaitForSchema waits until the database carries the set's migrations.
//
// The same reason as [WaitForSchema]: the migration Job and the pods start together. The set
// migrates after the core, so waiting for the core alone would leave a window in which its tables
// do not exist yet.
func (s MigrationSet) WaitForSchema(
	ctx context.Context, cfg config.DatabaseConfig, timeout time.Duration, logger *slog.Logger,
) error {
	deadline := time.Now().Add(timeout)
	for attempt := 1; ; attempt++ {
		pending, err := s.Pending(ctx, cfg)
		if err == nil && len(pending) == 0 {
			if attempt > 1 {
				logger.Info("schema up to date", "set", s.Name, "attempts", attempt)
			}
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("state of the %s schema unreadable after %s: %w", s.Name, timeout, err)
			}
			return fmt.Errorf("%w: %s, %d migration(s) pending after %s",
				ErrSchemaOutdated, s.Name, len(pending), timeout)
		}
		logger.Info("schema pending", "set", s.Name, "attempt", attempt, "pending", len(pending))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDelay):
		}
	}
}

// ExpectedVersion returns the number of the set's last migration.
func (s MigrationSet) ExpectedVersion() (int64, error) {
	entries, err := fs.ReadDir(s.Files, ".")
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
		return 0, fmt.Errorf("%s: %w", s.Name, errors.New("no migration"))
	}
	return highest, nil
}

// CheckSchema returns the readiness probe of the set's schema.
//
// Through the pool rather than through goose: the probe runs every few seconds, and opening a
// connection at each pass is not justified.
func (s MigrationSet) CheckSchema(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error {
		if !tableName.MatchString(s.Table) {
			return fmt.Errorf("%s: version table %q", s.Name, s.Table)
		}
		expected, err := s.ExpectedVersion()
		if err != nil {
			return err
		}
		var applied int64
		if err := pool.QueryRow(ctx,
			`select coalesce(max(version_id), 0) from `+s.Table+` where is_applied`,
		).Scan(&applied); err != nil {
			return fmt.Errorf("reading the %s schema version: %w", s.Name, err)
		}
		if applied < expected {
			return fmt.Errorf("%w: %s, version %d applied, %d expected",
				ErrSchemaOutdated, s.Name, applied, expected)
		}
		return nil
	}
}
