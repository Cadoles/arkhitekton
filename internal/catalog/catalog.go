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

// DefaultKindConcurrency est le plafond d'un type qui n'en déclare pas.
//
// Cinq, et non un : la plupart des pipelines — analyse statique, tests
// unitaires, audit — n'ont aucune raison de s'exclure, et les faire attendre
// l'un l'autre allongerait la vérification d'une release sans rien protéger.
const DefaultKindConcurrency = 5

// ExclusiveKindConcurrency est le plafond d'un type qui ne supporte pas le
// parallélisme : tests d'intégration, E2E, charge. Chacun déploie ou mesure une
// cible unique, et deux exécutions simultanées se marchent dessus.
const ExclusiveKindConcurrency = 1

// Catalog est le catalogue chargé.
type Catalog struct {
	Pipelines []Pipeline `json:"pipelines"`
	// Environments vit dans le même fichier que les pipelines : les deux
	// décrivent ce que le dashboard peut déclencher, et les séparer imposerait
	// deux montages et deux chargements pour une même intention.
	Environments []Environment `json:"environments,omitempty"`
	// ScoreWeights porte la pondération du score de qualité. Vide, la pondération
	// acceptée par le client s'applique — l'absence de configuration ne doit pas
	// rendre le score incalculable (B11).
	Score map[string]float64 `json:"score,omitempty"`
	// MaxConcurrentRuns protège le cluster : les pipelines demandent des PVC et
	// des pods de build, et rien n'empêche mécaniquement d'en lancer vingt.
	MaxConcurrentRuns int `json:"maxConcurrentRuns"`
	// PodTemplate s'applique à tous les pipelines, sauf à ceux qui en déclarent
	// un.
	//
	// Global plutôt que répété par entrée, et c'est le correctif d'un défaut
	// constaté : **les cinq pipelines tirent au moins une image de
	// `reg.cadoles.com`**, projet privé, et aucun ne déclarait de secret de
	// tirage. Les runs échouaient en `TaskRunImagePullFailed` — pas au moment de
	// pousser, mais avant même de démarrer l'étape. Répéter le réglage sur chaque
	// entrée l'aurait fait oublier sur la suivante ; ici, une entrée nouvelle en
	// hérite sans qu'on y pense.
	PodTemplate *PodTemplate `json:"podTemplate,omitempty"`
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
	// MaxConcurrent borne la file de ce type. Zéro vaut DefaultKindConcurrency :
	// un type nouveau démarre parallélisable, ce qui est le cas courant, et
	// s'exclut en le déclarant plutôt qu'en l'oubliant.
	//
	// Remplace la classe d'exclusion du modèle précédent, qui groupait plusieurs
	// types dans une même file sans que rien ne dise lesquels. Un plafond répond
	// à la seule question posée — combien peuvent tourner ensemble — et se lit
	// sans avoir à parcourir le catalogue entier.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
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
	// Param est le paramètre du Pipeline qui reçoit l'URL pré-signée. Sans lui, la
	// clé serait calculée sans que personne ne sache où l'envoyer.
	Param string `json:"param"`
	// Artifact est le nom de l'objet sous `raw/<run>/`. Il porte l'extension du
	// contenu réel — `.tar.gz` pour un répertoire empaqueté — parce que la Task
	// publie ce que le mode déclare, et non ce qu'elle trouve.
	Artifact string `json:"artifact"`
	Family   string `json:"family"`
	Format   string `json:"format"`
}

// Workspace décrit un workspace du PipelineRun.
type Workspace struct {
	Name                string               `json:"name"`
	Secret              *SecretSource        `json:"secret,omitempty"`
	VolumeClaimTemplate *VolumeClaimTemplate `json:"volumeClaimTemplate,omitempty"`
	EmptyDir            bool                 `json:"emptyDir,omitempty"`
}

// SecretSource désigne le secret monté sur un workspace.
//
// Deux formes, exclusives :
//
//	secret: { secretName: git-credentials }         # nom littéral
//	secret: { configKey: pipeline.gitSecret }       # nom tiré de la configuration
//
// La seconde existe pour qu'on puisse viser un dépôt de test sans modifier le
// catalogue versionné : les identifiants qui donnent accès à `CNOUS/mse` ne sont
// pas ceux qui donnent accès à un bac à sable, et le nom du secret est la seule
// chose qui distingue les deux.
//
// **C'est le nom qui circule, jamais la matière.** Le dashboard n'a pas
// `secrets` dans sa RBAC (doc 07 §7.3) et ne doit pas l'avoir : transmettre une
// clé en paramètre la ferait atterrir dans la spec du PipelineRun, lisible par
// quiconque a `pipelineruns: get` sur le namespace. C'est le même principe que
// les URLs pré-signées de B14 — le dashboard désigne, il ne distribue pas.
type SecretSource struct {
	SecretName string `json:"secretName,omitempty"`
	ConfigKey  string `json:"configKey,omitempty"`
}

type VolumeClaimTemplate struct {
	AccessModes      []string `json:"accessModes"`
	StorageClassName string   `json:"storageClassName,omitempty"`
	Storage          string   `json:"storage"`
}

type PodTemplate struct {
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`
	// ImagePullSecretsConfigKey nomme une clé de configuration portant le nom du
	// secret, pour la même raison que `SecretSource.ConfigKey` : viser un autre
	// registre ne doit pas demander de modifier le catalogue versionné.
	ImagePullSecretsConfigKey string `json:"imagePullSecretsConfigKey,omitempty"`
}

// DefaultMaxConcurrentRuns est le plafond global retenu à défaut.
//
// Au-dessus du plafond par type, et pas par confort : à quatre, aucune file de
// cinq places n'atteindrait jamais la sienne et le réglage par type serait
// décoratif.
const DefaultMaxConcurrentRuns = 10

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
	if p.MaxConcurrent < 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: maxConcurrent doit être >= 1 (ou absent), vaut %d", p.Kind, p.MaxConcurrent))
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

// Concurrency rend le plafond effectif d'un type.
func (p Pipeline) Concurrency() int {
	if p.MaxConcurrent > 0 {
		return p.MaxConcurrent
	}
	return DefaultKindConcurrency
}

// Concurrency rend les plafonds déclarés, par type.
//
// Les types absents du catalogue — les tâches locales, qui ne deviennent pas des
// PipelineRun — n'y figurent pas : l'ordonnanceur leur applique le défaut, comme
// à un type nouveau.
func (c *Catalog) Concurrency() map[string]int {
	limits := make(map[string]int, len(c.Pipelines))
	for _, pipeline := range c.Pipelines {
		limits[pipeline.Kind] = pipeline.Concurrency()
	}
	return limits
}

// Find rend l'entrée d'un kind.
func (c *Catalog) Find(kind string) (Pipeline, bool) {
	for _, pipeline := range c.Pipelines {
		if pipeline.Kind == kind {
			// Le podTemplate global s'applique à qui n'en déclare pas. Hérité ici
			// plutôt qu'au chargement : le catalogue reste alors le reflet exact
			// du fichier, et ce qui est hérité se lit dans une seule fonction.
			if pipeline.PodTemplate == nil {
				pipeline.PodTemplate = c.PodTemplate
			}
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

// ScoreWeights rend la pondération du score, sous la forme attendue par le
// calcul.
//
// La conversion se fait ici plutôt qu'au chargement : le catalogue est du YAML
// générique, et lui faire connaître le type d'un autre paquet le lierait à une
// implémentation qu'il n'a pas à connaître.
func (c *Catalog) ScoreWeights() map[string]float64 {
	if len(c.Score) == 0 {
		return nil
	}
	weights := make(map[string]float64, len(c.Score))
	for signal, weight := range c.Score {
		weights[signal] = weight
	}
	return weights
}

// ResolveSecrets rend une copie du pipeline dont les noms de secrets sont
// résolus depuis la configuration.
//
// Résolu avant la création du PipelineRun plutôt qu'au chargement du catalogue :
// le catalogue est lu une fois au démarrage, et une clé de configuration absente
// doit se signaler au moment où elle manque, avec le kind qui la réclame.
func (p Pipeline) ResolveSecrets(config map[string]string) (Pipeline, error) {
	resolved := p
	resolved.Workspaces = make([]Workspace, len(p.Workspaces))
	copy(resolved.Workspaces, p.Workspaces)

	if template := resolved.PodTemplate; template != nil && template.ImagePullSecretsConfigKey != "" {
		name, present := config[template.ImagePullSecretsConfigKey]
		if !present || name == "" {
			return Pipeline{}, fmt.Errorf(
				"kind %q, secret de tirage: clé de configuration %q absente",
				p.Kind, template.ImagePullSecretsConfigKey)
		}
		// Copie, pour la même raison que les workspaces : le catalogue est partagé.
		resolved.PodTemplate = &PodTemplate{ImagePullSecrets: []string{name}}
	}

	for i, workspace := range resolved.Workspaces {
		if workspace.Secret == nil || workspace.Secret.ConfigKey == "" {
			continue
		}

		name, present := config[workspace.Secret.ConfigKey]
		if !present || name == "" {
			return Pipeline{}, fmt.Errorf(
				"kind %q, workspace %q: clé de configuration %q absente",
				p.Kind, workspace.Name, workspace.Secret.ConfigKey)
		}

		// La copie du pointeur est nécessaire : sans elle, la résolution
		// écrirait dans le catalogue partagé, et le second run verrait le nom
		// résolu du premier comme s'il était littéral.
		resolved.Workspaces[i].Secret = &SecretSource{SecretName: name}
	}

	return resolved, nil
}
