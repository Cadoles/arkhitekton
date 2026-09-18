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

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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
	// `bootstrap_grants` en fait partie : une déclaration consommée par le test
	// précédent laisserait le suivant sans rien à réclamer, et son échec
	// désignerait la résolution plutôt que l'isolation.
	if _, err := pool.Exec(context.Background(),
		"truncate sessions, user_roles, bootstrap_grants"); err != nil {
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

	declare(t, pool, "vcarroy", "admin")

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy")
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if granted != "" {
		t.Errorf("un rôle existant ne doit pas être écrasé, rôle rendu = %q", granted)
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

	declare(t, pool, "vcarroy", "admin")

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy")
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if granted == "" {
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

	declare(t, pool, "121", "admin")

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy")
	if err != nil || granted == "" {
		t.Fatalf("granted = %q, err = %v", granted, err)
	}
}

// Un compte absent de la liste ne gagne rien : sans quoi l'amorçage promouvrait
// le premier venu.
func TestBootstrapIgnoresUndeclaredAccounts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	declare(t, pool, "vcarroy", "admin")

	granted, err := store.ApplyBootstrap(ctx, "999", "intrus")
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if granted != "" {
		t.Errorf("un compte non déclaré ne doit pas être promu, rôle = %q", granted)
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

// declare pose une déclaration d'amorçage, comme le fait la migration.
//
// Les rôles ne viennent plus d'une liste passée à chaque connexion mais d'une
// table écrite une fois : c'est ce qui empêche une variable d'environnement de
// redonner un rôle indéfiniment.
func declare(t *testing.T, pool *pgxpool.Pool, identifier, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`insert into bootstrap_grants (identifier, role) values ($1, $2)
		 on conflict (identifier) do nothing`, identifier, role); err != nil {
		t.Fatalf("déclaration de %q: %v", identifier, err)
	}
}

// Une déclaration consommée ne repromeut plus personne.
//
// C'est la différence avec la configuration relue à chaque connexion : celle-ci
// donnait à qui pouvait l'éditer le pouvoir de se redonner un rôle indéfiniment,
// sans autre trace qu'une connexion. Une déclaration se réclame une fois.
func TestUneDeclarationNeSeReclameQuUneFois(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	declare(t, pool, "vcarroy", "admin")

	if granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy"); err != nil || granted == "" {
		t.Fatalf("première réclamation: %q, %v", granted, err)
	}

	// Un administrateur rétrograde le compte.
	if err := store.GrantRole(ctx, "121", "vcarroy", RoleViewer, "admin"); err != nil {
		t.Fatalf("rétrogradation: %v", err)
	}

	// La reconnexion ne doit pas le repromouvoir.
	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy")
	if err != nil {
		t.Fatalf("seconde réclamation: %v", err)
	}
	if granted != "" {
		t.Errorf("rôle rendu = %q: une déclaration consommée a repromu", granted)
	}

	session, _ := store.Create(ctx, "121", "vcarroy", "")
	_, role, _ := store.Lookup(ctx, session.ID)
	if role != RoleViewer {
		t.Errorf("rôle = %s, attendu viewer: la rétrogradation a été annulée", role)
	}
}

// Le rôle déclaré est celui posé, et pas seulement « administrateur ».
func TestLeRoleDeclareEstCeluiAttribue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	declare(t, pool, "alice", "releaser")

	granted, err := store.ApplyBootstrap(ctx, "204", "alice")
	if err != nil {
		t.Fatalf("réclamation: %v", err)
	}
	if granted != string(RoleReleaser) {
		t.Fatalf("rôle rendu = %q, attendu releaser", granted)
	}

	session, _ := store.Create(ctx, "204", "alice", "")
	_, role, _ := store.Lookup(ctx, session.ID)
	if role != RoleReleaser {
		t.Errorf("rôle = %s, attendu releaser", role)
	}
}

// Un rôle global et un rôle de projet coexistent pour le même compte.
//
// C'est ce que les deux index partiels garantissent : au plus un rôle global, au
// plus un par projet. Une clé composite `(subject, project_id)` aurait laissé
// passer plusieurs globaux, `null` n'étant jamais égal à `null`.
func TestLesDeuxPorteesCoexistent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	var projectID int64
	if err := pool.QueryRow(ctx,
		// `projects` n'est pas purgée entre les tests de ce paquet — seules les
		// sessions, les rôles et les déclarations le sont. L'insertion est donc
		// idempotente plutôt que de dépendre de l'ordre d'exécution.
		`insert into projects (slug, repository, label, forge_url, build_context, created_by)
		 values ('theme', 'CNOUS/theme', 'Thème', 'https://forge.example', '.', 'test')
		 on conflict (repository) do update set label = excluded.label
		 returning id`).Scan(&projectID); err != nil {
		t.Fatalf("projet: %v", err)
	}

	if err := store.GrantRole(ctx, "121", "vcarroy", RoleViewer, "test"); err != nil {
		t.Fatalf("rôle global: %v", err)
	}
	if err := store.GrantRoleIn(ctx, "121", "vcarroy", RoleReleaser, &projectID, "test"); err != nil {
		t.Fatalf("rôle de projet: %v", err)
	}

	session, err := store.Create(ctx, "121", "vcarroy", "v@example.fr")
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	_, global, projects, err := store.LookupRoles(ctx, session.ID)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if global != RoleViewer {
		t.Errorf("rôle global = %q, attendu viewer", global)
	}
	if projects[projectID] != RoleReleaser {
		t.Errorf("rôle du projet = %q, attendu releaser", projects[projectID])
	}

	// Et le remplacement reste par portée : réattribuer le global ne touche pas
	// celui du projet.
	if err := store.GrantRole(ctx, "121", "vcarroy", RoleOperator, "test"); err != nil {
		t.Fatalf("réattribution globale: %v", err)
	}
	_, global, projects, err = store.LookupRoles(ctx, session.ID)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if global != RoleOperator || projects[projectID] != RoleReleaser {
		t.Errorf("global = %q, projet = %q", global, projects[projectID])
	}
}

// `admin` ne s'attribue pas par projet : il gouverne les comptes, les dépôts et
// la synchronisation, dont aucun n'appartient à un projet.
func TestAdminNeSattribuePasParProjet(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	var projectID int64
	if err := pool.QueryRow(ctx,
		// `projects` n'est pas purgée entre les tests de ce paquet — seules les
		// sessions, les rôles et les déclarations le sont. L'insertion est donc
		// idempotente plutôt que de dépendre de l'ordre d'exécution.
		`insert into projects (slug, repository, label, forge_url, build_context, created_by)
		 values ('theme', 'CNOUS/theme', 'Thème', 'https://forge.example', '.', 'test')
		 on conflict (repository) do update set label = excluded.label
		 returning id`).Scan(&projectID); err != nil {
		t.Fatalf("projet: %v", err)
	}

	if err := store.GrantRoleIn(ctx, "121", "vcarroy", RoleAdmin, &projectID, "test"); err == nil {
		t.Error("un administrateur de projet ne doit pas pouvoir être attribué")
	}
}
