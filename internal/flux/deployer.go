package flux

import (
	"context"
	"fmt"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/version"
)

/*
 * Le déployeur GitOps, seule réalisation de [provider.Deployer].
 *
 * Il n'agit sur aucun cluster, et c'est la doctrine et non une limitation ([B7]) :
 * promouvoir consiste à poser un tag supplémentaire sur un digest déjà publié,
 * après quoi Flux le constate et déploie. Le dashboard ne se connecte jamais au
 * cluster cible — la production vit d'ailleurs dans un cluster qu'il ne voit pas.
 *
 * Ce que ce type ajoute au client : il **répond aux questions** du contrat, là où
 * le client se contente de lire des objets. La planification vient de
 * `internal/release`, qui la tenait déjà ; elle n'est pas réécrite ici, seulement
 * exposée sous la forme que la frontière demande (doc 12 §12.9, lot A).
 */

// Réglages qu'un environnement doit porter pour ce déployeur.
//
// Nommés ici, parce que c'est lui qui les lit. `provider.Target` ne les connaît
// pas : ils n'ont de sens que pour une sélection par métadonnée SemVer, et un
// déployeur qui procède autrement n'aurait pas à les fournir.
const (
	// SettingSuffix est la métadonnée SemVer que la plage de l'ImagePolicy retient.
	SettingSuffix = "promotionSuffix"
	// SettingAcceptsPrerelease dit si la plage admet les pré-releases. Redondant
	// avec la politique réelle, et confronté à elle par Check — c'est justement
	// ce désaccord qui doit empêcher le démarrage.
	SettingAcceptsPrerelease = "acceptsPrerelease"
	// SettingKustomization nomme la Kustomization à lire pour observer.
	SettingKustomization = "kustomization"
)

// DefaultPromotionKind est la famille de pipeline qui pose le tag supplémentaire.
//
// Portée ici parce que c'est le déployeur qui sait par quel geste son plan se
// réalise. Le dashboard emploie le même pipeline hors promotion — donner à une
// release finale son propre tag depuis celui de sa pré-release (B17) —, mais
// c'est une autre question : l'une achemine vers un environnement, l'autre
// établit une identité. Les deux constantes coïncident sans se confondre.
const DefaultPromotionKind = "retag"

// Deployer réalise [provider.Deployer] au-dessus de Flux.
type Deployer struct {
	client *Client
	// kind est la famille de pipeline qui pose le tag. Portée par le déployeur
	// plutôt que devinée par l'appelant : c'est lui qui sait par quel geste son
	// plan se réalise.
	kind string
	// template compose le tag de promotion. Configurable, `+` étant exclu de la
	// grammaire des tags OCI.
	template string
}

// NewDeployer construit le déployeur GitOps.
//
// Le client peut être nil : le dashboard reste utilisable sans accès au cluster
// Flux, l'observation répond alors « non observable » et le démarrage ne vérifie
// rien. Mieux vaut un écran qui le dit qu'un binaire qui refuse de démarrer sur
// une dépendance facultative.
func NewDeployer(client *Client, kind, template string) *Deployer {
	if kind == "" {
		kind = DefaultPromotionKind
	}
	return &Deployer{client: client, kind: kind, template: template}
}

var _ provider.Deployer = (*Deployer)(nil)

// Plan compose le tag à poser pour atteindre un environnement.
//
// Le refus est l'essentiel : une pré-release vers un environnement dont la plage
// SemVer les exclut produirait un tag valide, poussé sans erreur, que Flux
// ignorerait. `release.PromotionTag` porte cette règle et la rend telle quelle.
func (d *Deployer) Plan(_ context.Context, request provider.Deployment) (provider.Plan, error) {
	target := release.Target{
		ID:                request.Target.ID,
		Suffix:            request.Target.Setting(SettingSuffix, ""),
		AcceptsPrerelease: request.Target.Setting(SettingAcceptsPrerelease, "") == "true",
	}

	plan, err := release.PlanPromotion(request.Version, request.Artifact, target, d.template)
	if err != nil {
		return provider.Plan{}, err
	}

	return provider.Plan{
		Kind: d.kind,
		Params: map[string]string{
			"image-name": request.Name,
			"old-tag":    plan.OldTag,
			"new-tag":    plan.NewTag,
		},
		Reference: plan.NewTag,
	}, nil
}

// Observe rend ce que Flux a retenu pour cet environnement.
//
// Une **intention**, jamais un constat : la Kustomization dit ce que Flux a
// appliqué, pas ce qui tourne. Le niveau de preuve rendu est donc `declared`, et
// c'est un [provider.Attester] qui peut l'élever — leur écart est ce que l'écran
// des environnements existe pour montrer (B8).
func (d *Deployer) Observe(ctx context.Context, target provider.Target) (provider.Observation, error) {
	observation := provider.Observation{Resolution: provider.ResolutionUnobservable}

	if !target.Observable || d.client == nil {
		// La production, dans son cluster indépendant, ou aucun accès Flux. Non
		// observable et non « inconnu » : l'interface doit distinguer les deux,
		// sans quoi une carte figée ressemble à une panne.
		return observation, nil
	}

	name := target.Setting(SettingKustomization, "")
	if name == "" {
		return observation, nil
	}

	kustomizations, err := d.client.Kustomizations(ctx)
	if err != nil {
		return observation, err
	}

	for _, kustomization := range kustomizations {
		if kustomization.Name != name {
			continue
		}
		observation.State = kustomization.State()
		observation.Resolution = provider.ResolutionDeclared
		return observation, nil
	}
	return observation, nil
}

// CheckScheme refuse un schéma auquel ce déployeur ne peut pas attacher de
// marqueur.
//
// Ce déployeur promeut en **écrivant une métadonnée** dans le tag, que la plage
// d'une ImagePolicy retient ensuite. Un schéma qui ne sait pas où poser cette
// métadonnée rend la promotion impossible — et l'échec serait le plus coûteux
// qui soit : le tag partirait, le registre l'accepterait, Flux l'ignorerait, et
// la promotion paraîtrait réussie (B7).
//
// La question est posée au schéma plutôt que déduite de son nom : c'est ce qui
// fait qu'un schéma calendaire, le jour où il existera, sera accepté sans que
// cette ligne change — il sait suffixer, donc il convient.
func (d *Deployer) CheckScheme(scheme version.Scheme) error {
	// Un tag sonde, jamais poussé : on demande au schéma s'il **sait** poser un
	// marqueur, pas ce qu'il produirait.
	probe, err := scheme.Parse(probeTag)
	if err != nil {
		// Le schéma refuse jusqu'à un tag canonique : il ne peut pas décrire les
		// versions que ce déployeur achemine.
		return fmt.Errorf(
			"le schéma %q ne reconnaît pas une version de la forme %q, "+
				"que ce déployeur doit savoir écrire: %w", scheme.ID(), probeTag, err)
	}

	if _, err := scheme.Mark(probe.Raw, "prod"); err != nil {
		return fmt.Errorf(
			"le schéma %q ne sait pas attacher de marqueur de promotion, "+
				"or ce déployeur sélectionne par métadonnée: une promotion "+
				"produirait un tag que rien ne retiendrait (%w)", scheme.ID(), err)
	}
	return nil
}

// probeTag est la version canonique servant à interroger un schéma. Jamais
// poussée : elle ne sert qu'à savoir si le schéma sait répondre.
const probeTag = "1.0.0"

// Check confronte les environnements déclarés aux ImagePolicy réelles.
//
// Sans client, rien n'est vérifié plutôt que tout refusé : un déploiement sans
// accès Flux est une configuration valide, et faire échouer le démarrage dessus
// empêcherait d'utiliser le reste du dashboard.
func (d *Deployer) Check(ctx context.Context, targets []provider.Target) error {
	if d.client == nil {
		return nil
	}

	policies, err := d.client.ImagePolicies(ctx)
	if err != nil {
		return fmt.Errorf("lecture des ImagePolicy: %w", err)
	}

	environments := make([]Environment, 0, len(targets))
	for _, target := range targets {
		environments = append(environments, Environment{
			ID:                target.ID,
			PromotionSuffix:   target.Setting(SettingSuffix, ""),
			AcceptsPrerelease: target.Setting(SettingAcceptsPrerelease, "") == "true",
			Observable:        target.Observable,
		})
	}
	return CheckPromotionTargets(environments, policies)
}
