package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les files vues de l'API.
 *
 * Deux choses méritent d'être tenues ici : le regroupement — une file par type,
 * y compris vide, puisque c'est quand rien ne tourne qu'on vient vérifier ce
 * qu'une file autorise — et le refus d'un plafond qui immobiliserait la file.
 */

type fakeQueueStore struct {
	jobs     []store.Job
	releases []store.Release
	limits   map[string]int
	set      map[string]int
	// cleared retient les retours au défaut : la distinction entre régler et
	// défaire est ce qui rend un réglage réversible.
	cleared []string
}

func newFakeQueueStore() *fakeQueueStore {
	return &fakeQueueStore{limits: map[string]int{}, set: map[string]int{}}
}

func (f *fakeQueueStore) Enqueue(context.Context, store.JobRequest) (int64, error) { return 1, nil }
func (f *fakeQueueStore) ListQueue(context.Context) ([]store.Job, error)           { return f.jobs, nil }
func (f *fakeQueueStore) GetJob(context.Context, int64) (store.Job, error) {
	return store.Job{}, store.ErrJobNotFound
}
func (f *fakeQueueStore) CancelQueuedJob(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (f *fakeQueueStore) RequestCancel(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (f *fakeQueueStore) Directory(context.Context) (map[string]string, error) {
	return map[string]string{"121": "vcarroy"}, nil
}
func (f *fakeQueueStore) QueueLimits(context.Context) (map[string]int, error) { return f.limits, nil }
func (f *fakeQueueStore) SetQueueLimit(_ context.Context, kind string, max int, _ string) error {
	f.set[kind] = max
	return nil
}
func (f *fakeQueueStore) ClearQueueLimit(_ context.Context, kind string) error {
	f.cleared = append(f.cleared, kind)
	return nil
}
func (f *fakeQueueStore) Audit(context.Context, string, string, string, map[string]any) error {
	return nil
}
func (f *fakeQueueStore) ListReleases(context.Context, int64, int) ([]store.Release, error) {
	return f.releases, nil
}

func queueHandler(fake *fakeQueueStore) *QueueHandler {
	return NewQueueHandler(fake, &catalog.Catalog{
		MaxConcurrentRuns: 10,
		Environments: []catalog.Environment{
			{ID: "dev", Label: "Unstable", Deployer: map[string]string{"promotionSuffix": "dev"}, Rank: 1},
		},
		Pipelines: []catalog.Pipeline{
			{
				Kind: "unit-tests", PipelineRef: "php-unit-tests", Label: "Tests unitaires",
				Params: map[string]catalog.Param{
					"revision": {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					// Facultatif, et sans nature : c'est le cas qui doit rester
					// une saisie libre, et qui n'apparaissait pas du tout avant.
					"debug":    {Source: catalog.SourceInput},
					"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			{
				Kind: "e2e-tests", PipelineRef: "e2e-test", Label: "Tests E2E", MaxConcurrent: 1,
				Params: map[string]catalog.Param{
					"image-tag": {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestReleaseTag},
					// La liste des références se constitue à partir de ce que le
					// catalogue déclare : sans ce paramètre, aucune image ne serait
					// proposée, ce qui est le comportement voulu.
					"portal-image": {
						Source: catalog.SourceInput, Required: true,
						Suggest: catalog.SuggestImage, Image: "portal"},
				},
			},
		},
	}).WithSuggestions(fake, "reg.example.fr/mse", nil)
}

type queueListing struct {
	Queues []struct {
		Kind          string `json:"kind"`
		Label         string `json:"label"`
		MaxConcurrent int    `json:"max_concurrent"`
		DefaultMax    int    `json:"default_max_concurrent"`
		Overridden    bool   `json:"overridden"`
		Known         bool   `json:"known"`
		Running       int    `json:"running"`
		Waiting       int    `json:"waiting"`
		Jobs          []struct {
			ID          int64  `json:"id"`
			RequestedBy string `json:"requested_by"`
		} `json:"jobs"`
	} `json:"queues"`
	Kinds []struct {
		Kind   string `json:"kind"`
		Inputs []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
			Suggest  string `json:"suggest"`
		} `json:"inputs"`
	} `json:"kinds"`
	Suggestions   map[string][]struct{ Value, Label string } `json:"suggestions"`
	MaxConcurrent int                                        `json:"max_concurrent"`
}

func listQueues(t *testing.T, handler *QueueHandler) queueListing {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.List(recorder, httptest.NewRequest(http.MethodGet, "/queue", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}

	var listing queueListing
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	return listing
}

// Une file par type déclaré, occupée ou non.
func TestChaqueTypeALaSienneMemeVide(t *testing.T) {
	fake := newFakeQueueStore()
	fake.jobs = []store.Job{
		{ID: 1, Kind: "unit-tests", State: store.JobRunning, RequestedBy: "121"},
		{ID: 2, Kind: "unit-tests", State: store.JobQueued, RequestedBy: "121"},
	}

	listing := listQueues(t, queueHandler(fake))

	if len(listing.Queues) != 2 {
		t.Fatalf("%d file(s), attendu une par type déclaré: %+v", len(listing.Queues), listing.Queues)
	}

	unit := listing.Queues[0]
	if unit.Kind != "unit-tests" || unit.Running != 1 || unit.Waiting != 1 {
		t.Errorf("file unit-tests = %+v", unit)
	}
	// Le plafond du catalogue, faute de réglage.
	if unit.MaxConcurrent != catalog.DefaultKindConcurrency || unit.Overridden {
		t.Errorf("plafond = %d, réglé %v", unit.MaxConcurrent, unit.Overridden)
	}
	// Le sujet OIDC est traduit : c'est « qui » qu'on lit sur une file.
	if len(unit.Jobs) != 2 || unit.Jobs[0].RequestedBy != "vcarroy" {
		t.Errorf("jobs = %+v", unit.Jobs)
	}

	e2e := listing.Queues[1]
	if e2e.MaxConcurrent != 1 || len(e2e.Jobs) != 0 {
		t.Errorf("file e2e = %+v — une file vide reste affichée", e2e)
	}
}

// Un réglage remplace le plafond du catalogue, qui reste rendu à côté : l'écran
// doit pouvoir proposer d'y revenir.
func TestUnReglageEstRenduAvecSonDefaut(t *testing.T) {
	fake := newFakeQueueStore()
	fake.limits["unit-tests"] = 2

	listing := listQueues(t, queueHandler(fake))

	unit := listing.Queues[0]
	if unit.MaxConcurrent != 2 || unit.DefaultMax != catalog.DefaultKindConcurrency || !unit.Overridden {
		t.Errorf("file réglée = %+v", unit)
	}
}

// Les tâches locales ne deviennent pas des PipelineRun et n'ont pas d'entrée au
// catalogue, mais occupent bien une file. Les taire ferait disparaître de
// l'écran des jobs qui attendent réellement.
func TestUneFileHorsCatalogueResteAffichee(t *testing.T) {
	fake := newFakeQueueStore()
	fake.jobs = []store.Job{{ID: 7, Kind: "delivery-note", State: store.JobQueued, RequestedBy: "121"}}

	listing := listQueues(t, queueHandler(fake))

	if len(listing.Queues) != 3 {
		t.Fatalf("%d file(s), attendu les deux du catalogue plus la constatée", len(listing.Queues))
	}
	local := listing.Queues[2]
	if local.Kind != "delivery-note" || local.Known {
		t.Errorf("file locale = %+v", local)
	}
	if local.Waiting != 1 {
		t.Errorf("attente = %d", local.Waiting)
	}
}

func setLimit(t *testing.T, handler *QueueHandler, kind, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPut, "/queue/limits/"+kind, strings.NewReader(body))
	request.SetPathValue("kind", kind)
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "121", Username: "vcarroy", Role: auth.RoleAdmin}))

	recorder := httptest.NewRecorder()
	handler.SetLimit(recorder, request)
	return recorder
}

// Un plafond nul ou négatif immobiliserait la file pour toujours, sans que rien
// ne le dise. Il est refusé, comme celui qui dépasse ce que le cluster tient.
func TestUnPlafondHorsBornesEstRefuse(t *testing.T) {
	fake := newFakeQueueStore()
	handler := queueHandler(fake)

	for _, body := range []string{`{"max_concurrent":0}`, `{"max_concurrent":-1}`, `{"max_concurrent":999}`} {
		recorder := setLimit(t, handler, "unit-tests", body)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, attendu 400", body, recorder.Code)
		}
	}
	if len(fake.set) != 0 {
		t.Errorf("un plafond invalide a été enregistré: %v", fake.set)
	}

	if recorder := setLimit(t, handler, "unit-tests", `{"max_concurrent":3}`); recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	if fake.set["unit-tests"] != 3 {
		t.Errorf("plafond enregistré = %v", fake.set)
	}
}

// Défaire un réglage est une opération distincte de le poser à sa valeur d'avant :
// envoyer un nombre ne dirait pas « comme le catalogue », mais « exactement ce
// nombre », qui cesserait de suivre le catalogue s'il change.
func TestLeRetourAuDefautEffaceLeReglage(t *testing.T) {
	fake := newFakeQueueStore()
	handler := queueHandler(fake)

	request := httptest.NewRequest(http.MethodDelete, "/queue/limits/unit-tests", nil)
	request.SetPathValue("kind", "unit-tests")
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "121", Username: "vcarroy", Role: auth.RoleAdmin}))

	recorder := httptest.NewRecorder()
	handler.ClearLimit(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	if len(fake.cleared) != 1 || fake.cleared[0] != "unit-tests" {
		t.Errorf("réglages effacés = %v", fake.cleared)
	}
}

// Le formulaire de lancement se peuple depuis le catalogue et la base.
//
// Ses champs étaient des saisies libres : un `revision` vide oblige à aller
// chercher le nom d'une branche ailleurs pour le recopier, et la faute de frappe
// ne se découvre qu'à l'échec du clone.
func TestLeFormulaireDeLancementEstPeuple(t *testing.T) {
	fake := newFakeQueueStore()
	fake.releases = []store.Release{{Tag: "3.2.0-rc3"}, {Tag: "3.1.0"}}

	listing := listQueues(t, queueHandler(fake))

	if len(listing.Kinds) != 2 {
		t.Fatalf("%d type(s)", len(listing.Kinds))
	}

	// Les requis d'abord, puis l'ordre alphabétique : une map Go n'a pas d'ordre,
	// et un formulaire dont les champs changent de place à chaque ouverture est
	// impraticable.
	unit := listing.Kinds[0].Inputs
	if len(unit) != 2 {
		t.Fatalf("paramètres de unit-tests = %+v — les facultatifs comptent aussi", unit)
	}
	if unit[0].Name != "revision" || !unit[0].Required || unit[0].Suggest != catalog.SuggestGitRef {
		t.Errorf("premier paramètre = %+v", unit[0])
	}
	if unit[1].Name != "debug" || unit[1].Required || unit[1].Suggest != "" {
		t.Errorf("second paramètre = %+v — un facultatif sans nature reste libre", unit[1])
	}

	// Les tags de release, et les images composées du préfixe du registre. Le
	// navigateur ne connaît pas ce préfixe : le lui transmettre pour qu'il
	// assemble reviendrait à le publier pour un gain nul.
	tags := listing.Suggestions[catalog.SuggestReleaseTag]
	if len(tags) != 2 || tags[0].Value != "3.2.0-rc3" {
		t.Errorf("tags proposés = %+v", tags)
	}
	images := listing.Suggestions[catalog.Input{Suggest: catalog.SuggestImage, Image: "portal"}.SuggestionKey()]
	if len(images) != 2 || images[0].Value != "reg.example.fr/mse/portal:3.2.0-rc3" {
		t.Errorf("images proposées = %+v", images)
	}
	// Les environnements viennent du plugin MSE : sans lui, aucun n'est proposé
	// et le champ reste libre, même si le catalogue en déclare.
	if environments, present := listing.Suggestions[catalog.SuggestEnvironment]; present {
		t.Errorf("environnements proposés sans plugin = %+v", environments)
	}

	proposed := []Suggestion{{Value: "dev", Label: "Unstable"}}
	withPlugin := listQueues(t, queueHandler(fake).WithEnvironmentSuggestions(
		func(context.Context, string) []Suggestion { return proposed }))
	if environments := withPlugin.Suggestions[catalog.SuggestEnvironment]; len(environments) != 1 ||
		environments[0].Value != "dev" {
		t.Errorf("environnements proposés par le plugin = %+v", environments)
	}

	// Les références Git manquent délibérément : elles se lisent en direct chez
	// la forge, et les recharger avec la file les ferait payer toutes les deux
	// secondes.
	if _, present := listing.Suggestions[catalog.SuggestGitRef]; present {
		t.Error("les références Git ne doivent pas transiter par la file")
	}
}

func launch(t *testing.T, handler *QueueHandler, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/queue", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "121", Username: "vcarroy", Role: auth.RoleOperator}))

	recorder := httptest.NewRecorder()
	handler.Launch(recorder, request)
	return recorder
}

// Les paramètres supplémentaires atterrissent dans la spec d'un objet
// Kubernetes : un nom fantaisiste y produit une erreur de validation illisible
// plutôt qu'un refus clair.
func TestUnNomDeParametreFantaisisteEstRefuse(t *testing.T) {
	handler := queueHandler(newFakeQueueStore())

	refused := launch(t, handler,
		`{"kind":"unit-tests","params":{"revision":"main","mauvais nom":"x"}}`)
	if refused.Code != http.StatusBadRequest {
		t.Errorf("code %d, attendu 400", refused.Code)
	}

	// Un nom acceptable passe, et le paramètre n'a pas à être déclaré au
	// catalogue : c'est ce qui rend une exécution personnalisée possible.
	accepted := launch(t, handler, `{"kind":"unit-tests","params":{"revision":"main","debug-mode":"1"}}`)
	if accepted.Code != http.StatusAccepted {
		t.Errorf("code %d, attendu 202", accepted.Code)
	}
}

// BuildableProject nomme le projet d'accueil. Un seul suffit ici : ces tests
// portent sur ce que le handler fait du projet, pas sur son choix.
func (f *fakeQueueStore) BuildableProject(context.Context) (store.Project, error) {
	return store.Project{ID: 1, Repository: "vcarroy/test", Dockerfile: "Dockerfile"}, nil
}

// ProjectBySlug résout le projet nommé dans le chemin. Un seul ici, et il répond
// à tout slug : ces tests portent sur ce que le handler fait du projet, pas sur
// sa résolution.
func (f *fakeQueueStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != testProjectSlug {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 1, Slug: slug, Repository: "vcarroy/test", Dockerfile: "Dockerfile"}, nil
}
