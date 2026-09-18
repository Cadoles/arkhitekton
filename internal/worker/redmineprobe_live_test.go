package worker

import (
	"context"
	"os"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/redmine"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * La sonde du suivi contre l'instance réelle.
 *
 * Gardé derrière `REDMINE_LIVE=1`, comme les vérifications du client : la clé est
 * renseignée dans l'environnement de développement courant, et faire dépendre le
 * test de sa seule présence ferait taper l'instance de **production** à chaque
 * `make test`.
 *
 *   REDMINE_LIVE=1 go test ./internal/worker/ -run Live -v
 *
 * # Ce qu'il éprouve, et que `internal/redmine` n'éprouve pas
 *
 * Celui-là vérifie que le format se **décode**. Celui-ci vérifie que les données
 * réelles, une fois décodées, produisent des faits **valides** — et c'est un autre
 * risque. Une version sans date de mise à jour, une demande sans sujet, un
 * identifiant vide : autant de choses qu'une fixture capturée un jour de beau
 * temps ne contient pas, et qui feraient échouer la sonde toutes les cinq minutes
 * sur un fait que la base refuse.
 *
 * Il mesure aussi ce que coûterait une **passe d'amorçage** : depuis la migration,
 * celle-ci enregistre les faits sans les aiguiller, et le nombre de lignes écrites
 * mérite d'être connu avant de l'être en production.
 *
 * # Il n'écrit rien
 *
 * La projection va dans un magasin en mémoire, et le transport de
 * `internal/redmine` refuse structurellement toute méthode autre que GET et HEAD.
 * Une exécution est sans effet, des deux côtés.
 */

// magasinEnMemoire reçoit la projection sans toucher à la base.
type magasinEnMemoire struct {
	versions []store.TrackedVersion
	issues   []store.TrackedIssue
}

func (m *magasinEnMemoire) UpsertRedmineVersion(_ context.Context, version store.TrackedVersion) (int64, error) {
	m.versions = append(m.versions, version)
	return int64(len(m.versions)), nil
}

func (m *magasinEnMemoire) UpsertRedmineIssue(_ context.Context, issue store.TrackedIssue) error {
	m.issues = append(m.issues, issue)
	return nil
}

func (m *magasinEnMemoire) AdoptPendingVersions(context.Context) ([]store.Adoption, error) {
	return nil, nil
}

func (m *magasinEnMemoire) StatusMapping(context.Context, int64) (map[string]string, error) {
	// Vide : ce test porte sur les faits, pas sur la correspondance des statuts.
	// Toutes les demandes tomberont donc dans `todo`, ce qui est sans conséquence
	// ici — le drapeau de clôture vient de Redmine, pas du seau.
	return nil, nil
}

func sondeLive(t *testing.T) (*TrackerSync, TrackerReader, string, *magasinEnMemoire) {
	t.Helper()
	if os.Getenv("REDMINE_LIVE") != "1" {
		t.Skip("REDMINE_LIVE non défini: vérification contre l'instance réelle ignorée")
	}

	project := os.Getenv("REDMINE_PROJECT")
	if project == "" {
		t.Fatal("REDMINE_PROJECT est requis pour la vérification réelle")
	}

	client, err := redmine.New(redmine.Options{
		URL:     os.Getenv("REDMINE_URL"),
		APIKey:  os.Getenv("REDMINE_API_KEY"),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		// Le message ne cite pas la clé, et n'a pas à le faire : ce qui manque se
		// lit dans le nom de la variable.
		t.Fatalf("client Redmine: %v", err)
	}

	magasin := &magasinEnMemoire{}
	connect := func(TrackerConnection) (TrackerReader, error) { return client, nil }
	return NewTrackerSync(magasin, connect, quietLogger()), client, project, magasin
}

// Les données réelles produisent-elles des faits que la base accepterait ?
//
// C'est la seule question que des fixtures ne peuvent pas trancher, puisqu'elles
// en sont issues. Un fait invalide ferait échouer la sonde à chaque passe, sur un
// projet précis, sans que rien d'autre ne bouge.
func TestLiveLesFaitsDuSuiviSontValides(t *testing.T) {
	sync, client, project, magasin := sondeLive(t)

	facts, cursor, err := sync.Observe(context.Background(), 1, project, client, "")
	if err != nil {
		t.Fatalf("passe: %v", err)
	}
	if cursor == "" {
		t.Error("aucun curseur rendu: la passe suivante relirait tout")
	}

	parType := map[string]int{}
	cles := map[string]event.Event{}
	for _, fact := range facts {
		if err := fact.Validate(); err != nil {
			// Le fait est décrit sans son contenu : un sujet de demande peut porter
			// le nom d'un client, et un journal de test n'a pas à le retenir.
			t.Errorf("fait %q invalide (clé %q): %v", fact.Type, fact.DedupKey, err)
			continue
		}
		if precedent, vu := cles[fact.DedupKey]; vu {
			t.Errorf("clé %q partagée par deux faits (%q et %q): l'un des deux "+
				"ne serait jamais enregistré", fact.DedupKey, precedent.Type, fact.Type)
		}
		cles[fact.DedupKey] = fact
		parType[string(fact.Type)]++

		// Une date manquante donnerait un fait de l'an zéro, enterré sous tout le
		// reste dans un fil trié par date.
		if fact.OccurredAt.IsZero() {
			t.Errorf("fait %q sans date (clé %q)", fact.Type, fact.DedupKey)
		}
	}

	// Ce que coûterait une passe d'amorçage en production : autant de lignes
	// écrites dans le fil des faits, sans qu'aucune réaction ne parte.
	t.Logf("projet %q: %d versions, %d demandes lues", project,
		len(magasin.versions), len(magasin.issues))
	t.Logf("faits d'un amorçage: %d au total — %d versions, %d clôtures",
		len(facts), parType[string(event.VersionCreated)], parType[redmineIssueClosed])
}

// Une seconde passe ne reconstate pas ce que la première a vu.
//
// C'est la propriété corrigée par la migration, éprouvée sur des données réelles
// plutôt que sur une sonde de test : les versions sont relues en entier à chaque
// passe, et seule la stabilité des clés empêche l'instantané de tout redéclencher.
func TestLiveUneSecondePasseNeReconstatePasLesVersions(t *testing.T) {
	sync, client, project, _ := sondeLive(t)

	premiere, cursor, err := sync.Observe(context.Background(), 1, project, client, "")
	if err != nil {
		t.Fatalf("première passe: %v", err)
	}

	seconde, _, err := sync.Observe(context.Background(), 1, project, client, cursor)
	if err != nil {
		t.Fatalf("seconde passe: %v", err)
	}

	vues := map[string]bool{}
	for _, fact := range premiere {
		vues[fact.DedupKey] = true
	}

	// Sans cette garde, le test passerait pour une mauvaise raison : une seconde
	// passe qui ne rendrait **rien** n'aurait aucune clé neuve, et ne prouverait
	// rien du tout. Ce qu'on veut voir, c'est qu'elle reconstate les mêmes
	// versions — l'instantané est relu en entier — et que leurs clés coïncident.
	if len(seconde) == 0 {
		t.Fatal("la seconde passe n'a rien rendu: elle ne peut rien prouver " +
			"sur la stabilité des clés")
	}
	t.Logf("passes: %d faits puis %d, dont %d clés déjà vues",
		len(premiere), len(seconde), communes(vues, seconde))

	nouvelles := 0
	for _, fact := range seconde {
		if !vues[fact.DedupKey] {
			nouvelles++
			t.Logf("clé neuve à la seconde passe: %q", fact.DedupKey)
		}
	}

	// Zéro, sauf si quelqu'un déclare une version entre les deux appels — ce qui
	// serait une coïncidence, pas un défaut. Le message le dit plutôt que
	// d'accuser le code.
	if nouvelles > 0 {
		t.Errorf("%d clés neuves entre deux passes immédiates: les versions "+
			"seraient reconstatées à chaque tour (ou une saisie a eu lieu entre-temps)",
			nouvelles)
	}
}

// communes compte les faits dont la clé était déjà connue.
func communes(vues map[string]bool, facts []event.Event) int {
	total := 0
	for _, fact := range facts {
		if vues[fact.DedupKey] {
			total++
		}
	}
	return total
}
