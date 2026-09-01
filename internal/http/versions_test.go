package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La déclaration manuelle d'une version.
 *
 * Elle existe parce qu'un projet **sans suivi** n'avait aucun moyen de démarrer :
 * ses versions naissaient de la création d'une release, dont le formulaire vit
 * sur l'écran d'une version qui n'existait pas. Écran vide, aucun bouton, aucune
 * issue.
 */

type fakeVersionStore struct {
	project store.Project
	created []string
	audits  []string
}

func (f *fakeVersionStore) ListVersionSummaries(context.Context, int64) ([]store.VersionSummary, error) {
	return nil, nil
}

func (f *fakeVersionStore) GetVersionSummary(context.Context, int64) (store.VersionSummary, error) {
	return store.VersionSummary{}, store.ErrVersionNotFoundByID
}

func (f *fakeVersionStore) ListVersionIssues(context.Context, int64) ([]store.VersionIssue, error) {
	return nil, nil
}

func (f *fakeVersionStore) ListVersionReleases(context.Context, int64) ([]store.VersionRelease, error) {
	return nil, nil
}

func (f *fakeVersionStore) GetSyncState(context.Context, string) (store.SyncState, error) {
	return store.SyncState{}, nil
}

func (f *fakeVersionStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != f.project.Slug {
		return store.Project{}, store.ErrProjectNotFound
	}
	return f.project, nil
}

func (f *fakeVersionStore) GetProject(context.Context, int64) (store.Project, error) {
	return f.project, nil
}

func (f *fakeVersionStore) EnsureVersion(_ context.Context, _ int64, name string) (int64, error) {
	f.created = append(f.created, name)
	return int64(len(f.created)), nil
}

func (f *fakeVersionStore) Audit(_ context.Context, _, action, _ string, _ map[string]any) error {
	f.audits = append(f.audits, action)
	return nil
}

func declareVersion(t *testing.T, store *fakeVersionStore, body string) *httptest.ResponseRecorder {
	t.Helper()

	handler := NewVersionsHandler(store, "").
		WithNormalizer(release.NewNormalizer([]string{release.DefaultPrefix}))

	request := httptest.NewRequest(http.MethodPost, "/projects/app/versions",
		strings.NewReader(body))
	request.SetPathValue("project", "app")
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "vcarroy"}))

	recorder := httptest.NewRecorder()
	handler.Create(recorder, request)
	return recorder
}

func untracked() *fakeVersionStore {
	return &fakeVersionStore{project: store.Project{ID: 1, Slug: "app"}}
}

// Le cas qui débloque tout : un projet sans suivi déclare sa première version.
func TestUnProjetSansSuiviDeclareSaVersion(t *testing.T) {
	projects := untracked()

	recorder := declareVersion(t, projects, `{"name":"1.8.0"}`)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body)
	}
	if len(projects.created) != 1 || projects.created[0] != "1.8.0" {
		t.Errorf("versions créées: %v", projects.created)
	}
	// Tracée : c'est la seule chose qui dira qui a décidé qu'un périmètre
	// existait, puisque aucune synchronisation ne le dira à sa place.
	if len(projects.audits) != 1 || projects.audits[0] != store.AuditVersionDeclare {
		t.Errorf("journal: %v", projects.audits)
	}
}

// Sur un projet suivi, la déclaration est refusée : les versions y appartiennent
// à l'outil, qui en fait autorité. Une version créée ici resterait en attente
// d'une adoption qui n'arriverait jamais.
func TestUnProjetSuiviRefuseLaDeclarationManuelle(t *testing.T) {
	projects := untracked()
	projects.project.TrackerProject = "mse-dev"

	recorder := declareVersion(t, projects, `{"name":"1.8.0"}`)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("code %d, attendu 409: %s", recorder.Code, recorder.Body)
	}
	if len(projects.created) != 0 {
		t.Errorf("versions créées: %v", projects.created)
	}
	// Le message doit dire *pourquoi*, sans quoi on croira à une panne.
	if !strings.Contains(recorder.Body.String(), "autorité") {
		t.Errorf("corps %s", recorder.Body)
	}
}

// Un nom vide ne désigne rien, et le laisser passer produirait une version que
// l'écran afficherait sans titre.
func TestUnNomDeVersionVideEstRefuse(t *testing.T) {
	for _, body := range []string{`{"name":""}`, `{"name":"   "}`, `{}`} {
		recorder := declareVersion(t, untracked(), body)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, attendu 400", body, recorder.Code)
		}
	}
}

// La forme est vérifiée contre le schéma du projet, préfixes retirés : `v1.8.0`
// et `1.8.0` désignent la même version, et refuser le premier obligerait à
// connaître une convention interne pour saisir un numéro.
func TestLaFormeEstVerifieeContreLeSchemaDuProjet(t *testing.T) {
	if recorder := declareVersion(t, untracked(), `{"name":"v1.8.0"}`); recorder.Code != http.StatusCreated {
		t.Errorf("un nom préfixé doit être accepté: %d %s", recorder.Code, recorder.Body)
	}

	recorder := declareVersion(t, untracked(), `{"name":"pas une version"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code %d, attendu 400", recorder.Code)
	}
}

// Un schéma opaque accepte tout : refuser imposerait une convention que le
// projet n'a pas déclarée.
func TestUnSchemaOpaqueAccepteNimporteQuelNom(t *testing.T) {
	projects := untracked()
	projects.project.VersionScheme = "opaque"

	if recorder := declareVersion(t, projects, `{"name":"build-4711"}`); recorder.Code != http.StatusCreated {
		t.Errorf("code %d: %s", recorder.Code, recorder.Body)
	}
}

// Le nom créé est celui qui a été saisi, jamais la forme normalisée : celle-ci
// sert à comparer, pas à renommer ce que l'utilisateur a écrit.
func TestLeNomSaisiEstConserveTelQuel(t *testing.T) {
	projects := untracked()

	recorder := declareVersion(t, projects, `{"name":"v1.8.0"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d", recorder.Code)
	}

	var payload struct{ Name string }
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if payload.Name != "v1.8.0" || projects.created[0] != "v1.8.0" {
		t.Errorf("nom rendu %q, créé %v", payload.Name, projects.created)
	}
}

// Le drapeau qui décide de l'affichage du bouton.
//
// Le refus côté serveur était testé, l'émission du drapeau ne l'était pas : un
// `tracked` absent du corps rendrait `!data.sync.tracked` vrai côté interface, et
// le bouton apparaîtrait sur un projet suivi — exactement ce qu'il ne doit pas
// faire. Le serveur refuserait ensuite, mais l'utilisateur aurait cliqué vers un
// mur.
func TestLaListeDitSiLeProjetEstSuivi(t *testing.T) {
	cases := map[string]struct {
		tracker string
		want    bool
	}{
		"projet suivi":      {tracker: "mse-dev", want: true},
		"projet sans suivi": {tracker: "", want: false},
	}

	for name, expected := range cases {
		projects := untracked()
		projects.project.TrackerProject = expected.tracker

		handler := NewVersionsHandler(projects, "").
			WithNormalizer(release.NewNormalizer([]string{release.DefaultPrefix}))

		request := httptest.NewRequest(http.MethodGet, "/projects/app/versions", nil)
		request.SetPathValue("project", "app")
		recorder := httptest.NewRecorder()
		handler.List(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: code %d: %s", name, recorder.Code, recorder.Body)
		}

		var payload struct {
			Sync struct {
				Tracked bool `json:"tracked"`
			} `json:"sync"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s: corps illisible: %v", name, err)
		}
		if payload.Sync.Tracked != expected.want {
			t.Errorf("%s: tracked = %v, attendu %v — le bouton s'afficherait à tort",
				name, payload.Sync.Tracked, expected.want)
		}
	}
}
