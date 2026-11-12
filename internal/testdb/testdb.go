// Package testdb gives each integration test a database of its own, copied from a template
// migrated once per test binary.
//
// Replaying every migration for every test made the store's tests spend most of their time
// migrating. A copy by `CREATE DATABASE … TEMPLATE` is several times faster and keeps what a reset
// gave: a fresh schema, sequences starting at one.
//
// The account behind TEST_DATABASE_URL must be allowed to create databases.
package testdb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// Migrate brings an empty database to the schema the tests expect.
type Migrate func(context.Context, config.DatabaseConfig) error

const prefix = "arkhitekton_test_"

var (
	once     sync.Once
	template string
	setupErr error
	copies   atomic.Int64
)

// Fresh returns a migrated database no other test uses, dropped when the test ends, or skips the
// test without TEST_DATABASE_URL. The first call migrates the template with migrate.
func Fresh(t testing.TB, migrate Migrate) config.DatabaseConfig {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set: integration test skipped")
	}
	ctx := context.Background()

	once.Do(func() { template, setupErr = prepareTemplate(ctx, dsn, migrate) })
	if setupErr != nil {
		t.Fatalf("the template database: %v", setupErr)
	}

	name := fmt.Sprintf("%s%d_%d", prefix, os.Getpid(), copies.Add(1))
	if err := exec(ctx, dsn, "create database "+name+" template "+template); err != nil {
		t.Fatalf("copying the template: %v", err)
	}
	t.Cleanup(func() {
		if err := exec(context.Background(), dsn, "drop database if exists "+name+" with (force)"); err != nil {
			t.Errorf("dropping %s: %v", name, err)
		}
	})

	cfg, err := databaseConfig(dsn, name)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// prepareTemplate creates this binary's template, after dropping what crashed binaries left.
func prepareTemplate(ctx context.Context, dsn string, migrate Migrate) (string, error) {
	if err := dropLeftovers(ctx, dsn); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s%d_template", prefix, os.Getpid())
	if err := exec(ctx, dsn, "create database "+name); err != nil {
		return "", err
	}
	cfg, err := databaseConfig(dsn, name)
	if err != nil {
		return "", err
	}
	if err := migrate(ctx, cfg); err != nil {
		return "", fmt.Errorf("migrating: %w", err)
	}
	return name, nil
}

// dropLeftovers drops the databases of test binaries no longer running: a binary that crashed, or
// one that ended, since the template outlives its last test. A running binary's are left alone,
// packages being tested in parallel on the same server.
func dropLeftovers(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `select datname from pg_database where datname like 'arkhitekton\_test\_%'`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range names {
		pid, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(name, prefix), "_", 2)[0])
		if err != nil || running(pid) {
			continue
		}
		if _, err := conn.Exec(ctx, "drop database if exists "+name+" with (force)"); err != nil {
			return fmt.Errorf("dropping %s: %w", name, err)
		}
	}
	return nil
}

func running(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// exec runs a statement outside any transaction, as CREATE and DROP DATABASE require.
func exec(ctx context.Context, dsn, statement string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, statement)
	return err
}

func databaseConfig(dsn, name string) (config.DatabaseConfig, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return config.DatabaseConfig{}, errors.New("TEST_DATABASE_URL must be a postgres:// URL")
	}
	parsed.Path = "/" + name
	return config.DatabaseConfig{DSN: parsed.String(), MaxConns: 4, ConnMaxLifetime: time.Minute}, nil
}
