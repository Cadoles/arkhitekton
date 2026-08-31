package release

import (
	"sort"
	"strings"

	versionpkg "forge.cadoles.com/vcarroy/mse-dashboard/internal/version"
)

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

// DefaultPrefix est le seul préfixe qu'un déploiement obtient sans rien déclarer.
//
// `v3.2.0` est une convention de fait, pas une convention de projet : elle vient
// du versionnement sémantique lui-même, et la retirer d'office ne suppose rien
// d'un projet donné. `MSE `, en revanche, est le nom d'un client — il était en
// dur à côté, et c'est ce que ce chantier déplace vers la configuration
// (doc 12 §12.9, chantier 5).
const DefaultPrefix = "v"

// Normalizer rend comparables des noms de version écrits selon des conventions
// différentes.
//
// Une valeur et non une fonction de paquet : les préfixes viennent de la
// configuration, et une fonction libre laisserait n'importe quel appelant
// normaliser sans eux — en silence, et différemment de la colonne stockée.
type Normalizer struct {
	// prefixes est trié du plus long au plus court. `MSE ` doit être essayé avant
	// `M` si les deux sont déclarés, sans quoi le résultat dépendrait de l'ordre
	// de saisie.
	prefixes []string
}

// NewNormalizer construit un normaliseur.
//
// Les entrées vides sont écartées et les doublons ignorés : une liste mal saisie
// — virgule en trop, préfixe répété — donne le même résultat qu'une liste propre
// plutôt qu'un comportement à expliquer.
func NewNormalizer(prefixes []string) Normalizer {
	seen := map[string]bool{}
	kept := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimLeft(prefix, " \t")
		if strings.TrimSpace(prefix) == "" || seen[strings.ToLower(prefix)] {
			continue
		}
		seen[strings.ToLower(prefix)] = true
		kept = append(kept, prefix)
	}
	sort.SliceStable(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })
	return Normalizer{prefixes: kept}
}

// DefaultNormalizer ne retire que [DefaultPrefix].
//
// Sert aux tests et aux chemins qui n'ont pas de configuration à porter. Un
// appelant qui rapproche des noms réels doit recevoir celui de la configuration :
// normaliser autrement que la colonne stockée ne rapproche rien.
var DefaultNormalizer = NewNormalizer([]string{DefaultPrefix})

// Prefixes rend les préfixes retenus, du plus long au plus court.
func (n Normalizer) Prefixes() []string { return append([]string(nil), n.prefixes...) }

// Normalize rend la forme comparable d'un nom de version.
//
// Un seul préfixe est retiré, le premier qui s'applique — enchaîner les retraits
// ferait dépendre le résultat de l'ordre de la liste, et `vv3.0` n'est pas un nom
// qu'on cherche à rattraper.
//
// La coupe exige que le reste **commence par un chiffre**. C'est ce qui protège
// une version nommée `validation` d'être ramenée à `alidation` par le préfixe
// `v`, et la garde vaut pour tous les préfixes plutôt que pour celui-là seul.
//
// `MSE3-1.5` reste donc intact même quand `MSE` est déclaré : le reste commence
// par un chiffre, mais ce préfixe ne désigne pas la même chose — « MSE3, version
// 1.5 » se lirait `3.1.5` ou `1.5` selon l'intention, et rien dans les données ne
// permet de trancher. Seul `MSE ` avec son espace est déclaré, et il ne s'applique
// pas ici. Inventer une règle rattacherait des releases à la mauvaise version, ce
// qui est pire que de ne rien rattacher.
//
// La normalisation n'est pas injective : `MSE 3.1.2` et `v3.1.2` rendent tous deux
// `3.1.2`, et ces deux versions coexistent réellement. Tout rapprochement doit donc
// traiter le cas ambigu, et jamais supposer une correspondance unique.
func (n Normalizer) Normalize(name string) string {
	normalized := strings.TrimSpace(name)

	for _, prefix := range n.prefixes {
		rest, found := cutPrefixFold(normalized, prefix)
		if !found {
			continue
		}
		rest = strings.TrimSpace(rest)
		if !startsWithDigit(rest) {
			continue
		}
		return rest
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
func (n Normalizer) SameVersion(version versionpkg.Version, redmineName string) bool {
	return n.Normalize(version.Base) == n.Normalize(redmineName)
}
