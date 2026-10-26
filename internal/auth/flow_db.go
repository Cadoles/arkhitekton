package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBFlowStore keeps authentication transactions in the database.
//
// This is [FlowStore] made replicable. In-memory storage assumed /auth/login and
// /auth/callback reached the same instance; with two replicas and no session
// affinity, one sign-in in two failed — the `state` was nowhere to be found on
// the replica handling the callback.
//
// The cost is one insert and one delete per sign-in attempt, negligible against
// its frequency.
type DBFlowStore struct {
	pool *pgxpool.Pool
	ttl  time.Duration
	now  func() time.Time
}

// NewDBFlowStore builds the persistent store.
func NewDBFlowStore(pool *pgxpool.Pool, ttl time.Duration) *DBFlowStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &DBFlowStore{pool: pool, ttl: ttl, now: time.Now}
}

// Begin creates an authentication transaction and returns its state.
func (s *DBFlowStore) Begin(ctx context.Context, redirectAfter string) (FlowState, error) {
	state, err := randomToken(32)
	if err != nil {
		return FlowState{}, err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return FlowState{}, err
	}
	verifier, err := randomToken(64)
	if err != nil {
		return FlowState{}, err
	}

	now := s.now().UTC()
	flow := FlowState{
		State:         state,
		Nonce:         nonce,
		CodeVerifier:  verifier,
		RedirectAfter: redirectAfter,
		CreatedAt:     now,
	}

	_, err = s.pool.Exec(ctx,
		`insert into auth_flows (state, nonce, code_verifier, redirect_after, created_at, expires_at)
		 values ($1, $2, $3, nullif($4, ''), $5, $6)`,
		state, nonce, verifier, redirectAfter, now, now.Add(s.ttl))
	if err != nil {
		return FlowState{}, fmt.Errorf("opening the authentication transaction: %w", err)
	}
	return flow, nil
}

// Consume returns the associated state and removes it.
//
// `delete ... returning` in a single query, which is what makes it single-use:
// two concurrent callbacks carrying the same `state` — a replay — cannot both
// succeed, since PostgreSQL lets only one through. A select followed by a delete
// would open exactly that window.
//
// Removal happens whether the state is valid or expired: a `state` already
// presented must not be presentable a second time, even a lapsed one.
func (s *DBFlowStore) Consume(ctx context.Context, state string) (FlowState, error) {
	var flow FlowState
	var redirectAfter *string
	var expiresAt time.Time

	err := s.pool.QueryRow(ctx,
		`delete from auth_flows where state = $1
		 returning state, nonce, code_verifier, redirect_after, created_at, expires_at`, state).
		Scan(&flow.State, &flow.Nonce, &flow.CodeVerifier, &redirectAfter,
			&flow.CreatedAt, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Replay, an expiry already purged, or a forged `state`: all three are treated
			// alike, and distinguishing them would inform an attacker.
			return FlowState{}, ErrUnknownState
		}
		return FlowState{}, fmt.Errorf("reading the authentication transaction: %w", err)
	}

	if redirectAfter != nil {
		flow.RedirectAfter = *redirectAfter
	}

	if s.now().After(expiresAt) {
		return FlowState{}, ErrUnknownState
	}
	return flow, nil
}

// PurgeExpiredFlows deletes abandoned transactions. Called periodically by the
// worker, like the session purge: a sign-in started and then interrupted leaves a
// row nobody will ever consume.
func (s *DBFlowStore) PurgeExpiredFlows(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `delete from auth_flows where expires_at <= $1`, s.now().UTC())
	if err != nil {
		return 0, fmt.Errorf("purging authentication transactions: %w", err)
	}
	return tag.RowsAffected(), nil
}
