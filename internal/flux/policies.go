package flux

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
)

// ImagePolicy est ce que le dashboard lit d'une politique de sélection.
type ImagePolicy struct {
	Name string
	// Pattern est le `filterTags.pattern` : c'est lui qui dit quels tags cette
	// politique retient, donc quel suffixe de promotion elle attend.
	Pattern string
	// Range est la plage SemVer. Sa forme décide si les pré-releases sont
	// éligibles, et c'est le point le plus glissant de tout le dispositif.
	Range string
	// LatestImage est ce que Flux a retenu, vide tant qu'il n'a rien scanné.
	LatestImage string
}

// AcceptsPrerelease dit si la plage sélectionne les pré-releases.
//
// Une plage SemVer sans composante de pré-release n'en retient **aucune** :
// `>=0.0.0` ne correspond pas à `1.8.0-rc1`. C'est le comportement voulu pour la
// production, et un piège partout ailleurs — une `ImagePolicy` de qualif écrite
// sans `-0` ignorerait silencieusement toutes les RC promues vers elle.
//
// La détection est volontairement grossière : la présence d'un tiret dans la
// plage. Reproduire l'analyse de Masterminds/semver donnerait une seconde
// implémentation à garder d'accord avec la première, pour un gain nul — ce qui
// compte est de comparer cette lecture à ce que la table `environments` déclare,
// et de refuser de démarrer si les deux divergent.
func (p ImagePolicy) AcceptsPrerelease() bool {
	return strings.Contains(p.Range, "-")
}

// Suffix extrait le suffixe de promotion attendu par la politique.
//
// Le motif documenté est `^(?P<semver>.+)_testing$` : le suffixe est ce qui suit
// le dernier underscore, avant l'ancre de fin. Rendre vide plutôt que deviner
// quand le motif ne suit pas cette forme — un motif exotique doit produire un
// écart visible à la validation, pas une correspondance inventée.
func (p ImagePolicy) Suffix() string {
	pattern := strings.TrimSuffix(p.Pattern, "$")

	index := strings.LastIndex(pattern, "_")
	if index < 0 || index == len(pattern)-1 {
		return ""
	}
	suffix := pattern[index+1:]

	// Un suffixe qui contient encore de la syntaxe d'expression régulière n'en
	// est pas un.
	if strings.ContainsAny(suffix, `.*+?()[]{}|\^$`) {
		return ""
	}
	return suffix
}

// ImagePolicies rend les politiques du namespace Flux.
func (c *Client) ImagePolicies(ctx context.Context) ([]ImagePolicy, error) {
	items, err := c.list(ctx, c.imagePolicies, c.Namespace)
	if err != nil {
		return nil, err
	}

	policies := make([]ImagePolicy, 0, len(items))
	for _, item := range items {
		object := unstructured.Unstructured{Object: item}

		policy := ImagePolicy{Name: object.GetName()}
		policy.Pattern, _, _ = unstructured.NestedString(item, "spec", "filterTags", "pattern")
		policy.Range, _, _ = unstructured.NestedString(item, "spec", "policy", "semver", "range")
		policy.LatestImage, _, _ = unstructured.NestedString(item, "status", "latestRef", "name")
		if policy.LatestImage == "" {
			// Flux a déplacé l'information de `status.latestImage` vers
			// `status.latestRef` en cours de route. Lire les deux évite de
			// dépendre de la version installée, que ce paquet ne code pas.
			policy.LatestImage, _, _ = unstructured.NestedString(item, "status", "latestImage")
		}

		policies = append(policies, policy)
	}
	return policies, nil
}

// Kustomization est l'état de rapprochement d'un environnement.
type Kustomization struct {
	Name string
	// Ready porte la condition du même nom : `True`, `False` ou `Unknown`.
	Ready string
	// Reason et Message expliquent un rapprochement en échec. Sans eux,
	// l'interface ne peut qu'afficher « pas prêt ».
	Reason  string
	Message string
	// LastAppliedRevision est la révision Git effectivement appliquée. C'est elle
	// qui dit ce qui tourne, par opposition à ce qui a été demandé.
	LastAppliedRevision string
}

// State projette la condition Ready vers l'état affiché.
//
// Trois états et non deux : `Unknown` signifie « rapprochement en cours », ce qui
// n'est ni un succès ni un échec. Les confondre ferait afficher une erreur
// pendant chaque déploiement normal.
func (k Kustomization) State() string {
	switch k.Ready {
	case "True":
		return "ready"
	case "False":
		return "failed"
	default:
		return "reconciling"
	}
}

// Kustomizations rend les rapprochements du namespace Flux.
func (c *Client) Kustomizations(ctx context.Context) ([]Kustomization, error) {
	items, err := c.list(ctx, c.kustomizations, c.Namespace)
	if err != nil {
		return nil, err
	}

	kustomizations := make([]Kustomization, 0, len(items))
	for _, item := range items {
		object := unstructured.Unstructured{Object: item}

		entry := Kustomization{Name: object.GetName()}
		entry.LastAppliedRevision, _, _ = unstructured.NestedString(item, "status", "lastAppliedRevision")

		conditions, _, _ := unstructured.NestedSlice(item, "status", "conditions")
		for _, raw := range conditions {
			condition, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if conditionType, _, _ := unstructured.NestedString(condition, "type"); conditionType != "Ready" {
				continue
			}
			entry.Ready, _, _ = unstructured.NestedString(condition, "status")
			entry.Reason, _, _ = unstructured.NestedString(condition, "reason")
			entry.Message, _, _ = unstructured.NestedString(condition, "message")
			break
		}

		kustomizations = append(kustomizations, entry)
	}
	return kustomizations, nil
}

// Client réalise [provider.Attester]. Il ne réalise **pas** de frontière de
// déploiement : elle n'existe pas encore, parce qu'elle exige de trancher ce que
// « promouvoir » veut dire, et qu'une interface écrite avant cette réponse serait
// un calque de Flux qu'on croirait générique (doc 12 §12.4).
var _ provider.Attester = (*Client)(nil)

// Attestations lit ce que les applications d'un namespace rapportent
// d'elles-mêmes, par le CRD `PortalData`.
//
// Seule source issue de l'application **en exécution**, et la seule à dire si les
// migrations ont abouti : une image déployée dont les migrations ont échoué n'est
// pas une version livrée, et rien d'autre ne le signale (B8).
//
// Réalise [provider.Attester]. Le CRD lui-même est propre au portail — c'est
// exactement ce que le plugin MSE portera le jour venu (doc 12 §12.5) — mais ce
// qu'il rapporte, « telle version tourne et elle est exploitable », ne l'est pas.
// La conversion se fait donc ici, au plus près du décodage.
//
// Porté par ce client parce qu'il lit le même cluster par le même client
// dynamique, non parce que déploiement et attestation seraient la même chose :
// l'un dit l'intention, l'autre le constat, et c'est leur écart qui fait la
// valeur de l'écran des environnements.
func (c *Client) Attestations(ctx context.Context, namespace string) ([]provider.Attestation, error) {
	items, err := c.list(ctx, c.portalData, namespace)
	if err != nil {
		return nil, err
	}

	states := make([]provider.Attestation, 0, len(items))
	for _, item := range items {
		object := unstructured.Unstructured{Object: item}

		state := provider.Attestation{Namespace: namespace, Name: object.GetName()}
		state.State, _, _ = unstructured.NestedString(item, "status", "phase")
		// `dataVersion` est la version de l'**image** ayant réalisé le dernier
		// bootstrap, non celle de la release. Une release finale réutilisant
		// l'image de sa RC, le rapprochement se fait sur `releases.image_version`
		// et jamais sur le tag (B17, B8).
		state.Version, _, _ = unstructured.NestedString(item, "status", "dataVersion")
		// `Ready` est le verdict, et il est rendu ici parce que le vocabulaire
		// qui le porte appartient au CRD. `status.schemaVersion` existe aussi
		// mais n'est pas lu : c'est un nom de classe Doctrine, et le confondre
		// avec une version de release est l'erreur que la relecture du CRD a
		// corrigée.
		state.Ready = state.State == "Ready"

		states = append(states, state)
	}
	return states, nil
}

// FindPolicy retrouve la politique d'un suffixe de promotion.
func FindPolicy(policies []ImagePolicy, suffix string) (ImagePolicy, bool) {
	for _, policy := range policies {
		if policy.Suffix() == suffix {
			return policy, true
		}
	}
	return ImagePolicy{}, false
}

// Describe rend une politique lisible dans un message d'erreur.
func (p ImagePolicy) Describe() string {
	return fmt.Sprintf("%s (motif %q, plage %q)", p.Name, p.Pattern, p.Range)
}
