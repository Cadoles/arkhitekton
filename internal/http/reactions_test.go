package http

import (
	"slices"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
)

/*
 * Le catalogue des faits du noyau, et la seconde vérité qu'il constitue.
 *
 * `coreEventChoices` est écrit à la main : les faits du noyau n'ont pas de
 * descripteur, et leur en donner un pour la seule commodité de cette liste
 * ajouterait un registre à tenir aligné. Le prix de ce choix est que la liste peut
 * dériver de ce que les constructeurs produisent — et la dérive est **muette** :
 * l'éditeur proposerait un champ qu'aucune charge ne porte, et l'appariement
 * n'apparierait jamais.
 *
 * Ces deux tests sont le contrepoids de ce choix.
 */

// instant fixe la date des faits d'exemple.
var instant = time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)

// Le catalogue offre exactement les quatre faits du noyau.
//
// La réduction de doc 15 §15.3 en a retiré trois — `release.built`,
// `release.verified`, `pipeline.finished`. Les laisser ici les aurait laissés
// choisissables dans l'éditeur, pour des réactions que plus rien ne déclenche.
func TestLeCatalogueOffreLesQuatreFaitsDuNoyau(t *testing.T) {
	attendus := map[string]bool{
		string(event.VersionCreated): true, string(event.ReleaseCreated): true,
		string(event.ReleasePromoted): true, string(event.ReleaseReleased): true,
	}

	for _, choice := range coreEventChoices() {
		if !attendus[choice.Type] {
			t.Errorf("fait %q offert — il n'est pas des quatre du noyau", choice.Type)
		}
		delete(attendus, choice.Type)
		if choice.Label == "" {
			t.Errorf("fait %q sans libellé — la liste de l'éditeur serait muette", choice.Type)
		}
	}
	for manquant := range attendus {
		t.Errorf("le fait %q n'est pas offert à l'éditeur", manquant)
	}
}

// Chaque champ annoncé existe dans la charge que le constructeur produit.
//
// C'est le contrôle qui compte : un champ mal orthographié ici se proposerait
// dans l'éditeur, s'enregistrerait sans broncher, et l'appariement n'apparierait
// jamais. Rien ne le dirait — une réaction qui ne part pas ressemble à une
// réaction qu'on n'a pas déclenchée.
func TestChaqueChampAnnonceExisteDansLaCharge(t *testing.T) {
	echeance := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// Les constructeurs renseignés **au complet** : un champ facultatif laissé nu
	// ferait passer son absence pour une faute de frappe.
	charges := map[string]map[string]string{
		string(event.VersionCreated): event.VersionCreatedAt(
			instant, 805, "v3.2.0", "open", "redmine", "412", &echeance).Payload,
		string(event.ReleaseCreated): event.ReleaseCreatedAt(
			instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0").Payload,
		string(event.ReleaseReleased): event.ReleaseReleasedAt(
			instant, 7, 42, "1.8.0", "vcarroy").Payload,
		string(event.ReleasePromoted): event.ReleasePromotedAt(
			instant, 7, 42, 1400, "1.8.0", "preprod").Payload,
	}

	for _, choice := range coreEventChoices() {
		charge, connu := charges[choice.Type]
		if !connu {
			t.Errorf("fait %q sans exemplaire — le contrôle ne le couvre pas", choice.Type)
			continue
		}
		for _, champ := range choice.Fields {
			if _, present := charge[champ]; !present {
				t.Errorf("fait %q: le champ %q est offert et la charge ne le porte pas",
					choice.Type, champ)
			}
		}
		// Et l'inverse : un champ porté mais non annoncé est appariable sans être
		// proposé. Moins grave — rien ne casse —, mais c'est une capacité qu'on
		// offre sans le dire.
		for champ := range charge {
			if !slices.Contains(choice.Fields, champ) {
				t.Errorf("fait %q: la charge porte %q et l'éditeur ne le propose pas",
					choice.Type, champ)
			}
		}
	}
}
