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

// semverPrefix is the prefix stripped before a version number.
const semverPrefix = "v"

// NormalizeVersionName returns the comparable form of a version name.
//
// The prefix is stripped only if the rest starts with a digit: that is what stops a
// version named `validation` from being reduced to `alidation`.
//
// Normalisation is not injective: `3.1.2` and `v3.1.2` both yield `3.1.2`, and a
// tracker may carry both. Any matching must therefore handle the ambiguous case and
// never assume a unique correspondence.
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

// SameVersion says whether a tag and a tracker version name mean the same version.
//
// The comparison is on the base version: a pre-release `3.2.0-rc1` attaches to
// version `3.2.0`, which is what links a release candidate to its functional scope.
func SameVersion(version versionpkg.Version, trackedName string) bool {
	return NormalizeVersionName(version.Base) == NormalizeVersionName(trackedName)
}
