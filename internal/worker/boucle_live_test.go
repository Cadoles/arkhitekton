package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/reaction"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/redmine"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/secrets"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/webhook"
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

// harnais porte ce qu'un test de bout en bout doit monter.
type harnais struct {
	db             *store.DB
	cipher         *secrets.Cipher
	projectID      int64
	trackerProject string
	sonde          Probe
}

// monterHarnais assemble base, projet, plugin et sonde comme le ferait `main`.
func monterHarnais(t *testing.T) harnais {
	t.Helper()
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

	sonde := NewRedmineProbe(NewTrackerSync(db,
		func(connection TrackerConnection) (TrackerReader, error) {
			return redmine.New(redmine.Options{
				URL: connection.URL, APIKey: connection.APIKey, Timeout: 30 * time.Second,
			})
		}, quietLogger()))

	return harnais{
		db: db, cipher: cipher, projectID: projectID,
		trackerProject: trackerProject, sonde: sonde,
	}
}

// TestLiveLaBoucleCompleteSurUneVraieBase fait tourner sonde, base et aiguillage
// ensemble.
func TestLiveLaBoucleCompleteSurUneVraieBase(t *testing.T) {
	h := monterHarnais(t)
	ctx := context.Background()
	db, projectID, sonde := h.db, h.projectID, h.sonde

	// Une réaction sur les versions déclarées. C'est elle qui aurait tiré vingt
	// fois si l'amorçage avait jeté ses faits.
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Prévenir sur nouvelle version",
		EventType: string(event.VersionCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "webhook"}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("réaction: %v", err)
	}

	compteur := &gesteCompteur{}
	engine := NewEngine(db, compteur, quietLogger())
	dispatcher := NewDispatcher(db, engine, quietLogger())

	runner := NewProbeRunner(db, h.cipher, time.Minute, quietLogger(), sonde).
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
	oublier(t, db, string(event.VersionCreated))
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

/*
 * Un appel sortant qui part vraiment.
 *
 * # Où il va, et pourquoi pas ailleurs
 *
 * Vers un récepteur local, monté par le test. La chaîne est entière — vrai
 * PostgreSQL, vrais faits Redmine, vrai `webhook.Client`, vrai TCP, vraie
 * réponse — mais **rien ne quitte la machine**. Viser une messagerie d'équipe ou
 * un service public ferait publier des noms de version chez un tiers pour prouver
 * une propriété qui se prouve ici.
 *
 * Conséquence assumée : le transport tourne avec `AllowPrivate` et
 * `AllowInsecure`, sans quoi il refuserait la boucle locale. Le second cas de ce
 * test remet le garde-fou à son défaut et vérifie qu'il refuse — les deux moitiés
 * comptent.
 */

// recepteur retient l'appel qu'il reçoit.
type recepteur struct {
	server  *httptest.Server
	mu      sync.Mutex
	appels  int
	methode string
	typeMIM string
	auth    string
	corps   string
}

func nouveauRecepteur(t *testing.T) *recepteur {
	t.Helper()
	r := &recepteur{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		corps, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.appels++
		r.methode = req.Method
		r.typeMIM = req.Header.Get("Content-Type")
		r.auth = req.Header.Get("Authorization")
		r.corps = string(corps)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *recepteur) vu() (int, string, string, string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appels, r.methode, r.typeMIM, r.auth, r.corps
}

// resolveurDeTest ouvre un champ sensible désigné, comme le fait `main`.
type resolveurDeTest struct {
	db     *store.DB
	cipher *secrets.Cipher
}

func (r resolveurDeTest) ResolveSecret(
	ctx context.Context, projectID *int64, reference string,
) (string, error) {
	pluginID, field, found := strings.Cut(reference, ".")
	if !found || projectID == nil {
		return "", fmt.Errorf("désignation %q invalide", reference)
	}
	settings, err := r.db.ResolvedPlugin(ctx, *projectID, pluginID)
	if err != nil {
		return "", err
	}
	return r.cipher.Open(settings.Config[field])
}

// jetonFabrique n'est le secret de personne : il sert à vérifier qu'un jeton
// **désigné** est descellé, posé en en-tête, et n'atteint jamais le corps.
const jetonFabrique = "jeton-fabrique-pour-ce-test-uniquement"

func TestLiveUnAppelSortantPartVraiment(t *testing.T) {
	h := monterHarnais(t)
	ctx := context.Background()
	db, projectID, sonde := h.db, h.projectID, h.sonde

	// Un plugin qui porte un champ sensible, avec une valeur fabriquée. C'est le
	// jeton que le geste **désignera** — jamais une clé réelle : la faire sortir
	// vers un serveur de test serait exactement ce que la règle interdit.
	scelle, err := h.cipher.Seal(jetonFabrique)
	if err != nil {
		t.Fatalf("scellement du jeton: %v", err)
	}
	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "gitea", Enabled: true,
		Config: map[string]string{"url": "https://forge.example.fr", "token": scelle},
	}, "test"); err != nil {
		t.Fatalf("plugin porteur du jeton: %v", err)
	}

	recu := nouveauRecepteur(t)

	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Appel sortant réel",
		EventType: string(event.VersionCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "webhook",
				Params: map[string]string{
					"url": recu.server.URL,
					// Le gabarit est lié à la charge du fait : `{name}` doit
					// devenir un vrai nom de version.
					"body":        `{"version":"{name}","statut":"{status}"}`,
					"auth_header": "Authorization",
					"auth_secret": "gitea.token",
				},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("réaction: %v", err)
	}

	// Le vrai runner d'actions, avec le vrai transport. Les deux autorisations
	// sont celles qu'un déploiement local déclare, et `main` les journalise en
	// avertissement quand elles sont posées.
	actions := NewActionRunner(webhook.New(webhook.Options{
		AllowPrivate: true, AllowInsecure: true, Timeout: 10 * time.Second,
	}), resolveurDeTest{db: db, cipher: h.cipher}, quietLogger())

	engine := NewEngine(db, gesteRouter{actions: actions}, quietLogger())
	runner := NewProbeRunner(db, h.cipher, time.Minute, quietLogger(), sonde).
		WithDispatcher(NewDispatcher(db, engine, quietLogger()))

	runner.sweep(ctx, sonde) // amorçage : rien ne part
	if appels, _, _, _, _ := recu.vu(); appels != 0 {
		t.Fatalf("%d appels à l'amorçage, attendu aucun", appels)
	}

	// Un fait redevient neuf, et la réaction part pour de bon.
	oublier(t, db, string(event.VersionCreated))
	runner.sweep(ctx, sonde)

	appels, methode, typeMIM, auth, corps := recu.vu()
	if appels != 1 {
		t.Fatalf("%d appels reçus, attendu 1", appels)
	}
	if methode != http.MethodPost {
		t.Errorf("méthode %q", methode)
	}
	if !strings.Contains(typeMIM, "application/json") {
		t.Errorf("type de contenu %q", typeMIM)
	}

	// Le jeton est descellé et posé en en-tête.
	if auth != jetonFabrique {
		t.Errorf("en-tête d'authentification %q, attendu le jeton descellé", auth)
	}
	// Et il n'est **nulle part** dans le corps. C'est la règle de doc 14 §14.7
	// rendue observable : un champ sensible est désigné, jamais interpolé.
	if strings.Contains(corps, jetonFabrique) {
		t.Error("le jeton a fui dans le corps de l'appel")
	}

	// Le gabarit a été lié : `{name}` n'est plus littéral.
	var charge map[string]string
	if err := json.Unmarshal([]byte(corps), &charge); err != nil {
		t.Fatalf("corps illisible (%q): %v", corps, err)
	}
	if charge["version"] == "" || strings.Contains(charge["version"], "{") {
		t.Errorf("version rendue %q — le gabarit n'a pas été lié", charge["version"])
	}
	t.Logf("appel reçu: POST, version %q, statut %q", charge["version"], charge["statut"])

	// L'exécution conclut, et son geste aussi.
	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("exécutions: %v", err)
	}
	if len(executions) != 1 {
		t.Fatalf("%d exécutions, attendu 1", len(executions))
	}
	if executions[0].State != "succeeded" {
		t.Errorf("exécution %q, attendue conclue", executions[0].State)
	}
	if len(executions[0].Steps) != 1 || executions[0].Steps[0].State != "succeeded" {
		t.Errorf("gestes = %+v", executions[0].Steps)
	}
}

// Les deux garde-fous du transport sont vivants dans la chaîne réelle.
//
// Le test précédent les a désarmés pour viser la boucle locale — ce qu'un
// déploiement local déclare en connaissance de cause. Celui-ci les rearme, un à
// la fois, parce qu'ils ne protègent pas de la même chose :
//
//   - **le clair** refuse `http://`, quelle que soit la cible. Une charge de
//     webhook porte un tag, parfois un titre de demande, et le jeton qui
//     l'authentifie ;
//   - **la cible privée** refuse la boucle locale et les réseaux internes, en
//     jugeant l'**adresse résolue**. C'est la défense contre la falsification de
//     requête côté serveur, et c'est la plus importante des deux.
//
// Les éprouver ensemble ne prouverait que le premier : il s'applique le premier,
// et masquerait le second.
func TestLiveLesGardeFousDuTransportRefusent(t *testing.T) {
	h := monterHarnais(t)
	ctx := context.Background()
	db, projectID, sonde := h.db, h.projectID, h.sonde

	recu := nouveauRecepteur(t)

	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Appel vers la boucle locale",
		EventType: string(event.VersionCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "webhook",
				Params: map[string]string{"url": recu.server.URL},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("réaction: %v", err)
	}

	// L'amorçage une seule fois : les deux phases partagent la base et le curseur,
	// et relire Redmine deux fois ne prouverait rien de plus.
	amorcer := func(options webhook.Options) *ProbeRunner {
		actions := NewActionRunner(webhook.New(options),
			resolveurDeTest{db: db, cipher: h.cipher}, quietLogger())
		return NewProbeRunner(db, h.cipher, time.Minute, quietLogger(), sonde).
			WithDispatcher(NewDispatcher(db,
				NewEngine(db, gesteRouter{actions: actions}, quietLogger()), quietLogger()))
	}

	phases := []struct {
		nom     string
		options webhook.Options
		attendu string
	}{
		{
			nom:     "le défaut de production refuse le clair",
			options: webhook.Options{Timeout: 10 * time.Second},
			attendu: "clair",
		},
		{
			// Le clair autorisé, la cible privée non : c'est le contrôle
			// d'adresse qui doit parler cette fois, et lui seul.
			nom:     "le clair autorisé, la cible privée reste refusée",
			options: webhook.Options{AllowInsecure: true, Timeout: 10 * time.Second},
			attendu: "",
		},
	}

	// La première passe amorce sans déclencher ; chaque phase rend ensuite un fait
	// neuf pour faire partir exactement une exécution.
	amorcer(phases[0].options).sweep(ctx, sonde)

	for index, phase := range phases {
		t.Run(phase.nom, func(t *testing.T) {
			oublier(t, db, string(event.VersionCreated))
			amorcer(phase.options).sweep(ctx, sonde)

			if appels, _, _, _, _ := recu.vu(); appels != 0 {
				t.Fatalf("%d appels reçus: le garde-fou a laissé passer", appels)
			}

			executions, err := db.ListPlanRuns(ctx, projectID, 0)
			if err != nil {
				t.Fatalf("exécutions: %v", err)
			}
			if len(executions) != index+1 {
				t.Fatalf("%d exécutions, attendu %d", len(executions), index+1)
			}
			// La plus récente d'abord : c'est celle de cette phase.
			derniere := executions[0]
			if derniere.State != "failed" {
				t.Errorf("exécution %q, attendue échouée", derniere.State)
			}
			if len(derniere.Steps) != 1 {
				t.Fatalf("gestes = %+v", derniere.Steps)
			}
			// Le motif doit se lire : « échoué » sans raison obligerait à relancer
			// pour comprendre qu'il s'agit d'un refus délibéré et non d'une panne
			// du destinataire.
			motif := derniere.Steps[0].Detail
			t.Logf("refus consigné: %s", motif)
			if motif == "" {
				t.Fatal("aucun motif consigné pour le refus")
			}
			if phase.attendu != "" && !strings.Contains(motif, phase.attendu) {
				t.Errorf("motif %q — attendu qu'il nomme %q", motif, phase.attendu)
			}
			// Le refus d'une cible privée ne doit pas se déguiser en refus du
			// clair : ce sont deux protections distinctes, et les confondre ferait
			// croire la seconde éprouvée alors que la première l'a masquée.
			if phase.attendu == "" && strings.Contains(motif, "clair") {
				t.Errorf("motif %q — c'est le refus du clair, pas celui de la cible", motif)
			}
		})
	}
}
