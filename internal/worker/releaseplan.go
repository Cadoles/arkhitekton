package worker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"log/slog"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Le plan d'une release.
 *
 * # La porte du chantier
 *
 * Doc 15 §15.9 en fait une porte, et la question qu'elle pose est simple : la
 * suite **construire puis vérifier** entre-t-elle dans un plan unique ? Si elle
 * n'y entre pas, le modèle est trop pauvre, et on l'apprend avant d'avoir écrit
 * un éditeur.
 *
 * Elle y entre, et sa forme dit pourquoi : deux étapes, la seconde conditionnée
 * à la réussite de la première. C'est exactement ce que faisaient `enqueueBuild`
 * — une boucle sur les cibles, dans l'API — et `launchVerification` — une boucle
 * sur les familles, dans l'observateur. Deux boucles dans deux fichiers, reliées
 * par un fait intermédiaire, là où il y avait un plan non déclaré.
 *
 * # Déclaré, et plus composé
 *
 * Le plan était composé ici, en Go, depuis la colonne `targets` et le catalogue.
 * Il est désormais **lu** : le projet le déclare depuis l'onglet automatisation,
 * et ce fichier ne connaît plus ni cible, ni famille particulière (doc 15 §15.5).
 *
 * Ce que cela retire est visible à l'œil : la famille `build` n'apparaît plus
 * nulle part, et le `if geste.Ref == "build"` qui aiguillait deux façons de
 * renseigner les paramètres non plus. Les trois paramètres qu'il posait en clair
 * — `revision`, `version`, `tag-name` — étaient la recopie de ce que le catalogue
 * déclarait déjà par ses `suggest`.
 *
 * La composition survit à un seul endroit, `automationreprise.go`, où elle dote
 * d'une automatisation les projets qui n'en ont jamais déclaré.
 *
 * # Ce que le geste porte, et ce que le contexte porte
 *
 * Le geste porte ce que la **déclaration** fixe : l'image à produire, l'étape du
 * Dockerfile, son contexte de build. Le contexte de l'exécution porte ce qui
 * dépend du **fait déclencheur** : la révision, le tag.
 *
 * Cette séparation n'est pas une commodité. C'est elle qui rend un plan
 * réutilisable d'une release à l'autre — un plan qui figerait la révision ne
 * servirait qu'une fois — et c'est la même règle que pour les réactions
 * déclarées (doc 14 §14.3).
 */

// Clés du contexte d'exécution du plan d'une release.
//
// Nommées plutôt qu'écrites en clair aux deux bouts : le plan les pose, le runner
// les lit, et deux orthographes produiraient un geste sans paramètres dont rien
// ne dirait pourquoi.
const (
	contextRevision  = "revision"
	contextTag       = "tag"
	contextReleaseID = "release_id"
	contextRegistry  = "registry"
)

// releaseTrigger nomme le déclencheur du plan d'une release.
//
// Distinct de `builtin:verification`, qu'il remplace : celui-ci couvrait la
// moitié aval de la chaîne, celui-ci la porte entière.
//
// Défini dans `plan` depuis que l'API doit **retrouver** cette exécution pour
// lire les poids figés d'un score : deux orthographes désigneraient deux
// exécutions, et le score se calculerait sur un plan qui n'a pas tourné.
const releaseTrigger = plan.ReleaseTrigger

// verificationRequester nomme le demandeur des jobs qu'un plan enfile.
//
// Distinct d'un identifiant humain : ces jobs ne sont demandés par personne, et
// afficher le nom de qui a créé la release ferait croire à un lancement manuel.
const verificationRequester = "system:plan"

// releaseContext compose le contexte d'exécution du plan d'une release.
//
// La révision est le **commit**, jamais la branche dont il vient : reconstruire
// la même release six mois plus tard doit produire la même image (B17).
func releaseContext(release store.Release, revision, registryProject string) map[string]string {
	return map[string]string{
		contextRevision:  revision,
		contextTag:       release.Tag,
		contextReleaseID: strconv.FormatInt(release.ID, 10),
		contextRegistry:  registryProject,
	}
}

// gesteParams compose les paramètres d'un geste du plan d'une release.
//
// # Trois origines, et le catalogue les dit déjà
//
// Ce qu'un geste **déclare** d'abord, gabarits liés au contexte. Puis, pour
// chaque entrée que le pipeline demande, l'une des trois règles que porte
// [catalog.Input.Origin] :
//
//   - le contexte **impose** la révision et le tag. Une déclaration ne peut pas
//     les changer : deux gestes d'une même release qui viseraient deux commits
//     publieraient deux images sous le même tag (B17) ;
//   - une référence d'image est un **repli** — la déclaration l'emporte quand
//     elle vise l'artefact qu'une construction a réellement produit ;
//   - le reste n'est renseigné que par la déclaration.
//
// # Ce que cela remplace
//
// Deux chemins vivaient ici, aiguillés par `geste.Ref == "build"` : l'un posait
// `revision`, `version` et `tag-name` en clair, l'autre appelait
// `VerificationInputs`. Les trois noms en clair étaient la recopie de ce que le
// catalogue déclarait déjà par ses `suggest` — la famille `build` cesse donc
// d'être un cas particulier de ce fichier.
func gesteParams(
	pipeline catalog.Pipeline, geste plan.Geste, context map[string]string,
) (map[string]string, error) {
	params := make(map[string]string, len(geste.Params)+4)
	for name, value := range geste.Params {
		// Les gabarits sont liés au contexte, qui porte la charge du fait **et**
		// ce que les étapes précédentes ont produit : `{artifacts.portal.image}`.
		params[name] = action.Expand(value, context)
	}

	if manquant := artefactNonResolu(params); manquant != "" {
		// Refusé plutôt que parti avec un gabarit littéral. Un pipeline qui
		// recevrait `{artifacts.portal.image}` comme image clonerait, échouerait
		// au `docker pull`, et le motif parlerait d'un dépôt introuvable — à
		// mille lieues de la cause.
		//
		// Doc 15 §15.4 tranche dans ce sens : un test qui ne tourne pas en
		// silence se lit comme un test passé.
		return nil, fmt.Errorf("l'artefact %s n'a pas été produit par une étape "+
			"précédente", manquant)
	}

	for _, input := range pipeline.Inputs() {
		fourni := input.FromContext(
			context[contextRevision], context[contextTag], context[contextRegistry])

		switch input.Origin() {
		case catalog.OriginContext:
			params[input.Name] = fourni
		case catalog.OriginFallback:
			if strings.TrimSpace(params[input.Name]) == "" {
				params[input.Name] = fourni
			}
		case catalog.OriginDeclared:
			// Rien à fournir : seule la déclaration le renseigne.
		}

		if input.Required && strings.TrimSpace(params[input.Name]) == "" {
			// Un paramètre requis qu'on ne sait pas renseigner ferait échouer le
			// run à l'ordonnancement, sur un message qui ne désigne pas la cause.
			// Ne pas l'enfiler du tout est plus honnête.
			return nil, fmt.Errorf("paramètre %q non renseigné par la déclaration "+
				"ni par l'exécution", input.Name)
		}
	}
	return params, nil
}

// artefactNonResolu rend la première référence d'artefact restée littérale.
//
// Seulement `{artifacts.…}` : les autres variables restent littérales quand elles
// ne sont pas dans la charge, et c'est délibéré — un `{tag}` visible dans un
// message se remarque et se corrige. Un artefact, lui, désigne quelque chose qui
// devait exister : son absence est une erreur de plan, pas de gabarit.
func artefactNonResolu(params map[string]string) string {
	for _, value := range params {
		reste := value
		for {
			debut := strings.Index(reste, "{artifacts.")
			if debut < 0 {
				break
			}
			fin := strings.IndexByte(reste[debut:], '}')
			if fin < 0 {
				break
			}
			return reste[debut+1 : debut+fin]
		}
	}
	return ""
}

// ReleaseRunner met en œuvre les gestes du plan d'une release.
//
// Les deux natures de pipeline passent par lui : construire et vérifier ne
// diffèrent que par la façon de composer leurs paramètres, et les séparer en
// deux runners aurait demandé au moteur de savoir laquelle il tient.
// Il prend [Enqueuer] plutôt qu'un `runSink`, et c'est ce qui permet à l'API
// comme au worker de construire le même runner : ouvrir le plan d'une release est
// une écriture en base, pas un ordonnancement. L'ordonnanceur reste unique, dans
// le worker.
type ReleaseRunner struct {
	catalog *catalog.Catalog
	queue   Enqueuer
	logger  *slog.Logger
}

func NewReleaseRunner(
	registry *catalog.Catalog, queue Enqueuer, logger *slog.Logger,
) *ReleaseRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReleaseRunner{catalog: registry, queue: queue, logger: logger}
}

func (r *ReleaseRunner) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	if geste.Kind != plan.GestePipeline {
		// Les actions d'un plan de release passeront par le routeur habituel. Ici
		// seuls les pipelines : refuser nommément vaut mieux que rendre « parti ».
		return nil, fmt.Errorf("geste %q: ce runner ne met en œuvre que des pipelines",
			geste.Ref)
	}

	pipeline, found := r.catalog.Find(geste.Ref)
	if !found {
		return nil, fmt.Errorf("famille %q absente du catalogue", geste.Ref)
	}

	params, err := gesteParams(pipeline, geste, run.Context)
	if err != nil {
		return nil, err
	}

	id, err := r.queue.Enqueue(ctx, store.JobRequest{
		Kind:        pipeline.Kind,
		Params:      params,
		RequestedBy: verificationRequester,
		ReleaseID:   run.ReleaseID,
		ProjectID:   run.ProjectID,
	})
	if err != nil {
		return nil, err
	}

	r.logger.Info("geste de release lancé",
		"release", run.Context[contextTag], "kind", geste.Ref,
		"geste", strings.TrimSpace(geste.Label), "job", id)
	return &id, nil
}

// ReleaseStates est ce que le cycle de vie attend de la base.
type ReleaseStates interface {
	AdvanceReleaseState(ctx context.Context, releaseID int64, from, to string) (bool, error)
}

// ReleaseLifecycle avance une release au rythme des étapes de son plan.
//
// # Ce qu'il remplace
//
// Le décompte des constructions. L'observateur comptait les builds d'une release
// — `CountBuilds` — pour savoir si la dernière venait de conclure, parce qu'il
// était appelé une fois par image sans rien savoir de l'ensemble.
//
// L'étape **est** cet ensemble. Son règlement dit exactement ce que le décompte
// cherchait à établir, et le dit sans requête : le moteur tient déjà l'état de
// chaque geste.
//
// # Idempotent par construction
//
// `StageSettled` est appelé à chaque avancement, sur toutes les étapes déjà
// réglées — le moteur ne tient pas de marque de « déjà annoncé ». La répétition
// est sans effet parce que la transition est **conditionnée à l'état de départ** :
// une seule livraison la gagne, les suivantes n'écrivent rien.
type ReleaseLifecycle struct {
	releases ReleaseStates
	logger   *slog.Logger
}

func NewReleaseLifecycle(releases ReleaseStates, logger *slog.Logger) *ReleaseLifecycle {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReleaseLifecycle{releases: releases, logger: logger}
}

func (l *ReleaseLifecycle) StageSettled(
	ctx context.Context, run store.PlanRun, role plan.Role, outcome plan.State,
) {
	if run.ReleaseID == nil {
		return
	}

	// Une étape entièrement sautée n'a rien tenté, et ne dit donc rien de la
	// release. Le cas se produit quand la construction a échoué : les tests sont
	// sautés, et la release est déjà `failed` par l'étape d'avant.
	var from, to string
	switch {
	case role == plan.RoleBuild && outcome == plan.Succeeded:
		from, to = string(release.StateBuilding), string(release.StateVerifying)
	case role == plan.RoleBuild && outcome == plan.Failed:
		from, to = string(release.StateBuilding), string(release.StateFailed)
	case role == plan.RoleTest && outcome == plan.Succeeded:
		from, to = string(release.StateVerifying), string(release.StateReady)
	case role == plan.RoleTest && outcome == plan.Failed:
		from, to = string(release.StateVerifying), string(release.StateFailed)
	default:
		return
	}

	advanced, err := l.releases.AdvanceReleaseState(ctx, *run.ReleaseID, from, to)
	if err != nil {
		l.logger.Error("avancement d'une release par son plan",
			"execution", run.ID, "release", *run.ReleaseID, "vers", to, "error", err)
		return
	}
	if !advanced {
		// Le cas courant d'une répétition, et non une anomalie : l'étape est
		// annoncée à chaque avancement du plan.
		l.logger.Debug("release déjà avancée", "execution", run.ID,
			"release", *run.ReleaseID, "de", from, "vers", to)
		return
	}
	l.logger.Info("release avancée par son plan", "execution", run.ID,
		"release", *run.ReleaseID, "etape", role, "de", from, "vers", to)
}

// ReleasePlanner ouvre le plan d'une release.
//
// # Pourquoi côté API
//
// La création d'une release est une décision humaine, servie par l'`api`. Ouvrir
// son plan **là** conserve ce que faisait `enqueueBuild` : la personne qui crée
// la release sait tout de suite si la construction est partie, plutôt que de
// l'apprendre au tour de boucle suivant d'un worker.
//
// Et cela ne duplique aucun ordonnanceur : ouvrir un plan écrit une exécution et
// met des jobs en file. C'est ce que l'API faisait déjà. Le worker garde
// l'exclusivité de ce qui **sort** — créer les PipelineRun — et de l'avancement.
type ReleasePlanner struct {
	engine      *Engine
	catalog     *catalog.Catalog
	automations Automations
	registry    string
	logger      *slog.Logger
}

// Automations rend le plan qu'un projet a déclaré.
//
// Interface étroite plutôt que le magasin entier : ouvrir le plan d'une release
// n'a besoin que de cette réponse.
type Automations interface {
	ReleaseAutomation(ctx context.Context, projectID int64) (store.ReleaseAutomation, error)
}

func NewReleasePlanner(
	engine *Engine, registry *catalog.Catalog, automations Automations,
	registryProject string, logger *slog.Logger,
) *ReleasePlanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReleasePlanner{
		engine: engine, catalog: registry, automations: automations,
		registry: registryProject, logger: logger,
	}
}

// OpenReleasePlan compose le plan d'une release et le fait partir.
//
// Rend le nombre de gestes effectivement partis. L'appelant en dépend : une
// release dont rien n'est parti ne doit pas être annoncée « en construction ».
func (p *ReleasePlanner) OpenReleasePlan(
	ctx context.Context, project store.Project, record store.Release, revision string,
) int {
	if p == nil || p.engine == nil {
		return 0
	}
	if strings.TrimSpace(revision) == "" {
		// Rien ne part sans référence. Construire « ce que la branche désigne à
		// cet instant » est exactement ce que l'invariant de version interdit, et
		// une révision vide produirait un clone sur le défaut du pipeline — donc
		// une image dont personne ne saurait dire d'où elle vient (B17).
		p.logger.Warn("aucune construction lancée",
			"release", record.Tag, "raison", "aucune référence Git à construire")
		return 0
	}

	// Le plan **déclaré**, et plus composé ici.
	//
	// Ce que cela retire : la boucle sur `targets`, le filtre sur les familles
	// activées, le drapeau `verification`. Un projet qui produit un binaire ou une
	// archive entre par la même porte, sans que ce fichier connaisse sa forme
	// (doc 15 §15.5).
	sujet, err := p.declaredPlan(ctx, project)
	if err != nil {
		// Une lecture impossible n'ouvre **rien**. Composer un repli ferait
		// construire autre chose que ce que le projet déclare, sous le tag d'une
		// release — et personne ne saurait que ce n'est pas la bonne.
		p.logger.Error("lecture de l'automatisation",
			"release", record.Tag, "projet", project.Slug, "error", err)
		return 0
	}
	if sujet.Empty() {
		p.logger.Info("aucun plan pour cette release",
			"release", record.Tag, "projet", project.Slug,
			"raison", "le projet ne déclare aucune automatisation")
		return 0
	}

	projectID := project.ID
	releaseID := record.ID
	return p.engine.Trigger(ctx, store.PlanRun{
		Trigger:   releaseTrigger,
		EventKey:  event.ReleaseCreatedKey(record.ID),
		ProjectID: &projectID,
		ReleaseID: &releaseID,
		Plan:      sujet,
		Context:   releaseContext(record, revision, p.registry),
	})
}

// declaredPlan rend le plan que le projet a déclaré.
//
// Une automatisation absente rend un plan vide et **aucune erreur** : c'est
// l'état d'un projet neuf, que la reprise du worker remplira à son prochain
// démarrage. Le traiter en panne enverrait chercher un défaut là où il n'y a
// qu'une déclaration à écrire.
func (p *ReleasePlanner) declaredPlan(
	ctx context.Context, project store.Project,
) (plan.Plan, error) {
	if p.automations == nil {
		return plan.Plan{}, nil
	}

	entry, err := p.automations.ReleaseAutomation(ctx, project.ID)
	if errors.Is(err, store.ErrNoAutomation) {
		return plan.Plan{}, nil
	}
	if err != nil {
		return plan.Plan{}, err
	}
	return entry.Plan, nil
}

// ProductOf rend la nature de ce qu'une famille du catalogue laisse derrière
// elle.
//
// Le **premier** produit déclaré, et vide quand la famille n'en déclare aucun ou
// déclare `none`. Un pipeline qui construit puis teste en rend deux ; retenir le
// premier suffit à nommer la nature de ses artefacts, et choisir par artefact
// demanderait au catalogue une déclaration par résultat qu'il ne porte pas.
func ProductOf(registry *catalog.Catalog) func(ref string) string {
	return func(ref string) string {
		if registry == nil {
			return ""
		}
		pipeline, found := registry.Find(ref)
		if !found {
			return ""
		}
		for _, product := range pipeline.Produces {
			if product != catalog.ProductNone {
				return string(product)
			}
		}
		return ""
	}
}
