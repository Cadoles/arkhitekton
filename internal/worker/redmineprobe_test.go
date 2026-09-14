package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les faits que le suivi constate.
 *
 * Ce que la migration ajoute au produit — jusqu'ici, la synchronisation projetait
 * en base et personne ne pouvait s'y brancher.
 */

func instant(moment time.Time) *time.Time { return &moment }

// Les versions et les demandes closes deviennent des faits ; le reste, non.
func TestLeSuiviConstateLesVersionsEtLesDemandesCloses(t *testing.T) {
	echeance := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	client := &fakeRedmine{
		versions: []provider.Version{
			{ID: "805", Name: "v3.2.0", Status: "open", DueDate: &echeance,
				UpdatedOn: instant(time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC))},
		},
		issues: []provider.Issue{
			{ID: "1", Tracker: "Anomalie", Status: "Fermé", StatusClosed: true,
				UpdatedOn: instant(time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC))},
			// Ouverte : elle se projette, mais ne constate rien. Un fait par
			// changement de statut ferait du fil des faits un journal de saisie.
			{ID: "2", Tracker: "Évolution", Status: "En cours"},
		},
	}
	db := &fakeRedmineStore{}

	facts, _, err := passe(t, client, db)
	if err != nil {
		t.Fatalf("%v", err)
	}

	parType := map[string]int{}
	for _, fact := range facts {
		parType[string(fact.Type)]++
		if err := fact.Validate(); err != nil {
			t.Errorf("fait %q invalide: %v", fact.Type, err)
		}
		// La date du **fait**, celle du suivi — jamais celle de la lecture. Une
		// sonde qui rattrape trois jours doit produire des faits datés de ces
		// trois jours, sinon l'ordre est perdu.
		if fact.OccurredAt.After(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("fait %q daté de la lecture: %v", fact.Type, fact.OccurredAt)
		}
	}

	if parType[string(event.VersionCreated)] != 1 {
		t.Errorf("%d faits de version, attendu 1", parType[string(event.VersionCreated)])
	}
	if parType[redmineIssueClosed] != 1 {
		t.Errorf("%d faits de clôture, attendu 1 — la demande ouverte n'en produit pas",
			parType[redmineIssueClosed])
	}
}

// La clé de déduplication ne bouge pas d'une passe à l'autre.
//
// C'est elle qui porte la sémantique de « created » : chaque passe relit toutes
// les versions, et seule l'unicité de la clé fait qu'une version déjà vue n'est
// pas reconstatée. Une clé qui varierait — parce qu'elle porterait la date de
// mise à jour, par exemple — redéclencherait à chaque modification.
func TestLaCleDUnFaitDuSuiviNeBougePasDUnePasseALautre(t *testing.T) {
	version := provider.Version{ID: "805", Name: "v3.2.0", Status: "open"}
	client := &fakeRedmine{versions: []provider.Version{version}}
	// **Le même magasin** pour les deux passes, comme en production : la clé porte
	// désormais la ligne locale, et deux magasins neufs prouveraient seulement que
	// deux compteurs partis de zéro donnent le même nombre.
	db := &fakeRedmineStore{}

	premiere, _, err := passe(t, client, db)
	if err != nil {
		t.Fatalf("%v", err)
	}

	// La version change de statut et de date : elle reste la même version.
	client.versions[0].Status = "closed"
	client.versions[0].UpdatedOn = instant(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	seconde, _, err := passe(t, client, db)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if len(premiere) != 1 || len(seconde) != 1 {
		t.Fatalf("faits = %d puis %d", len(premiere), len(seconde))
	}
	if premiere[0].DedupKey != seconde[0].DedupKey {
		t.Errorf("clés %q puis %q — la version serait reconstatée",
			premiere[0].DedupKey, seconde[0].DedupKey)
	}
}

// Deux projets ne partagent pas la clé, et ce n'est plus pour la même raison.
//
// # Ce que le passage au noyau a déplacé
//
// La clé portait le projet du suivi, parce que deux instances Redmine numérotent
// chacune à partir de 1. Elle porte désormais la **ligne locale**, dont l'unicité
// est `(project_id, tool, external_id)` — le projet y est donc encore, mais une
// fois, et du côté du dashboard.
//
// Ce que cela gagne : `version.created` est devenu un fait du noyau (doc 15
// §15.3), et sa clé ne dépend plus de l'outil qui l'a observé. Une instance qui
// changerait de suivi ne reconstaterait pas tout son historique.
func TestDeuxProjetsNePartagentPasLaCleDUneVersion(t *testing.T) {
	client := &fakeRedmine{versions: []provider.Version{{ID: "805", Name: "v3.2.0"}}}
	sync := newSync(t, client, &fakeRedmineStore{})

	un, _, err := sync.Observe(context.Background(), 1, "projet-un", client, "curseur")
	if err != nil {
		t.Fatalf("%v", err)
	}
	deux, _, err := sync.Observe(context.Background(), 2, "projet-deux", client, "curseur")
	if err != nil {
		t.Fatalf("%v", err)
	}

	if un[0].DedupKey == deux[0].DedupKey {
		t.Errorf("deux projets partagent la clé %q", un[0].DedupKey)
	}
	// Et le nom du projet dans le suivi n'y est pour rien : le mettre dans la clé
	// ferait qu'un projet renommé chez le client reconstaterait ses versions.
	for _, nom := range []string{"projet-un", "projet-deux"} {
		if strings.Contains(un[0].DedupKey, nom) || strings.Contains(deux[0].DedupKey, nom) {
			t.Errorf("clés %q / %q — elles portent le projet du suivi",
				un[0].DedupKey, deux[0].DedupKey)
		}
	}
}

// La charge nomme l'outil et l'identifiant qu'il emploie.
//
// C'est ce que la clé a cessé de porter, et il faut bien que ça vive quelque
// part : sans eux, un fait du noyau ne permettrait plus de remonter à la fiche
// dont il parle.
func TestLaChargeDUneVersionNommeLOutilEtSonIdentifiant(t *testing.T) {
	client := &fakeRedmine{versions: []provider.Version{{ID: "805", Name: "v3.2.0"}}}

	facts, _, err := passe(t, client, &fakeRedmineStore{})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("%d faits", len(facts))
	}
	if facts[0].Payload["tool"] != trackerTool || facts[0].Payload["external_id"] != "805" {
		t.Errorf("charge = %v", facts[0].Payload)
	}
}

// Un projet sans identifiant de suivi est ignoré, en silence.
//
// # Une correction de jugement
//
// Ce test exigeait l'inverse : que la sonde **refuse** nommément, au motif qu'un
// plugin allumé sur un projet est une intention. Le journal de production a
// montré que non — le plugin est activé au niveau de l'instance, et la plupart
// des projets n'ont pas de correspondance dans le suivi. Le cas normal criait
// donc toutes les cinq minutes, pour chacun d'eux.
func TestUnProjetSansIdentifiantDeSuiviEstIgnore(t *testing.T) {
	sonde := NewRedmineProbe(NewTrackerSync(&fakeRedmineStore{},
		func(TrackerConnection) (TrackerReader, error) {
			t.Error("aucune connexion ne doit être ouverte")
			return &fakeRedmine{}, nil
		},
		quietLogger()))

	facts, cursor, err := sonde.Poll(context.Background(),
		store.Project{ID: 1, Slug: "mse"}, map[string]string{"url": "https://r.fr"}, "curseur")

	if err != nil {
		t.Fatalf("%v — l'absence de suivi n'est pas une panne", err)
	}
	if len(facts) != 0 {
		t.Errorf("%d fait(s) rendus", len(facts))
	}
	// Le curseur est rendu inchangé : rien n'a été lu, donc rien n'a avancé.
	if cursor != "curseur" {
		t.Errorf("curseur = %q, attendu inchangé", cursor)
	}
}
