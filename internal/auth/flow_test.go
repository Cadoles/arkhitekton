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
				t.Errorf("valeur trop courte pour être imprévisible: %q", value)
			}
			if seen[value] {
				t.Fatalf("valeur répétée entre deux transactions: %q", value)
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
		t.Fatalf("première consommation refusée: %v", err)
	}
	if _, err := store.Consume(context.Background(), flow.State); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("le state a été accepté une seconde fois: %v", err)
	}
}

func TestConsumeRejectsUnknownState(t *testing.T) {
	store := NewFlowStore(time.Minute)

	if _, err := store.Consume(context.Background(), "state-forgé"); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("attendu ErrUnknownState, obtenu %v", err)
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
		t.Fatalf("un state expiré a été accepté: %v", err)
	}
}

// Une connexion commencée puis abandonnée ne doit pas laisser d'entrée à vie.
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
		t.Fatalf("attendu 10 transactions en cours, obtenu %d", store.Len())
	}

	current = current.Add(2 * time.Minute)
	if _, err := store.Begin(context.Background(), "/"); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if store.Len() != 1 {
		t.Errorf("les transactions abandonnées n'ont pas été purgées: %d restantes", store.Len())
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
		t.Errorf("%d transaction(s) résiduelle(s)", store.Len())
	}
}

// Le challenge doit être le SHA-256 du vérificateur en base64url sans padding,
// faute de quoi le fournisseur rejette l'échange de code.
func TestCodeChallengeMatchesS256(t *testing.T) {
	verifier := "un-vérificateur-de-test"
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])

	if got := CodeChallenge(verifier); got != expected {
		t.Errorf("CodeChallenge = %q, attendu %q", got, expected)
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
