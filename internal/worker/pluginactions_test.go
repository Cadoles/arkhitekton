package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les actions qu'un plugin fournit.
 *
 * Ce que ces tests tiennent n'est pas la forme des appels — la forge s'en charge —
 * mais les trois propriétés qui distinguent une action de plugin d'un appel
 * sortant : la cible vient de la configuration et non du geste, le jeton est celui
 * du plugin, et un plugin éteint refuse au lieu de partir.
 */

// forgeEcrivante enregistre ce que le runner envoie à la forge.
type forgeEcrivante struct {
	server *httptest.Server

	mu     sync.Mutex
	method string
	path   string
	auth   string
	body   map[string]string
	status int
}

func nouvelleForgeEcrivante(t *testing.T) *forgeEcrivante {
	t.Helper()
	forge := &forgeEcrivante{status: http.StatusCreated}
	forge.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		forge.mu.Lock()
		forge.method, forge.path = r.Method, r.URL.Path
		forge.auth = r.Header.Get("Authorization")
		forge.body = map[string]string{}
		_ = json.Unmarshal(raw, &forge.body)
		code := forge.status
		forge.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(forge.server.Close)
	return forge
}

func (f *forgeEcrivante) vu() (string, string, map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.method, f.path, f.body
}

// fakePluginStore rend une connexion de plugin et un projet.
type fakePluginStore struct {
	url      string
	enabled  bool
	repo     string
	consulte []string
}

func (f *fakePluginStore) ResolvedPlugin(
	_ context.Context, _ int64, pluginID string,
) (store.PluginSettings, error) {
	f.consulte = append(f.consulte, pluginID)
	return store.PluginSettings{
		PluginID: pluginID, Enabled: f.enabled,
		// Le jeton est **scellé** en base, comme il l'est réellement : c'est ce que
		// l'ouverture doit traverser pour atteindre la forge.
		Config: map[string]string{"url": f.url, "token": "scellé:jeton-de-forge"},
	}, nil
}

func (f *fakePluginStore) GetProject(_ context.Context, id int64) (store.Project, error) {
	return store.Project{ID: id, Slug: "mse", Repository: f.repo}, nil
}

func runnerDePlugin(t *testing.T, magasin *fakePluginStore) *PluginActionRunner {
	t.Helper()
	return NewPluginActionRunner(magasin, openerDeTest{}, 2*time.Second, quietLogger())
}

func execution() store.PlanRun {
	id := int64(7)
	return store.PlanRun{
		ID: 42, ProjectID: &id,
		Context: map[string]string{
			"git_commit": "9f1c0deadbeef", "tag": "1.8.0-rc1",
			"outcome": "success", "number": "128",
		},
	}
}

// Le chemin nominal d'un statut : la cible vient de la configuration du plugin,
// le commit du fait déclencheur, et le jeton part descellé dans l'en-tête.
func TestUnStatutDeCommitEmprunteLaConnexionDuPlugin(t *testing.T) {
	forge := nouvelleForgeEcrivante(t)
	magasin := &fakePluginStore{url: forge.server.URL, enabled: true, repo: "equipe/mse"}

	job, err := runnerDePlugin(t, magasin).Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{
			"commit": "{git_commit}", "state": "success",
			"context": "mse-dashboard/vérifications", "description": "{tag} vérifiée",
		},
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	// Une action est conclue à l'instant où elle est faite : rien à attendre, donc
	// pas de job.
	if job != nil {
		t.Errorf("identifiant de job %v — une action ne passe pas par la file", *job)
	}

	methode, chemin, corps := forge.vu()
	if methode != http.MethodPost {
		t.Errorf("méthode %q", methode)
	}
	// Le commit résolu, et non le gabarit littéral : sans l'interpolation, le
	// statut se poserait sur une référence qui n'existe pas.
	if chemin != "/api/v1/repos/equipe/mse/statuses/9f1c0deadbeef" {
		t.Errorf("chemin %q", chemin)
	}
	if corps["state"] != "success" || corps["context"] != "mse-dashboard/vérifications" {
		t.Errorf("corps %v", corps)
	}
	if corps["description"] != "1.8.0-rc1 vérifiée" {
		t.Errorf("description %q — le gabarit doit être lié à la charge du fait",
			corps["description"])
	}

	forge.mu.Lock()
	auth := forge.auth
	forge.mu.Unlock()
	// Descellé le temps de l'appel. Le geste n'a désigné aucun jeton : il emprunte
	// celui du plugin, et ne peut donc pas servir à en faire sortir un autre.
	if auth != "token jeton-de-forge" {
		t.Errorf("en-tête d'authentification %q", auth)
	}
}

// Un commentaire va sur le fil de la demande, avec le numéro que le fait porte.
func TestUnCommentaireVaSurLaDemandeQueLeFaitDesigne(t *testing.T) {
	forge := nouvelleForgeEcrivante(t)
	magasin := &fakePluginStore{url: forge.server.URL, enabled: true, repo: "equipe/mse"}

	_, err := runnerDePlugin(t, magasin).Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.comment",
		Params: map[string]string{"number": "{number}", "body": "Vérifiée en {tag}."},
	})
	if err != nil {
		t.Fatalf("%v", err)
	}

	_, chemin, corps := forge.vu()
	// Le fil d'une demande de fusion vit sous la route des issues : dans Gitea,
	// une demande **est** une issue augmentée.
	if chemin != "/api/v1/repos/equipe/mse/issues/128/comments" {
		t.Errorf("chemin %q", chemin)
	}
	if corps["body"] != "Vérifiée en 1.8.0-rc1." {
		t.Errorf("corps %q", corps["body"])
	}
}

// Un plugin éteint refuse nommément, et n'appelle rien.
//
// La configuration existe encore en base : partir avec enverrait un statut au nom
// d'une intégration qu'on croit désactivée.
func TestUnPluginEteintRefuseAuLieuDePartir(t *testing.T) {
	forge := nouvelleForgeEcrivante(t)
	magasin := &fakePluginStore{url: forge.server.URL, enabled: false, repo: "equipe/mse"}

	_, err := runnerDePlugin(t, magasin).Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{"commit": "{git_commit}", "state": "success", "context": "c"},
	})

	if err == nil || !strings.Contains(err.Error(), "éteint") {
		t.Fatalf("%v — le refus doit dire que le plugin est éteint", err)
	}
	if methode, _, _ := forge.vu(); methode != "" {
		t.Errorf("la forge a été appelée (%s) alors que le plugin est éteint", methode)
	}
}

// Un gabarit resté littéral se dit en citant sa valeur.
//
// C'est le cas courant, et il vaut d'être nommé ainsi : une réaction branchée sur
// `release.built` n'a pas de `{number}`, et « numéro invalide » sans citer la
// valeur enverrait chercher une faute de frappe là où c'est le fait qui ne porte
// pas la variable.
func TestUnNumeroNonInterpoleCiteCeQuIlEstDevenu(t *testing.T) {
	forge := nouvelleForgeEcrivante(t)
	magasin := &fakePluginStore{url: forge.server.URL, enabled: true, repo: "equipe/mse"}

	run := execution()
	delete(run.Context, "number")

	_, err := runnerDePlugin(t, magasin).Run(context.Background(), run, plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.comment",
		Params: map[string]string{"number": "{number}", "body": "bonjour"},
	})

	if err == nil || !strings.Contains(err.Error(), "{number}") {
		t.Fatalf("%v — le message doit citer ce que le gabarit est devenu", err)
	}
	if methode, _, _ := forge.vu(); methode != "" {
		t.Errorf("la forge a été appelée (%s) sur un numéro illisible", methode)
	}
}

// Un fait qui ne désigne aucun projet n'a pas de connexion à emprunter.
//
// Partir sur celle de l'instance choisirait un dépôt au hasard, et le statut se
// poserait chez quelqu'un d'autre.
func TestUneActionDePluginSansProjetRefuse(t *testing.T) {
	magasin := &fakePluginStore{url: "https://forge.example.fr", enabled: true, repo: "equipe/mse"}

	_, err := runnerDePlugin(t, magasin).Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{"commit": "abc", "state": "success", "context": "c"},
	})

	if err == nil || !strings.Contains(err.Error(), "projet") {
		t.Fatalf("%v", err)
	}
	if len(magasin.consulte) != 0 {
		t.Errorf("le plugin a été consulté sans projet: %v", magasin.consulte)
	}
}

// Un refus de la forge devient le motif du geste, avec ce qu'il faut pour agir.
func TestUnRefusDeLaForgeNommeLaPortéeAttendue(t *testing.T) {
	forge := nouvelleForgeEcrivante(t)
	forge.status = http.StatusForbidden
	magasin := &fakePluginStore{url: forge.server.URL, enabled: true, repo: "equipe/mse"}

	_, err := runnerDePlugin(t, magasin).Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{"commit": "{git_commit}", "state": "success", "context": "c"},
	})

	if err == nil {
		t.Fatal("un 403 doit faire échouer le geste")
	}
	// La portée du jeton est la cause la plus fréquente, et la nommer évite d'aller
	// chercher un droit de dépôt qui est déjà là.
	if !strings.Contains(err.Error(), "write:repository") {
		t.Errorf("message %q — il doit nommer la portée manquante", err)
	}
}
