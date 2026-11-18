package store

import (
	"context"
	"fmt"
)

// ScoreWeights returns the weights set by hand, by kind. A kind absent weighs its nature's default.
func (db *DB) ScoreWeights(ctx context.Context) (map[string]float64, error) {
	rows, err := db.pool.Query(ctx, `select kind, weight from score_weights`)
	if err != nil {
		return nil, fmt.Errorf("reading the score weights: %w", err)
	}
	defer rows.Close()

	weights := map[string]float64{}
	for rows.Next() {
		var kind string
		var weight float64
		if err := rows.Scan(&kind, &weight); err != nil {
			return nil, fmt.Errorf("reading the score weights: %w", err)
		}
		weights[kind] = weight
	}
	return weights, rows.Err()
}

// SetScoreWeight sets a kind's weight in the score; zero leaves it out.
func (db *DB) SetScoreWeight(ctx context.Context, kind string, weight float64, by string) error {
	_, err := db.pool.Exec(ctx,
		`insert into score_weights (kind, weight, updated_by)
		 values ($1, $2, $3)
		 on conflict (kind) do update
		     set weight = excluded.weight,
		         updated_by = excluded.updated_by,
		         updated_at = now()`, kind, weight, by)
	if err != nil {
		return fmt.Errorf("setting the weight of %q: %w", kind, err)
	}
	return nil
}

// ClearScoreWeight returns a kind to its nature's weight.
func (db *DB) ClearScoreWeight(ctx context.Context, kind string) error {
	if _, err := db.pool.Exec(ctx, `delete from score_weights where kind = $1`, kind); err != nil {
		return fmt.Errorf("clearing the weight of %q: %w", kind, err)
	}
	return nil
}
