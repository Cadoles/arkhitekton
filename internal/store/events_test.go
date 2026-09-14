package store

import (
	"context"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
)

/*
 * Les faits, et la seule propriété qui compte à ce stade : une redélivrance
 * n'écrit rien.
 *
 * L'observateur repasse sur les runs terminés — resynchronisation de l'informer,
 * redémarrage du worker. Sans cette borne, chaque passage produirait un doublon.
 * Aujourd'hui personne n'écoute et cela ne coûterait rien ; le jour où une
 * réaction enverra une notification, cela en enverrait une par redémarrage.
 */

func faitDeTest(cle string) event.Event {
	return event.Event{
		Type: event.ReleaseCreated, ProjectID: ptr(testProjectID),
		Subject:    "1.8.0-rc1",
		Payload:    map[string]string{"tag": "1.8.0-rc1", "kind": "pre"},
		OccurredAt: time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC),
		DedupKey:   cle,
	}
}

func ptr(value int64) *int64 { return &value }

// Le fait qui porte tout le mécanisme : réémettre n'écrit rien, et le dit.
func TestUneRedelivranceNecritRien(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	recorded, err := db.RecordEvent(ctx, faitDeTest("release.created:42"))
	if err != nil {
		t.Fatalf("première émission: %v", err)
	}
	if !recorded {
		t.Fatal("le premier constat d'un fait doit s'écrire")
	}

	// La redélivrance. Le fait est le même ; seule l'heure de lecture a changé.
	repete := faitDeTest("release.created:42")
	repete.OccurredAt = repete.OccurredAt.Add(time.Hour)
	recorded, err = db.RecordEvent(ctx, repete)
	if err != nil {
		t.Fatalf("redélivrance: %v", err)
	}
	if recorded {
		t.Error("une redélivrance a produit un second fait")
	}

	events, err := db.ListEvents(ctx, testProjectID, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d faits enregistrés, attendu 1", len(events))
	}
	// Et c'est le **premier** qui subsiste : `do nothing` ne remplace pas. Un
	// écrasement ferait avancer l'instant du fait à chaque redémarrage, et
	// l'histoire ne serait plus datée de ce qui s'est passé.
	//
	// Comparé en UTC : PostgreSQL rend un `timestamptz` dans le fuseau de la
	// session, et comparer une heure locale ferait échouer ce test selon la
	// machine qui l'exécute.
	if got := events[0].OccurredAt.UTC(); !got.Equal(faitDeTest("").OccurredAt) {
		t.Errorf("instant = %v, attendu celui du premier constat", got)
	}
}

// Deux faits distincts coexistent : la borne porte sur la clé, pas sur le type.
func TestDeuxFaitsDistinctsCoexistent(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	for _, cle := range []string{"release.created:42", "release.created:43"} {
		if _, err := db.RecordEvent(ctx, faitDeTest(cle)); err != nil {
			t.Fatalf("émission %q: %v", cle, err)
		}
	}

	events, err := db.ListEvents(ctx, testProjectID, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("%d faits, attendu 2 — deux releases sont deux faits", len(events))
	}
}

// Un fait invalide est refusé avant d'atteindre la base.
//
// La clé absente en particulier : l'insertion aurait réussi sur une colonne
// vide, et le défaut ne se serait vu qu'une fois une réaction branchée.
func TestUnFaitInvalideNatteintPasLaBase(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	sansCle := faitDeTest("")
	if _, err := db.RecordEvent(ctx, sansCle); err == nil {
		t.Fatal("un fait sans clé a été accepté")
	}

	events, _ := db.ListEvents(ctx, testProjectID, 0)
	if len(events) != 0 {
		t.Errorf("%d faits écrits malgré le refus", len(events))
	}
}

// Le fil est rendu du plus récent au plus ancien : c'est l'ordre dans lequel on
// vient chercher ce qui vient de se passer.
func TestLeFilRendLePlusRecentDAbord(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	for index, decalage := range []time.Duration{0, 2 * time.Hour, time.Hour} {
		fait := faitDeTest("fait-" + string(rune('a'+index)))
		fait.OccurredAt = base.Add(decalage)
		if _, err := db.RecordEvent(ctx, fait); err != nil {
			t.Fatalf("émission: %v", err)
		}
	}

	events, err := db.ListEvents(ctx, testProjectID, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("%d faits", len(events))
	}
	for index := 1; index < len(events); index++ {
		if events[index].OccurredAt.After(events[index-1].OccurredAt) {
			t.Errorf("ordre rompu en position %d: %v après %v",
				index, events[index].OccurredAt, events[index-1].OccurredAt)
		}
	}
}

// La charge traverse la base sans se déformer, et le fil est **borné au projet**.
func TestLaChargeTraverseEtLeFilResteAuProjet(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.RecordEvent(ctx, faitDeTest("release.created:42")); err != nil {
		t.Fatalf("émission: %v", err)
	}

	events, err := db.ListEvents(ctx, testProjectID, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d faits", len(events))
	}
	if events[0].Payload["tag"] != "1.8.0-rc1" || events[0].Payload["kind"] != "pre" {
		t.Errorf("charge = %v", events[0].Payload)
	}
	if events[0].Source != "" {
		t.Errorf("source = %q, attendue vide pour le noyau", events[0].Source)
	}

	// Un autre projet ne voit rien : le fil répond « il ne s'est rien passé
	// **ici** », ce qui est la question posée.
	autre, err := db.CreateProject(ctx, Project{
		Repository: "vcarroy/autre", Slug: "autre", Label: "Autre",
		BuildContext: ".",
	})
	if err != nil {
		t.Fatalf("projet: %v", err)
	}
	voisins, err := db.ListEvents(ctx, autre, 0)
	if err != nil {
		t.Fatalf("lecture du voisin: %v", err)
	}
	if len(voisins) != 0 {
		t.Errorf("%d faits ont débordé sur un autre projet", len(voisins))
	}
}
