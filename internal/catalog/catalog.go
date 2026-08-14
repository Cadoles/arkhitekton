// Package catalog porte la description déclarative des pipelines lançables.
//
// Créer un PipelineRun à la main suppose de reproduire ce que faisaient les
// TriggerTemplate de `mse-tekton` : workspaces, secrets, volumeClaimTemplate,
// compte de service, secrets de tirage d'image, valeurs par défaut. Coder cela en
// Go ferait que toute évolution de `mse-tekton` casse le dashboard sans que rien
// ne le signale — au mieux à la première utilisation, souvent bien plus tard.
//
// Le catalogue vit donc en ConfigMap, hors du binaire, et il est **validé au
// démarrage contre les Pipeline réellement présents dans le cluster**. Un écart
// empêche le démarrage avec un message qui le nomme, plutôt que de produire des
// runs invalides (doc 06 §6.2).
package catalog

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Classes d'exclusion reconnues. La sémantique est décrite en doc 06 §6.3.
const (
	// ClassParallel n'exclut rien : seul le plafond global s'applique.
	ClassParallel = "parallel"
	// ClassBarrier exclut **tous** les jobs, quelle que soit leur classe. Le
	// cluster Tekton héberge aussi la cible des tests de charge : tout job
	// concurrent fausse la mesure (B16).
	ClassBarrier = "exclusive-cluster"
	// exclusivePrefix introduit une exclusion par classe : un seul job de cette
	// classe à la fois, sans effet sur les autres.
	exclusivePrefix = "exclusive-"
)

// Catalog est le catalogue chargé.
type Catalog struct {
	Pipelines []Pipeline `json:"pipelines"`
	// Environments vit dans le même fichier que les pipelines : les deux
	// décrivent ce que le dashboard peut déclencher, et les séparer imposerait
	// deux montages et deux chargements pour une même intention.
	Environments []Environment `json:"environments,omitempty"`
	// MaxConcurrentRuns protège le cluster : les pipelines demandent des PVC et
	// des pods de build, et rien n'empêche mécaniquement d'en lancer vingt.
	MaxConcurrentRuns int `json:"maxConcurrentRuns"`
}

// Environment décrit un environnement de déploiement.
//
// Ces valeurs sont de la configuration et non des constantes : le libellé, le
// rang et surtout le suffixe de promotion sont propres au déploiement — Flux
// désigne `qualif` sous le nom `testing`. Coder cette correspondance reviendrait
// à promouvoir vers le mauvais environnement au premier oubli (B13).
type Environment struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// PromotionSuffix est la métadonnée SemVer attendue par Flux. Vérifiée au
	// démarrage contre les ImagePolicy réelles : une erreur ici ne produit aucun
	// échec visible, seulement une promotion sans effet.
	PromotionSuffix   string `json:"promotionSuffix"`
	AcceptsPrerelease bool   `json:"acceptsPrerelease"`
	Rank              int    `json:"rank"`
	Namespace         string `json:"namespace"`
	FluxKustomization string `json:"fluxKustomization,omitempty"`
	// Observable vaut false pour la production, dans un cluster indépendant.
	Observable *bool `json:"observable,omitempty"`
	Visible    *bool `json:"visible,omitempty"`
}

// IsObservable rend la valeur effective, l'absence valant « observable ».
func (e Environment) IsObservable() bool { return e.Observable == nil || *e.Observable }

// IsVisible rend la valeur effective, l'absence valant « visible ».
func (e Environment) IsVisible() bool { return e.Visible == nil || *e.Visible }

// Pipeline décrit un pipeline lançable.
type Pipeline struct {
	// Kind est l'identifiant employé par le dashboard. Distinct de PipelineRef :
	// il survit à un renommage côté `mse-tekton`, que le catalogue absorbe.
	Kind string `json:"kind"`
	// PipelineRef est le nom du Pipeline dans le cluster.
	PipelineRef string `json:"pipelineRef"`
	Label       string `json:"label"`
	QueueClass  string `json:"queueClass"`
	// Timeout borne l'exécution. Vide vaut le défaut de Tekton.
	Timeout string `json:"timeout,omitempty"`
	// ServiceAccountName n'est pas décoratif : `php-integration-test` déploie
	// dans un namespace applicatif et exige `tekton-kubectl`.
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
	Params             map[string]Param  `json:"params,omitempty"`
	Uploads            map[string]Upload `json:"uploads,omitempty"`
	Workspaces         []Workspace       `json:"workspaces,omitempty"`
	PodTemplate        *PodTemplate      `json:"podTemplate,omitempty"`
}

// Param décrit d'où vient la valeur d'un paramètre.
type Param struct {
	// Source vaut "input" (fourni au lancement), "config" (tiré de la
	// configuration) ou "literal" (valeur fixe).
	Source   string `json:"source"`
	Key      string `json:"key,omitempty"`
	Value    string `json:"value,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Sources reconnues.
const (
	SourceInput   = "input"
	SourceConfig  = "config"
	SourceLiteral = "literal"
)

// Upload déclare un artefact attendu.
//
// C'est cette déclaration qui permet au dashboard de calculer les clés de
// stockage et de minter les URLs pré-signées **avant** de créer le run : sans
// elle, il faudrait deviner ce que le pipeline produit (B14).
type Upload struct {
	Family string `json:"family"`
	Format string `json:"format"`
}

// Workspace décrit un workspace du PipelineRun.
type Workspace struct {
	Name                string               `json:"name"`
	Secret              *SecretSource        `json:"secret,omitempty"`
	VolumeClaimTemplate *VolumeClaimTemplate `json:"volumeClaimTemplate,omitempty"`
	EmptyDir            bool                 `json:"emptyDir,omitempty"`
}

type SecretSource struct {
	SecretName string `json:"secretName"`
}

type VolumeClaimTemplate struct {
	AccessModes      []string `json:"accessModes"`
	StorageClassName string   `json:"storageClassName,omitempty"`
	Storage          string   `json:"storage"`
}

type PodTemplate struct {
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`
}

// DefaultMaxConcurrentRuns est le plafond retenu à défaut (doc 06 §6.3).
const DefaultMaxConcurrentRuns = 4

// Load lit un catalogue depuis un fichier.
func Load(path string) (*Catalog, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lecture du catalogue %q: %w", path, err)
	}
	return Parse(content)
}

// Parse analyse un catalogue et le valide structurellement.
//
// La validation structurelle est séparée de la validation contre le cluster : la
// première n'a besoin de rien, la seconde d'une connexion. Les distinguer permet
// de tester la première sans Kubernetes, et de donner deux messages d'erreur qui
// ne se confondent pas.
func Parse(content []byte) (*Catalog, error) {
	var catalog Catalog
	if err := yaml.UnmarshalStrict(content, &catalog); err != nil {
		// `UnmarshalStrict` refuse les champs inconnus. C'est voulu : une clé mal
		// orthographiée dans un ConfigMap serait sinon ignorée en silence, et le
		// pipeline tournerait sans le workspace ou le compte de service attendu.
		return nil, fmt.Errorf("catalogue illisible: %w", err)
	}

	if catalog.MaxConcurrentRuns == 0 {
		catalog.MaxConcurrentRuns = DefaultMaxConcurrentRuns
	}

	if err := catalog.validate(); err != nil {
		return nil, err
	}
	return &catalog, nil
}

var kindPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func (c *Catalog) validate() error {
	var problems []error

	if c.MaxConcurrentRuns < 1 {
		problems = append(problems, fmt.Errorf("maxConcurrentRuns doit être >= 1, vaut %d", c.MaxConcurrentRuns))
	}
	if len(c.Pipelines) == 0 {
		problems = append(problems, errors.New("le catalogue ne déclare aucun pipeline"))
	}

	suffixes := make(map[string]string, len(c.Environments))
	ranks := make(map[int]string, len(c.Environments))
	for _, environment := range c.Environments {
		if strings.TrimSpace(environment.ID) == "" || strings.TrimSpace(environment.PromotionSuffix) == "" {
			problems = append(problems, fmt.Errorf(
				"environnement %q: id et promotionSuffix sont requis", environment.ID))
		}
		// Deux environnements partageant un suffixe promouvraient au même
		// endroit : l'un des deux ne serait jamais déployé, sans erreur.
		if other, clash := suffixes[environment.PromotionSuffix]; clash {
			problems = append(problems, fmt.Errorf(
				"environnements %q et %q partagent le suffixe %q",
				other, environment.ID, environment.PromotionSuffix))
		}
		suffixes[environment.PromotionSuffix] = environment.ID

		if other, clash := ranks[environment.Rank]; clash {
			problems = append(problems, fmt.Errorf(
				"environnements %q et %q partagent le rang %d", other, environment.ID, environment.Rank))
		}
		ranks[environment.Rank] = environment.ID
	}

	seen := make(map[string]bool, len(c.Pipelines))
	for _, pipeline := range c.Pipelines {
		if seen[pipeline.Kind] {
			// Un doublon rendrait le lancement non déterministe : deux entrées
			// pour un même `kind`, et rien ne dit laquelle s'applique.
			problems = append(problems, fmt.Errorf("kind %q déclaré deux fois", pipeline.Kind))
		}
		seen[pipeline.Kind] = true
		problems = append(problems, pipeline.validate()...)
	}

	return errors.Join(problems...)
}

func (p Pipeline) validate() []error {
	var problems []error

	if !kindPattern.MatchString(p.Kind) {
		problems = append(problems, fmt.Errorf(
			"kind %q invalide: minuscules, chiffres et tirets uniquement", p.Kind))
	}
	if strings.TrimSpace(p.PipelineRef) == "" {
		problems = append(problems, fmt.Errorf("kind %q: pipelineRef est requis", p.Kind))
	}
	if !ValidQueueClass(p.QueueClass) {
		problems = append(problems, fmt.Errorf(
			"kind %q: queueClass %q invalide (attendu %q, %q ou %q)",
			p.Kind, p.QueueClass, ClassParallel, exclusivePrefix+"<nom>", ClassBarrier))
	}

	for name, param := range p.Params {
		switch param.Source {
		case SourceInput:
		case SourceConfig:
			if param.Key == "" {
				problems = append(problems, fmt.Errorf(
					"kind %q, paramètre %q: source config exige une clé", p.Kind, name))
			}
		case SourceLiteral:
			if param.Value == "" {
				problems = append(problems, fmt.Errorf(
					"kind %q, paramètre %q: source literal exige une valeur", p.Kind, name))
			}
		default:
			problems = append(problems, fmt.Errorf(
				"kind %q, paramètre %q: source %q inconnue (attendu %s, %s ou %s)",
				p.Kind, name, param.Source, SourceInput, SourceConfig, SourceLiteral))
		}
	}

	declared := make(map[string]bool, len(p.Workspaces))
	for _, workspace := range p.Workspaces {
		if declared[workspace.Name] {
			problems = append(problems, fmt.Errorf(
				"kind %q: workspace %q déclaré deux fois", p.Kind, workspace.Name))
		}
		declared[workspace.Name] = true

		name := workspace.Name
		sources := 0
		if workspace.Secret != nil {
			sources++
		}
		if workspace.VolumeClaimTemplate != nil {
			sources++
		}
		if workspace.EmptyDir {
			sources++
		}
		if sources != 1 {
			problems = append(problems, fmt.Errorf(
				"kind %q, workspace %q: exactement une source attendue (secret, volumeClaimTemplate ou emptyDir)",
				p.Kind, name))
		}
	}

	return problems
}

// ValidQueueClass dit si une classe est reconnue.
func ValidQueueClass(class string) bool {
	switch {
	case class == ClassParallel, class == ClassBarrier:
		return true
	case strings.HasPrefix(class, exclusivePrefix) && len(class) > len(exclusivePrefix):
		return true
	default:
		return false
	}
}

// Find rend l'entrée d'un kind.
func (c *Catalog) Find(kind string) (Pipeline, bool) {
	for _, pipeline := range c.Pipelines {
		if pipeline.Kind == kind {
			return pipeline, true
		}
	}
	return Pipeline{}, false
}

// Kinds rend les kinds déclarés, triés.
func (c *Catalog) Kinds() []string {
	kinds := make([]string, 0, len(c.Pipelines))
	for _, pipeline := range c.Pipelines {
		kinds = append(kinds, pipeline.Kind)
	}
	sort.Strings(kinds)
	return kinds
}

// RequiredInputs rend les paramètres que le lancement doit fournir.
func (p Pipeline) RequiredInputs() []string {
	var required []string
	for name, param := range p.Params {
		if param.Source == SourceInput && param.Required {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	return required
}
