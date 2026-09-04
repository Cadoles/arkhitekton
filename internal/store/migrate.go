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

// waitDelay borne l'attente entre deux tentatives de connexion.
const waitDelay = 2 * time.Second

// PendingMigrations rend les migrations que ce binaire embarque et que la base
// n'a pas encore.
//
// Vide, le schéma est celui que ce binaire attend.
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

// WaitForSchema attend que la base porte les migrations de ce binaire.
//
// # Pourquoi une attente, et pas seulement la connexion
//
// La migration est un **Job**, délibérément — un initContainer s'exécuterait une
// fois par réplique et deux `goose up` concurrents se marcheraient dessus (voir
// `deploy/base/job-migrate.yaml`). La contrepartie est qu'aucun ordre n'est
// garanti entre ce Job et les pods applicatifs : ils démarrent ensemble.
//
// [WaitForDatabase] ne suffit donc pas. Un worker peut se connecter à une base
// **joignable mais pas encore migrée**, et sa réconciliation de démarrage
// s'exécute alors contre un schéma incomplet. C'est ce qui a été constaté :
// une vingtaine de `relation "plan_steps" does not exist` à chaque déploiement,
// le temps que le Job passe.
//
// Rien n'en restait cassé — les faits suivants s'écrivaient normalement — mais
// un journal de démarrage rempli d'erreurs attendues apprend à ignorer les
// erreurs, et c'est ainsi qu'une vraie panne passe inaperçue.
//
// # Attendre plutôt que migrer soi-même
//
// L'`api` et le `worker` pourraient appliquer les migrations eux-mêmes. Ce serait
// rouvrir précisément ce que le Job a fermé : deux répliques d'`api` migreraient
// en parallèle. Ils attendent donc, et c'est le Job qui écrit.
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
			// Une lecture impossible n'est pas un schéma en retard. On réessaie,
			// puis on rend l'erreur telle quelle : elle dit autre chose.
			if time.Now().After(deadline) {
				return fmt.Errorf("état du schéma illisible après %s: %w",
					timeout, redactError(err, cfg.DSN))
			}
		case len(pending) == 0:
			if attempt > 1 {
				logger.Info("schéma à jour", "tentatives", attempt)
			}
			return nil
		case time.Now().After(deadline):
			// Le message nomme ce qui manque : « schéma en retard » seul
			// enverrait lire les journaux du Job sans savoir quoi y chercher.
			return fmt.Errorf(
				"schéma incomplet après %s, %d migration(s) en attente: %s",
				timeout, len(pending), strings.Join(pending, ", "))
		default:
			logger.Info("schéma en retard, attente du Job de migration",
				"tentative", attempt, "en_attente", len(pending), "délai", waitDelay)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDelay):
		}
	}
}

// WaitForDatabase attend que PostgreSQL accepte des connexions.
//
// Nécessaire parce que la migration est une étape d'**initialisation** : dans un
// déploiement neuf, son Job démarre en même temps que la base, et l'ordre entre
// les deux n'est pas garanti. Sans cette attente, le Job échoue, Kubernetes le
// relance, et le second essai réussit — la migration finit par passer, mais
// chaque déploiement laisse derrière lui un pod en `Error`.
//
// Ce résidu n'est pas cosmétique : il apprend à ignorer les pods en erreur, et
// c'est ainsi qu'une vraie panne passe inaperçue.
//
// L'attente est réservée à ce rôle. L'`api` et le `worker` doivent au contraire
// échouer vite : ils tournent en permanence, Kubernetes les redémarre, et un
// processus qui attend indéfiniment une base absente se déclarerait vivant sans
// l'être.
func WaitForDatabase(ctx context.Context, cfg config.DatabaseConfig, timeout time.Duration, logger *slog.Logger) error {
	deadline := time.Now().Add(timeout)
	attempt := 0

	for {
		attempt++
		db, err := Open(ctx, cfg)
		if err == nil {
			db.Close()
			if attempt > 1 {
				logger.Info("base disponible", "tentatives", attempt)
			}
			return nil
		}

		if time.Now().After(deadline) {
			// Le message porte le nombre de tentatives et la durée : « connexion
			// refusée » seul ne dit pas si la base est absente ou simplement lente
			// à démarrer.
			return fmt.Errorf(
				"base injoignable après %d tentative(s) en %s: %w",
				attempt, timeout, redactError(err, cfg.DSN))
		}

		logger.Info("base indisponible, nouvelle tentative",
			"tentative", attempt, "délai", waitDelay)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDelay):
		}
	}
}
