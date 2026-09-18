package worker

import (
	"context"
	"log/slog"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * La reprise des projets qui n'ont jamais déclaré leur automatisation.
 *
 * # Pourquoi une reprise, et pas une migration SQL
 *
 * Doc 15 §15.8 demande que le passage soit silencieux : sans lui, tous les
 * projets cesseraient de construire au déploiement. Ce qui vient d'arriver pour
 * une autre raison, et qu'on ne va pas refaire.
 *
 * Composer ce plan en SQL demanderait de recopier la règle du Dockerfile résolu,
 * les familles de vérification du catalogue et le filtre par projet — trois
 * choses qui vivent dans le binaire, dont une dans une ConfigMap. La reprise
 * appelle donc le **même code** qui composait le plan jusqu'ici : le plan repris
 * est identique à celui qui tournait, par construction et non par relecture.
 *
 * # Elle ne s'exécute qu'une fois par projet, et ne revient pas
 *
 * Le critère est l'**absence de ligne**, jamais un plan vide. Un projet dont
 * quelqu'un a vidé l'automatisation a une ligne : il ne construit plus, et c'est
 * une décision. La reprise qui le reprendrait ferait réapparaître au redémarrage
 * suivant ce qu'on venait d'effacer.
 */

// buildKind est la famille du catalogue qui construit.
//
// Employée par la seule reprise : le plan d'une release ne connaît plus de
// famille particulière depuis qu'il est déclaré. C'est ce qui restait de la
// composition en Go, et il n'en reste que ça.
const buildKind = "build"

// ProjectPipelines dit ce qu'un projet déclare vérifier.
//
// Ce que la reprise reprend. La table `project_pipelines` n'est plus lue à
// l'ouverture d'un plan — l'automatisation déclare ses gestes —, mais elle porte
// ce que chaque projet a coché, et le perdre ferait repartir des vérifications
// que quelqu'un avait éteintes.
type ProjectPipelines interface {
	PipelineEnabled(ctx context.Context, projectID int64, kind string) (bool, error)
}

// AutomationStore est ce que la reprise attend de la base.
type AutomationStore interface {
	ProjectPipelines
	ProjectsWithoutAutomation(ctx context.Context) ([]store.Project, error)
	SaveReleaseAutomation(ctx context.Context, entry store.ReleaseAutomation, actor string) error
}

// repriseActor nomme l'auteur d'une automatisation reprise.
//
// Distinct d'un identifiant humain, et lisible à l'écran : quelqu'un qui ouvre
// l'onglet doit pouvoir dire « ceci n'a pas été saisi, ça vient de mes cibles ».
const repriseActor = "system:reprise"

// AutomationReprise dote d'une automatisation les projets qui n'en ont pas.
type AutomationReprise struct {
	store   AutomationStore
	catalog *catalog.Catalog
	logger  *slog.Logger
}

func NewAutomationReprise(
	db AutomationStore, registry *catalog.Catalog, logger *slog.Logger,
) *AutomationReprise {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutomationReprise{store: db, catalog: registry, logger: logger}
}

// Run reprend les projets sans automatisation.
//
// Un projet dont la reprise ne compose **rien** — aucune cible constructible,
// aucune famille activée — reçoit tout de même sa ligne, vide. C'est ce qui
// empêche la reprise de le réexaminer à chaque démarrage, et c'est vrai : il ne
// construisait rien avant non plus.
func (r *AutomationReprise) Run(ctx context.Context) error {
	if r == nil || r.store == nil || r.catalog == nil {
		return nil
	}

	projects, err := r.store.ProjectsWithoutAutomation(ctx)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return nil
	}

	repris := 0
	for _, project := range projects {
		sujet := DefaultAutomation(r.catalog, project, r.enabled(ctx, project.ID))

		if err := r.store.SaveReleaseAutomation(ctx, store.ReleaseAutomation{
			ProjectID: project.ID, Plan: sujet,
		}, repriseActor); err != nil {
			// Un projet qu'on n'a pas su reprendre n'arrête pas les autres : il
			// sera revu au démarrage suivant, puisqu'il n'a toujours pas de ligne.
			r.logger.Error("reprise de l'automatisation",
				"projet", project.Slug, "error", err)
			continue
		}
		repris++
		r.logger.Info("automatisation reprise depuis les cibles du projet",
			"projet", project.Slug, "etapes", len(sujet.Stages),
			"gestes", gestesDe(sujet))
	}

	r.logger.Info("reprise des automatisations",
		"examines", len(projects), "repris", repris)
	return nil
}

// enabled rend le filtre des familles activées d'un projet.
func (r *AutomationReprise) enabled(ctx context.Context, projectID int64) func(string) bool {
	return func(kind string) bool {
		enabled, err := r.store.PipelineEnabled(ctx, projectID, kind)
		if err != nil {
			// Une lecture impossible ne doit pas retirer une vérification : mieux
			// vaut la déclarer et la voir échouer que la perdre en silence — une
			// reprise ne repasse pas.
			r.logger.Error("familles déclarées du projet",
				"projet", projectID, "kind", kind, "error", err)
			return true
		}
		return enabled
	}
}

func gestesDe(p plan.Plan) int {
	total := 0
	for _, stage := range p.Stages {
		total += len(stage.Gestes)
	}
	return total
}

/*
 * La composition, telle qu'elle était — et telle qu'elle reste, pour une fois.
 */

// DefaultAutomation compose l'automatisation d'un projet depuis ses cibles.
//
// Exportée parce qu'elle est le **contrat de la reprise** : ce qu'elle rend doit
// être ce qui tournait avant elle, et un test le tient. Rend un plan vide quand
// rien ne s'applique.
func DefaultAutomation(
	registry *catalog.Catalog, project store.Project, enabled func(kind string) bool,
) plan.Plan {
	if registry == nil {
		return plan.Plan{}
	}

	var stages []plan.Stage
	if build := buildStage(registry, project); len(build.Gestes) > 0 {
		stages = append(stages, build)
	}
	if test := testStage(registry, project, enabled); len(test.Gestes) > 0 {
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
// désormais déclarés.
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
			// la carte des artefacts — `{artifacts.portal.image}` désigne ce
			// geste-ci.
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
func testStage(
	registry *catalog.Catalog, project store.Project, enabled func(kind string) bool,
) plan.Stage {
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
			// Les images que ce pipeline analyse, visées comme **artefacts** de
			// l'étape de construction plutôt que recomposées depuis le registre.
			//
			// `when` reste au défaut — succès de l'étape précédente.
			Params: artefactsVises(pipeline, project),
		})
	}

	return plan.Stage{Role: plan.RoleTest, Label: "Vérifier", Gestes: gestes}
}

// artefactsVises rend les paramètres d'image que la construction peut fournir.
//
// # Pourquoi seulement celles que le projet construit
//
// Un pipeline qui analyse `portal` déclare `suggest: image, image: portal`. Sans
// déclaration, l'exécution recompose `registre/portal:tag` — ce qui vaut même
// quand personne n'a publié cette image, et l'analyse échoue alors sur un
// `docker pull` dont le motif ne dit pas la cause.
//
// Viser l'artefact est plus vrai : il échoue en nommant ce qui manque (doc 15
// §15.4). Mais seulement là où le projet construit effectivement cette image —
// ailleurs, la reprise laisserait un plan qui refuse de partir, alors qu'il
// partait la veille. Une reprise ne doit rien casser en passant.
func artefactsVises(pipeline catalog.Pipeline, project store.Project) map[string]string {
	construites := map[string]bool{}
	for _, target := range project.Buildable() {
		construites[target.Image] = true
	}

	var params map[string]string
	for _, input := range pipeline.Inputs() {
		if input.Origin() != catalog.OriginFallback || !construites[input.Image] {
			continue
		}
		if params == nil {
			params = map[string]string{}
		}
		params[input.Name] = "{artifacts." + input.Image + ".image}"
	}
	return params
}
