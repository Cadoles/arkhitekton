// Package storage porte les tables du plugin MSE et leurs migrations.
//
// Environnements, promotions demandées, relevés et validations de recette : le
// noyau ne les lit plus, et ne les crée plus (doc 16 §16.17). Le plugin tient son
// propre historique de migrations, dans sa propre table de versions.
package storage

import "github.com/jackc/pgx/v5/pgxpool"

// Store lit et écrit les tables du plugin, sur le pool du dashboard.
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
