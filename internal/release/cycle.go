package release

import (
	"errors"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/version"
)

/*
 * Le cycle de release : quelles natures existent, et ce que chacune fait.
 *
 * `pre` / `final` / `patch` ont d'abord été classés « génériques » au motif que
 * le vocabulaire est courant. C'était une erreur de méthode : la fréquence d'un
 * mot ne dit rien de sa généralité, et il fallait regarder ce que ces natures
 * *font* (doc 12 §12.4).
 *
 * Elles ne nomment pas trois vocables mais trois points dans un espace de traits
 * indépendants — et le tableau montrait le défaut : **`pre` et `patch` ne
 * diffèrent que par la forme du tag et la permission.** Leur comportement est le
 * même, construire depuis du code. Trois noms pour deux comportements, et un
 * `check (kind in ('pre','final','patch'))` en base qui interdisait d'en
 * déclarer un quatrième.
 *
 * Un projet en `alpha` / `beta` / `rc` / `ga` n'entrait pas dans ces trois cases.
 * Un projet qui ne distingue rien devait en choisir une arbitrairement.
 *
 * # Ce que la séparation gagne
 *
 * [B17] — une finale est un re-tag, jamais une reconstruction — cesse d'être une
 * propriété de la **valeur** `final` pour devenir une propriété du **mode**
 * `retag`. Elle vaut alors pour toute nature qui le déclare, quel que soit son
 * nom, et le code qui la tient ne cite plus aucune nature.
 */

// Mode est ce qu'une nature fait pour produire son image.
//
// Jeu **fermé**, et c'est ce qui le distingue des natures : ce sont les deux
// primitives que le dashboard sait exécuter. En ajouter une demanderait du code
// nouveau, alors qu'ajouter une nature ne demande qu'une ligne de configuration.
type Mode string

const (
	// ModeBuild construit une image depuis une référence Git.
	ModeBuild Mode = "build"
	// ModeRetag reprend l'image d'une release existante, bit à bit.
	//
	// C'est ici que vit B17 depuis que les natures sont déclarées : promouvoir
	// ou livrer ne reconstruit jamais, sous peine de livrer une image que
	// personne n'a testée.
	ModeRetag Mode = "retag"
)

// TagRule dit ce que la nature exige de la forme du tag.
type TagRule string

const (
	// TagAny n'exige rien. Seule règle déclarable sur un schéma qui ne sait pas
	// distinguer une pré-release.
	TagAny TagRule = "any"
	// TagPrereleaseRequired exige une composante de pré-release.
	TagPrereleaseRequired TagRule = "prerelease-required"
	// TagPrereleaseForbidden l'interdit.
	TagPrereleaseForbidden TagRule = "prerelease-forbidden"
)

// KindSpec déclare une nature de release.
type KindSpec struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Mode  Mode   `json:"mode"`
	// Tag borne la forme du tag. Vide vaut [TagAny].
	Tag TagRule `json:"tag,omitempty"`
	// Permission exigée pour créer une release de cette nature. Vide vaut la
	// permission de création ordinaire.
	Permission string `json:"permission,omitempty"`
	// Source nomme la nature dont une nature en `retag` reprend l'image. Vide,
	// n'importe quelle release antérieure convient.
	Source string `json:"source,omitempty"`
}

// Cycle est le jeu des natures d'un projet, dans l'ordre où l'écran les propose.
type Cycle []KindSpec

// DefaultCycle rend le cycle historique — `pre`, `final`, `patch`.
//
// Exactement ce que l'énumération en base décrivait, traits pour traits : c'est
// ce qui permet à un projet déclaré avant cette bascule de continuer sans que
// personne n'ait rien à écrire. Le tableau de doc 12 §12.4 est la source de ces
// valeurs.
func DefaultCycle() Cycle {
	return Cycle{
		{
			ID: "pre", Label: "Pré-release", Mode: ModeBuild,
			Tag: TagPrereleaseRequired, Permission: "release:create",
		},
		{
			ID: "final", Label: "Finale", Mode: ModeRetag, Source: "pre",
			Tag: TagPrereleaseForbidden, Permission: "release:create:final",
		},
		{
			ID: "patch", Label: "Patch", Mode: ModeBuild,
			Tag: TagPrereleaseForbidden, Permission: "release:create:final",
		},
	}
}

// ErrUnknownKind signale une nature que le cycle du projet ne déclare pas.
var ErrUnknownKind = errors.New("nature de release inconnue")

// Find rend la nature déclarée sous cet identifiant.
func (c Cycle) Find(id string) (KindSpec, error) {
	for _, spec := range c {
		if spec.ID == id {
			return spec, nil
		}
	}

	declared := make([]string, 0, len(c))
	for _, spec := range c {
		declared = append(declared, spec.ID)
	}
	return KindSpec{}, fmt.Errorf("%w %q (déclarées: %s)",
		ErrUnknownKind, id, strings.Join(declared, ", "))
}

// Validate refuse un cycle incohérent, ou incompatible avec le schéma.
//
// Vérifié à la **configuration** et non au premier usage : une nature en `retag`
// dont la source n'existe pas ne se découvrirait qu'au moment de livrer, c'est-à-
// dire au pire moment.
func (c Cycle) Validate(scheme version.Scheme) error {
	if len(c) == 0 {
		return errors.New("un projet doit déclarer au moins une nature de release")
	}

	// Le schéma sait-il distinguer une pré-release ? La question lui est posée
	// une fois, sur un tag canonique, plutôt que déduite de son nom.
	probe, err := scheme.Parse("1.0.0")
	knowsPrerelease := err == nil && probe.KnowsPrerelease

	seen := map[string]bool{}
	var problems []error

	for _, spec := range c {
		if strings.TrimSpace(spec.ID) == "" {
			problems = append(problems, errors.New("une nature est déclarée sans identifiant"))
			continue
		}
		if seen[spec.ID] {
			problems = append(problems, fmt.Errorf("la nature %q est déclarée deux fois", spec.ID))
		}
		seen[spec.ID] = true

		switch spec.Mode {
		case ModeBuild, ModeRetag:
		default:
			problems = append(problems, fmt.Errorf(
				"nature %q: mode %q inconnu (attendu %s ou %s)",
				spec.ID, spec.Mode, ModeBuild, ModeRetag))
		}

		switch spec.Tag {
		case "", TagAny:
		case TagPrereleaseRequired, TagPrereleaseForbidden:
			// Le couplage nature × schéma, refusé ici plutôt que découvert. Sur
			// un schéma qui ne distingue pas les pré-releases, cette contrainte
			// n'a pas de sens : elle ne serait jamais vérifiable, et une
			// release créée en la croyant tenue partirait dans un environnement
			// qui l'exclut.
			if !knowsPrerelease {
				problems = append(problems, fmt.Errorf(
					"nature %q: la règle %q suppose un schéma qui distingue les "+
						"pré-releases, or %q ne sait pas répondre",
					spec.ID, spec.Tag, scheme.ID()))
			}
		default:
			problems = append(problems, fmt.Errorf(
				"nature %q: règle de tag %q inconnue", spec.ID, spec.Tag))
		}
	}

	// Les sources sont vérifiées en second : elles se référencent entre natures,
	// et les contrôler au fil de la boucle rejetterait une source déclarée plus
	// bas dans la liste.
	for _, spec := range c {
		if spec.Mode != ModeRetag || spec.Source == "" {
			continue
		}
		if !seen[spec.Source] {
			problems = append(problems, fmt.Errorf(
				"nature %q: sa source %q n'est pas déclarée", spec.ID, spec.Source))
		}
	}

	return errors.Join(problems...)
}

// CheckTag vérifie que la forme du tag correspond à ce que la nature exige.
//
// L'erreur que cela attrape est banale et coûteuse : créer `1.8.0-rc1` en
// déclarant une nature qui interdit les pré-releases, puis constater que la
// production ne la prend jamais — sa plage SemVer les excluant, le déployeur
// l'ignore sans rien dire (B7).
//
// Une règle que le schéma ne sait pas vérifier est **acceptée en silence** plutôt
// que refusée : la configuration l'a déjà interdite ([Cycle.Validate]), et
// échouer ici sur un cycle valide bloquerait une création pour une raison que
// l'utilisateur ne peut pas corriger.
func CheckTag(spec KindSpec, parsed version.Version) error {
	prerelease, known := parsed.Prereleased()
	if !known {
		return nil
	}

	switch spec.Tag {
	case TagPrereleaseRequired:
		if !prerelease {
			return fmt.Errorf(
				"le tag %q n'a pas de composante de pré-release: une release %q s'écrit par exemple %s-rc1",
				parsed.Raw, spec.ID, parsed.Base)
		}
	case TagPrereleaseForbidden:
		if prerelease {
			return fmt.Errorf(
				"le tag %q porte une composante de pré-release %q: une release %q s'écrit %s",
				parsed.Raw, parsed.Prerelease, spec.ID, parsed.Base)
		}
	}
	return nil
}
