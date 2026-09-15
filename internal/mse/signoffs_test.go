package mse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type fakeSignoffStore struct {
	release      store.Release
	releaseErr   error
	environments []store.Environment
	signoffs     []store.Signoff
	set          []string
	deleted      []string
	deleteErr    error
	audit        []string
	unknownEnv   bool
}

func (f *fakeSignoffStore) GetRelease(context.Context, int64, string) (store.Release, error) {
	if f.releaseErr != nil {
		return store.Release{}, f.releaseErr
	}
	return f.release, nil
}

func (f *fakeSignoffStore) ListSignoffs(context.Context, int64) ([]store.Signoff, error) {
	return f.signoffs, nil
}

func (f *fakeSignoffStore) SetSignoff(_ context.Context, _ int64, environmentID, role, status, detail, decidedBy string) error {
	f.set = append(f.set, strings.Join([]string{environmentID, role, status, detail, decidedBy}, "|"))
	return nil
}

func (f *fakeSignoffStore) DeleteSignoff(_ context.Context, _ int64, environmentID, role string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, environmentID+"|"+role)
	return nil
}

func (f *fakeSignoffStore) SignoffEnvironmentExists(context.Context, string) (bool, error) {
	return !f.unknownEnv, nil
}

func (f *fakeSignoffStore) ListEnvironments(context.Context) ([]store.Environment, error) {
	return f.environments, nil
}

func (f *fakeSignoffStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	f.audit = append(f.audit, action+":"+target)
	return nil
}

func newSignoffStore() *fakeSignoffStore {
	return &fakeSignoffStore{
		release: store.Release{ID: 1, Tag: "1.8.0-rc1"},
		environments: []store.Environment{
			{ID: "qualif", Label: "Qualification", Rank: 2},
			{ID: "prod", Label: "Production", Rank: 4},
		},
	}
}

func signoffRequestFor(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.SetPathValue("project", projectSlug)
	request.SetPathValue("tag", "1.8.0-rc1")
	request.SetPathValue("env", "qualif")
	identity := auth.Identity{Subject: "121", Username: "vcarroy", Role: auth.RoleOperator}
	return request.WithContext(dashhttp.ContextWithIdentity(request.Context(), identity))
}

// Une réserve ou un refus sans motif ne dit rien à qui le lit ensuite, et c'est
// pourtant à ce moment que la question « pourquoi ? » se pose.
func TestUnRefusSansMotifEstRefuse(t *testing.T) {
	for _, status := range []string{"warn", "fail"} {
		fake := newSignoffStore()
		handler := NewSignoffsHandler(fake, quietLogger())

		request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
			`{"role":"Client","status":"`+status+`","detail":""}`)
		recorder := httptest.NewRecorder()
		handler.Set(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("avis %s sans motif: code %d, attendu 400", status, recorder.Code)
		}
		if len(fake.set) != 0 {
			t.Errorf("avis %s sans motif enregistré: %v", status, fake.set)
		}
	}
}

// Une validation n'a pas besoin de motif : c'est le cas courant, et l'exiger
// ferait écrire « ok » à tout le monde — un champ obligatoire vide de sens se
// remplit quand même.
func TestUneValidationSansMotifEstAcceptee(t *testing.T) {
	fake := newSignoffStore()
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
		`{"role":"Client","status":"pass"}`)
	recorder := httptest.NewRecorder()
	handler.Set(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("code %d, attendu 204: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.set) != 1 || !strings.HasPrefix(fake.set[0], "qualif|Client|pass|") {
		t.Errorf("avis enregistré: %v", fake.set)
	}
	// L'avis est enregistré sur le sujet réel et non sur le nom de compte : c'est
	// la même distinction qui avait fait échouer l'amorçage des administrateurs.
	if !strings.HasSuffix(fake.set[0], "|121") {
		t.Errorf("avis attribué à %q, attendu le sujet 121", fake.set[0])
	}
}

// `na` n'est pas enregistrable : l'attente est l'absence de ligne. L'accepter
// créerait deux représentations du même état, dont une que le retrait ne saurait
// pas distinguer.
func TestLAttenteNestPasUnAvisEnregistrable(t *testing.T) {
	for _, status := range []string{"na", "", "valide", "PASS"} {
		fake := newSignoffStore()
		handler := NewSignoffsHandler(fake, quietLogger())

		request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
			`{"role":"Client","status":"`+status+`","detail":"peu importe"}`)
		recorder := httptest.NewRecorder()
		handler.Set(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("statut %q: code %d, attendu 400", status, recorder.Code)
		}
	}
}

// Un avis sans intervenant ne peut pas être lu : « validé » sans dire par qui
// n'apprend rien.
func TestUnAvisSansRoleEstRefuse(t *testing.T) {
	fake := newSignoffStore()
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
		`{"role":"   ","status":"pass"}`)
	recorder := httptest.NewRecorder()
	handler.Set(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code %d, attendu 400", recorder.Code)
	}
}

// Un environnement non déclaré est une faute de l'appelant, pas une panne : 404
// avec son nom, et non 500 remonté d'une clé étrangère.
func TestUnEnvironnementNonDeclareRepond404(t *testing.T) {
	fake := newSignoffStore()
	fake.unknownEnv = true
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
		`{"role":"Client","status":"pass"}`)
	recorder := httptest.NewRecorder()
	handler.Set(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code %d, attendu 404", recorder.Code)
	}
}

// La décision est consignée : la table ne garde que le dernier état, et « qui
// avait validé avant de se rétracter » n'a de réponse que dans le journal.
func TestChaqueDecisionEstConsignee(t *testing.T) {
	fake := newSignoffStore()
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
		`{"role":"Client","status":"fail","detail":"panier cassé"}`)
	handler.Set(httptest.NewRecorder(), request)

	if len(fake.audit) != 1 || fake.audit[0] != store.AuditSignoffSet+":1.8.0-rc1" {
		t.Errorf("journal: %v", fake.audit)
	}

	retrait := signoffRequestFor(t, http.MethodDelete, "/releases/1.8.0-rc1/signoffs/qualif?role=Client", "")
	handler.Clear(httptest.NewRecorder(), retrait)

	if len(fake.audit) != 2 || fake.audit[1] != store.AuditSignoffClear+":1.8.0-rc1" {
		t.Errorf("journal après retrait: %v", fake.audit)
	}
}

// Le retrait exige le rôle : sans lui, on ne saurait pas quel avis effacer, et
// effacer tous les avis d'un environnement serait bien pire qu'une erreur 400.
func TestLeRetraitExigeUnRole(t *testing.T) {
	fake := newSignoffStore()
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodDelete, "/releases/1.8.0-rc1/signoffs/qualif", "")
	recorder := httptest.NewRecorder()
	handler.Clear(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code %d, attendu 400", recorder.Code)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("retrait effectué sans rôle: %v", fake.deleted)
	}
}

// Tous les environnements sont rendus, y compris ceux sans avis. Ne renvoyer que
// les validés laisserait l'interface incapable de distinguer « personne ne s'est
// prononcé » de « cet environnement n'existe pas ».
func TestTousLesEnvironnementsSontRendusMemeSansAvis(t *testing.T) {
	fake := newSignoffStore()
	fake.signoffs = []store.Signoff{
		{EnvironmentID: "qualif", Role: "Client", Status: "pass", DecidedBy: "121"},
	}
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodGet, "/releases/1.8.0-rc1/signoffs", "")
	recorder := httptest.NewRecorder()
	handler.List(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}

	var response struct {
		Environments []struct {
			Environment string `json:"environment"`
			Signoffs    []struct {
				Role string `json:"role"`
			} `json:"signoffs"`
		} `json:"environments"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}

	if len(response.Environments) != 2 {
		t.Fatalf("%d environnement(s), attendu 2", len(response.Environments))
	}
	if response.Environments[1].Environment != "prod" {
		t.Errorf("second environnement %q", response.Environments[1].Environment)
	}
	// Un tableau vide et non `null` : l'interface itère dessus sans garde.
	if response.Environments[1].Signoffs == nil {
		t.Error("les avis d'un environnement sans avis doivent être un tableau vide")
	}
}

// ProjectBySlug résout le projet nommé dans le chemin. Un seul ici : ces tests
// portent sur ce que le handler fait du projet, pas sur sa résolution.
func (f *fakeSignoffStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != projectSlug {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 1, Slug: slug, Repository: "vcarroy/test", Dockerfile: "Dockerfile"}, nil
}
