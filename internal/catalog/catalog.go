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
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/images"
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
	// Projects porte ce que le catalogue déclare pour un projet, par slug : ses
	// environnements (doc 16 §16.18).
	//
	// Dans le même fichier que les pipelines : les deux décrivent ce que le
	// dashboard peut déclencher, et les séparer imposerait deux montages et deux
	// chargements pour une même intention.
	Projects map[string]ProjectDeclaration `json:"projects,omitempty"`
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
	ID        string `json:"id"`
	Label     string `json:"label"`
	Rank      int    `json:"rank"`
	Namespace string `json:"namespace"`
	// Deployer porte ce dont l'outil de déploiement a besoin, dans **son**
	// vocabulaire et sans que le catalogue le connaisse.
	//
	// Trois champs nommés vivaient ici jusqu'au lot D — `promotionSuffix`,
	// `acceptsPrerelease`, `fluxKustomization` —, et le dernier disait son
	// origine dans son nom. Les deux premiers la disaient moins fort mais tout
	// autant : une métadonnée SemVer retenue par une plage n'a de sens que pour
	// une sélection par plage, et rien de tout cela ne s'applique à un déployeur
	// qui procède autrement.
	//
	// La validation, surtout, **exigeait** un `promotionSuffix` non vide sur
	// chaque environnement. Un projet sans outil de déploiement ne pouvait donc
	// pas déclarer un environnement — le catalogue imposait Flux à qui n'en
	// avait pas (doc 12 §12.9, lot D).
	//
	// Chaînes plutôt qu'un type : c'est ce qu'une configuration transporte, et la
	// validation revient au déployeur, qui seul sait ce que ces clés veulent
	// dire. Le prix d'une frontière que l'on veut franchissable.
	Deployer map[string]string `json:"deployer,omitempty"`
	// Observable vaut false pour la production, dans un cluster indépendant.
	Observable *bool `json:"observable,omitempty"`
	Visible    *bool `json:"visible,omitempty"`
	// Sensitive réserve la promotion au rôle `releaser`. Vaut pour les
	// environnements qui reçoivent ce qui part chez l'utilisateur final.
	Sensitive *bool `json:"sensitive,omitempty"`
}

// IsObservable rend la valeur effective, l'absence valant « observable ».
func (e Environment) IsObservable() bool { return e.Observable == nil || *e.Observable }

// IsVisible rend la valeur effective, l'absence valant « visible ».
func (e Environment) IsVisible() bool { return e.Visible == nil || *e.Visible }

// ProjectDeclaration est ce que le catalogue déclare pour un projet.
type ProjectDeclaration struct {
	// Environments sont les environnements vers lesquels le projet déploie, dans
	// leur ordre de promotion.
	Environments []Environment `json:"environments,omitempty"`
}

// EnvironmentsOf rend les environnements déclarés pour un projet, ou aucun.
func (c *Catalog) EnvironmentsOf(project string) []Environment {
	if c == nil {
		return nil
	}
	return c.Projects[project].Environments
}

// IsSensitive rend la valeur effective, l'absence valant « sensible » : un
// environnement déclaré sans y penser doit être plus difficile à atteindre, jamais
// plus facile.
func (e Environment) IsSensitive() bool { return e.Sensitive == nil || *e.Sensitive }

/*
 * Ce qu'un pipeline laisse derrière lui.
 *
 * Le catalogue classait par un booléen, `verification`, dont le commentaire disait
 * deux choses : « une construction réussie enchaîne ce pipeline » **et** « il est
 * relançable seul ». Deux propriétés indépendantes, un seul drapeau — et le
 * catalogue en service portait déjà la ligne où elles divergent : `e2e-tests`
 * publie trois artefacts et déclare `verification: false`, parce que le drapeau
 * exprime l'enchaînement et qu'une campagne E2E vise un environnement partagé.
 *
 * `Produces` nomme le second axe, celui que `verification` ne disait que par
 * corrélation. C'est ce qui rend le contrat de doc 13 **vérifiable** : un
 * pipeline qui annonce des résultats et n'en publie aucun devient une rupture
 * qu'on peut désigner, au lieu d'un écran vide dont rien ne dit la cause
 * (doc 12 §12.6, doc 13 §13.1).
 *
 * Le mot « famille » n'était pas disponible : il désigne déjà une famille de
 * résultats de tests — `unit`, `static`, `e2e` — et s'en resservir un niveau plus
 * haut aurait produit deux sens du même mot, à demeure.
 */

// Product nomme une chose qu'un pipeline produit.
//
// Jeu **fermé** : ce sont les issues que le dashboard sait lire, et en ajouter une
// demande du code — un lecteur, un écran — pas une ligne de configuration.
type Product string

const (
	// ProductImage : le pipeline publie des images, dont le dashboard lit les
	// digests et qu'un re-tag pourra reprendre.
	ProductImage Product = "image"
	// ProductResults : le pipeline publie des rapports conformes à doc 13 —
	// familles, marqueurs, artefacts pré-signés.
	ProductResults Product = "results"
	// ProductNone : le pipeline ne rend que son verdict. Un déploiement, une
	// migration, un nettoyage.
	//
	// Déclaré explicitement plutôt que représenté par une liste vide : « ce
	// pipeline ne produit rien » et « personne n'a répondu à la question » sont
	// deux choses différentes, et seule la seconde doit être refusée.
	ProductNone Product = "none"
)

// products énumère les valeurs admises, pour les nommer dans un message.
var products = []Product{ProductImage, ProductResults, ProductNone}

// Pipeline décrit un pipeline lançable.
type Pipeline struct {
	// Kind est l'identifiant employé par le dashboard. Distinct de PipelineRef :
	// il survit à un renommage côté `mse-tekton`, que le catalogue absorbe.
	Kind string `json:"kind"`
	// PipelineRef est le nom du Pipeline dans le cluster.
	PipelineRef string `json:"pipelineRef"`
	Label       string `json:"label"`
	// Verification dit qu'une construction réussie enchaîne ce pipeline, et qu'il
	// est relançable seul depuis l'écran d'une release.
	//
	// C'est une décision de **processus** — « ce projet vérifie ceci après avoir
	// construit » — qui vivait en Go, dans deux listes en dur tenues alignées par
	// un test : une dans l'ordonnanceur, une dans l'API. Elle appartient au
	// catalogue, à côté du pipeline qu'elle décrit : un déploiement sans campagne
	// E2E cesse ainsi d'avoir un pipeline fantôme dans sa file, sans recompiler
	// (doc 12 §12.9, chantier 6).
	//
	// Porté par chaque pipeline plutôt que par une liste en tête du catalogue :
	// une liste peut nommer une famille absente, et l'ordre d'enchaînement
	// deviendrait une seconde vérité à côté de l'ordre de déclaration.
	//
	// **Ne dit plus que l'enchaînement.** Ce qu'un pipeline produit se déclare
	// désormais dans `Produces`, et les deux axes cessent d'être confondus. Ce
	// drapeau disparaîtra le jour où l'enchaînement deviendra un plan déclaré
	// (doc 14).
	Verification bool `json:"verification,omitempty"`
	// Produces énumère ce que le pipeline laisse derrière lui.
	//
	// Une **liste** et non une valeur : un pipeline qui construit une image puis
	// lance ses tests dans la foulée rend les deux, et n'a pas à être coupé en
	// deux pour entrer dans le modèle.
	Produces []Product `json:"produces,omitempty"`
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
	// Suggest nomme la nature de la valeur attendue, pour que le formulaire de
	// lancement propose une liste plutôt qu'un champ vide.
	//
	// Déclaré ici et non déduit du nom du paramètre : `revision`,
	// `app-revision` et `mse-revision` désignent la même chose sous trois noms,
	// et coder cette correspondance en Go la ferait diverger du catalogue au
	// premier pipeline ajouté. Le catalogue sait ce qu'il demande.
	//
	// Sans effet sur l'exécution : une suggestion est une aide à la saisie, pas
	// une contrainte. Le champ reste libre — une référence absente d'une liste
	// n'est pas une référence inexistante.
	// Elle sert aussi à **renseigner** un paramètre lors d'un enchaînement
	// automatique, où personne ne saisit rien : c'est elle qui dit qu'un
	// `app-revision` veut une référence Git, sans que le code ait à connaître ce
	// nom-là. Voir Pipeline.VerificationInputs.
	Suggest string `json:"suggest,omitempty"`
	// Image nomme le dépôt d'image court — `portal`, `portal-cli` — que ce
	// paramètre doit recevoir. N'a de sens qu'avec `suggest: image`.
	//
	// Déclaré ici parce que la correspondance « ce paramètre veut cette image »
	// appartient au pipeline, pas au dashboard. Elle vivait dans une table de
	// noms en dur — `portal-image` → `portal` — recopiée à deux endroits, et qui
	// obligeait à modifier le Go pour tout pipeline nommant autrement ses
	// paramètres.
	Image string `json:"image,omitempty"`
	// Label, Help et Placeholder rendent le paramètre **saisissable**.
	//
	// # Pourquoi ils arrivent maintenant
	//
	// Tant qu'un plan de release était composé en Go, personne n'avait à lire un
	// nom de paramètre : `buildStage` écrivait `image-name` et `target` en clair.
	// Déclarer la construction depuis l'onglet automatisation renverse cela — un
	// humain choisit un pipeline et doit renseigner ce qu'il demande, sans lire
	// le catalogue (doc 15 §15.5).
	//
	// C'est exactement ce que `plugin.ActionField` fait déjà pour les actions. La
	// symétrie est voulue : les deux natures de geste se saisissent dans le même
	// formulaire, et rien ne justifierait qu'une action s'explique et qu'un
	// pipeline se devine.
	//
	// Facultatifs. Sans libellé, le nom du paramètre en tient lieu : un catalogue
	// existant reste utilisable, il est seulement plus sec.
	Label       string `json:"label,omitempty"`
	Help        string `json:"help,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

// Sources reconnues.
const (
	SourceInput   = "input"
	SourceConfig  = "config"
	SourceLiteral = "literal"
)

// Natures suggérées reconnues.
//
// Volontairement peu nombreuses : chacune correspond à une liste que le
// dashboard sait constituer. En ajouter une sans la source qui la peuple
// donnerait un menu déroulant vide, pire qu'un champ libre.
const (
	SuggestGitRef      = "git-ref"
	SuggestReleaseTag  = "release-tag"
	SuggestEnvironment = "environment"
	// SuggestImage attend une **référence** d'image complète, registre et tag
	// compris. Laquelle est dite par `Param.Image` : une seule nature suffit donc
	// là où il en fallait une par image — `portal-image`, `consumer-image` —, et
	// publier une image de plus ne demande plus de toucher au code.
	SuggestImage = "image"
	// SuggestImageName propose les dépôts publiés — `portal`, `portal-cli`,
	// `portal-dev` — pour les pipelines qui en prennent un en paramètre depuis
	// qu'ils n'en traitent qu'un par exécution.
	SuggestImageName = "image-name"
	// SuggestBuildTarget propose les cibles du Dockerfile qui les alimentent.
	SuggestBuildTarget = "build-target"
)

var suggestions = map[string]bool{
	SuggestGitRef: true, SuggestReleaseTag: true, SuggestEnvironment: true,
	SuggestImage: true, SuggestImageName: true, SuggestBuildTarget: true,
}

// Input décrit un paramètre à fournir au lancement.
type Input struct {
	Name     string
	Required bool
	Suggest  string
	Image    string
	// Label retombe sur le nom quand le catalogue n'en déclare pas : un
	// formulaire avec un champ sans étiquette est un champ qu'on ne remplit pas.
	Label       string
	Help        string
	Placeholder string
}

// Origin dit **qui renseigne** un paramètre dans le plan d'une release.
//
// La distinction existait, écrite en Go : `buildParams` posait `revision`,
// `version` et `tag-name` après les paramètres du geste, pour qu'une déclaration
// ne puisse pas les changer. Elle vaut pour tout pipeline, et le catalogue la
// disait déjà par ses `suggest` — il suffisait de la lire.
type Origin string

const (
	// OriginContext : l'exécution l'impose, une déclaration ne peut pas le
	// changer. La révision construite et le tag de la release, et rien d'autre :
	// deux gestes d'une même release qui viseraient deux commits publieraient
	// deux images sous le même tag (B17).
	OriginContext Origin = "context"
	// OriginFallback : l'exécution le renseigne **à défaut** de déclaration.
	//
	// La référence d'une image publiée. Sans déclaration, elle se recompose
	// depuis le registre et le tag, comme avant ; une déclaration peut viser
	// l'artefact qu'une étape de construction a réellement produit —
	// `{artifacts.portal.image}` — ce qui est plus vrai et échoue quand rien ne
	// l'a produit (doc 15 §15.4).
	OriginFallback Origin = "fallback"
	// OriginDeclared : seule une déclaration le renseigne. Le dépôt à publier, la
	// cible du Dockerfile, l'environnement visé.
	OriginDeclared Origin = "declared"
)

// Origin classe ce paramètre.
func (i Input) Origin() Origin {
	switch i.Suggest {
	case SuggestGitRef, SuggestReleaseTag:
		return OriginContext
	case SuggestImage:
		return OriginFallback
	default:
		return OriginDeclared
	}
}

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

	// La déduction vient **avant** la validation : elle comble ce qui se déduit
	// sans risque, et laisse la validation refuser le reste.
	catalog.deduceProducts()

	if err := catalog.validate(); err != nil {
		return nil, err
	}
	return &catalog, nil
}

// deduceProducts complète les pipelines dont le produit se déduit sans ambiguïté.
//
// Une seule règle, et c'est délibérément peu : `verification: true` implique la
// publication de rapports — la validation l'exige déjà, un pipeline enchaîné sans
// artefact ayant un verdict qui n'atteint jamais le dashboard.
//
// # Ce qui n'est pas déduit, et pourquoi
//
// Le reste demande une réponse humaine, parce que le catalogue ne porte aucun
// signal qui le dise. `build` et `retag` publient des images ; ni l'un ni l'autre
// ne déclare d'artefact, et rien ne les distingue structurellement d'un pipeline
// qui déploie. Les deviner par leur **nom** rétablirait la convention de nommage
// que cette déclaration existe pour supprimer.
//
// Et le cas qui décide : `e2e-tests` publie trois artefacts avec
// `verification: false`. Une déduction qui le rangerait dans « rien » perpétuerait
// exactement la confusion qu'on corrige. Il vaut mieux exiger la réponse.
func (c *Catalog) deduceProducts() {
	for index, pipeline := range c.Pipelines {
		if len(pipeline.Produces) > 0 || !pipeline.Verification {
			continue
		}
		c.Pipelines[index].Produces = []Product{ProductResults}
	}
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

	projects := make([]string, 0, len(c.Projects))
	for slug := range c.Projects {
		projects = append(projects, slug)
	}
	sort.Strings(projects)
	for _, slug := range projects {
		if strings.TrimSpace(slug) == "" {
			problems = append(problems, errors.New("une déclaration de projet est sans slug"))
		}
		ids := make(map[string]bool, len(c.Projects[slug].Environments))
		ranks := make(map[int]string, len(c.Projects[slug].Environments))
		for _, environment := range c.Projects[slug].Environments {
			if strings.TrimSpace(environment.ID) == "" {
				problems = append(problems, fmt.Errorf("projet %q: un environnement est déclaré sans id", slug))
			}
			if ids[environment.ID] {
				problems = append(problems, fmt.Errorf("projet %q: environnement %q déclaré deux fois", slug, environment.ID))
			}
			ids[environment.ID] = true
			// Ce que le catalogue ne vérifie **pas** : que le suffixe de promotion
			// existe et soit unique. C'est une règle du déployeur, vérifiée par lui
			// contre les politiques réelles du cluster.
			if other, clash := ranks[environment.Rank]; clash {
				problems = append(problems, fmt.Errorf(
					"projet %q: environnements %q et %q partagent le rang %d", slug, other, environment.ID, environment.Rank))
			}
			ranks[environment.Rank] = environment.ID
		}
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
	// Une vérification sans artefact déclaré a un verdict qui n'atteint jamais le
	// dashboard : le run tourne, conclut, et la release reste en attente d'un
	// résultat que rien ne remonte. Refusé au démarrage plutôt que constaté au
	// bout d'une campagne de quarante minutes (doc 06 §6.2).
	if p.Verification && len(p.Uploads) == 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: une vérification doit déclarer au moins un artefact", p.Kind))
	}
	problems = append(problems, p.validateProducts()...)
	if p.MaxConcurrent < 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: maxConcurrent doit être >= 1 (ou absent), vaut %d", p.Kind, p.MaxConcurrent))
	}

	for name, param := range p.Params {
		switch param.Source {
		case SourceInput:
			if param.Suggest != "" && !suggestions[param.Suggest] {
				problems = append(problems, fmt.Errorf(
					"kind %q, paramètre %q: suggestion %q inconnue", p.Kind, name, param.Suggest))
			}
			// Une image non nommée ne se devine pas : l'enchaînement rendrait une
			// référence tronquée, et le run échouerait au tirage sur un message
			// qui ne désigne pas le catalogue.
			if param.Suggest == SuggestImage && param.Image == "" {
				problems = append(problems, fmt.Errorf(
					"kind %q, paramètre %q: suggestion %q exige le nom de l'image",
					p.Kind, name, SuggestImage))
			}
			if param.Image != "" && param.Suggest != SuggestImage {
				problems = append(problems, fmt.Errorf(
					"kind %q, paramètre %q: image nommée sans suggestion %q",
					p.Kind, name, SuggestImage))
			}
		case SourceConfig:
			if param.Suggest != "" {
				// Une suggestion sur un paramètre que l'utilisateur ne saisit pas
				// ne s'afficherait nulle part : la signaler évite de croire le
				// formulaire cassé.
				problems = append(problems, fmt.Errorf(
					"kind %q, paramètre %q: suggestion sur une source %q", p.Kind, name, param.Source))
			}
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

// validateProducts refuse un produit absent, inconnu, ou en contradiction avec ce
// que le pipeline déclare par ailleurs.
//
// **L'absence est refusée au démarrage**, et c'est la doctrine du dépôt : une
// configuration incomplète découverte au premier clic se manifeste sur
// l'opération la plus rare, alors qu'un refus au démarrage est une erreur de
// déploiement, visible immédiatement et par la bonne personne.
//
// Le prix de la laisser passer est connu : sans produit déclaré, le dashboard ne
// sait pas si un run muet est un pipeline qui n'avait rien à dire ou un pipeline
// en rupture de contrat. C'est précisément le diagnostic qu'on cherche à rendre
// possible.
func (p Pipeline) validateProducts() []error {
	if len(p.Produces) == 0 {
		return []error{fmt.Errorf(
			"kind %q: `produces` est requis (%s) — ce qu'un pipeline laisse "+
				"derrière lui ne se devine pas, et le déduire de son nom "+
				"rétablirait la convention que cette déclaration remplace",
			p.Kind, joinProducts(products))}
	}

	var problems []error
	seen := map[Product]bool{}

	for _, product := range p.Produces {
		switch product {
		case ProductImage, ProductResults, ProductNone:
		default:
			problems = append(problems, fmt.Errorf(
				"kind %q: produit %q inconnu (admis: %s)",
				p.Kind, product, joinProducts(products)))
			continue
		}
		if seen[product] {
			problems = append(problems, fmt.Errorf(
				"kind %q: produit %q déclaré deux fois", p.Kind, product))
		}
		seen[product] = true
	}

	// « Rien » ne se cumule pas : un pipeline qui déclare `none` à côté d'un autre
	// produit se contredit, et rien ne dit laquelle des deux moitiés croire.
	if seen[ProductNone] && len(seen) > 1 {
		problems = append(problems, fmt.Errorf(
			"kind %q: %q ne se cumule pas avec un autre produit", p.Kind, ProductNone))
	}

	// Des résultats annoncés sans artefact déclaré ne remonteraient jamais : le
	// run tourne, conclut, et l'écran reste vide. Même contrôle que pour une
	// vérification, désormais adossé au produit plutôt qu'au drapeau.
	if seen[ProductResults] && len(p.Uploads) == 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: annonce des résultats sans déclarer d'artefact — son verdict "+
				"n'atteindrait jamais le dashboard", p.Kind))
	}

	// L'écart inverse est refusé lui aussi : des artefacts publiés que rien ne
	// vient lire. C'est le cas qu'`e2e-tests` illustrait — trois artefacts, et un
	// drapeau disant « pas une vérification » parce qu'il parlait d'autre chose.
	if !seen[ProductResults] && len(p.Uploads) > 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: déclare %d artefact(s) sans annoncer %q — ils seraient "+
				"publiés puis jamais lus", p.Kind, len(p.Uploads), ProductResults))
	}

	return problems
}

func joinProducts(list []Product) string {
	names := make([]string, 0, len(list))
	for _, product := range list {
		names = append(names, string(product))
	}
	return strings.Join(names, ", ")
}

// Yields dit si le pipeline produit cette chose.
func (p Pipeline) Yields(product Product) bool {
	return slices.Contains(p.Produces, product)
}

// Yields dit si le type nommé produit cette chose.
//
// Rendue par le catalogue et non calculée par l'appelant : c'est la question que
// posent l'ingestion, les faits et la validation d'un plan, et trois façons de la
// poser finiraient par diverger.
func (c *Catalog) Yields(kind string, product Product) bool {
	pipeline, found := c.Find(kind)
	return found && pipeline.Yields(product)
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

// VerificationInputs renseigne les entrées obligatoires d'un enchaînement.
//
// Les valeurs viennent de ce que le catalogue **déclare vouloir**, et non d'une
// table de noms tenue dans le code. Celle-ci existait en double — une copie dans
// l'enchaînement automatique, une dans la relance manuelle, qu'il fallait penser
// à modifier ensemble — et énumérait `revision`, `app-revision`, `mse-revision`,
// `portal-image`, `consumer-image` : cinq noms propres à un projet, qu'un
// pipeline nommant autrement ses paramètres ne pouvait pas employer.
//
// Rend le nom du paramètre resté vide plutôt qu'une table incomplète : un run
// créé sans ses entrées requises échoue à l'ordonnancement, sur un message qui
// ne désigne pas la cause.
func (p Pipeline) VerificationInputs(revision, tag, registry string) (map[string]string, string) {
	params := map[string]string{}

	for _, input := range p.Inputs() {
		if !input.Required {
			continue
		}

		value := input.FromContext(revision, tag, registry)
		if value == "" {
			return nil, input.Name
		}
		params[input.Name] = value
	}
	return params, ""
}

// FromContext rend la valeur que l'exécution d'une release donne à ce paramètre.
//
// Vide quand elle n'en donne aucune : c'est alors à la déclaration de le
// renseigner, et [Input.Origin] dit lequel des deux a le dernier mot.
func (i Input) FromContext(revision, tag, registry string) string {
	switch i.Suggest {
	case SuggestGitRef:
		return revision
	case SuggestReleaseTag:
		return tag
	case SuggestImage:
		// La vérification analyse les images **publiées** par la construction,
		// jamais une reconstruction : elle produirait un artefact différent de
		// celui qui sera déployé, et le verdict porterait à côté (B17).
		return images.Reference(registry, i.Image, tag)
	default:
		return ""
	}
}

// Declared rend les entrées qu'une déclaration peut renseigner.
//
// Tout sauf celles que l'exécution impose : les proposer inviterait à saisir une
// révision qui serait écrasée, ce qui est pire qu'une absence — on croirait
// l'avoir fixée.
func (p Pipeline) Declared() []Input {
	var declared []Input
	for _, input := range p.Inputs() {
		if input.Origin() == OriginContext {
			continue
		}
		declared = append(declared, input)
	}
	return declared
}

// Inputs rend tous les paramètres saisis au lancement, requis ou non.
//
// Distinct de RequiredInputs, qui sert à refuser un lancement incomplet : le
// formulaire, lui, doit montrer aussi les facultatifs — un paramètre qu'on ne
// voit pas est un paramètre qu'on ne renseigne jamais, et il fallait alors
// passer par le champ libre pour l'atteindre.
func (p Pipeline) Inputs() []Input {
	inputs := make([]Input, 0, len(p.Params))
	for name, param := range p.Params {
		if param.Source != SourceInput {
			continue
		}
		label := param.Label
		if strings.TrimSpace(label) == "" {
			label = name
		}
		inputs = append(inputs, Input{
			Name: name, Required: param.Required,
			Suggest: param.Suggest, Image: param.Image,
			Label: label, Help: param.Help, Placeholder: param.Placeholder,
		})
	}
	// Les requis d'abord, puis l'ordre alphabétique : une map Go n'a pas d'ordre,
	// et un formulaire dont les champs changent de place à chaque ouverture est
	// impraticable.
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].Required != inputs[j].Required {
			return inputs[i].Required
		}
		return inputs[i].Name < inputs[j].Name
	})
	return inputs
}

// Family rend la famille de signal que ce pipeline mesure.
//
// Vide quand il n'en déclare aucune — `build`, `retag` — ou plusieurs, cas où
// rien ne dit à laquelle rattacher un outil : mieux vaut ne pas classer que
// classer au hasard, une famille erronée déplaçant un verdict sur un signal qui
// n'a pas été mesuré.
//
// Lue des `uploads`, qui la déclarent déjà pour ranger les artefacts. Le canal de
// marqueurs s'en sert pour attribuer une famille à un outil qui n'a **rien**
// publié : il n'a alors pas d'artefact d'où la déduire, et c'est précisément le
// cas qu'on cherche à rendre visible.
func (p Pipeline) Family() string {
	var found string
	for _, upload := range p.Uploads {
		if upload.Family == "" || upload.Family == found {
			continue
		}
		if found != "" {
			return ""
		}
		found = upload.Family
	}
	return found
}

// VerificationKinds énumère ce qu'une construction réussie enchaîne.
//
// Dans l'ordre de déclaration, qui devient l'ordre de mise en file : une map Go
// n'a pas d'ordre, et des runs qui changent de rang à chaque construction rendent
// impossible de comparer deux releases d'un coup d'œil.
//
// Sert aussi à décider ce qui est relançable à l'unité. Les deux questions ont la
// même réponse, et les avoir séparées donnait un bouton qui pouvait lancer ce que
// la chaîne ne lançait pas — ou l'inverse, une famille enchaînée qu'on ne pouvait
// pas rattraper sans reconstruire toute la release.
func (c *Catalog) VerificationKinds() []string {
	kinds := make([]string, 0, len(c.Pipelines))
	for _, pipeline := range c.Pipelines {
		if pipeline.Verification {
			kinds = append(kinds, pipeline.Kind)
		}
	}
	return kinds
}

// IsVerification dit si une famille est une vérification enchaînée.
func (c *Catalog) IsVerification(kind string) bool {
	pipeline, found := c.Find(kind)
	return found && pipeline.Verification
}

// SuggestionKey nomme la liste à proposer pour cette entrée.
//
// La nature seule ne suffit plus depuis que `image` les désigne toutes : deux
// paramètres qui veulent tous deux une référence d'image n'attendent pas la même
// liste. La clé compose donc la nature et l'image, et c'est elle — et non le nom
// du paramètre — qui indexe les listes envoyées au formulaire.
func (i Input) SuggestionKey() string {
	if i.Suggest == "" || i.Image == "" {
		return i.Suggest
	}
	return i.Suggest + ":" + i.Image
}

// SuggestedImages rend les dépôts d'image que le catalogue déclare attendre.
//
// Constituer les listes à partir de là plutôt que d'une énumération en Go veut
// dire qu'un pipeline référençant une image de plus obtient son menu déroulant
// sans qu'on touche au dashboard.
func (c *Catalog) SuggestedImages() []string {
	seen := map[string]bool{}
	for _, pipeline := range c.Pipelines {
		for _, param := range pipeline.Params {
			if param.Suggest == SuggestImage && param.Image != "" {
				seen[param.Image] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

// ServiceAccounts rend les comptes de service que les pipelines emploient.
//
// Trié et dédoublonné. Sert au lot G : la clé d'un projet doit être montable par
// **chacun** d'eux, faute de quoi elle le serait pour certains de ses pipelines
// et pas pour les autres — et l'échec désignerait le clone plutôt que le
// montage.
//
// Le compte par défaut, celui qu'un pipeline qui n'en nomme aucun emploie, est
// rendu sous le nom `default` : c'est ainsi que Kubernetes le nomme, et c'est
// bien lui qu'il faudra autoriser.
func (c *Catalog) ServiceAccounts() []string {
	seen := map[string]bool{}
	for _, pipeline := range c.Pipelines {
		name := strings.TrimSpace(pipeline.ServiceAccountName)
		if name == "" {
			name = "default"
		}
		seen[name] = true
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
