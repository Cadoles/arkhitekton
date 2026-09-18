package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/webhook"
)

/*
 * Les actions, vues du moteur.
 *
 * Ce qui se joue ici n'est pas l'appel HTTP — `internal/webhook` s'en charge et
 * l'éprouve — mais la **frontière du secret** : un jeton désigné doit atteindre
 * l'en-tête d'authentification sans jamais traverser un gabarit, un corps, ou un
 * message d'erreur.
 */

// fakeSecrets rend un jeton pour une désignation connue.
type fakeSecrets struct {
	values map[string]string
	err    error
}

func (f fakeSecrets) ResolveSecret(_ context.Context, _ *int64, reference string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.values[reference], nil
}

func runnerFor(t *testing.T, secrets SecretResolver) *ActionRunner {
	t.Helper()
	return NewActionRunner(webhook.New(webhook.Options{
		AllowPrivate: true, AllowInsecure: true,
	}), secrets, quietLogger())
}

func projet(id int64) *int64 { return &id }

// Le chemin nominal : le gabarit est lié à la charge du fait, et le jeton part
// dans l'en-tête — jamais dans le corps.
func TestUnAppelSortantLieSonGabaritEtPoseSonJeton(t *testing.T) {
	var recu struct {
		body string
		auth string
	}
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corps := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(corps)
		recu.body = string(corps)
		recu.auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer serveur.Close()

	runner := runnerFor(t, fakeSecrets{values: map[string]string{
		"redmine.api_key": "jeton-de-production",
	}})

	job, err := runner.Run(context.Background(), store.PlanRun{
		ID: 1, ProjectID: projet(7),
		Context: map[string]string{"tag": "1.8.0-rc1", "outcome": "succeeded"},
	}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{
			"url":         serveur.URL,
			"body":        `{"text":"release {tag} : {outcome}"}`,
			"auth_header": "Authorization",
			"auth_secret": "redmine.api_key",
		},
	})
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	// Une action est conclue à l'instant où elle est faite : rien n'arrivera plus
	// tard pour la conclure, donc pas de job à attendre.
	if job != nil {
		t.Errorf("identifiant de job = %v, attendu nul", *job)
	}

	if recu.body != `{"text":"release 1.8.0-rc1 : succeeded"}` {
		t.Errorf("corps reçu = %q", recu.body)
	}
	if recu.auth != "jeton-de-production" {
		t.Errorf("en-tête d'authentification = %q", recu.auth)
	}
	// **Le jeton n'est pas dans le corps.** C'est la règle : un champ sensible est
	// désigné, jamais interpolé.
	if strings.Contains(recu.body, "jeton-de-production") {
		t.Error("le jeton transparaît dans le corps")
	}
}

// Un corps vide envoie la charge du fait telle quelle : un destinataire générique
// reçoit tout ce qu'on sait, sans qu'un gabarit ait à être écrit.
func TestUnCorpsVideEnvoieLaChargeDuFait(t *testing.T) {
	var recu string
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corps := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(corps)
		recu = string(corps)
		w.WriteHeader(http.StatusOK)
	}))
	defer serveur.Close()

	_, err := runnerFor(t, nil).Run(context.Background(), store.PlanRun{
		Context: map[string]string{"tag": "1.8.0"},
	}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{"url": serveur.URL},
	})
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	if !strings.Contains(recu, `"tag":"1.8.0"`) {
		t.Errorf("charge reçue = %q", recu)
	}
}

// Un gabarit qui rend du JSON invalide est refusé **avant** de partir : le
// destinataire répondrait sur *sa* syntaxe, et le message enverrait chercher chez
// lui un défaut qui est chez nous.
func TestUnGabaritQuiRendDuJSONInvalideEstRefuseAvantDePartir(t *testing.T) {
	var atteint bool
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atteint = true
		w.WriteHeader(http.StatusOK)
	}))
	defer serveur.Close()

	_, err := runnerFor(t, nil).Run(context.Background(), store.PlanRun{
		Context: map[string]string{"tag": "1.8.0"},
	}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{"url": serveur.URL, "body": `{"tag": {tag}}`},
	})

	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("%v, attendu un refus nommant le JSON", err)
	}
	if atteint {
		t.Error("la requête est partie malgré un corps invalide")
	}
}

// L'en-tête et le jeton se déclarent ensemble.
//
// Un jeton sans en-tête ne partirait nulle part ; un en-tête sans jeton partirait
// vide, et le destinataire refuserait sur un message d'authentification qui ne
// dirait pas que la valeur manque chez nous.
func TestLEnteteEtLeJetonSeDeclarentEnsemble(t *testing.T) {
	runner := runnerFor(t, fakeSecrets{})

	for _, params := range []map[string]string{
		{"url": "https://example.test/h", "auth_header": "Authorization"},
		{"url": "https://example.test/h", "auth_secret": "redmine.api_key"},
	} {
		_, err := runner.Run(context.Background(), store.PlanRun{}, plan.Geste{
			Kind: plan.GesteAction, Ref: action.Webhook, Params: params,
		})
		if err == nil || !strings.Contains(err.Error(), "ensemble") {
			t.Errorf("params %v: %v", params, err)
		}
	}
}

// Un jeton désigné mais vide est refusé plutôt qu'envoyé vide : le destinataire
// répondrait 401, et le message ne dirait pas que le réglage est absent chez nous.
func TestUnJetonNonRenseigneEstRefuse(t *testing.T) {
	_, err := runnerFor(t, fakeSecrets{values: map[string]string{}}).Run(
		context.Background(), store.PlanRun{ProjectID: projet(7)}, plan.Geste{
			Kind: plan.GesteAction, Ref: action.Webhook,
			Params: map[string]string{
				"url":         "https://example.test/h",
				"auth_header": "Authorization", "auth_secret": "redmine.api_key",
			},
		})

	if err == nil || !strings.Contains(err.Error(), "renseigné") {
		t.Fatalf("%v", err)
	}
}

// Une cible interdite est refusée par le transport, et l'erreur reste
// reconnaissable au travers du runner : « refusé » se corrige dans la réaction,
// « injoignable » chez le destinataire.
func TestUneCibleInterditeResteReconnaissable(t *testing.T) {
	runner := NewActionRunner(
		webhook.New(webhook.Options{}), fakeSecrets{}, quietLogger())

	_, err := runner.Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{"url": "https://169.254.169.254/latest/"},
	})

	if !errors.Is(err, webhook.ErrBlockedTarget) {
		t.Fatalf("%v, attendu ErrBlockedTarget", err)
	}
}

// Un refus du destinataire fait échouer le geste, avec son code et un extrait :
// « échoué » seul n'apprend rien et oblige à relancer pour voir.
func TestUnRefusDuDestinataireFaitEchouerLeGesteEnLeDisant(t *testing.T) {
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("jeton expiré"))
	}))
	defer serveur.Close()

	_, err := runnerFor(t, nil).Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{"url": serveur.URL},
	})

	if err == nil {
		t.Fatal("un 401 n'a pas fait échouer le geste")
	}
	for _, attendu := range []string{"401", "jeton expiré"} {
		if !strings.Contains(err.Error(), attendu) {
			t.Errorf("message %q — il doit porter %q", err, attendu)
		}
	}
}

// Une action de plugin sur un déploiement qui ne les a pas branchées est refusée
// nommément : rendre « parti » sur un geste que personne n'exécute serait pire.
func TestUneActionDePluginNonBrancheeEstRefuseeNommement(t *testing.T) {
	_, err := runnerFor(t, nil).Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{
			"commit": "abc1234", "state": "success", "context": "dashboard",
		},
	})

	if err == nil || !strings.Contains(err.Error(), "gitea") {
		t.Fatalf("%v — le refus doit nommer le plugin attendu", err)
	}
}

// Le partage se lit dans le descripteur, jamais dans un découpage de la
// référence.
//
// C'est le piège que ce test tient : `forge.status` — action interne d'une
// version antérieure — porte un point et ressemble à s'y méprendre à une action
// du plugin `forge`. La confier au runner de plugins la ferait échouer sur
// « aucun plugin ne déclare », message qui envoie chercher au mauvais endroit.
func TestUnPointDansUneReferenceNeFaitPasUnPlugin(t *testing.T) {
	var confie bool
	runner := runnerFor(t, nil).WithPlugins(gesteRunnerFunc(
		func(context.Context, store.PlanRun, plan.Geste) (*int64, error) {
			confie = true
			return nil, nil
		}))

	_, err := runner.Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: "forge.status",
	})

	if confie {
		t.Error("une action inconnue a été confiée au runner de plugins")
	}
	if err == nil || !errors.Is(err, action.ErrUnknown) {
		t.Fatalf("%v, attendu ErrUnknown", err)
	}
}

// gesteRunnerFunc adapte une fonction en [GesteRunner].
type gesteRunnerFunc func(context.Context, store.PlanRun, plan.Geste) (*int64, error)

func (f gesteRunnerFunc) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	return f(ctx, run, geste)
}

// L'aiguillage sépare les deux natures : le moteur n'a pas à connaître la
// différence entre un pipeline, qui devient un job, et une action, qui sort.
func TestLAiguillageSepareLesDeuxNaturesDeGeste(t *testing.T) {
	router := gesteRouter{actions: runnerFor(t, nil)}

	_, err := router.Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: "courriel", Ref: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "inconnue") {
		t.Errorf("nature inconnue: %v", err)
	}

	// Sans runner de pipeline branché, un geste de pipeline échoue plutôt que de
	// partir dans le vide.
	if _, err := router.Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GestePipeline, Ref: "unit-tests",
	}); err == nil {
		t.Error("un geste de pipeline est parti sans runner")
	}
}
