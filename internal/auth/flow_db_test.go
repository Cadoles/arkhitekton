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

// La panne à corriger, reproduite : `/auth/login` est servi par une réplique et
// `/auth/callback` par une autre. Chaque réplique a son propre magasin ; seul un
// stockage partagé leur permet de se comprendre.
func TestAFlowOpenedByOneReplicaIsConsumableByAnother(t *testing.T) {
	pool, first := flowStores(t)
	ctx := context.Background()

	// Une seconde instance, comme un second pod : même base, magasin distinct.
	second := NewDBFlowStore(pool, 10*time.Minute)

	flow, err := first.Begin(ctx, "/releases/1.8.0")
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}

	consumed, err := second.Consume(ctx, flow.State)
	if err != nil {
		t.Fatalf("consommation par l'autre réplique: %v", err)
	}
	if consumed.CodeVerifier != flow.CodeVerifier || consumed.Nonce != flow.Nonce {
		t.Error("le vérificateur PKCE et le nonce doivent traverser les répliques")
	}
	// Le contexte de navigation est conservé : ramener l'utilisateur à l'accueil
	// lui ferait perdre sa place.
	if consumed.RedirectAfter != "/releases/1.8.0" {
		t.Errorf("redirection = %q", consumed.RedirectAfter)
	}
}

// Un `state` rejouable permettrait de rejouer un code d'autorisation intercepté.
func TestDBConsumeIsSingleUse(t *testing.T) {
	_, store := flowStores(t)
	ctx := context.Background()

	flow, _ := store.Begin(ctx, "")
	if _, err := store.Consume(ctx, flow.State); err != nil {
		t.Fatalf("première consommation: %v", err)
	}
	if _, err := store.Consume(ctx, flow.State); !errors.Is(err, ErrUnknownState) {
		t.Errorf("seconde consommation = %v, attendu ErrUnknownState", err)
	}
}

// Deux retours concurrents portant le même `state` — un rejeu — ne doivent pas
// réussir tous les deux. C'est ce que garantit le `delete ... returning` en une
// seule requête, là où un `select` suivi d'un `delete` ouvrirait la fenêtre.
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
			// Chaque goroutine emploie sa propre instance, comme autant de pods.
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
		t.Errorf("%d consommations réussies, attendu exactement 1", succeeded)
	}
}

// Un état échu est retiré **et** refusé : un `state` déjà présenté ne doit pas
// pouvoir l'être une seconde fois, même expiré.
func TestAnExpiredFlowIsRefusedAndRemoved(t *testing.T) {
	pool, _ := flowStores(t)
	ctx := context.Background()

	// TTL négatif : l'état naît expiré.
	store := NewDBFlowStore(pool, time.Nanosecond)
	flow, err := store.Begin(ctx, "")
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	if _, err := store.Consume(ctx, flow.State); !errors.Is(err, ErrUnknownState) {
		t.Errorf("erreur = %v, attendu ErrUnknownState", err)
	}

	var remaining int
	pool.QueryRow(ctx, `select count(*) from auth_flows where state = $1`, flow.State).Scan(&remaining)
	if remaining != 0 {
		t.Error("un état présenté doit être retiré, même expiré")
	}
}

// Rejeu, expiration purgée ou `state` forgé : les trois se traitent pareil, et
// distinguer les cas renseignerait un attaquant.
func TestAnUnknownStateIsRefused(t *testing.T) {
	_, store := flowStores(t)

	if _, err := store.Consume(context.Background(), "jamais-emis"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("erreur = %v, attendu ErrUnknownState", err)
	}
}

// Une connexion commencée puis abandonnée laisse une ligne que personne ne
// consommera : sans purge, elle y reste à vie.
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
		t.Errorf("%d ligne(s) purgée(s), attendu 3", removed)
	}
	// La transaction en cours survit : purger sans discernement déconnecterait
	// quelqu'un en pleine authentification.
	if _, err := live.Consume(ctx, kept.State); err != nil {
		t.Errorf("la transaction vivante a été purgée: %v", err)
	}
}
