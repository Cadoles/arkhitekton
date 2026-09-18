// Package storage carries the MSE plugin's tables and their migrations.
//
// Environments, requested promotions, readings and acceptance sign-offs: the core no longer
// reads them, and no longer creates them. The plugin keeps its own migration history, in its own
// version table.
package storage

import "github.com/jackc/pgx/v5/pgxpool"

// Store reads and writes the plugin's tables, on the dashboard's pool.
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func value(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}
