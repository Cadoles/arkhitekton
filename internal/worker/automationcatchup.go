package worker

import (
	"context"
	"log/slog"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Catching up the projects that never declared their automation.
 *
 * # Why a catch-up, and not an SQL migration
 *
 * The change must be silent: without it, every project would stop building on deployment.
 *
 * Composing this plan in SQL would mean copying the resolved Dockerfile's rule, the catalogue's
 * verification kinds and the per-project filter — three things that live in the binary, one of
 * them in a ConfigMap. The catch-up therefore calls the **same code** that composed the plan
 * until now: the plan caught up is identical to the one that ran, by construction and not by
 * re-reading.
 *
 * # It runs once per project, and does not come round again
 *
 * The criterion is the **absence of a row**, never an empty plan. A project whose automation
 * someone emptied has a row: it no longer builds, and that is a decision. A catch-up that
 * picked it up would make what had just been erased reappear at the next start-up.
 */

// buildKind is the catalogue kind that builds.
//
// Used by the catch-up alone: a release's plan no longer knows any particular kind now that
// it is declared. It is what remains of the composition in Go, and nothing else remains.
const buildKind = catalog.KindBuild

// ProjectPipelines says what a project declares it verifies.
//
// What the catch-up picks up. The `project_pipelines` table is no longer read when a plan
// opens — the automation declares its steps — but it carries what each project ticked, and
// losing it would restart verifications someone had switched off.
type ProjectPipelines interface {
	PipelineEnabled(ctx context.Context, projectID int64, kind string) (bool, error)
}

// AutomationStore is what the catch-up needs from the database.
type AutomationStore interface {
	ProjectPipelines
	ProjectsWithoutAutomation(ctx context.Context) ([]store.Project, error)
	SaveReleaseAutomation(ctx context.Context, entry store.ReleaseAutomation, actor string) error
}

// catchUpActor names the author of a caught-up automation.
//
// Distinct from a human identifier, and readable on screen: someone opening the tab must be
// able to say "this was not typed, it comes from my targets".
const catchUpActor = "system:reprise" // the value is stored, and stays as written

// AutomationCatchUp gives an automation to the projects that have none.
type AutomationCatchUp struct {
	store   AutomationStore
	catalog *catalog.Catalog
	logger  *slog.Logger
}

func NewAutomationCatchUp(
	db AutomationStore, registry *catalog.Catalog, logger *slog.Logger,
) *AutomationCatchUp {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutomationCatchUp{store: db, catalog: registry, logger: logger}
}

// Run catches up the projects with no automation.
//
// A project for which the catch-up composes **nothing** — no buildable target, no kind
// enabled — still gets its row, empty. That is what stops the catch-up re-examining it at
// every start-up, and it is true: it built nothing before either.
func (r *AutomationCatchUp) Run(ctx context.Context) error {
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

	caught := 0
	for _, project := range projects {
		plan := DefaultAutomation(r.catalog, project, r.enabled(ctx, project.ID))

		if err := r.store.SaveReleaseAutomation(ctx, store.ReleaseAutomation{
			ProjectID: project.ID, Plan: plan,
		}, catchUpActor); err != nil {
			// A project that could not be caught up does not stop the others: it will be seen again at
			// the next start-up, since it still has no row.
			r.logger.Error("catching up the automation",
				"project", project.Slug, "error", err)
			continue
		}
		caught++
		r.logger.Info("automation caught up from the project's targets",
			"project", project.Slug, "stages", len(plan.Stages),
			"steps", stepsOf(plan))
	}

	r.logger.Info("catching up the automations",
		"examines", len(projects), "caught", caught)
	return nil
}

// enabled returns the filter of a project's enabled kinds.
func (r *AutomationCatchUp) enabled(ctx context.Context, projectID int64) func(string) bool {
	return func(kind string) bool {
		enabled, err := r.store.PipelineEnabled(ctx, projectID, kind)
		if err != nil {
			// A failed read must not remove a verification: better to declare it and see it fail than to
			// lose it silently — a catch-up does not come round again.
			r.logger.Error("the project's declared kinds",
				"project", projectID, "kind", kind, "error", err)
			return true
		}
		return enabled
	}
}

func stepsOf(p plan.Plan) int {
	total := 0
	for _, stage := range p.Stages {
		total += len(stage.Gestes)
	}
	return total
}

/*
 * The composition, as it was — and as it stays, for once.
 */

// DefaultAutomation composes a project's automation from its targets.
//
// Exported because it is the **catch-up's contract**: what it returns must be what ran before
// it, and a test holds that. Returns an empty plan when nothing applies.
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

// buildStage composes the build stage from the project's targets.
//
// One step per target, and that is the point: MSE's three images stop being a loop in the API
// and become three steps that are readable, re-runnable and now declared.
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
			// The label is the image's name: it is what tells apart three steps carrying the same
			// catalogue reference, on screen as in the artifact map — `{artifacts.portal.image}` names
			// this step.
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

// testStage composes the verification stage.
//
// `enabled` says what the **project** verifies. The catalogue says what the instance can run,
// which is not the same question: a project with no load campaign saw `load-tests` follow on,
// fail for want of a scenario, and leave its release waiting for a verdict that would never
// come.
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
			// The images this pipeline analyses, targeted as **artifacts** of the build stage rather than
			// recomposed from the registry.
			//
			// `when` stays at its default — the previous stage succeeded.
			Params: targetedArtifacts(pipeline, project),
		})
	}

	return plan.Stage{Role: plan.RoleTest, Label: "Verify", Gestes: gestes}
}

// targetedArtifacts returns the image parameters the build can supply.
//
// # Why only those the project builds
//
// A pipeline that analyses `portal` declares `suggest: image, image: portal`. With no
// declaration, the run recomposes `registry/portal:tag` — which holds even when nobody has
// published that image, and the analysis then fails on a `docker pull` whose reason does not
// say the cause.
//
// Targeting the artifact is truer: it fails naming what is missing (docs/05-pipeline-catalogue.md).
// But only where the project actually builds that image — elsewhere, the catch-up would leave
// a plan that refuses to start, although it started the day before. A catch-up must break
// nothing on its way.
func targetedArtifacts(pipeline catalog.Pipeline, project store.Project) map[string]string {
	built := map[string]bool{}
	for _, target := range project.Buildable() {
		built[target.Image] = true
	}

	var params map[string]string
	for _, input := range pipeline.Inputs() {
		if input.Origin() != catalog.OriginFallback || !built[input.Image] {
			continue
		}
		if params == nil {
			params = map[string]string{}
		}
		params[input.Name] = "{artifacts." + input.Image + ".image}"
	}
	return params
}
