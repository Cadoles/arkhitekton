package redmine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

/*
 * Tests de décodage sur fixtures.
 *
 * Les fixtures sont synthétiques et non capturées telles quelles : les réponses
 * réelles portent des sujets de demandes — du contenu métier du client — et des
 * noms de personnes dans `assigned_to`. Les versionner reviendrait à faire entrer
 * des données du client et des données personnelles dans le dépôt, pour un gain de
 * couverture nul.
 *
 * Elles reproduisent en revanche les bizarreries réellement constatées sur
 * l'instance, qui sont la seule raison d'avoir des fixtures :
 *
 *   - quatre conventions de nommage des versions, dont deux noms distincts pour ce
 *     qui paraît être la même version (`MSE 3.1.2` close et `v3.1.2` ouverte) ;
 *   - deux trackers d'anomalie (`Anomalie` et `Anomalie (SDN4)`) ;
 *   - deux statuts ne différant que par la casse (`En Standby`, `En StandBy`) ;
 *   - `Ré-ouvert` déclaré clos par Redmine ;
 *   - des demandes sans version, des échéances et horodatages absents.
 *
 * La conformité au format réel est vérifiée par les tests `Live`, exécutés à la
 * demande contre l'instance.
 */

func fixtureServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if offset := r.URL.Query().Get("offset"); offset != "" && offset != "0" {
			key += "?offset=" + offset
		}
		name, ok := routes[key]
		if !ok {
			t.Errorf("route non prévue: %s", key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestLesVersionsSeDecodentAvecLeursQuatreConventions(t *testing.T) {
	server := fixtureServer(t, map[string]string{
		"/projects/mse-dev/versions.json": "versions.json",
	})

	versions, err := clientFor(t, server.URL).ListVersions(context.Background(), "mse-dev")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 6 {
		t.Fatalf("%d versions, attendu 6", len(versions))
	}

	// Le nom est conservé tel quel : l'identité est l'identifiant Redmine, et
	// normaliser à l'écriture perdrait l'information sans rien résoudre.
	byID := map[int64]Version{}
	for _, version := range versions {
		byID[version.ID] = version
	}

	if byID[801].Name != "MSE3-1.5" || byID[803].Name != "MSE 3.1.2" || byID[805].Name != "v3.2.0" {
		t.Errorf("noms altérés: %q %q %q", byID[801].Name, byID[803].Name, byID[805].Name)
	}

	// Deux versions distinctes portent le même numéro sous deux conventions. Elles
	// doivent rester deux lignes : les fusionner inventerait une donnée.
	if byID[803].ID == byID[804].ID {
		t.Error("MSE 3.1.2 et v3.1.2 doivent rester distinctes")
	}
	if byID[803].Status != "closed" || byID[804].Status != "open" {
		t.Errorf("statuts: 803=%q 804=%q", byID[803].Status, byID[804].Status)
	}

	// Échéance absente et horodatage vide ne sont pas des erreurs.
	if byID[802].DueDate != nil {
		t.Errorf("échéance inventée pour 802: %v", byID[802].DueDate)
	}
	if byID[806].UpdatedOn != nil {
		t.Errorf("horodatage inventé pour 806: %v", byID[806].UpdatedOn)
	}
	if got := byID[801].DueDate; got == nil || got.Format("2006-01-02") != "2025-04-14" {
		t.Errorf("échéance de 801: %v", got)
	}
}

func TestLesDemandesSeDecodentSurPlusieursPages(t *testing.T) {
	server := fixtureServer(t, map[string]string{
		"/issues.json":          "issues_page1.json",
		"/issues.json?offset=5": "issues_page2.json",
	})

	issues, err := clientFor(t, server.URL).ListIssues(context.Background(),
		IssueQuery{Project: "mse-dev"})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}

	// Les deux pages réunies, sans doublon ni omission.
	if len(issues) != 8 {
		t.Fatalf("%d demandes, attendu 8", len(issues))
	}
	seen := map[int64]int{}
	for _, issue := range issues {
		seen[issue.ID]++
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("demande %d rendue %d fois", id, count)
		}
	}
}

func TestUneDemandeSansVersionResteSansVersion(t *testing.T) {
	server := fixtureServer(t, map[string]string{
		"/issues.json":          "issues_page1.json",
		"/issues.json?offset=5": "issues_page2.json",
	})

	issues, err := clientFor(t, server.URL).ListIssues(context.Background(),
		IssueQuery{Project: "mse-dev"})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}

	byID := map[int64]Issue{}
	for _, issue := range issues {
		byID[issue.ID] = issue
	}

	if byID[9004].VersionID != nil {
		t.Errorf("version inventée pour 9004: %v", *byID[9004].VersionID)
	}
	if byID[9001].VersionID == nil || *byID[9001].VersionID != 805 {
		t.Errorf("version de 9001: %v", byID[9001].VersionID)
	}
}

// Le drapeau de Redmine est repris sans jugement, y compris quand il surprend :
// l'instance déclare `Ré-ouvert` comme clos. La projection en bucket est une
// décision distincte, portée par une table éditable.
func TestLeDrapeauDeClotureEstReprisSansJugement(t *testing.T) {
	server := fixtureServer(t, map[string]string{
		"/issues.json":          "issues_page1.json",
		"/issues.json?offset=5": "issues_page2.json",
	})

	issues, err := clientFor(t, server.URL).ListIssues(context.Background(),
		IssueQuery{Project: "mse-dev"})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}

	byID := map[int64]Issue{}
	for _, issue := range issues {
		byID[issue.ID] = issue
	}

	reouverte := byID[9005]
	if reouverte.Status != "Ré-ouvert" || !reouverte.StatusClosed {
		t.Errorf("9005: statut %q clos=%v — le drapeau de Redmine doit être conservé",
			reouverte.Status, reouverte.StatusClosed)
	}

	// Les deux trackers d'anomalie sont distincts : compter les anomalies sur
	// l'égalité à « Anomalie » en manquerait une sur deux.
	if byID[9002].Tracker != "Anomalie" || byID[9003].Tracker != "Anomalie (SDN4)" {
		t.Errorf("trackers: %q et %q", byID[9002].Tracker, byID[9003].Tracker)
	}

	// Deux statuts ne différant que par la casse coexistent dans l'instance.
	if byID[9008].Status != "En StandBy" {
		t.Errorf("9008: statut %q", byID[9008].Status)
	}
}

// `status_id=*` est indispensable : sans lui Redmine ne rend que les demandes
// ouvertes, et une version terminée paraîtrait vide — une perte de données
// silencieuse.
func TestLaRequeteDemandeTousLesStatutsEtUnTriStable(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{"issues":[],"total_count":0}`))
	}))
	defer server.Close()

	if _, err := clientFor(t, server.URL).ListIssues(context.Background(),
		IssueQuery{Project: "mse-dev", Since: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("demandes: %v", err)
	}

	for _, expected := range []string{
		"status_id=%2A",
		"sort=updated_on",
		"project_id=mse-dev",
		"updated_on=%3E%3D2026-08-01T00%3A00%3A00Z",
	} {
		if !strings.Contains(query, expected) {
			t.Errorf("la requête ne porte pas %s: %s", expected, query)
		}
	}
}

// Une page vide alors que le total en annonce davantage signalerait une incohérence
// côté Redmine. Boucler dessus tournerait sans fin.
func TestUnePageVideArreteLaPagination(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(`{"issues":[],"total_count":5000}`))
	}))
	defer server.Close()

	issues, err := clientFor(t, server.URL).ListIssues(context.Background(),
		IssueQuery{Project: "mse-dev"})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("%d demandes rendues", len(issues))
	}
	if calls != 1 {
		t.Errorf("%d appels: la pagination doit s'arrêter sur une page vide", calls)
	}
}

// Un projet non renseigné est une faute d'appel, détectée avant tout appel réseau.
func TestUnProjetAbsentEstRefuseAvantAppel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("aucun appel ne devrait partir")
	}))
	defer server.Close()

	client := clientFor(t, server.URL)
	if _, err := client.ListVersions(context.Background(), ""); err == nil {
		t.Error("ListVersions devrait refuser un projet vide")
	}
	if _, err := client.ListIssues(context.Background(), IssueQuery{}); err == nil {
		t.Error("ListIssues devrait refuser un projet vide")
	}
}
