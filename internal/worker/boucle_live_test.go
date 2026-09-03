package worker

import (
	"context"
	"os"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/reaction"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/redmine"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/secrets"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La boucle entière, sur une vraie base et une vraie instance.
 *
 * # Ce que les vérifications précédentes ne couvraient pas
 *
 * `redmineprobe_live_test.go` interroge le vrai Redmine, mais projette dans un
 * magasin en mémoire : rien n'était écrit, aucun curseur n'était persisté, aucune
 * réaction n'était aiguillée. Les tests unitaires couvrent la boucle, mais avec
 * une sonde de test et une base simulée.
 *
 * Ce qui restait donc non éprouvé est précisément la jonction : **des faits réels
 * traversant le vrai schéma**. C'est là que vivent les contraintes d'unicité, le
 * type de la colonne de charge, la contrainte de forme sur `payload`, et le
 * curseur de `sync_state`. Une clé trop longue, une charge qu'une contrainte
 * refuse, un fait sans date — rien de tout cela ne se voit ailleurs.
 *
 * # Il écrit, mais jamais là où ça compte
 *
 *   - **Redmine** n'est que lu : son transport refuse structurellement toute
 *     méthode autre que GET et HEAD ;
 *   - **la base** est celle de `make test-integration`, jetable, dont le schéma
 *     est remis à zéro au début de ce test. Ce n'est pas la base d'un
 *     déploiement, et le DSN vient de `TEST_DATABASE_URL` ;
 *   - **les gestes** ne sortent pas : le plan ne porte qu'une action confiée à un
 *     runner qui compte, et rien d'autre.
 *
 * Double garde : `TEST_DATABASE_URL` **et** `REDMINE_LIVE=1`.
 *
 *   set -a; . deploy/overlays/local/secrets/dashboard.env; set +a
 *   TEST_DATABASE_URL=… REDMINE_LIVE=1 REDMINE_URL=$TRACKER_URL \
 *     REDMINE_PROJECT=$TRACKER_PROJECT REDMINE_API_KEY=$TRACKER_API_KEY \
 *     go test -count=1 -run TestLiveLaBoucle ./internal/worker/ -v
 */

// baseJetable ouvre la base d'intégration et remet son schéma à zéro.
//
// À zéro, et c'est délibéré : hériter de l'état d'un test précédent ferait passer
// ce test-ci sur des faits qu'il n'a pas produits.
func baseJetable(t *testing.T) *store.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL non défini: la boucle complète est ignorée")
	}
	cfg := config.DatabaseConfig{DSN: dsn, MaxConns: 4, ConnMaxLifetime: time.Minute}

	ctx := context.Background()
	brut, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	if _, err := brut.Pool().Exec(ctx,
		"drop schema public cascade; create schema public;"); err != nil {
		brut.Close()
		t.Fatalf("réinitialisation du schéma: %v", err)
	}
	brut.Close()

	if err := store.Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// gesteCompteur retient les gestes qu'on lui confie, sans rien émettre.
type gesteCompteur struct {
	partis []plan.Geste
}

func (g *gesteCompteur) Run(
	_ context.Context, _ store.PlanRun, geste plan.Geste,
) (*int64, error) {
	g.partis = append(g.partis, geste)
	return nil, nil
}

// TestLiveLaBoucleCompleteSurUneVraieBase fait tourner sonde, base et aiguillage
// ensemble.
func TestLiveLaBoucleCompleteSurUneVraieBase(t *testing.T) {
	if os.Getenv("REDMINE_LIVE") != "1" {
		t.Skip("REDMINE_LIVE non défini: vérification contre l'instance réelle ignorée")
	}
	trackerProject := os.Getenv("REDMINE_PROJECT")
	if trackerProject == "" {
		t.Fatal("REDMINE_PROJECT est requis")
	}

	ctx := context.Background()
	db := baseJetable(t)

	// Le scellement, comme en production : la clé d'API n'entre en base que
	// chiffrée, et c'est la boucle qui l'ouvrira.
	cipher, err := secrets.New(cleDeChiffrementDeTest)
	if err != nil {
		t.Fatalf("chiffreur: %v", err)
	}
	scellee, err := cipher.Seal(os.Getenv("REDMINE_API_KEY"))
	if err != nil {
		t.Fatalf("scellement de la clé: %v", err)
	}

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: "CNOUS/mse", Label: "MSE", ForgeURL: "https://forge.example.fr",
		Dockerfile: "./Dockerfile", BuildContext: ".",
		TrackerProject: trackerProject, CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("projet: %v", err)
	}

	// Le plugin, aux deux portées : l'instance porte la connexion, le projet
	// l'active. C'est le partage réel (doc 12 §12.5).
	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "redmine", Enabled: true,
		Config: map[string]string{
			"url": os.Getenv("REDMINE_URL"), "api_key": scellee,
		},
	}, "test"); err != nil {
		t.Fatalf("réglages globaux: %v", err)
	}
	if err := db.SetProjectPluginSettings(ctx, projectID, store.PluginSettings{
		PluginID: "redmine", Enabled: true,
		Config: map[string]string{"project": trackerProject},
	}, "test"); err != nil {
		t.Fatalf("réglages du projet: %v", err)
	}

	// Une réaction sur les versions déclarées. C'est elle qui aurait tiré vingt
	// fois si l'amorçage avait jeté ses faits.
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Prévenir sur nouvelle version",
		EventType: redmineVersionCreated, Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "webhook"}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("réaction: %v", err)
	}

	compteur := &gesteCompteur{}
	engine := NewEngine(db, compteur, quietLogger())
	dispatcher := NewDispatcher(db, engine, quietLogger())

	sonde := NewRedmineProbe(NewTrackerSync(db,
		func(connection TrackerConnection) (TrackerReader, error) {
			return redmine.New(redmine.Options{
				URL: connection.URL, APIKey: connection.APIKey, Timeout: 30 * time.Second,
			})
		}, quietLogger()))

	runner := NewProbeRunner(db, cipher, time.Minute, quietLogger(), sonde).
		WithDispatcher(dispatcher)

	/*
	 * 1. L'amorçage : il écrit ses faits, et ne déclenche rien.
	 */
	runner.sweep(ctx, sonde)

	apresAmorcage := compterFaits(t, db)
	if apresAmorcage == 0 {
		t.Fatal("aucun fait écrit à l'amorçage: la sonde n'a rien lu, " +
			"ou le schéma les a refusés")
	}
	if len(compteur.partis) != 0 {
		t.Errorf("%d gestes partis à l'amorçage, attendu aucun — "+
			"une activation ne réveille pas le passé", len(compteur.partis))
	}
	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("exécutions: %v", err)
	}
	if len(executions) != 0 {
		t.Errorf("%d exécutions ouvertes à l'amorçage, attendu aucune", len(executions))
	}
	// Le curseur est posé, en base cette fois.
	etat, err := db.GetSyncState(ctx, redmineSource, projectID)
	if err != nil {
		t.Fatalf("curseur: %v", err)
	}
	if etat.Cursor == "" {
		t.Error("aucun curseur persisté: la passe suivante relirait tout")
	}
	t.Logf("amorçage: %d faits écrits, curseur posé, aucune exécution", apresAmorcage)

	/*
	 * 2. La passe suivante : elle revoit l'instantané, et ne redéclenche pas.
	 *
	 * C'est le défaut corrigé lors de la migration, éprouvé ici de bout en bout —
	 * vraie base, vraies contraintes d'unicité, vrai aiguillage.
	 */
	runner.sweep(ctx, sonde)

	if apres := compterFaits(t, db); apres != apresAmorcage {
		t.Errorf("%d faits après la seconde passe, %d après l'amorçage: "+
			"l'instantané a été reconstaté", apres, apresAmorcage)
	}
	if len(compteur.partis) != 0 {
		t.Errorf("%d gestes partis à la seconde passe, attendu aucun", len(compteur.partis))
	}

	/*
	 * 3. Un fait réellement neuf déclenche, lui.
	 *
	 * Sans cette étape, tout ce qui précède se satisferait d'un aiguillage
	 * débranché : « rien ne part » se démontre trop facilement quand rien ne peut
	 * partir. On efface un fait déjà constaté, ce qui est exactement l'état où
	 * l'on serait si cette version venait d'être déclarée.
	 */
	oublier(t, db, redmineVersionCreated)
	runner.sweep(ctx, sonde)

	if len(compteur.partis) == 0 {
		t.Fatal("un fait neuf n'a déclenché aucun geste: l'aiguillage est débranché, " +
			"et les deux constats précédents ne prouvent rien")
	}
	executions, err = db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("exécutions: %v", err)
	}
	if len(executions) != 1 {
		t.Errorf("%d exécutions, attendu 1", len(executions))
	}
	t.Logf("fait neuf: %d geste(s) parti(s), %d exécution(s) ouverte(s)",
		len(compteur.partis), len(executions))
}

// compterFaits rend le nombre de faits écrits.
func compterFaits(t *testing.T, db *store.DB) int {
	t.Helper()

	var total int
	if err := db.Pool().QueryRow(context.Background(),
		"select count(*) from events").Scan(&total); err != nil {
		t.Fatalf("comptage des faits: %v", err)
	}
	return total
}

// oublier efface un fait d'un type donné, pour que la passe suivante le revoie
// comme neuf.
func oublier(t *testing.T, db *store.DB, factType string) {
	t.Helper()

	tag, err := db.Pool().Exec(context.Background(),
		"delete from events where id = (select id from events where type = $1 limit 1)",
		factType)
	if err != nil {
		t.Fatalf("oubli d'un fait: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("aucun fait de type %q à oublier", factType)
	}
}

// cleDeChiffrementDeTest scelle la clé d'API le temps du test. Sans valeur en
// dehors : elle est régénérée à chaque exécution du schéma.
const cleDeChiffrementDeTest = "sN0Xk1t2Qv8yZ3aB4cD5eF6gH7iJ8kL9mN0oP1qR2sQ="
