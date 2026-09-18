package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBeginProducesDistinctUnpredictableValues(t *testing.T) {
	store := NewFlowStore(time.Minute)

	seen := map[string]bool{}
	for range 50 {
		flow, err := store.Begin(context.Background(), "/versions")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		for _, value := range []string{flow.State, flow.Nonce, flow.CodeVerifier} {
			if len(value) < 32 {
				t.Errorf("value too short to be unpredictable: %q", value)
			}
			if seen[value] {
				t.Fatalf("value repeated across two transactions: %q", value)
			}
			seen[value] = true
		}
	}
}

// Un `state` rejouable permettrait de rejouer un code d'autorisation intercepté.
func TestConsumeIsSingleUse(t *testing.T) {
	store := NewFlowStore(time.Minute)

	flow, err := store.Begin(context.Background(), "/")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if _, err := store.Consume(context.Background(), flow.State); err != nil {
		t.Fatalf("first consumption refused: %v", err)
	}
	if _, err := store.Consume(context.Background(), flow.State); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("the state was accepted a second time: %v", err)
	}
}

func TestConsumeRejectsUnknownState(t *testing.T) {
	store := NewFlowStore(time.Minute)

	if _, err := store.Consume(context.Background(), "forged-state"); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("expected ErrUnknownState, got %v", err)
	}
}

func TestConsumeRejectsExpiredState(t *testing.T) {
	store := NewFlowStore(5 * time.Minute)
	current := time.Now()
	store.now = func() time.Time { return current }

	flow, err := store.Begin(context.Background(), "/")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	current = current.Add(6 * time.Minute)

	if _, err := store.Consume(context.Background(), flow.State); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("an expired state was accepted: %v", err)
	}
}

// A sign-in started and then abandoned must not leave an entry for good.
func TestBeginEvictsAbandonedTransactions(t *testing.T) {
	store := NewFlowStore(time.Minute)
	current := time.Now()
	store.now = func() time.Time { return current }

	for range 10 {
		if _, err := store.Begin(context.Background(), "/"); err != nil {
			t.Fatalf("Begin: %v", err)
		}
	}
	if store.Len() != 10 {
		t.Fatalf("expected 10 transactions in flight, got %d", store.Len())
	}

	current = current.Add(2 * time.Minute)
	if _, err := store.Begin(context.Background(), "/"); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if store.Len() != 1 {
		t.Errorf("abandoned transactions were not purged: %d left", store.Len())
	}
}

func TestFlowStoreIsSafeForConcurrentUse(t *testing.T) {
	store := NewFlowStore(time.Minute)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			flow, err := store.Begin(context.Background(), "/")
			if err != nil {
				t.Errorf("Begin: %v", err)
				return
			}
			if _, err := store.Consume(context.Background(), flow.State); err != nil {
				t.Errorf("Consume: %v", err)
			}
		}()
	}
	wg.Wait()

	if store.Len() != 0 {
		t.Errorf("%d leftover transaction(s)", store.Len())
	}
}

// The challenge must be the SHA-256 of the verifier, base64url without padding, or
// the provider rejects the code exchange.
func TestCodeChallengeMatchesS256(t *testing.T) {
	verifier := "a-test-verifier"
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])

	if got := CodeChallenge(verifier); got != expected {
		t.Errorf("CodeChallenge = %q, expected %q", got, expected)
	}
}

func TestBeginPreservesRequestedRedirect(t *testing.T) {
	store := NewFlowStore(time.Minute)

	flow, err := store.Begin(context.Background(), "/releases/1.8.0/tests/unit")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	consumed, err := store.Consume(context.Background(), flow.State)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}

	if consumed.RedirectAfter != "/releases/1.8.0/tests/unit" {
		t.Errorf("destination perdue: %q", consumed.RedirectAfter)
	}
}
