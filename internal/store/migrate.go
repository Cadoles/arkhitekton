package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // enregistre le pilote "pgx"
	"github.com/pressly/goose/v3"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

const migrationsDir = "migrations"

// MigrationStatus describes a migration and whether it is applied.
type MigrationStatus struct {
	Name    string
	Applied bool
}

// Migrate applies the pending migrations.
//
// goose requires a *sql.DB where the rest of the dashboard uses pgxpool: pgx's stdlib
// bridge is exactly for that. The connection is opened for the duration of the operation
// and then closed — migrations run in an init Job, not in a long-lived process.
func Migrate(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) error {
	db, err := openSQL(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("dialecte goose: %w", err)
	}
	goose.SetBaseFS(migrationFiles)
	// goose's log goes to slog rather than raw stdout, so the Job's output stays as
	// analysable as the rest.
	goose.SetLogger(gooseLogger{logger})

	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("applying the migrations: %w", redactError(err, cfg.DSN))
	}
	return nil
}

// MigrationState lists the migrations and says which are applied. Used by
// `migrate --status`, and to check before deploying that a version of the binary is not
// expecting a newer schema than the one in place.
func MigrationState(ctx context.Context, cfg config.DatabaseConfig) ([]MigrationStatus, error) {
	db, err := openSQL(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		return nil, fmt.Errorf("dialecte goose: %w", err)
	}
	goose.SetBaseFS(migrationFiles)

	migrations, err := goose.CollectMigrations(migrationsDir, 0, goose.MaxVersion)
	if err != nil {
		return nil, fmt.Errorf("collecting the migrations: %w", err)
	}

	applied, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("schema version: %w", redactError(err, cfg.DSN))
	}

	state := make([]MigrationStatus, 0, len(migrations))
	for _, migration := range migrations {
		state = append(state, MigrationStatus{
			Name:    migration.Source,
			Applied: migration.Version <= applied,
		})
	}
	return state, nil
}

// Rollback undoes the last applied migration. Reserved for development: undoing in
// production drops columns, hence data.
func Rollback(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) error {
	db, err := openSQL(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("dialecte goose: %w", err)
	}
	goose.SetBaseFS(migrationFiles)
	goose.SetLogger(gooseLogger{logger})

	if err := goose.DownContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("rolling back the migration: %w", redactError(err, cfg.DSN))
	}
	return nil
}

func openSQL(cfg config.DatabaseConfig) (*sql.DB, error) {
	// The driver is registered under the name "pgx" by the blank import of pgx/stdlib: one
	// PostgreSQL implementation in the binary, the one the application pool already uses.
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("DSN PostgreSQL invalide: %w", redactError(err, cfg.DSN))
	}
	return db, nil
}

// gooseLogger adapts the interface goose expects to slog.
type gooseLogger struct{ logger *slog.Logger }

func (l gooseLogger) Fatalf(format string, v ...any) {
	l.logger.Error(fmt.Sprintf(format, v...))
}

func (l gooseLogger) Printf(format string, v ...any) {
	l.logger.Info(fmt.Sprintf(format, v...))
}

// discardLogger serves the tests, where goose's log adds nothing.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitDelay bounds the wait between two connection attempts.
const waitDelay = 2 * time.Second

// PendingMigrations returns the migrations this binary embeds that the database does not
// yet have.
//
// Empty means the schema is the one this binary expects.
func PendingMigrations(ctx context.Context, cfg config.DatabaseConfig) ([]string, error) {
	state, err := MigrationState(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var pending []string
	for _, migration := range state {
		if !migration.Applied {
			pending = append(pending, migration.Name)
		}
	}
	return pending, nil
}

// WaitForSchema waits for the database to carry this binary's migrations.
//
// Migration is a Job, deliberately — an init container would run once per replica and two
// concurrent `goose up` would tread on each other. The trade-off is that no order is
// guaranteed between that Job and the application pods: they start together.
//
// [WaitForDatabase] is therefore not enough. A worker can connect to a database that is
// reachable but not yet migrated, and its start-up reconciliation then runs against an
// incomplete schema. That was observed: a score of "relation does not exist" errors at
// every deployment, while the Job caught up.
//
// Nothing stayed broken — the following facts were written normally — but a start-up log
// full of expected errors teaches people to ignore errors, and that is how a real
// failure goes unnoticed.
//
// The api and the worker could apply the migrations themselves. That would reopen
// exactly what the Job closed: two api replicas would migrate in parallel. They wait,
// and the Job writes.
func WaitForSchema(
	ctx context.Context, cfg config.DatabaseConfig, timeout time.Duration, logger *slog.Logger,
) error {
	deadline := time.Now().Add(timeout)
	attempt := 0

	for {
		attempt++
		pending, err := PendingMigrations(ctx, cfg)
		switch {
		case err != nil:
			// An impossible read is not a schema that is behind. It retries, then returns the error
			// as it is: it says something else.
			if time.Now().After(deadline) {
				return fmt.Errorf("schema state unreadable after %s: %w",
					timeout, redactError(err, cfg.DSN))
			}
		case len(pending) == 0:
			if attempt > 1 {
				logger.Info("schema up to date", "attempts", attempt)
			}
			return nil
		case time.Now().After(deadline):
			// The message names what is missing: "schema behind" alone would send someone to read
			// the Job's logs without knowing what to look for.
			return fmt.Errorf(
				"schema incomplete after %s, %d migration(s) pending: %s",
				timeout, len(pending), strings.Join(pending, ", "))
		default:
			logger.Info("schema behind, waiting for the migration Job",
				"attempt", attempt, "pending", len(pending), "timeout", waitDelay)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDelay):
		}
	}
}

// WaitForDatabase waits for PostgreSQL to accept connections.
//
// Necessary because migration is an initialisation step: in a fresh deployment its Job
// starts at the same time as the database, and the order between them is not guaranteed.
// Without this wait the Job fails, Kubernetes restarts it, and the second attempt
// succeeds — the migration does go through, but every deployment leaves a pod in `Error`
// behind it.
//
// That residue is not cosmetic: it teaches people to ignore failed pods, and that is how
// a real failure goes unnoticed.
//
// The wait is reserved to this role. The api and the worker must fail fast instead: they
// run permanently, Kubernetes restarts them, and a process waiting indefinitely for an
// absent database would declare itself alive without being so.
func WaitForDatabase(ctx context.Context, cfg config.DatabaseConfig, timeout time.Duration, logger *slog.Logger) error {
	deadline := time.Now().Add(timeout)
	attempt := 0

	for {
		attempt++
		db, err := Open(ctx, cfg)
		if err == nil {
			db.Close()
			if attempt > 1 {
				logger.Info("database available", "attempts", attempt)
			}
			return nil
		}

		if time.Now().After(deadline) {
			// The message carries the number of attempts and the duration: "connection refused"
			// alone does not say whether the database is absent or merely slow to start.
			return fmt.Errorf(
				"database unreachable after %d attempt(s) in %s: %w",
				attempt, timeout, redactError(err, cfg.DSN))
		}

		logger.Info("database unavailable, retrying",
			"attempt", attempt, "timeout", waitDelay)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDelay):
		}
	}
}
