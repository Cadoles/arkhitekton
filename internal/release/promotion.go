package release

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultTagTemplate compose le tag de promotion.
//
// Le gabarit est configurable plutôt que codé : `+` est exclu de la grammaire des
// tags OCI (`[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`), d'où l'underscore ; si la
// convention évolue, le séparateur reste un paramètre (B7).
const DefaultTagTemplate = "{version}_{suffix}"

// Target décrit l'environnement visé par une promotion.
type Target struct {
	ID string
	// Suffix est la métadonnée SemVer attendue par Flux — `testing` pour la
	// qualification, dont Flux ignore le nom courant.
	Suffix string
	// AcceptsPrerelease reflète la plage SemVer de l'ImagePolicy correspondante,
	// vérifiée au démarrage contre le cluster.
	AcceptsPrerelease bool
}

// ociTag borne ce qu'un registre accepte comme tag.
var ociTag = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

// PromotionTag compose le tag à pousser pour atteindre un environnement.
//
// La fonction refuse une promotion que Flux ignorerait plutôt que de la laisser
// partir. C'est le point le plus important de tout le dispositif de promotion :
// pousser un tag qu'aucune `ImagePolicy` ne retient **ne produit aucune erreur**.
// Le tag est accepté par le registre, la task réussit, et rien ne se déploie —
// l'échec le plus coûteux qui soit, puisqu'il ressemble à un succès.
//
// Deux refus, donc :
//
//  1. **Pré-release vers un environnement qui n'en accepte pas.** La plage SemVer
//     de sa politique s'écrit sans `-0`, ce qui rend une RC structurellement
//     inéligible. Le dashboard refuse en amont, avec un message explicite, plutôt
//     que de s'en remettre à un silence.
//  2. **Tag hors grammaire OCI.** Le registre le refuserait, mais plus tard et
//     avec un message qui parle de manifeste.
func PromotionTag(imageVersion string, target Target, template string) (string, error) {
	version := strings.TrimSpace(imageVersion)
	if version == "" {
		return "", fmt.Errorf("aucune version d'image à promouvoir")
	}
	if strings.TrimSpace(target.Suffix) == "" {
		return "", fmt.Errorf("environnement %q: aucun suffixe de promotion", target.ID)
	}

	parsed, err := ParseVersion(version)
	if err != nil {
		return "", fmt.Errorf("version d'image %q: %w", version, err)
	}

	if parsed.IsPrerelease() && !target.AcceptsPrerelease {
		return "", fmt.Errorf(
			"l'environnement %q n'accepte pas les pré-releases: promouvoir %q y produirait un tag "+
				"que Flux ignorerait, sans erreur ni déploiement",
			target.ID, version)
	}

	if template == "" {
		template = DefaultTagTemplate
	}
	tag := strings.NewReplacer(
		"{version}", version,
		"{suffix}", target.Suffix,
	).Replace(template)

	if !ociTag.MatchString(tag) {
		return "", fmt.Errorf(
			"le tag %q n'est pas un tag OCI valide: le gabarit %q produit une valeur que le registre refuserait",
			tag, template)
	}

	return tag, nil
}

// PromotionPlan décrit ce qu'une promotion doit faire.
type PromotionPlan struct {
	EnvironmentID string
	// OldTag est le tag existant, celui de l'image construite.
	OldTag string
	// NewTag est le tag suffixé à ajouter. Le digest est le même : promouvoir
	// n'est pas reconstruire.
	NewTag string
}

// PlanPromotion prépare la promotion d'une release vers un environnement.
func PlanPromotion(imageVersion string, target Target, template string) (PromotionPlan, error) {
	tag, err := PromotionTag(imageVersion, target, template)
	if err != nil {
		return PromotionPlan{}, err
	}

	return PromotionPlan{
		EnvironmentID: target.ID,
		OldTag:        strings.TrimSpace(imageVersion),
		NewTag:        tag,
	}, nil
}
