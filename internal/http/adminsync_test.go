package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type fakeSyncStore struct {
	// tracked dit si le projet déclare un suivi. C'est ce que « configuré »
	// signifie depuis que la connexion est un réglage de projet : un drapeau
	// global répondait pour tout le monde.
	tracked   bool
	state     store.SyncState
	unmapped  []store.UnmappedStatus
	ambiguous []store.AmbiguousVersion
	pending   []string
	mapping   map[string]string
	set       []string
	reset     int
	moved     int64
	audit     []string
}

func (f *fakeSyncStore) GetSyncState(context.Context, string, int64) (store.SyncState, error) {
	return f.state, nil
}

func (f *fakeSyncStore) ResetSyncCursor(context.Context, string, int64) error {
	f.reset++
	return nil
}

func (f *fakeSyncStore) UnmappedStatuses(context.Context, int64) ([]store.UnmappedStatus, error) {
	return f.unmapped, nil
}

func (f *fakeSyncStore) AmbiguousVersions(context.Context, int64) ([]store.AmbiguousVersion, error) {
	return f.ambiguous, nil
}

func (f *fakeSyncStore) PendingVersions(context.Context, int64) ([]string, error) {
	return f.pending, nil
}

func (f *fakeSyncStore) StatusMapping(context.Context, int64) (map[string]string, error) {
	return f.mapping, nil
}

func (f *fakeSyncStore) SetStatusBucket(_ context.Context, _ int64, statusName, bucket string) error {
	f.set = append(f.set, statusName+"="+bucket)
	return nil
}

func (f *fakeSyncStore) ReprojectIssues(context.Context, int64) (int64, error) {
	return f.moved, nil
}

// ProjectBySlug résout le projet du chemin. Le suivi qu'il déclare — ou pas — est
// ce que l'écran lit désormais pour dire « configuré ».
func (f *fakeSyncStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "mse" {
		return store.Project{}, store.ErrProjectNotFound
	}
	project := store.Project{ID: 7, Slug: "mse"}
	if f.tracked {
		project.TrackerProject = "mse-dev"
	}
	return project, nil
}

// syncRequest compose une requête portant le projet dans son chemin.
func syncRequest(method, path, body string) *http.Request {
	request := adminRequest(method, path, body)
	request.SetPathValue("project", "mse")
	return request
}

func (f *fakeSyncStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	f.audit = append(f.audit, action+":"+target)
	return nil
}

// La projection **complète** doit être transmise, et pas seulement les statuts
// inconnus.
//
// C'est le contrat dont dépend le réglage dans l'interface. Il était rendu par
// l'API et jamais affiché : l'écran n'exposait que les statuts non mappés, donc rien
// du tout dès que l'amorçage les couvrait tous — ce qui est le cas sur l'instance
// réelle, où aucun statut n'est resté sans projection. Les cinq statuts qui attendent
// l'arbitrage du client étaient ainsi inaccessibles.
func TestLEtatPorteLaProjectionCompleteEtPasSeulementLesInconnus(t *testing.T) {
	fake := &fakeSyncStore{
		tracked: true,
		mapping: map[string]string{
			"Livré":         "doing",
			"Prêt à livrer": "doing",
			"Fermé":         "done",
			"Rejeté":        "rejected",
			"Nouveau":       "todo",
		},
		// Aucun statut inconnu : la situation réelle après l'amorçage.
		unmapped: nil,
	}
	handler := NewSyncAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.State(recorder, syncRequest(http.MethodGet, "/projects/mse/sync", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}

	var response struct {
		Configured bool              `json:"configured"`
		Mapping    map[string]string `json:"mapping"`
		Unmapped   []any             `json:"unmapped"`
		Pending    []string          `json:"pending"`
		Ambiguous  []any             `json:"ambiguous"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}

	if len(response.Mapping) != 5 {
		t.Errorf("%d statuts transmis, attendu 5 — la projection complète est ce qui rend le réglage possible",
			len(response.Mapping))
	}
	// Les statuts en attente d'arbitrage doivent en faire partie.
	for _, status := range []string{"Livré", "Prêt à livrer"} {
		if _, ok := response.Mapping[status]; !ok {
			t.Errorf("statut %q absent de la projection transmise", status)
		}
	}
	// Tableaux vides et non `null` : l'interface itère dessus sans garde.
	if response.Unmapped == nil || response.Pending == nil || response.Ambiguous == nil {
		t.Error("les listes vides doivent être des tableaux, non null")
	}
	if !response.Configured {
		t.Error("configured devrait être vrai")
	}
}

// Un projet **sans suivi déclaré** doit dire « non configuré » et non « jamais
// synchronisé », qui ferait chercher une panne.
//
// La question a changé de nature : elle portait sur la clé de l'instance, elle
// porte désormais sur ce que le projet déclare. Un projet qui n'a pas de suivi
// n'est pas en panne de synchronisation — il n'en attend aucune.
func TestUnProjetSansSuiviDitNonConfigure(t *testing.T) {
	handler := NewSyncAdminHandler(&fakeSyncStore{})

	recorder := httptest.NewRecorder()
	handler.State(recorder, syncRequest(http.MethodGet, "/projects/mse/sync", ""))

	var response struct {
		Configured bool `json:"configured"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if response.Configured {
		t.Error("configured devrait être faux sans clé")
	}
}

// Déclarer un statut réapplique la règle dans le même geste, et rend le nombre
// déplacé : sans cela la nouvelle règle ne vaudrait que pour les demandes modifiées
// ensuite dans Redmine, et l'écran mélangerait deux règles sans le dire.
func TestDeclarerUnStatutReappliqueEtRendLeNombreDeplace(t *testing.T) {
	fake := &fakeSyncStore{tracked: true, moved: 17}
	handler := NewSyncAdminHandler(fake)

	request := syncRequest(http.MethodPut, "/projects/mse/sync/redmine/mapping/Livr%C3%A9", `{"bucket":"done"}`)
	request.SetPathValue("status", "Livré")
	recorder := httptest.NewRecorder()
	handler.SetMapping(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.set) != 1 || fake.set[0] != "Livré=done" {
		t.Errorf("projection enregistrée: %v", fake.set)
	}

	var response struct {
		IssuesMoved int64 `json:"issues_moved"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if response.IssuesMoved != 17 {
		t.Errorf("%d demandes déplacées annoncées, attendu 17", response.IssuesMoved)
	}

	// Changer une projection déplace un taux affiché au client : c'est consigné.
	if len(fake.audit) != 1 || !strings.HasPrefix(fake.audit[0], "sync.redmine.mapping:") {
		t.Errorf("journal: %v", fake.audit)
	}
}

// Un seau inventé est refusé : la contrainte de base l'interdirait, mais une erreur
// 500 remontée d'une violation de contrainte ne dit pas à l'appelant ce qui est admis.
func TestUnSeauInventeEstRefuse(t *testing.T) {
	for _, bucket := range []string{"", "termine", "DONE", "en-cours"} {
		fake := &fakeSyncStore{}
		handler := NewSyncAdminHandler(fake)

		request := syncRequest(http.MethodPut, "/projects/mse/sync/redmine/mapping/X", `{"bucket":"`+bucket+`"}`)
		request.SetPathValue("status", "X")
		recorder := httptest.NewRecorder()
		handler.SetMapping(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("seau %q: code %d, attendu 400", bucket, recorder.Code)
		}
		if len(fake.set) != 0 {
			t.Errorf("seau %q enregistré malgré tout: %v", bucket, fake.set)
		}
	}
}

// La resynchronisation n'interroge pas Redmine : elle efface le curseur, et la passe
// suivante du worker relit tout. Le message doit dire l'attente, sans quoi un
// utilisateur qui ne voit rien changer relancerait en boucle.
func TestLaResynchronisationEffaceLeCurseurEtAnnonceLAttente(t *testing.T) {
	fake := &fakeSyncStore{}
	handler := NewSyncAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Resync(recorder, syncRequest(http.MethodPost, "/projects/mse/sync/redmine/resync", ""))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("code %d", recorder.Code)
	}
	if fake.reset != 1 {
		t.Errorf("%d réinitialisations", fake.reset)
	}

	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if !strings.Contains(response.Message, "cinq minutes") {
		t.Errorf("le message doit annoncer l'attente: %q", response.Message)
	}
	if len(fake.audit) != 1 {
		t.Errorf("journal: %v", fake.audit)
	}
}
