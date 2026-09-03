package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La sonde des demandes de fusion, contre une fausse forge.
 *
 * Ce qui se joue ici est ce que la sonde doit tenir elle-même — la boucle tient
 * le reste : la **clé de déduplication**, dérivée du dépôt et du numéro, et la
 * **date du fait**, celle de l'ouverture et jamais celle de la lecture.
 */

// forgeDeTest sert une liste de demandes ouvertes.
func forgeDeTest(t *testing.T, corps string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pulls") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("état demandé = %q, attendu open", r.URL.Query().Get("state"))
		}
		// La seconde page est vide : la pagination s'arrête sur une page courte.
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(corps))
	}))
}

const deuxDemandes = `[
  {"number":412,"title":"Corriger le clone","html_url":"https://forge.example.fr/CNOUS/mse/pulls/412",
   "user":{"login":"vcarroy"},"head":{"ref":"fix/clone"},"base":{"ref":"main"},
   "created_at":"2026-09-03T10:30:00Z"},
  {"number":405,"title":"Ancienne demande","html_url":"https://forge.example.fr/CNOUS/mse/pulls/405",
   "user":{"login":"autre"},"head":{"ref":"feat/x"},"base":{"ref":"main"},
   "created_at":"2026-08-01T09:00:00Z"}
]`

func configDeForge(url string) map[string]string {
	return map[string]string{"url": url, "token": "jeton"}
}

// Une demande ouverte devient un fait, daté de son ouverture et identifié par son
// dépôt et son numéro.
func TestUneDemandeOuverteDevientUnFaitDateDeSonOuverture(t *testing.T) {
	serveur := forgeDeTest(t, deuxDemandes)
	defer serveur.Close()

	facts, cursor, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"},
		configDeForge(serveur.URL), "")
	if err != nil {
		t.Fatalf("sonde: %v", err)
	}

	if len(facts) != 2 {
		t.Fatalf("%d faits, attendu 2", len(facts))
	}

	premier := facts[0]
	if premier.Type != "gitea.pull_request.opened" {
		t.Errorf("type = %q — il doit être préfixé par le plugin", premier.Type)
	}
	if premier.Subject != "#412" {
		t.Errorf("sujet = %q", premier.Subject)
	}
	// La date du **fait**, pas celle de la lecture.
	if !premier.OccurredAt.Equal(time.Date(2026, 9, 3, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("instant = %v, attendu celui de l'ouverture", premier.OccurredAt)
	}
	// La clé porte le dépôt **et** le numéro : un numéro seul se répéterait d'un
	// dépôt à l'autre, et deux projets se voleraient leurs faits.
	if premier.DedupKey != "gitea.pull_request.opened:CNOUS/mse:412" {
		t.Errorf("clé = %q", premier.DedupKey)
	}
	for _, champ := range []string{"number", "title", "author", "source_branch", "target_branch", "url"} {
		if premier.Payload[champ] == "" {
			t.Errorf("charge sans %q: %v", champ, premier.Payload)
		}
	}

	// Le curseur retient la plus récente, pour que la passe suivante reparte de là.
	if cursor != "2026-09-03T10:30:00Z" {
		t.Errorf("curseur = %q", cursor)
	}
}

// Le curseur écarte ce qui a été ouvert avant lui.
//
// La date de **création** décide, jamais celle de mise à jour : une demande
// ouverte il y a un mois et commentée ce matin n'est pas un fait nouveau, et la
// traiter comme tel renotifierait à chaque commentaire.
func TestLeCurseurEcarteCeQuiEstAnterieur(t *testing.T) {
	serveur := forgeDeTest(t, deuxDemandes)
	defer serveur.Close()

	facts, _, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"},
		configDeForge(serveur.URL), "2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatalf("sonde: %v", err)
	}

	if len(facts) != 1 || facts[0].Subject != "#412" {
		t.Errorf("faits = %d, attendu la seule demande postérieure au curseur", len(facts))
	}
}

// Le curseur ne recule jamais.
//
// Une passe qui ne voit rien de neuf rend le curseur précédent, plutôt qu'un zéro
// qui relancerait un amorçage et perdrait tout l'historique de déduplication.
func TestLeCurseurNeReculeJamais(t *testing.T) {
	serveur := forgeDeTest(t, `[]`)
	defer serveur.Close()

	_, cursor, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"},
		configDeForge(serveur.URL), "2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatalf("sonde: %v", err)
	}
	if cursor != "2026-09-01T00:00:00Z" {
		t.Errorf("curseur = %q, attendu inchangé", cursor)
	}
}

// Un dépôt sans aucune demande sort tout de même de l'amorçage.
//
// Sans cela, il y resterait éternellement — et la première demande ouverte
// n'émettrait rien, puisque la boucle n'émet pas à l'amorçage.
func TestUnDepotSansDemandeSortDeLAmorcage(t *testing.T) {
	serveur := forgeDeTest(t, `[]`)
	defer serveur.Close()

	_, cursor, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"},
		configDeForge(serveur.URL), "")
	if err != nil {
		t.Fatalf("sonde: %v", err)
	}
	if cursor == "" {
		t.Error("curseur vide : le projet resterait en amorçage pour toujours")
	}
}

// Une forge non configurée est nommée comme telle, plutôt que laissée au client.
//
// « URL invalide » enverrait chercher une faute de frappe là où c'est un réglage
// jamais saisi.
func TestUneForgeNonConfigureeEstNommeeCommeTelle(t *testing.T) {
	probe := NewGiteaPullProbe(time.Second)
	project := store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"}

	for _, config := range []map[string]string{
		{},
		{"url": "https://forge.example.fr"},
		{"token": "jeton"},
	} {
		_, _, err := probe.Poll(context.Background(), project, config, "")
		if err == nil || !strings.Contains(err.Error(), "non configurée") {
			t.Errorf("config %v: %v", config, err)
		}
	}
}

// Un projet sans dépôt déclaré est refusé nommément : la sonde n'a rien à
// interroger, et le dire évite une erreur d'URL qui ne désignerait pas la cause.
func TestUnProjetSansDepotEstRefuseNommement(t *testing.T) {
	_, _, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(), store.Project{ID: 1, Slug: "mse"},
		configDeForge("https://forge.example.fr"), "")

	if err == nil || !strings.Contains(err.Error(), "dépôt") {
		t.Errorf("%v", err)
	}
}

// La sonde se déclare : son plugin, et son curseur propre.
func TestLaSondeSeDeclare(t *testing.T) {
	probe := NewGiteaPullProbe(time.Second)

	if probe.PluginID() != "gitea" {
		t.Errorf("plugin = %q", probe.PluginID())
	}
	// Propre à la sonde et non au plugin : Gitea pourrait en porter d'autres, et
	// un curseur partagé ferait que l'une avance celui de l'autre.
	if probe.Source() != "gitea.pull_requests" {
		t.Errorf("source = %q", probe.Source())
	}
}
