package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func flowStores(t *testing.T) (*pgxpool.Pool, *DBFlowStore) {
	t.Helper()
	pool := testPool(t)
	if _, err := pool.Exec(context.Background(), "truncate auth_flows"); err != nil {
		t.Fatalf("nettoyage: %v", err)
	}
	return pool, NewDBFlowStore(pool, 10*time.Minute)
}

// The failure being fixed, reproduced: /auth/login served by one replica and
// /auth/callback by another. Each replica has its own store; only shared storage
// lets them understand each other.
func TestAFlowOpenedByOneReplicaIsConsumableByAnother(t *testing.T) {
	pool, first := flowStores(t)
	ctx := context.Background()

	// A second instance, like a second pod: same database, distinct store.
	second := NewDBFlowStore(pool, 10*time.Minute)

	flow, err := first.Begin(ctx, "/releases/1.8.0")
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}

	consumed, err := second.Consume(ctx, flow.State)
	if err != nil {
		t.Fatalf("consumption by the other replica: %v", err)
	}
	if consumed.CodeVerifier != flow.CodeVerifier || consumed.Nonce != flow.Nonce {
		t.Error("the PKCE verifier and the nonce must cross replicas")
	}
	// The browsing context is kept: sending the user back to the home page would lose
	// their place.
	if consumed.RedirectAfter != "/releases/1.8.0" {
		t.Errorf("redirection = %q", consumed.RedirectAfter)
	}
}

// A replayable `state` would allow replaying an intercepted authorization code.
func TestDBConsumeIsSingleUse(t *testing.T) {
	_, store := flowStores(t)
	ctx := context.Background()

	flow, _ := store.Begin(ctx, "")
	if _, err := store.Consume(ctx, flow.State); err != nil {
		t.Fatalf("first consumption: %v", err)
	}
	if _, err := store.Consume(ctx, flow.State); !errors.Is(err, ErrUnknownState) {
		t.Errorf("second consumption = %v, expected ErrUnknownState", err)
	}
}

// Two concurrent callbacks carrying the same `state` — a replay — must not both
// succeed. That is what the single `delete ... returning` guarantees.
func TestConcurrentConsumeLetsExactlyOneThrough(t *testing.T) {
	pool, store := flowStores(t)
	ctx := context.Background()

	flow, _ := store.Begin(ctx, "")

	const racers = 8
	var wait sync.WaitGroup
	results := make([]error, racers)
	for i := range racers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			// Each goroutine uses its own instance, as separate pods would.
			_, err := NewDBFlowStore(pool, 10*time.Minute).Consume(ctx, flow.State)
			results[index] = err
		}(i)
	}
	wait.Wait()

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Errorf("%d successful consumptions, expected exactly 1", succeeded)
	}
}

// A lapsed state is removed and refused: a `state` already presented must not be
// presentable a second time, even expired.
func TestAnExpiredFlowIsRefusedAndRemoved(t *testing.T) {
	pool, _ := flowStores(t)
	ctx := context.Background()

	// A negative TTL: the state is born expired.
	store := NewDBFlowStore(pool, time.Nanosecond)
	flow, err := store.Begin(ctx, "")
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	if _, err := store.Consume(ctx, flow.State); !errors.Is(err, ErrUnknownState) {
		t.Errorf("error = %v, expected ErrUnknownState", err)
	}

	var remaining int
	pool.QueryRow(ctx, `select count(*) from auth_flows where state = $1`, flow.State).Scan(&remaining)
	if remaining != 0 {
		t.Error("a presented state must be removed, even expired")
	}
}

// Replay, a purged expiry or a forged `state`: all three are treated alike, and
// distinguishing them would inform an attacker.
func TestAnUnknownStateIsRefused(t *testing.T) {
	_, store := flowStores(t)

	if _, err := store.Consume(context.Background(), "jamais-emis"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("error = %v, expected ErrUnknownState", err)
	}
}

// A sign-in started and then abandoned leaves a row nobody will consume: without
// a purge it stays for good.
func TestPurgeRemovesAbandonedFlows(t *testing.T) {
	pool, _ := flowStores(t)
	ctx := context.Background()

	expired := NewDBFlowStore(pool, time.Nanosecond)
	for range 3 {
		if _, err := expired.Begin(ctx, ""); err != nil {
			t.Fatalf("ouverture: %v", err)
		}
	}
	live := NewDBFlowStore(pool, time.Hour)
	kept, _ := live.Begin(ctx, "")

	time.Sleep(2 * time.Millisecond)

	removed, err := live.PurgeExpiredFlows(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if removed != 3 {
		t.Errorf("%d row(s) purged, expected 3", removed)
	}
	// The transaction in flight survives: purging indiscriminately would disconnect
	// someone mid-authentication.
	if _, err := live.Consume(ctx, kept.State); err != nil {
		t.Errorf("the live transaction was purged: %v", err)
	}
}
