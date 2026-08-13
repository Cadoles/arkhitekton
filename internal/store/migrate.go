package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib" // enregistre le pilote "pgx"
	"github.com/pressly/goose/v3"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
)

const migrationsDir = "migrations"

// MigrationStatus décrit une migration et son état d'application.
type MigrationStatus struct {
	Name    string
	Applied bool
}

// Migrate applique les migrations en attente.
//
// goose exige un *sql.DB là où le reste du dashboard utilise pgxpool : la
// passerelle stdlib de pgx sert exactement à cela. La connexion est ouverte pour
// la durée de l'opération puis refermée — les migrations tournent dans un Job
// d'initialisation, pas dans un processus de longue durée.
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
	// Le journal de goose part vers slog plutôt que stdout brut, pour que la
	// sortie du Job reste analysable comme le reste.
	goose.SetLogger(gooseLogger{logger})

	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("application des migrations: %w", redactError(err, cfg.DSN))
	}
	return nil
}

// MigrationState liste les migrations et indique lesquelles sont appliquées.
// Utilisé par `migrate --status`, et pour vérifier avant de déployer qu'une
// version du binaire n'attend pas un schéma plus récent que celui en place.
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
		return nil, fmt.Errorf("collecte des migrations: %w", err)
	}

	applied, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("version du schéma: %w", redactError(err, cfg.DSN))
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

// Rollback annule la dernière migration appliquée. Réservé au développement :
// annuler en production supprime des colonnes, donc des données.
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
		return fmt.Errorf("annulation de la migration: %w", redactError(err, cfg.DSN))
	}
	return nil
}

func openSQL(cfg config.DatabaseConfig) (*sql.DB, error) {
	// Le pilote est enregistré sous le nom "pgx" par l'import en blanc de
	// pgx/stdlib : une seule implémentation PostgreSQL dans le binaire, celle
	// déjà utilisée par le pool applicatif.
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("DSN PostgreSQL invalide: %w", redactError(err, cfg.DSN))
	}
	return db, nil
}

// gooseLogger adapte l'interface attendue par goose à slog.
type gooseLogger struct{ logger *slog.Logger }

func (l gooseLogger) Fatalf(format string, v ...any) {
	l.logger.Error(fmt.Sprintf(format, v...))
}

func (l gooseLogger) Printf(format string, v ...any) {
	l.logger.Info(fmt.Sprintf(format, v...))
}

// discardLogger sert aux tests, où le journal de goose n'apporte rien.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
