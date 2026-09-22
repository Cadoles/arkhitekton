package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// DB wraps the PostgreSQL connection pool.
type DB struct {
	pool *pgxpool.Pool
}

// Open establishes the pool and checks that a connection succeeds.
//
// The check is not superfluous: pgxpool is lazy, and without it a start-up would succeed
// with an unreachable database or a wrong password. The failure would show at the first
// user request, far from its cause.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*DB, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		// The DSN carries the password: never include it in the error.
		return nil, fmt.Errorf("DSN PostgreSQL invalide: %w", redactError(err, cfg.DSN))
	}

	poolConfig.MaxConns = int32(cfg.MaxConns)
	poolConfig.MaxConnLifetime = cfg.ConnMaxLifetime

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("creating the pool: %w", redactError(err, cfg.DSN))
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to PostgreSQL: %w", redactError(err, cfg.DSN))
	}

	return &DB{pool: pool}, nil
}

// Pool exposes the underlying pool to the layers that run queries.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// Ping feeds the /readyz probe.
func (db *DB) Ping(ctx context.Context) error {
	if db == nil || db.pool == nil {
		return fmt.Errorf("pool not initialised")
	}
	return db.pool.Ping(ctx)
}

func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}
