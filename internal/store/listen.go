package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// DeployKeysChannel carries a signal whenever a deploy key is to be set or a Secret taken back.
const DeployKeysChannel = "deploy_keys"

// listenRetry spaces two connection attempts after a loss.
const listenRetry = 5 * time.Second

// Listen calls wake at every notification on channel, until cancellation.
//
// On its own connection, outside the pool: a LISTEN holds its connection for good, and taking it
// from the pool would leave one fewer for the queries. wake is also called after every
// (re)connection, because notifications sent while nobody listened are lost.
func (db *DB) Listen(ctx context.Context, channel string, wake func(), logger *slog.Logger) error {
	for {
		err := db.listenOnce(ctx, channel, wake)
		if ctx.Err() != nil {
			return nil
		}
		logger.Warn("database notifications interrupted", "channel", channel,
			"error", redactError(err, db.pool.Config().ConnString()), "retry_in", listenRetry)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(listenRetry):
		}
	}
}

func (db *DB) listenOnce(ctx context.Context, channel string, wake func()) error {
	conn, err := pgx.ConnectConfig(ctx, db.pool.Config().ConnConfig.Copy())
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer func() {
		closing, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closing)
	}()

	if _, err := conn.Exec(ctx, "listen "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return fmt.Errorf("listening: %w", err)
	}
	wake()

	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return err
		}
		wake()
	}
}
