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
//
// La sonde du suivi rend un **instantané** : elle relit toutes les versions à
// chaque passe, parce que l'API de Redmine ne les filtre pas. C'est donc le cas
// nominal, et non un incident de redélivrance.
func TestLeMemeFaitPorteLaMemeCle(t *testing.T) {
	premier := VersionCreatedAt(instant, 805, "v3.2.0", "open", "redmine", "412", nil)
	// La passe suivante relit la même version, plus tard, et son statut a changé.
	second := VersionCreatedAt(instant.Add(time.Hour), 805, "v3.2.0", "closed", "redmine", "412", nil)

	if premier.DedupKey != second.DedupKey {
		t.Errorf("clés %q et %q — une version déjà vue serait reconstatée",
			premier.DedupKey, second.DedupKey)
	}
}

// La clé d'une version porte la ligne **locale**, pas l'identifiant du suivi.
//
// C'est ce qui rend le fait indépendant de l'outil : deux suivis branchés sur le
// même projet ne constatent pas deux fois la même version, et une instance qui
// changerait d'outil ne reconstaterait pas tout son historique.
func TestLaCleDUneVersionPorteLaLigneLocale(t *testing.T) {
	redmine := VersionCreatedAt(instant, 805, "v3.2.0", "open", "redmine", "412", nil)
	autre := VersionCreatedAt(instant, 805, "v3.2.0", "open", "jira", "MSE-9", nil)

	if redmine.DedupKey != autre.DedupKey {
		t.Errorf("clés %q et %q — la même version serait constatée deux fois",
			redmine.DedupKey, autre.DedupKey)
	}
	if !strings.Contains(redmine.DedupKey, "805") {
		t.Errorf("clé %q — elle doit porter la version locale", redmine.DedupKey)
	}
}

// Deux faits de nature différente ne se confondent jamais, même sur le même
// objet : la clé porte le type.
func TestDeuxNaturesNeSeConfondentPas(t *testing.T) {
	created := ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0")
	released := ReleaseReleasedAt(instant, 7, 42, "1.8.0-rc1", "vcarroy")

	if created.DedupKey == released.DedupKey {
		t.Errorf("clé partagée %q entre deux natures", created.DedupKey)
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
	fait := ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0")
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
	faits := tousLesFaits()

	for _, fait := range faits {
		if err := fait.Validate(); err != nil {
			t.Errorf("fait %q invalide: %v", fait.Type, err)
		}
		if fait.Subject == "" {
			t.Errorf("fait %q sans sujet — l'écran n'aurait rien à afficher", fait.Type)
		}
		// Aucun constructeur ne renseigne la source : c'est la boucle des sondes
		// qui nomme le plugin qui a constaté, et le faire ici aussi donnerait deux
		// endroits où elle peut diverger.
		if fait.Source != "" {
			t.Errorf("fait %q: source %q, attendue vide — c'est la sonde qui la pose",
				fait.Type, fait.Source)
		}
		if fait.Depth != 0 {
			t.Errorf("fait %q: profondeur %d, attendue nulle — rien ne réagit encore",
				fait.Type, fait.Depth)
		}
	}
}

/*
 * La réduction de doc 15 §15.3.
 *
 * Un fait du noyau existe quand **quelque chose arrive qu'aucun plan n'a causé**.
 * Trois faits sont tombés sur cette règle : `release.built`, `release.verified`,
 * `pipeline.finished` — des étapes internes, qui ressortaient dans le fil pour y
 * rentrer aussitôt.
 *
 * La règle n'est pas vérifiable par une machine ; le **compte** l'est. Ce test ne
 * dit pas qu'un cinquième fait serait faux, il dit qu'il ne doit pas arriver sans
 * qu'on l'ait décidé.
 *
 * `release.promoted` en est sorti à son tour : c'est le plugin MSE qui définit ce
 * qu'est une promotion, c'est donc lui qui la constate (doc 16 §16.7).
 */
func TestLeNoyauNeCompteQueTroisFaits(t *testing.T) {
	attendus := map[Type]bool{
		VersionCreated: true, ReleaseCreated: true, ReleaseReleased: true,
	}

	for _, fait := range tousLesFaits() {
		if !attendus[fait.Type] {
			t.Errorf("fait %q hors des trois du noyau — un plan le cause-t-il ?", fait.Type)
		}
		delete(attendus, fait.Type)
	}
	for manquant := range attendus {
		t.Errorf("le fait %q n'a plus de constructeur", manquant)
	}
}

// Une version sans échéance n'en porte pas une vide.
//
// Un appariement `due_date=` reconnaîtrait la chaîne vide, alors que l'absence
// d'échéance n'est pas une échéance.
func TestUneVersionSansEcheanceNEnPortePasUneVide(t *testing.T) {
	fait := VersionCreatedAt(instant, 805, "v3.2.0", "open", "redmine", "412", nil)

	if _, present := fait.Payload["due_date"]; present {
		t.Errorf("échéance inventée: %v", fait.Payload)
	}
}

// tousLesFaits rend un exemplaire de chaque constructeur du noyau.
func tousLesFaits() []Event {
	echeance := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return []Event{
		VersionCreatedAt(instant, 805, "v3.2.0", "open", "redmine", "412", &echeance),
		ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0"),
		ReleaseReleasedAt(instant, 7, 42, "1.8.0", "vcarroy"),
	}
}
