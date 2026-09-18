package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// DB enveloppe le pool de connexions PostgreSQL.
type DB struct {
	pool *pgxpool.Pool
}

// Open établit le pool et vérifie qu'une connexion aboutit.
//
// La vérification n'est pas superflue : pgxpool est paresseux, et sans elle un
// démarrage réussirait avec une base injoignable ou un mot de passe erroné. La
// panne se manifesterait à la première requête utilisateur, loin de sa cause.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*DB, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		// Le DSN porte le mot de passe : ne jamais l'inclure dans l'erreur.
		return nil, fmt.Errorf("DSN PostgreSQL invalide: %w", redactError(err, cfg.DSN))
	}

	poolConfig.MaxConns = int32(cfg.MaxConns)
	poolConfig.MaxConnLifetime = cfg.ConnMaxLifetime

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("création du pool: %w", redactError(err, cfg.DSN))
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connexion à PostgreSQL: %w", redactError(err, cfg.DSN))
	}

	return &DB{pool: pool}, nil
}

// Pool expose le pool sous-jacent aux couches qui exécutent des requêtes.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// Ping alimente la sonde /readyz.
func (db *DB) Ping(ctx context.Context) error {
	if db == nil || db.pool == nil {
		return fmt.Errorf("pool non initialisé")
	}
	return db.pool.Ping(ctx)
}

func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}
