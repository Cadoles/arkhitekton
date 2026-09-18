package release

import (
	"strings"

	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

/*
 * Rapprochement d'un tag de release et d'un nom de version du suivi.
 *
 * B8 et B17 posaient l'invariant « nom de version = tag de release ». Il ne tient
 * pas tel quel : le suivi nomme ses versions `v3.2.0`, le tag s'écrit `3.2.0`.
 * D'où une normalisation, appliquée au rapprochement seulement. Les noms sont
 * conservés tels quels en base : l'identité d'une version est son identifiant dans
 * le suivi, et réécrire le nom perdrait ce que l'utilisateur y lit.
 *
 * # Un seul préfixe, et plus de réglage
 *
 * Les préfixes étaient configurables, et le déploiement MSE déclarait `MSE ` en
 * plus de `v`. Ce préfixe ne rattachait que des versions closes sans release, et
 * créait avec leurs jumelles `v` des ambiguïtés que l'adoption devait ensuite
 * trancher (doc 16 §16.3). Il a été retiré, et avec lui le réglage : `v` vient du
 * versionnement sémantique et ne suppose rien d'un projet.
 */

// semverPrefix est le préfixe retiré devant un numéro de version.
const semverPrefix = "v"

// NormalizeVersionName rend la forme comparable d'un nom de version.
//
// Le préfixe n'est retiré que si le reste **commence par un chiffre** : c'est ce
// qui protège une version nommée `validation` d'être ramenée à `alidation`.
//
// La normalisation n'est pas injective : `3.1.2` et `v3.1.2` rendent tous deux
// `3.1.2`, et un suivi peut porter les deux. Tout rapprochement doit donc traiter
// le cas ambigu, et jamais supposer une correspondance unique.
func NormalizeVersionName(name string) string {
	normalized := strings.TrimSpace(name)

	if len(normalized) > len(semverPrefix) &&
		strings.EqualFold(normalized[:len(semverPrefix)], semverPrefix) {
		if rest := normalized[len(semverPrefix):]; startsWithDigit(rest) {
			return rest
		}
	}
	return normalized
}

func startsWithDigit(value string) bool {
	return value != "" && value[0] >= '0' && value[0] <= '9'
}

// SameVersion dit si un tag et un nom de version du suivi désignent la même version.
//
// La comparaison porte sur la version de base : une pré-release `3.2.0-rc1` se
// rattache à la version `3.2.0`, ce qui est ce qui relie une RC à son périmètre
// fonctionnel.
func SameVersion(version versionpkg.Version, trackedName string) bool {
	return NormalizeVersionName(version.Base) == NormalizeVersionName(trackedName)
}
