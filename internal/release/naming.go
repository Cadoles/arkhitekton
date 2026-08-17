package release

import "strings"

/*
 * Rapprochement d'un tag de release et d'un nom de version Redmine.
 *
 * B8 et B17 posaient l'invariant « nom de version Redmine = tag de release ». Il
 * ne tient pas sur l'instance réelle, qui porte quatre conventions simultanées dans
 * le même projet :
 *
 *   MSE3-1.5 … MSE3-1.21   treize versions, toutes closes — convention historique
 *   MSE 3.1.0 … MSE 3.1.2  trois versions, closes
 *   v3.1.1 … v3.2.0        quatre versions, ouvertes — convention courante
 *
 * D'où une normalisation, appliquée au rapprochement seulement. Les noms sont
 * conservés tels quels en base : l'identité d'une version est son identifiant
 * Redmine, et réécrire le nom perdrait ce que l'utilisateur lit dans Redmine.
 */

// NormalizeVersionName rend la forme comparable d'un nom de version.
//
// Deux préfixes sont retirés, et deux seulement :
//
//   - `v`, quand il précède un chiffre — `v3.2.0` → `3.2.0` ;
//   - `MSE ` — `MSE 3.1.2` → `3.1.2`.
//
// `MSE3-1.5` est délibérément laissé intact. Ce préfixe ne désigne pas la même
// chose : « MSE3, version 1.5 » se lirait `3.1.5` ou `1.5` selon l'intention, et
// rien dans les données ne permet de trancher. Inventer une règle rattacherait des
// releases à la mauvaise version, ce qui est pire que de ne rien rattacher — ces
// treize versions sont closes et n'attendent aucune release.
//
// La normalisation n'est pas injective : `MSE 3.1.2` et `v3.1.2` rendent tous deux
// `3.1.2`, et ces deux versions coexistent réellement. Tout rapprochement doit donc
// traiter le cas ambigu, et jamais supposer une correspondance unique.
func NormalizeVersionName(name string) string {
	normalized := strings.TrimSpace(name)

	if rest, found := cutPrefixFold(normalized, "MSE "); found {
		normalized = strings.TrimSpace(rest)
	}

	if rest, found := cutPrefixFold(normalized, "v"); found && startsWithDigit(rest) {
		normalized = rest
	}

	return normalized
}

func cutPrefixFold(value, prefix string) (string, bool) {
	if len(value) < len(prefix) {
		return value, false
	}
	if !strings.EqualFold(value[:len(prefix)], prefix) {
		return value, false
	}
	return value[len(prefix):], true
}

func startsWithDigit(value string) bool {
	return value != "" && value[0] >= '0' && value[0] <= '9'
}

// SameVersion dit si un tag et un nom de version Redmine désignent la même version.
//
// La comparaison porte sur la version de base : une pré-release `3.2.0-rc1` se
// rattache à la version `3.2.0`, ce qui est ce qui relie une RC à son périmètre
// fonctionnel.
func SameVersion(version Version, redmineName string) bool {
	return NormalizeVersionName(version.Base) == NormalizeVersionName(redmineName)
}
