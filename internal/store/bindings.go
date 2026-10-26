package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CapabilityBinding is a binding an administrator recorded, kept as the JSON the catalogue reads.
type CapabilityBinding struct {
	Ref       string
	Binding   json.RawMessage
	UpdatedBy string
	UpdatedAt time.Time
}

// CapabilityBindings lists the recorded bindings.
func (db *DB) CapabilityBindings(ctx context.Context) ([]CapabilityBinding, error) {
	rows, err := db.pool.Query(ctx, `select ref, binding, updated_by, updated_at from capability_bindings order by ref`)
	if err != nil {
		return nil, fmt.Errorf("capability bindings: %w", err)
	}
	defer rows.Close()
	var bindings []CapabilityBinding
	for rows.Next() {
		var binding CapabilityBinding
		if err := rows.Scan(&binding.Ref, &binding.Binding, &binding.UpdatedBy, &binding.UpdatedAt); err != nil {
			return nil, fmt.Errorf("capability bindings: %w", err)
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

// SetCapabilityBinding records a binding and moves the counter, in one transaction.
func (db *DB) SetCapabilityBinding(ctx context.Context, ref string, binding json.RawMessage, actor string) error {
	return db.changeCapabilities(ctx, `insert into capability_bindings (ref, binding, updated_by)
		values ($1, $2, $3)
		on conflict (ref) do update set binding = excluded.binding,
		    updated_by = excluded.updated_by, updated_at = now()`, ref, binding, actor)
}

// DeleteCapabilityBinding removes a binding and moves the counter.
func (db *DB) DeleteCapabilityBinding(ctx context.Context, ref string) error {
	return db.changeCapabilities(ctx, `delete from capability_bindings where ref = $1`, ref)
}

// CapabilityVersion is the counter the running dashboard watches.
func (db *DB) CapabilityVersion(ctx context.Context) (int64, error) {
	var version int64
	if err := db.pool.QueryRow(ctx, `select version from capability_state`).Scan(&version); err != nil {
		return 0, fmt.Errorf("capability version: %w", err)
	}
	return version, nil
}

// changeCapabilities applies a change to bindings or installations and moves the counter with it.
func (db *DB) changeCapabilities(ctx context.Context, statement string, args ...any) error {
	return db.changeCapabilitiesWith(ctx, func(transaction pgx.Tx) error {
		_, err := transaction.Exec(ctx, statement, args...)
		return err
	})
}

// changeCapabilitiesWith runs a change of several statements and moves the counter, in one
// transaction: a reload must see the change whole or not at all.
func (db *DB) changeCapabilitiesWith(ctx context.Context, change func(pgx.Tx) error) error {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if err := change(transaction); err != nil {
		return fmt.Errorf("changing capabilities: %w", err)
	}
	if _, err := transaction.Exec(ctx, `update capability_state set version = version + 1`); err != nil {
		return fmt.Errorf("changing capabilities: %w", err)
	}
	return transaction.Commit(ctx)
}
