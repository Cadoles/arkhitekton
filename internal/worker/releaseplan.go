package worker

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"log/slog"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
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
 * # Composé, pas encore déclaré
 *
 * Cette tranche compose le plan **en Go**, depuis la colonne `targets` et le
 * catalogue. C'est délibéré : elle prouve que le modèle porte l'enchaînement,
 * sans rien changer à ce qu'un projet a saisi. La déclaration vient ensuite, et
 * n'aura qu'à produire le même objet.
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
const (
	contextReleaseID = "release_id"
	contextRegistry  = "registry"
)

// releaseTrigger nomme le déclencheur du plan d'une release.
//
// Distinct de `builtin:verification`, qu'il remplace : celui-ci couvrait la
// moitié aval de la chaîne, celui-ci la porte entière.
const releaseTrigger = "builtin:release"

// buildKind est la famille du catalogue qui construit.
//
// En dur dans cette tranche, comme la composition elle-même : c'est ce que la
// déclaration retirera.
const buildKind = "build"

// releasePlan compose le plan d'une release : construire, puis vérifier.
//
// Rend un plan **vide** quand rien ne s'applique — ni cible à construire, ni
// famille à vérifier. L'appelant conclut alors tout de suite plutôt que
// d'attendre un verdict que personne ne rendra.
func releasePlan(
	registry *catalog.Catalog, project store.Project, enabled func(kind string) bool,
) plan.Plan {
	if registry == nil {
		return plan.Plan{}
	}

	var stages []plan.Stage
	if build := buildStage(registry, project); len(build.Gestes) > 0 {
		stages = append(stages, build)
	}
	if test := testStage(registry, enabled); len(test.Gestes) > 0 {
		stages = append(stages, test)
	}
	if len(stages) == 0 {
		return plan.Plan{}
	}
	return plan.Plan{Stages: stages}
}

// buildStage compose l'étape de construction depuis les cibles du projet.
//
// Un geste par cible, et c'est le point : les trois images de MSE cessent d'être
// une boucle dans l'API pour devenir trois gestes lisibles, relançables et
// — bientôt — déclarés.
func buildStage(registry *catalog.Catalog, project store.Project) plan.Stage {
	pipeline, found := registry.Find(buildKind)
	if !found {
		return plan.Stage{}
	}

	targets := project.Buildable()
	gestes := make([]plan.Geste, 0, len(targets))
	for _, target := range targets {
		gestes = append(gestes, plan.Geste{
			Kind: plan.GestePipeline, Ref: pipeline.Kind,
			// Le libellé est le nom de l'image : c'est lui qui distingue trois
			// gestes portant la même référence de catalogue, à l'écran comme dans
			// la carte des artefacts qui viendra.
			Label: target.Image,
			Params: map[string]string{
				"image-name": target.Image,
				"target":     target.Stage,
				"dockerfile": project.DockerfileFor(target),
				"context":    project.BuildContext,
			},
		})
	}

	return plan.Stage{Role: plan.RoleBuild, Label: "Construire", Gestes: gestes}
}

// testStage compose l'étape de vérification.
//
// `enabled` dit ce que le **projet** vérifie. Le catalogue dit ce que l'instance
// sait lancer, ce qui n'est pas la même question : un projet sans campagne de
// charge voyait `load-tests` s'enchaîner, échouer faute de scénario, et laisser
// sa release attendre un verdict qui ne viendrait jamais.
func testStage(registry *catalog.Catalog, enabled func(kind string) bool) plan.Stage {
	var gestes []plan.Geste
	for _, kind := range registry.VerificationKinds() {
		pipeline, found := registry.Find(kind)
		if !found {
			continue
		}
		if enabled != nil && !enabled(kind) {
			continue
		}
		gestes = append(gestes, plan.Geste{
			Kind: plan.GestePipeline, Ref: pipeline.Kind, Label: pipeline.Label,
			// `when` reste au défaut — succès de l'étape précédente. C'est ce qui
			// remplace le fait `release.built` : la condition d'étape dit la même
			// chose, sans aller-retour par le fil des faits.
		})
	}

	return plan.Stage{Role: plan.RoleTest, Label: "Vérifier", Gestes: gestes}
}

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

// buildParams compose les paramètres d'un geste de construction.
//
// Ceux du geste d'abord — ce que la déclaration fixe — puis ceux que le contexte
// impose. L'ordre compte : un geste ne doit pas pouvoir redéfinir la révision
// construite, sans quoi deux gestes d'une même release pourraient viser deux
// commits et publier deux images sous le même tag.
func buildParams(geste plan.Geste, context map[string]string) map[string]string {
	params := make(map[string]string, len(geste.Params)+4)
	maps.Copy(params, geste.Params)

	params["revision"] = context[contextRevision]
	params["version"] = context[contextTag]
	params["tag-name"] = context[contextTag]
	return params
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

	var params map[string]string
	if geste.Ref == buildKind {
		params = buildParams(geste, run.Context)
	} else {
		// Les valeurs sortent de ce que le catalogue déclare vouloir :
		// `revision`, `app-revision` et `mse-revision` désignent la même chose
		// sous trois noms hérités de pipelines écrits séparément.
		var missing string
		params, missing = pipeline.VerificationInputs(
			run.Context[contextRevision], run.Context[contextTag],
			run.Context[contextRegistry])
		if missing != "" {
			// Un paramètre requis qu'on ne sait pas renseigner ferait échouer le
			// run à l'ordonnancement : ne pas l'enfiler du tout est plus honnête,
			// et le motif dit lequel manque.
			return nil, fmt.Errorf("paramètre %q non renseignable", missing)
		}
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
	engine    *Engine
	catalog   *catalog.Catalog
	pipelines ProjectPipelines
	registry  string
	logger    *slog.Logger
}

func NewReleasePlanner(
	engine *Engine, registry *catalog.Catalog, pipelines ProjectPipelines,
	registryProject string, logger *slog.Logger,
) *ReleasePlanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReleasePlanner{
		engine: engine, catalog: registry, pipelines: pipelines,
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

	sujet := releasePlan(p.catalog, project, func(kind string) bool {
		if p.pipelines == nil {
			return true
		}
		enabled, err := p.pipelines.PipelineEnabled(ctx, project.ID, kind)
		if err != nil {
			// Une lecture impossible ne doit pas retirer une vérification : mieux
			// vaut l'enchaîner et la voir échouer que la sauter en silence.
			p.logger.Error("familles déclarées du projet",
				"projet", project.ID, "kind", kind, "error", err)
			return true
		}
		return enabled
	})
	if sujet.Empty() {
		p.logger.Info("aucun plan pour cette release",
			"release", record.Tag, "projet", project.Slug)
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
