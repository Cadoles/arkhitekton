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

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// VersionTable est la table de versions du plugin.
//
// Distincte de celle du noyau : les deux historiques avancent chacun à son
// rythme, et un numéro du plugin ne doit jamais se lire comme une migration du
// noyau déjà appliquée.
const VersionTable = "goose_mse_db_version"

// withProvider ouvre une connexion le temps d'une opération de migration.
//
// Un provider et non l'API globale de goose : le noyau s'en sert pour ses propres
// migrations, et changer la table ou le système de fichiers globaux en cours de
// route ferait migrer l'un avec l'historique de l'autre.
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
		return fmt.Errorf("migrations du plugin MSE: %w", err)
	}
	return run(provider)
}

// Migrate applique les migrations du plugin.
//
// À lancer **après** celles du noyau : ses tables référencent `releases` et
// `jobs`.
func Migrate(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) error {
	return withProvider(cfg, func(provider *goose.Provider) error {
		results, err := provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("application des migrations du plugin MSE: %w", store.RedactError(err, cfg.DSN))
		}
		for _, result := range results {
			logger.Info("migration du plugin MSE", "file", result.Source.Path, "duration", result.Duration)
		}
		return nil
	})
}

// Rollback annule la dernière migration du plugin. Réservé aux tests.
func Rollback(ctx context.Context, cfg config.DatabaseConfig) error {
	return withProvider(cfg, func(provider *goose.Provider) error {
		if _, err := provider.Down(ctx); err != nil {
			return fmt.Errorf("annulation d'une migration du plugin MSE: %w", store.RedactError(err, cfg.DSN))
		}
		return nil
	})
}

// PendingMigrations rend les migrations du plugin que la base n'a pas encore.
func PendingMigrations(ctx context.Context, cfg config.DatabaseConfig) ([]string, error) {
	var pending []string
	err := withProvider(cfg, func(provider *goose.Provider) error {
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("état des migrations du plugin MSE: %w", store.RedactError(err, cfg.DSN))
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

// WaitForSchema attend que la base porte les migrations du plugin.
//
// Même raison que [store.WaitForSchema] : le Job de migration et les pods
// démarrent ensemble, et le worker synchronise les environnements dès son
// démarrage. Le plugin migre après le noyau, donc attendre le noyau seul
// laisserait une fenêtre où `environments` n'existe pas encore.
func WaitForSchema(
	ctx context.Context, cfg config.DatabaseConfig, timeout time.Duration, logger *slog.Logger,
) error {
	deadline := time.Now().Add(timeout)
	for attempt := 1; ; attempt++ {
		pending, err := PendingMigrations(ctx, cfg)
		if err == nil && len(pending) == 0 {
			if attempt > 1 {
				logger.Info("schéma du plugin MSE à jour", "tentatives", attempt)
			}
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("état du schéma du plugin MSE illisible après %s: %w", timeout, err)
			}
			return fmt.Errorf("%w: plugin MSE, %d migration(s) en attente après %s",
				store.ErrSchemaOutdated, len(pending), timeout)
		}
		logger.Info("schéma du plugin MSE en attente", "tentative", attempt, "en_attente", len(pending))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ExpectedVersion rend le numéro de la dernière migration embarquée.
func ExpectedVersion() (int64, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return 0, err
	}
	var highest int64
	for _, entry := range entries {
		prefix, _, found := strings.Cut(entry.Name(), "_")
		if !found {
			return 0, fmt.Errorf("migration %q: nom sans préfixe de version", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q: préfixe illisible: %w", entry.Name(), err)
		}
		highest = max(highest, version)
	}
	if highest == 0 {
		return 0, errors.New("aucune migration du plugin MSE embarquée")
	}
	return highest, nil
}

// CheckSchema rend la sonde de préparation du schéma du plugin.
//
// Par le pool plutôt que par goose : la sonde tourne toutes les quelques
// secondes, et ouvrir une connexion à chaque passage ne se justifie pas.
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
			return fmt.Errorf("lecture de la version du schéma du plugin MSE: %w", err)
		}
		if applied < expected {
			return fmt.Errorf("%w: plugin MSE, version %d appliquée, %d attendue",
				store.ErrSchemaOutdated, applied, expected)
		}
		return nil
	}
}
