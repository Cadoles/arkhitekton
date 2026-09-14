package flux

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Environment est ce que le garde-fou a besoin de savoir d'un environnement.
//
// Type propre plutôt que celui de `store`, pour que ce paquet ne dépende pas de
// la base — et surtout pour que la vérification soit testable sans PostgreSQL.
type Environment struct {
	ID                string
	PromotionSuffix   string
	AcceptsPrerelease bool
	Observable        bool
}

// CheckPromotionTargets confronte la table des environnements aux ImagePolicy.
//
// C'est le garde-fou central de [B7], et il protège d'un mode de défaillance
// particulièrement vicieux : promouvoir vers un environnement dont le suffixe ne
// correspond à aucune politique produit un tag **valide**, poussé **sans erreur**,
// que Flux ignore. La promotion paraît réussie et rien ne se déploie.
//
// Deux écarts sont donc cherchés :
//
//  1. **Suffixe orphelin** — aucune `ImagePolicy` ne retient ce suffixe. La
//     promotion partirait dans le vide.
//  2. **Désaccord sur les pré-releases** — la table dit accepter les RC alors que
//     la plage SemVer de la politique les exclut, ou l'inverse. Le premier cas
//     laisse le dashboard proposer une promotion que Flux ignorera ; le second lui
//     fait refuser une promotion qui aurait fonctionné.
//
// Les environnements non observables — la production, dans son cluster
// indépendant — sont **exclus de la vérification** : leurs `ImagePolicy` vivent
// dans un cluster que le dashboard ne voit pas, et les exiger ici ferait échouer
// le démarrage sur une absence normale.
//  3. **Suffixe partagé** — deux environnements promouvant au même endroit.
//     L'un des deux ne serait jamais déployé, sans qu'aucune erreur ne le dise.
//     Cette règle vivait dans la validation du catalogue jusqu'au lot D ; elle
//     y imposait un concept de Flux à tout catalogue, y compris à ceux d'un
//     projet sans déployeur. Elle appartient ici.
func CheckPromotionTargets(environments []Environment, policies []ImagePolicy) error {
	var problems []error

	suffixes := make(map[string]string, len(environments))
	for _, environment := range environments {
		// Vérifiée sur **tous** les environnements, observables ou non : deux
		// environnements homonymes se marchent dessus indépendamment de notre
		// capacité à lire leurs politiques.
		if environment.PromotionSuffix == "" {
			problems = append(problems, fmt.Errorf(
				"environnement %q: aucun suffixe de promotion déclaré — ce déployeur "+
					"sélectionne par métadonnée SemVer et ne saurait pas quoi pousser",
				environment.ID))
			continue
		}
		if other, clash := suffixes[environment.PromotionSuffix]; clash {
			problems = append(problems, fmt.Errorf(
				"environnements %q et %q partagent le suffixe %q — l'un des deux ne "+
					"serait jamais déployé",
				other, environment.ID, environment.PromotionSuffix))
		}
		suffixes[environment.PromotionSuffix] = environment.ID
	}

	for _, environment := range environments {
		if !environment.Observable {
			continue
		}

		policy, found := FindPolicy(policies, environment.PromotionSuffix)
		if !found {
			problems = append(problems, fmt.Errorf(
				"environnement %q: aucune ImagePolicy ne retient le suffixe %q — une promotion "+
					"vers cet environnement produirait un tag que Flux ignorerait (politiques lues: %s)",
				environment.ID, environment.PromotionSuffix, strings.Join(describe(policies), ", ")))
			continue
		}

		if policy.AcceptsPrerelease() != environment.AcceptsPrerelease {
			problems = append(problems, fmt.Errorf(
				"environnement %q: la table déclare accepts_prerelease=%v, la politique %s dit %v",
				environment.ID, environment.AcceptsPrerelease,
				policy.Describe(), policy.AcceptsPrerelease()))
		}
	}

	return errors.Join(problems...)
}

func describe(policies []ImagePolicy) []string {
	described := make([]string, 0, len(policies))
	for _, policy := range policies {
		suffix := policy.Suffix()
		if suffix == "" {
			suffix = "(suffixe non reconnu)"
		}
		described = append(described, fmt.Sprintf("%s→%s", policy.Name, suffix))
	}
	sort.Strings(described)
	return described
}
