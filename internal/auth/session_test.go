package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// Les sessions et les rôles vivent en base : les simuler ne prouverait rien des
// requêtes, qui sont l'essentiel du code testé ici.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL non défini: test d'intégration ignoré")
	}

	// Le schéma est appliqué ici plutôt que supposé : les tests de `store`
	// réinitialisent la base, et rien ne garantit l'ordre entre paquets. Un
	// paquet qui dépend d'un schéma doit savoir l'établir.
	if err := store.Migrate(context.Background(), config.DatabaseConfig{DSN: dsn, MaxConns: 2}, discardTestLogger()); err != nil {
		t.Fatalf("migration du schéma: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	t.Cleanup(pool.Close)

	// Chaque test part d'un état propre : une session héritée du test précédent
	// ferait passer un test qui ne prouve rien.
	if _, err := pool.Exec(context.Background(), "truncate sessions, user_roles"); err != nil {
		t.Fatalf("nettoyage: %v", err)
	}
	return pool
}

func TestSessionLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	if err := store.GrantRole(ctx, "sub-1", "alice", RoleOperator, "test"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}

	session, err := store.Create(ctx, "sub-1", "alice", "alice@example.fr")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(session.ID) < 32 {
		t.Errorf("identifiant de session trop court: %q", session.ID)
	}

	found, role, err := store.Lookup(ctx, session.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if found.Subject != "sub-1" || found.Username != "alice" || role != RoleOperator {
		t.Errorf("session inattendue: %+v role=%s", found, role)
	}

	if err := store.Revoke(ctx, session.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, _, err := store.Lookup(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session révoquée toujours valide: %v", err)
	}
}

// Une session expirée ne doit pas pouvoir être retournée puis oubliée par un
// appelant distrait : le filtrage a lieu en SQL.
func TestLookupRejectsExpiredSession(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	store := NewSessionStore(pool, time.Hour)
	current := time.Now()
	store.now = func() time.Time { return current }

	if err := store.GrantRole(ctx, "sub-2", "bob", RoleViewer, "test"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	session, err := store.Create(ctx, "sub-2", "bob", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	current = current.Add(2 * time.Hour)

	if _, _, err := store.Lookup(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session expirée acceptée: %v", err)
	}
}

// Authentifié sans rôle : l'utilisateur doit être identifiable pour apparaître
// en attente d'attribution, plutôt que de recevoir une erreur opaque.
func TestLookupReportsMissingRoleButKeepsIdentity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	session, err := store.Create(ctx, "sub-3", "charlie", "charlie@example.fr")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, role, err := store.Lookup(ctx, session.ID)
	if !errors.Is(err, ErrNoRole) {
		t.Fatalf("attendu ErrNoRole, obtenu %v", err)
	}
	if found.Username != "charlie" {
		t.Errorf("identité perdue: %+v", found)
	}
	if role != "" {
		t.Errorf("rôle inattendu: %q", role)
	}
}

// Le retrait d'un rôle ou une réponse à incident doit pouvoir couper toutes les
// sessions d'un utilisateur d'un coup.
func TestRevokeSubjectClosesEverySession(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	if err := store.GrantRole(ctx, "sub-4", "dana", RoleReleaser, "test"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	first, _ := store.Create(ctx, "sub-4", "dana", "")
	second, _ := store.Create(ctx, "sub-4", "dana", "")

	if err := store.RevokeSubject(ctx, "sub-4"); err != nil {
		t.Fatalf("RevokeSubject: %v", err)
	}

	for _, session := range []Session{first, second} {
		if _, _, err := store.Lookup(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("session %s toujours valide", session.ID)
		}
	}
}

// Un compte rétrogradé volontairement ne doit pas redevenir administrateur à sa
// prochaine connexion.
func TestBootstrapDoesNotOverrideExistingRole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	if err := store.GrantRole(ctx, "121", "vcarroy", RoleViewer, "admin"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy", []string{"vcarroy"})
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if granted {
		t.Error("un rôle existant ne doit pas être écrasé")
	}

	session, _ := store.Create(ctx, "121", "vcarroy", "")
	_, role, err := store.Lookup(ctx, session.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if role != RoleViewer {
		t.Errorf("le rôle a été écrasé par l'amorçage: %s", role)
	}
}

// La panne à corriger : Gitea met son identifiant numérique dans `sub`, là où un
// humain écrit un nom de compte dans la configuration. L'ancienne version créait
// un rôle pour le nom, l'utilisateur se connectait sous le numéro, et personne
// n'avait de permission.
func TestBootstrapMatchesTheUsernameNotOnlyTheSubject(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy", []string{"vcarroy"})
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if !granted {
		t.Fatal("un nom de compte déclaré doit promouvoir le sujet correspondant")
	}

	// Le rôle porte le sujet réel, pas le nom : c'est lui qui portera
	// l'autorisation aux requêtes suivantes.
	session, _ := store.Create(ctx, "121", "vcarroy", "")
	_, role, err := store.Lookup(ctx, session.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if role != RoleAdmin {
		t.Errorf("rôle = %s, attendu admin", role)
	}
}

// Le sujet reste accepté : il est immuable, donc plus sûr pour qui le connaît.
func TestBootstrapAlsoMatchesTheSubject(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy", []string{"121"})
	if err != nil || !granted {
		t.Fatalf("granted = %v, err = %v", granted, err)
	}
}

// Un compte absent de la liste ne gagne rien : sans quoi l'amorçage promouvrait
// le premier venu.
func TestBootstrapIgnoresUndeclaredAccounts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	granted, err := store.ApplyBootstrap(ctx, "999", "intrus", []string{"vcarroy"})
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if granted {
		t.Error("un compte non déclaré ne doit pas être promu")
	}

	session, _ := store.Create(ctx, "999", "intrus", "")
	_, role, _ := store.Lookup(ctx, session.ID)
	if role == RoleAdmin {
		t.Error("rôle administrateur accordé à un compte non déclaré")
	}
}

func TestPurgeExpiredRemovesOnlyExpiredSessions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	store := NewSessionStore(pool, time.Hour)
	current := time.Now()
	store.now = func() time.Time { return current }

	old, _ := store.Create(ctx, "sub-5", "eve", "")
	current = current.Add(2 * time.Hour)
	fresh, _ := store.Create(ctx, "sub-6", "frank", "")

	removed, err := store.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if removed != 1 {
		t.Errorf("attendu 1 session purgée, obtenu %d", removed)
	}

	if _, _, err := store.Lookup(ctx, old.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Error("la session expirée subsiste")
	}
	if _, _, err := store.Lookup(ctx, fresh.ID); !errors.Is(err, ErrNoRole) {
		t.Errorf("la session valide a été purgée: %v", err)
	}
}

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
