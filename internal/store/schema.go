package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrSchemaOutdated reports a schema behind the binary.
var ErrSchemaOutdated = errors.New("database schema behind the binary")

// gooseVersionTable is the table goose keeps. The name is fixed by the library; querying it
// directly avoids opening a second *sql.DB on every probe call, which MigrationState would
// do.
const gooseVersionTable = "goose_db_version"

// ExpectedSchemaVersion returns the number of the last embedded migration.
//
// Files are named `NNNN_label.sql`; goose keeps the numeric prefix as the version. Deriving
// it here rather than hard-coding it means adding a migration is enough to move the
// expectation.
func ExpectedSchemaVersion() (int64, error) {
	names, err := MigrationNames()
	if err != nil {
		return 0, err
	}
	if len(names) == 0 {
		return 0, errors.New("no embedded migration")
	}

	var highest int64
	for _, name := range names {
		prefix, _, found := strings.Cut(name, "_")
		if !found {
			return 0, fmt.Errorf("migration %q: name with no version prefix", name)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q: unreadable version prefix: %w", name, err)
		}
		if version > highest {
			highest = version
		}
	}
	return highest, nil
}

// SchemaVersion returns the version applied in the database.
func (db *DB) SchemaVersion(ctx context.Context) (int64, error) {
	if db == nil || db.pool == nil {
		return 0, errors.New("pool not initialised")
	}

	// coalesce covers a table that exists but is empty; a missing table surfaces as an error,
	// which is the "migrations never run" case.
	var applied int64
	err := db.pool.QueryRow(ctx,
		`select coalesce(max(version_id), 0) from `+gooseVersionTable+` where is_applied`,
	).Scan(&applied)
	if err != nil {
		return 0, fmt.Errorf("reading the schema version: %w", err)
	}
	return applied, nil
}

// CheckSchema feeds the /readyz probe.
//
// Without it, an api pod started before the migration Job finished would declare itself
// ready — PostgreSQL answers the ping — then answer 500 on every request touching a missing
// table. A readiness probe exists precisely to keep an instance that cannot serve out of the
// Service: a schema lag falls into that category.
//
// A schema ahead is accepted: that is the normal state of an expand/contract deployment,
// where migrations are applied before the new code.
func (db *DB) CheckSchema(ctx context.Context) error {
	expected, err := ExpectedSchemaVersion()
	if err != nil {
		return err
	}

	applied, err := db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if applied < expected {
		return fmt.Errorf("%w: version %d applied, %d expected", ErrSchemaOutdated, applied, expected)
	}
	return nil
}
