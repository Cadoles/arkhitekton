package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Une action de plugin qui écrit vraiment sur une forge.
 *
 * # Contre une forge à soi, et pas celle de l'équipe
 *
 * Un statut de commit est **visible par d'autres** et ne se retire pas. Le prouver
 * sur le dépôt d'une équipe laisserait une trace de test sur un vrai commit pour
 * établir une propriété qui s'établit aussi bien sur une instance jetable.
 *
 * Le test vise donc un Gitea monté pour l'occasion. C'est un vrai serveur, sa
 * vraie API, un vrai jeton, un vrai commit — seule l'adresse change.
 *
 *   docker run -d --name gitea-live -p 3080:3000 \
 *     -e GITEA__security__INSTALL_LOCK=true -e GITEA__database__DB_TYPE=sqlite3 \
 *     gitea/gitea:1.22
 *   docker exec -u git gitea-live gitea admin user create --admin \
 *     --username mse --password … --email mse@example.invalid
 *   docker exec -u git gitea-live gitea admin user generate-access-token \
 *     -u mse --scopes write:repository --raw
 *
 * puis, le dépôt et son commit créés par l'API :
 *
 *   TEST_DATABASE_URL=… GITEA_LIVE=1 GITEA_URL=http://127.0.0.1:3080 \
 *     GITEA_TOKEN=… GITEA_REPO=mse/mse-demo GITEA_COMMIT=… \
 *     go test -count=1 -run TestLiveUnStatutDeCommit ./internal/worker/ -v
 *
 * # Ce qui n'est pas prouvé par un 201
 *
 * Que le statut soit **là**. La forge peut accepter un appel et n'en rien faire de
 * visible — un contexte mal formé, un commit inconnu accepté avec indulgence. Le
 * test relit donc le statut par l'API, et c'est cette lecture qui conclut.
 */

func forgeLocale(t *testing.T) (url, token, repo, commit string) {
	t.Helper()
	if os.Getenv("GITEA_LIVE") != "1" {
		t.Skip("GITEA_LIVE non défini: vérification contre une forge réelle ignorée")
	}
	url = os.Getenv("GITEA_URL")
	token = os.Getenv("GITEA_TOKEN")
	repo = os.Getenv("GITEA_REPO")
	commit = os.Getenv("GITEA_COMMIT")
	if url == "" || token == "" || repo == "" || commit == "" {
		t.Fatal("GITEA_URL, GITEA_TOKEN, GITEA_REPO et GITEA_COMMIT sont requis")
	}
	return url, token, repo, commit
}

// statutsDuCommit relit les statuts posés sur un commit.
type statutRendu struct {
	State       string `json:"status"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

func statutsDuCommit(t *testing.T, url, token, repo, commit string) []statutRendu {
	t.Helper()

	cible := strings.TrimSuffix(url, "/") + "/api/v1/repos/" + repo + "/statuses/" + commit
	request, err := http.NewRequest(http.MethodGet, cible, nil)
	if err != nil {
		t.Fatalf("requête de relecture: %v", err)
	}
	request.Header.Set("Authorization", "token "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("relecture des statuts: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("relecture des statuts: la forge a répondu %d", response.StatusCode)
	}

	var statuts []statutRendu
	if err := json.NewDecoder(response.Body).Decode(&statuts); err != nil {
		t.Fatalf("décodage des statuts: %v", err)
	}
	return statuts
}

func TestLiveUnStatutDeCommitEstVraimentPose(t *testing.T) {
	url, token, repo, commit := forgeLocale(t)
	db := baseJetable(t)
	ctx := context.Background()

	cipher, err := secrets.New(cleDeChiffrementDeTest)
	if err != nil {
		t.Fatalf("chiffreur: %v", err)
	}
	scelle, err := cipher.Seal(token)
	if err != nil {
		t.Fatalf("scellement du jeton: %v", err)
	}

	projectID, err := db.CreateProject(ctx, store.Project{
		// Le dépôt vient des paramètres du projet, et non du geste : c'est là
		// qu'il est déclaré une fois pour toutes.
		Repository: repo, Label: "Démo", ForgeURL: url,
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("projet: %v", err)
	}

	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "gitea", Enabled: true,
		Config: map[string]string{"url": url, "token": scelle},
	}, "test"); err != nil {
		t.Fatalf("plugin global: %v", err)
	}
	if err := db.SetProjectPluginSettings(ctx, projectID, store.PluginSettings{
		PluginID: "gitea", Enabled: true,
	}, "test"); err != nil {
		t.Fatalf("plugin du projet: %v", err)
	}

	// Le contexte et le tag sont surchargeables. Sur une forge jetable ils peuvent
	// ressembler à un vrai contrôle ; sur un dépôt réel, un statut ne se retire
	// pas, et il doit s'annoncer pour ce qu'il est — une vérification — plutôt que
	// de se faire passer pour un verdict de CI que personne n'a demandé.
	contexte := os.Getenv("GITEA_STATUS_CONTEXT")
	if contexte == "" {
		contexte = "arkhitekton/vérifications"
	}
	tag := os.Getenv("GITEA_STATUS_TAG")
	if tag == "" {
		tag = "1.8.0-rc1"
	}

	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Statut sur construction conclue",
		EventType: string(event.ReleaseCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "gitea.status",
				Params: map[string]string{
					"commit": "{git_ref}",
					// En dur, et c'est le bon usage : l'issue d'un fait et l'état
					// d'un statut de forge sont deux vocabulaires distincts. Une
					// réaction qui veut refléter l'issue s'apparie sur `outcome`.
					// Voir le test suivant, qui tient l'autre moitié.
					"state":       "success",
					"context":     contexte,
					"description": "Release {tag} construite",
					"target_url":  "https://dashboard.example.fr/releases/{tag}",
				},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("réaction: %v", err)
	}

	// Le runner d'actions, avec les actions de plugin branchées — l'assemblage de
	// `main`, sans le client de webhook dont ce geste n'a pas besoin.
	actions := NewActionRunner(nil,
		resolveurDeTest{db: db, cipher: cipher}, quietLogger()).
		WithPlugins(NewPluginActionRunner(db, cipher, 15*time.Second, quietLogger()))

	engine := NewEngine(db, gesteRouter{actions: actions}, quietLogger())
	dispatcher := NewDispatcher(db, engine, quietLogger())

	// Le fait déclencheur passe par le vrai chemin d'écriture : la provenance des
	// faits a déjà été éprouvée par la sonde, et la refaire ici lierait ce test à
	// la disponibilité de Redmine.
	fait := event.ReleaseCreatedAt(time.Now().UTC(), projectID, 42,
		tag, "pre", commit, "1.8.0")
	neuf, err := db.RecordEvent(ctx, fait)
	if err != nil {
		t.Fatalf("écriture du fait: %v", err)
	}
	if !neuf {
		t.Fatal("le fait était déjà connu: rien ne se déclencherait")
	}
	dispatcher.Dispatch(ctx, fait)

	/*
	 * 1. L'exécution conclut.
	 */
	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("exécutions: %v", err)
	}
	if len(executions) != 1 {
		t.Fatalf("%d exécutions, attendu 1", len(executions))
	}
	if executions[0].State != "succeeded" {
		t.Fatalf("exécution %q — motif: %s",
			executions[0].State, detailPremierGeste(executions[0]))
	}

	/*
	 * 2. Et le statut est **là**, relu depuis la forge.
	 *
	 * C'est la seule assertion qui compte : un appel accepté n'est pas un statut
	 * posé.
	 */
	statuts := statutsDuCommit(t, url, token, repo, commit)
	var pose *statutRendu
	for index := range statuts {
		if statuts[index].Context == contexte {
			pose = &statuts[index]
			break
		}
	}
	if pose == nil {
		t.Fatalf("aucun statut de contexte %q sur %s (%d statuts lus)",
			contexte, commit[:8], len(statuts))
	}

	if pose.State != "success" {
		t.Errorf("état %q, attendu success", pose.State)
	}
	if !strings.Contains(pose.Description, tag) {
		t.Errorf("description %q — elle doit porter le tag interpolé", pose.Description)
	}
	if !strings.Contains(pose.TargetURL, tag) {
		t.Errorf("lien %q — il doit porter le tag interpolé", pose.TargetURL)
	}
	t.Logf("statut relu sur %s: %s / %s — %q",
		commit[:8], pose.Context, pose.State, pose.Description)
}

// Un plugin éteint ne parle pas à la forge, même avec une connexion valide.
//
// La configuration reste en base quand on éteint un plugin : partir avec
// enverrait un statut au nom d'une intégration qu'on croit désactivée.
func TestLiveUnPluginEteintNecritPasSurLaForge(t *testing.T) {
	url, token, repo, commit := forgeLocale(t)
	db := baseJetable(t)
	ctx := context.Background()

	cipher, _ := secrets.New(cleDeChiffrementDeTest)
	scelle, err := cipher.Seal(token)
	if err != nil {
		t.Fatalf("scellement: %v", err)
	}

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: repo, Label: "Démo", ForgeURL: url,
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("projet: %v", err)
	}
	// La connexion est là, complète et valide — mais éteinte.
	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "gitea", Enabled: false,
		Config: map[string]string{"url": url, "token": scelle},
	}, "test"); err != nil {
		t.Fatalf("plugin global: %v", err)
	}

	const contexte = "arkhitekton/ne-doit-pas-apparaitre"
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Statut sur plugin éteint",
		EventType: string(event.ReleaseCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "gitea.status",
				Params: map[string]string{
					"commit": "{git_ref}", "state": "success", "context": contexte,
				},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("réaction: %v", err)
	}

	actions := NewActionRunner(nil, resolveurDeTest{db: db, cipher: cipher}, quietLogger()).
		WithPlugins(NewPluginActionRunner(db, cipher, 15*time.Second, quietLogger()))
	dispatcher := NewDispatcher(db,
		NewEngine(db, gesteRouter{actions: actions}, quietLogger()), quietLogger())

	fait := event.ReleaseCreatedAt(time.Now().UTC(), projectID, 43,
		"1.8.0-rc2", "pre", commit, "1.8.0")
	if _, err := db.RecordEvent(ctx, fait); err != nil {
		t.Fatalf("écriture du fait: %v", err)
	}
	dispatcher.Dispatch(ctx, fait)

	// Rien n'est arrivé sur la forge.
	for _, statut := range statutsDuCommit(t, url, token, repo, commit) {
		if statut.Context == contexte {
			t.Fatalf("un statut a été posé alors que le plugin est éteint: %+v", statut)
		}
	}

	// Et le refus se lit, plutôt que de passer pour un succès silencieux.
	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("exécutions: %v", err)
	}
	if len(executions) != 1 || executions[0].State != "failed" {
		t.Fatalf("exécutions = %+v, attendue une échouée", executions)
	}
	motif := detailPremierGeste(executions[0])
	t.Logf("refus consigné: %s", motif)
	// Le refus vient du magasin, qui rend `ErrPluginDisabled` avant même que le
	// runner ait à le constater. Ce qui compte est qu'il nomme le plugin et son
	// état, pas la formulation exacte.
	if !strings.Contains(motif, "gitea") || !strings.Contains(motif, "désactiv") {
		t.Errorf("motif %q — il doit nommer le plugin et son extinction", motif)
	}
}

// Un état hors du vocabulaire de la forge est refusé **avant** l'appel.
//
// # Le défaut que la vérification réelle a révélé
//
// Le gabarit fautif s'écrivait `state: {outcome}` : refléter l'issue du fait,
// qui rend `succeeded` parce que c'est le vocabulaire d'un fait, là où la forge
// dit `success`. Gitea a **accepté** l'appel et stocké l'état tel quel : un
// statut hors de son propre vocabulaire, qu'aucune interface ne sait afficher, et
// pas un message.
//
// Un destinataire indulgent est le pire des cas — rien n'échoue, et rien ne
// marche. Le jeu fermé est donc revérifié après interpolation, et le geste échoue
// en nommant ce qui est admis.
//
// # Pourquoi le gabarit du test a changé
//
// `outcome` n'existe plus : il vivait sur `release.built`, retiré par la
// réduction des faits (doc 15 §15.3). Le piège, lui, n'a pas bougé — **tout**
// champ de charge interpolé dans un paramètre à jeu fermé le tend. Le test le
// tend donc avec `{kind}`, qui rend `pre` : une valeur parfaitement légitime pour
// une release, et que Gitea n'a aucune raison d'admettre comme état.
func TestLiveUnEtatHorsVocabulaireNatteintPasLaForge(t *testing.T) {
	url, token, repo, commit := forgeLocale(t)
	db := baseJetable(t)
	ctx := context.Background()

	cipher, _ := secrets.New(cleDeChiffrementDeTest)
	scelle, err := cipher.Seal(token)
	if err != nil {
		t.Fatalf("scellement: %v", err)
	}

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: repo, Label: "Démo", ForgeURL: url,
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("projet: %v", err)
	}
	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "gitea", Enabled: true,
		Config: map[string]string{"url": url, "token": scelle},
	}, "test"); err != nil {
		t.Fatalf("plugin global: %v", err)
	}

	const contexte = "arkhitekton/vocabulaire-invalide"
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Statut reflétant l'issue",
		EventType: string(event.ReleaseCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "gitea.status",
				Params: map[string]string{
					"commit": "{git_ref}", "state": "{kind}", "context": contexte,
				},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("réaction: %v", err)
	}

	actions := NewActionRunner(nil, resolveurDeTest{db: db, cipher: cipher}, quietLogger()).
		WithPlugins(NewPluginActionRunner(db, cipher, 15*time.Second, quietLogger()))
	dispatcher := NewDispatcher(db,
		NewEngine(db, gesteRouter{actions: actions}, quietLogger()), quietLogger())

	fait := event.ReleaseCreatedAt(time.Now().UTC(), projectID, 44,
		"1.8.0-rc3", "pre", commit, "1.8.0")
	if _, err := db.RecordEvent(ctx, fait); err != nil {
		t.Fatalf("écriture du fait: %v", err)
	}
	dispatcher.Dispatch(ctx, fait)

	// Rien n'a atteint la forge. C'est l'assertion qui compte : avant la
	// correction, un statut d'état `succeeded` y était bel et bien déposé.
	for _, statut := range statutsDuCommit(t, url, token, repo, commit) {
		if statut.Context == contexte {
			t.Fatalf("un statut hors vocabulaire a été posé: %+v", statut)
		}
	}

	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("exécutions: %v", err)
	}
	if len(executions) != 1 || executions[0].State != "failed" {
		t.Fatalf("exécutions = %+v, attendue une échouée", executions)
	}
	motif := detailPremierGeste(executions[0])
	t.Logf("refus consigné: %s", motif)
	// Le message doit porter les trois éléments du diagnostic : ce qu'on a obtenu,
	// d'où ça vient, et ce qui est admis.
	for _, attendu := range []string{"pre", "{kind}", "success"} {
		if !strings.Contains(motif, attendu) {
			t.Errorf("motif %q — il doit porter %q", motif, attendu)
		}
	}
}

// detailPremierGeste rend le motif du premier geste d'une exécution.
func detailPremierGeste(run store.PlanRunSummary) string {
	if len(run.Steps) == 0 {
		return "(aucun geste)"
	}
	return run.Steps[0].Detail
}
