package worker

import (
	"context"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Catching up the projects that never declared their automation.
 *
 * A project with no automation receives a default one: an image named after it when its Docker
 * settings name a Dockerfile, then the verifications it ticked. Written once, then the project's
 * to change in the automation editor.
 *
 * # It runs once per project, and does not come round again
 *
 * The criterion is the **absence of a row**, never an empty plan. A project whose automation
 * someone emptied has a row: it no longer builds, and that is a decision. A catch-up that
 * picked it up would make what had just been erased reappear at the next start-up.
 */

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
// able to say "this was not typed, it was composed for me".
const catchUpActor = "system:reprise" // the value is stored, and stays as written

// AutomationCatchUp gives an automation to the projects that have none.
type AutomationCatchUp struct {
	store   AutomationStore
	catalog *catalog.Catalog
	stacks  map[string]bool
	// defaultBuild gives the build steps the plugins propose for a project. Nil: none.
	defaultBuild func(ctx context.Context, project store.Project) []plan.Geste
	logger       *slog.Logger
}

func NewAutomationCatchUp(
	db AutomationStore, registry *catalog.Catalog, logger *slog.Logger,
) *AutomationCatchUp {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutomationCatchUp{store: db, catalog: registry, logger: logger}
}

// WithDefaultBuild gives the build steps the plugins propose for a project: an image named after
// it when its Docker settings name a Dockerfile, for instance.
func (r *AutomationCatchUp) WithDefaultBuild(build func(ctx context.Context, project store.Project) []plan.Geste) *AutomationCatchUp {
	r.defaultBuild = build
	return r
}

// WithStacks names the language plugins, whose kinds a catch-up chains only for the legacy one.
func (r *AutomationCatchUp) WithStacks(names ...string) *AutomationCatchUp {
	r.stacks = map[string]bool{}
	for _, name := range names {
		r.stacks[name] = true
	}
	return r
}

// legacyStack is the language whose kinds were the only ones a build chained before a second one
// existed, and a catch-up composes what ran before.
const legacyStack = "php"

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
		plan := DefaultAutomation(r.catalog, r.builds(ctx, project), r.enabled(ctx, project))

		if err := r.store.SaveReleaseAutomation(ctx, store.ReleaseAutomation{
			ProjectID: project.ID, Plan: plan, Enabled: true,
		}, catchUpActor); err != nil {
			// A project that could not be caught up does not stop the others: it will be seen again at
			// the next start-up, since it still has no row.
			r.logger.Error("catching up the automation",
				"project", project.Slug, "error", err)
			continue
		}
		caught++
		r.logger.Info("automation composed",
			"project", project.Slug, "stages", len(plan.Stages),
			"steps", stepsOf(plan))
	}

	r.logger.Info("catching up the automations",
		"examines", len(projects), "caught", caught)
	return nil
}

func (r *AutomationCatchUp) builds(ctx context.Context, project store.Project) []plan.Geste {
	if r.defaultBuild == nil {
		return nil
	}
	return r.defaultBuild(ctx, project)
}

// enabled returns the filter of a project's enabled kinds: those it ticked, less those of another
// language than the legacy one. A project that never declared its automation never ran Go's tests.
func (r *AutomationCatchUp) enabled(ctx context.Context, project store.Project) func(string) bool {
	return func(kind string) bool {
		if prefix, _, found := strings.Cut(kind, "/"); found && r.stacks[prefix] && prefix != legacyStack {
			return false
		}
		enabled, err := r.store.PipelineEnabled(ctx, project.ID, kind)
		if err != nil {
			// A failed read must not remove a verification: better to declare it and see it fail than to
			// lose it silently — a catch-up does not come round again.
			r.logger.Error("the project's declared kinds",
				"project", project.ID, "kind", kind, "error", err)
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

// DefaultAutomation composes the automation of a project that has none: the build steps its plugins
// propose, then the verifications it ticked.
//
// Exported because it is the **catch-up's contract**, which a test holds. Returns an empty plan
// when nothing applies.
func DefaultAutomation(
	registry *catalog.Catalog, builds []plan.Geste, enabled func(kind string) bool,
) plan.Plan {
	if registry == nil {
		return plan.Plan{}
	}

	var stages []plan.Stage
	build := buildStage(registry, builds)
	if len(build.Gestes) > 0 {
		stages = append(stages, build)
	}
	if test := testStage(registry, build, enabled); len(test.Gestes) > 0 {
		stages = append(stages, test)
	}
	if len(stages) == 0 {
		return plan.Plan{}
	}
	return plan.Plan{Stages: stages}
}

// buildStage keeps the proposed build steps the catalogue can run: a pipeline its operator did not
// bind would leave a plan that never starts.
func buildStage(registry *catalog.Catalog, builds []plan.Geste) plan.Stage {
	var gestes []plan.Geste
	for _, geste := range builds {
		if pipeline, found := registry.Find(geste.Ref); found && pipeline.Builds() {
			gestes = append(gestes, geste)
		}
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
	registry *catalog.Catalog, build plan.Stage, enabled func(kind string) bool,
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
			Params: targetedArtifacts(pipeline, build),
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
func targetedArtifacts(pipeline catalog.Pipeline, build plan.Stage) map[string]string {
	built := map[string]bool{}
	for _, geste := range build.Gestes {
		built[geste.Params["image-name"]] = true
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
