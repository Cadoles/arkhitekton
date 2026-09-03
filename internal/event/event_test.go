package event

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var instant = time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)

/*
 * La règle de déduplication.
 *
 * C'est tout l'objet du paquet. L'observateur redélivre les runs terminés —
 * resynchronisation de l'informer, redémarrage du worker — et un fait sans clé
 * stable serait réémis à chaque fois. Aujourd'hui cela ne coûterait rien ; le
 * jour où une réaction enverra une notification, cela en enverrait une par
 * redémarrage.
 */

// Le même fait, constaté deux fois, porte la même clé.
func TestLeMemeFaitPorteLaMemeCle(t *testing.T) {
	premier := ReleaseBuiltAt(instant, 7, 42, 1381, "1.8.0-rc1", Succeeded, "abc123")
	// Une redélivrance survient plus tard, et rien d'autre ne change.
	second := ReleaseBuiltAt(instant.Add(time.Hour), 7, 42, 1381, "1.8.0-rc1", Succeeded, "abc123")

	if premier.DedupKey != second.DedupKey {
		t.Errorf("clés %q et %q — une redélivrance doit être reconnue",
			premier.DedupKey, second.DedupKey)
	}
}

// Une reconstruction est un **fait distinct**, et sa clé le dit.
//
// C'est le geste qui répare un échec d'infrastructure sans faire porter au numéro
// de version l'historique des pannes du cluster. Une clé sur la seule release
// ferait taire toutes les constructions après la première.
func TestUneReconstructionEstUnFaitDistinct(t *testing.T) {
	premiere := ReleaseBuiltAt(instant, 7, 42, 1381, "1.8.0-rc1", Failed, "abc123")
	reprise := ReleaseBuiltAt(instant, 7, 42, 1402, "1.8.0-rc1", Succeeded, "abc123")

	if premiere.DedupKey == reprise.DedupKey {
		t.Errorf("clé partagée %q — la seconde construction serait muette", premiere.DedupKey)
	}
}

// Une reprise de vérification conclut à nouveau, et le fait doit repartir.
//
// La clé porte le job **qui a conclu** — la dernière vérification à rendre son
// verdict. Sans cela, un essai réussi après un échec ne produirait rien, et une
// réaction ne verrait jamais qu'une release s'est relevée.
func TestUneRepriseDeVerificationProduitUnSecondFait(t *testing.T) {
	echec := ReleaseVerifiedAt(instant, 7, 42, 900, "1.8.0-rc1", Failed, []string{"unit"})
	reprise := ReleaseVerifiedAt(instant, 7, 42, 950, "1.8.0-rc1", Succeeded, []string{"unit"})

	if echec.DedupKey == reprise.DedupKey {
		t.Errorf("clé partagée %q — la reprise serait muette", echec.DedupKey)
	}
}

// Les tentatives d'un job sont autant de runs, donc autant de faits.
//
// Un job en échec est remis en file : une clé portant le job ferait disparaître
// toutes les tentatives après la première, alors que chacune est un run distinct
// avec son propre journal.
func TestLesTentativesDUnJobSontDesFaitsDistincts(t *testing.T) {
	premiere := PipelineFinishedAt(instant, projet(7), 501, 1381, "mse-unit-1", "unit-tests", Failed, nil)
	seconde := PipelineFinishedAt(instant, projet(7), 502, 1381, "mse-unit-2", "unit-tests", Succeeded, nil)

	if premiere.DedupKey == seconde.DedupKey {
		t.Errorf("clé partagée %q — la seconde tentative serait muette", premiere.DedupKey)
	}
}

// Deux faits de nature différente ne se confondent jamais, même sur le même
// objet : la clé porte le type.
func TestDeuxNaturesNeSeConfondentPas(t *testing.T) {
	built := ReleaseBuiltAt(instant, 7, 42, 1381, "1.8.0-rc1", Succeeded, "abc")
	verified := ReleaseVerifiedAt(instant, 7, 42, 1381, "1.8.0-rc1", Succeeded, nil)

	if built.DedupKey == verified.DedupKey {
		t.Errorf("clé partagée %q entre deux natures", built.DedupKey)
	}
}

/*
 * Ce qu'un fait doit porter pour être exploitable.
 */

// Un fait sans clé est refusé, et le message dit pourquoi.
//
// Le contrôle qui compte : sans lui, l'insertion réussirait et le défaut ne se
// verrait qu'une fois une réaction branchée, très loin d'ici.
func TestUnFaitSansCleEstRefuse(t *testing.T) {
	fait := ReleaseBuiltAt(instant, 7, 42, 1381, "1.8.0-rc1", Succeeded, "abc")
	fait.DedupKey = ""

	err := fait.Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("validation: %v, attendu ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "redélivrance") {
		t.Errorf("message %q — il doit dire la conséquence", err)
	}
}

// Un fait sans instant est refusé : c'est la date du fait qui l'ordonne, et zéro
// le placerait en 1970, avant tout le reste.
func TestUnFaitSansInstantEstRefuse(t *testing.T) {
	fait := ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0")
	fait.OccurredAt = time.Time{}

	if err := fait.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("validation: %v, attendu ErrInvalid", err)
	}
}

// Les faits que les constructeurs produisent se valident tous.
//
// Une garde contre l'oubli : ajouter un constructeur sans lui donner de clé
// passerait sinon inaperçu jusqu'à la première émission réelle.
func TestTousLesConstructeursProduisentDesFaitsValides(t *testing.T) {
	release := int64(42)
	faits := []Event{
		ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0"),
		ReleaseBuiltAt(instant, 7, 42, 1381, "1.8.0-rc1", Succeeded, "abc"),
		ReleaseVerifiedAt(instant, 7, 42, 1381, "1.8.0-rc1", Succeeded, []string{"unit"}),
		ReleaseReleasedAt(instant, 7, 42, "1.8.0", "vcarroy"),
		ReleasePromotedAt(instant, 7, 42, 1400, "1.8.0", "preprod"),
		PipelineFinishedAt(instant, projet(7), 501, 1381, "mse-unit-1", "unit-tests", Succeeded, &release),
	}

	for _, fait := range faits {
		if err := fait.Validate(); err != nil {
			t.Errorf("fait %q invalide: %v", fait.Type, err)
		}
		if fait.Subject == "" {
			t.Errorf("fait %q sans sujet — l'écran n'aurait rien à afficher", fait.Type)
		}
		if fait.Source != "" {
			t.Errorf("fait %q: source %q, attendue vide pour le noyau", fait.Type, fait.Source)
		}
		if fait.Depth != 0 {
			t.Errorf("fait %q: profondeur %d, attendue nulle — rien ne réagit encore",
				fait.Type, fait.Depth)
		}
	}
}

// Un run sans release ne s'invente pas de rattachement : la charge n'en porte
// simplement pas, et une valeur nulle textuelle désignerait la release 0.
func TestUnRunSansReleaseNInventePasDeRattachement(t *testing.T) {
	fait := PipelineFinishedAt(instant, nil, 501, 1381, "mse-unit-1", "unit-tests", Succeeded, nil)

	if _, present := fait.Payload["release_id"]; present {
		t.Errorf("rattachement inventé: %v", fait.Payload)
	}
	if fait.ProjectID != nil {
		t.Error("projet inventé sur un run qui n'en vise aucun")
	}
}

func projet(id int64) *int64 { return &id }
