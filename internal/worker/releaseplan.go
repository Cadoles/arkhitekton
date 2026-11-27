package worker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"log/slog"

	"github.com/Cadoles/arkhitekton/internal/action"
	"github.com/Cadoles/arkhitekton/internal/catalog"
	"github.com/Cadoles/arkhitekton/internal/event"
	"github.com/Cadoles/arkhitekton/internal/images"
	"github.com/Cadoles/arkhitekton/internal/plan"
	"github.com/Cadoles/arkhitekton/internal/release"
	"github.com/Cadoles/arkhitekton/internal/store"
)

/*
 * A release's plan.
 *
 * # The door of the work
 *
 * It is a door, and the question it asks is simple: does the sequence **build then verify** fit
 * into a single plan? If it does not, the model is too poor, and one learns it before having
 * written an editor.
 *
 * It does, and its shape says why: two stages, the second conditioned on the first succeeding.
 * That is exactly what `enqueueBuild` — a loop over the targets, in the API — and
 * `launchVerification` — a loop over the kinds, in the observer — did. Two loops in two files,
 * linked by an intermediate fact, where there was an undeclared plan.
 *
 * # Declared, no longer composed
 *
 * The plan was composed here, in Go, from the `targets` column and the catalogue. It is now
 * **read**: the project declares it from the automation tab, and this file knows neither target
 * nor particular kind any more.
 *
 * What that removes is visible to the eye: the `build` kind appears nowhere any more, and
 * neither does the `if geste.Ref == "build"` that routed two ways of filling in the parameters.
 * The three parameters it set literally — `revision`, `version`, `tag-name` — were a copy of
 * what the catalogue already declared through its `suggest`.
 *
 * The composition survives in a single place, `automationcatchup.go`, where it gives an
 * automation to the projects that never declared one.
 *
 * # What the step carries, and what the context carries
 *
 * The step carries what the **declaration** fixes: the image to produce, the Dockerfile stage,
 * its build context. The run's context carries what depends on the **triggering fact**: the
 * revision, the tag.
 *
 * That separation is no convenience. It is what makes a plan reusable from one release to the
 * next — a plan that froze the revision would serve only once — and it is the same rule as for
 * the declared reactions.
 */

// Keys of the run context of a release's plan.
//
// Named rather than written literally at both ends: the plan sets them, the runner reads them,
// and two spellings would produce a step with no parameters with nothing saying why.
const (
	contextRevision  = "revision"
	contextTag       = "tag"
	contextReleaseID = "release_id"
	// contextSourceTag is the tag the release's artifacts were produced under: its candidate's for a
	// final release, its own otherwise.
	contextSourceTag = "source_tag"
	// contextImages prefixes the images the run's builds published, by short name:
	// `images.portal`.
	contextImages = "images."
	// The two keys of a pull request's fact that name it: its row, and its number on the forge.
	contextPullRequest = "pull_request_id"
	contextNumber      = "number"
)

// releaseTrigger names the trigger of a release's plan.
//
// Distinct from `builtin:verification`, which it replaces: that one covered the downstream
// half of the chain, this one the whole door.
//
// Defined in `plan` since the API must **find** this run again in order to read a score's
// frozen weights: two spellings would name two runs, and the score would be computed on a plan
// that did not run.
const releaseTrigger = plan.ReleaseTrigger

// verificationRequester names the requester of the jobs a plan queues.
//
// Distinct from a human identifier: these jobs are requested by nobody, and displaying the
// name of whoever created the release would suggest a manual launch.
const verificationRequester = "system:plan"

// releaseContext composes the run context of a release's plan.
//
// The revision is the **commit**, never the branch it comes from: rebuilding the same release
// six months later must produce the same image.
func releaseContext(release store.Release, revision string) map[string]string {
	source := release.ImageVersion
	if source == "" {
		source = release.Tag
	}
	return map[string]string{
		contextRevision:  revision,
		contextTag:       release.Tag,
		contextSourceTag: source,
		contextReleaseID: strconv.FormatInt(release.ID, 10),
	}
}

// runValues reads from a run's context what it gives the parameters it imposes or falls back on.
func runValues(context map[string]string) catalog.RunValues {
	values := catalog.RunValues{
		Revision: context[contextRevision], Tag: context[contextTag], SourceTag: context[contextSourceTag],
		Images: map[string]string{},
	}
	// The pull request a run bears on, when it does: a release's fact carries no number.
	if context[contextPullRequest] != "" {
		values.Pull = context[contextNumber]
	}
	for key, reference := range context {
		if name, found := strings.CutPrefix(key, contextImages); found {
			values.Images[name] = reference
		}
	}
	return values
}

// stepParams composes the parameters of a step of a release's plan.
//
// # Three origins, and the catalogue states them already
//
// What a step **declares** first, templates bound to the context. Then, for each input the
// pipeline asks for, one of the three rules [catalog.Input.Origin] carries:
//
//   - the context **imposes** the revision and the tag. A declaration cannot change them: two
//     steps of one release targeting two commits would publish two images under the same tag;
//   - an image reference is a **fallback** — the declaration wins when it targets the artifact
//     a build actually produced;
//   - the rest is filled in by the declaration only.
//
// # What this replaces
//
// Two paths lived here, routed by `geste.Ref == "build"`: one set `revision`, `version` and
// `tag-name` literally, the other called `VerificationInputs`. The three literal names were a
// copy of what the catalogue already declared through its `suggest` — the `build` kind
// therefore stops being a special case of this file.
func stepParams(
	pipeline catalog.Pipeline, geste plan.Geste, context map[string]string,
) (map[string]string, error) {
	params := make(map[string]string, len(geste.Params)+4)
	for name, value := range geste.Params {
		// The templates are bound to the context, which carries the fact's payload **and** what the
		// previous stages produced: `{artifacts.portal.image}`.
		params[name] = action.Expand(value, context)
	}

	if missing := unresolvedArtifact(params); missing != "" {
		// Refused rather than started with a literal template. A pipeline receiving
		// `{artifacts.portal.image}` as an image would clone, fail at the `docker pull`, and the reason
		// would speak of a repository that cannot be found — a long way from the cause.
		//
		// A test that silently does not run reads as a test that passed.
		return nil, fmt.Errorf("the artifact %s was not produced by an earlier "+
			"stage", missing)
	}

	values := runValues(context)
	for _, input := range pipeline.Inputs() {
		provided := input.FromContext(values)

		switch input.Origin() {
		case catalog.OriginContext:
			params[input.Name] = provided
		case catalog.OriginFallback:
			if strings.TrimSpace(params[input.Name]) == "" {
				params[input.Name] = provided
			}
		case catalog.OriginDeclared:
			// Nothing to supply: only the declaration fills it in.
		}

		if input.Required && strings.TrimSpace(params[input.Name]) == "" {
			// A required parameter that cannot be filled in would fail the run at scheduling, with a
			// message that does not name the cause. Not queueing it at all is more honest.
			return nil, fmt.Errorf("parameter %q filled in neither by the declaration "+
				"nor by the run", input.Name)
		}
	}
	return params, nil
}

// unresolvedArtifact returns the first artifact reference left literal.
//
// Only `{artifacts.…}`: the other variables stay literal when they are not in the payload, and
// that is deliberate — a `{tag}` visible in a message is noticed and fixed. An artifact, on
// the other hand, names something that was supposed to exist: its absence is an error in the
// plan, not in the template.
func unresolvedArtifact(params map[string]string) string {
	for _, value := range params {
		remaining := value
		for {
			start := strings.Index(remaining, "{artifacts.")
			if start < 0 {
				break
			}
			fin := strings.IndexByte(remaining[start:], '}')
			if fin < 0 {
				break
			}
			return remaining[start+1 : start+fin]
		}
	}
	return ""
}

// ReleaseRunner carries out the steps of a release's plan.
//
// Both kinds of pipeline go through it: building and verifying differ only in how their
// parameters are composed, and splitting them into two runners would have required the engine
// to know which one it holds.
// It takes an [Enqueuer] rather than a `runSink`, and that is what lets the API and the worker
// build the same runner: opening a release's plan is a write to the database, not scheduling.
// The scheduler stays single, in the worker.
type ReleaseRunner struct {
	catalog catalog.Source
	queue   Enqueuer
	logger  *slog.Logger
}

func NewReleaseRunner(
	registry catalog.Source, queue Enqueuer, logger *slog.Logger,
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
		// A release plan's actions will go through the usual router. Here only the pipelines: refusing
		// by name is better than reporting "sent".
		return nil, fmt.Errorf("step %q: this runner carries out pipelines only",
			geste.Ref)
	}

	pipeline, found := catalog.Of(r.catalog).Find(geste.Ref)
	if !found {
		return nil, fmt.Errorf("kind %q missing from the catalogue", geste.Ref)
	}

	params, err := stepParams(pipeline, geste, run.Context)
	if err != nil {
		return nil, err
	}

	id, err := r.queue.Enqueue(ctx, store.JobRequest{
		Kind:          pipeline.Kind,
		Params:        params,
		RequestedBy:   verificationRequester,
		ReleaseID:     run.ReleaseID,
		ProjectID:     run.ProjectID,
		PullRequestID: run.PullRequestID,
	})
	if err != nil {
		return nil, err
	}

	r.logger.Info("release step launched",
		"release", run.Context[contextTag], "kind", geste.Ref,
		"step", strings.TrimSpace(geste.Label), "job", id)
	return &id, nil
}

// ReleaseStates is what the life cycle needs from the database.
type ReleaseStates interface {
	AdvanceReleaseState(ctx context.Context, releaseID int64, from, to string) (bool, error)
}

// ReleaseLifecycle advances a release at the pace of its plan's stages.
//
// # What it replaces
//
// The build count. The observer counted a release's builds — `CountBuilds` — to know whether
// the last one had just concluded, because it was called once per image knowing nothing of the
// whole.
//
// The stage **is** that whole. Its settling says exactly what the count sought to establish,
// and says it with no query: the engine already holds the state of each step.
//
// # Idempotent by construction
//
// `StageSettled` is called at every advance, on every stage already settled — the engine keeps
// no "already announced" mark. The repetition has no effect because the transition is
// **conditioned on the starting state**: a single delivery wins it, the following ones write
// nothing.
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

	// A stage skipped entirely attempted nothing, and therefore says nothing about the release. It
	// happens when the build failed: the tests are skipped, and the release is already `failed`
	// from the stage before.
	//
	// A plan with no verification is ready once built, a release produced from its source for
	// instance; a plan with no build verifies from the start.
	verifies := run.Plan.LastWithRole(plan.RoleTest) >= 0
	builds := run.Plan.LastWithRole(plan.RoleBuild) >= 0
	testedFrom := string(release.StateVerifying)
	if !builds {
		testedFrom = string(release.StateBuilding)
	}
	var from, to string
	switch {
	case role == plan.RoleBuild && outcome == plan.Succeeded && !verifies:
		from, to = string(release.StateBuilding), string(release.StateReady)
	case role == plan.RoleBuild && outcome == plan.Succeeded:
		from, to = string(release.StateBuilding), string(release.StateVerifying)
	case role == plan.RoleBuild && outcome == plan.Failed:
		from, to = string(release.StateBuilding), string(release.StateFailed)
	case role == plan.RoleTest && outcome == plan.Succeeded:
		from, to = testedFrom, string(release.StateReady)
	case role == plan.RoleTest && outcome == plan.Failed:
		from, to = testedFrom, string(release.StateFailed)
	default:
		return
	}

	advanced, err := l.releases.AdvanceReleaseState(ctx, *run.ReleaseID, from, to)
	if err != nil {
		l.logger.Error("advancing a release through its plan",
			"run", run.ID, "release", *run.ReleaseID, "to", to, "error", err)
		return
	}
	if !advanced {
		// The common case of a repetition, and not an anomaly: the stage is announced at every advance
		// of the plan.
		l.logger.Debug("release already advanced", "run", run.ID,
			"release", *run.ReleaseID, "from", from, "to", to)
		return
	}
	l.logger.Info("release advanced through its plan", "run", run.ID,
		"release", *run.ReleaseID, "stage", role, "from", from, "to", to)
}

// ReleasePlanner opens a release's plan.
//
// # Why on the API side
//
// Creating a release is a human decision, served by the `api`. Opening its plan **there** keeps
// what `enqueueBuild` did: whoever creates the release knows straight away whether the build
// started, rather than learning it at a worker's next loop.
//
// And it duplicates no scheduler: opening a plan writes a run and queues jobs. That is what the
// API already did. The worker keeps exclusive charge of what **leaves** — creating the
// PipelineRuns — and of the advance.
type ReleasePlanner struct {
	engine      *Engine
	catalog     catalog.Source
	automations Automations
	sources     SourcePlans
	logger      *slog.Logger
}

// Automations returns the plan a project declared.
//
// A narrow interface rather than the whole store: opening a release's plan needs only that
// answer.
type Automations interface {
	ReleaseAutomation(ctx context.Context, projectID int64) (store.ReleaseAutomation, error)
}

func NewReleasePlanner(
	engine *Engine, pipelines catalog.Source, automations Automations, logger *slog.Logger,
) *ReleasePlanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReleasePlanner{engine: engine, catalog: pipelines, automations: automations, logger: logger}
}

// OpenReleasePlan composes a release's plan and starts it.
//
// Returns the number of steps actually started. The caller depends on it: a release from which
// nothing started must not be announced as "building".
func (p *ReleasePlanner) OpenReleasePlan(
	ctx context.Context, project store.Project, record store.Release, revision string,
) int {
	started, _, err := p.open(ctx, project, record, revision)
	if err != nil {
		p.logger.Warn("no plan for this release", "release", record.Tag, "project", project.Slug, "reason", err)
	}
	return started
}

// errNothingDeclared says the project declares no automation: its releases are tags, with nothing
// to build or verify.
var errNothingDeclared = errors.New("the project declares no automation")

// open composes a release's plan and starts it. Returns how many steps started, whether the project
// declares nothing to run, and otherwise why nothing started.
func (p *ReleasePlanner) open(
	ctx context.Context, project store.Project, record store.Release, revision string,
) (int, bool, error) {
	if p == nil || p.engine == nil {
		return 0, false, errors.New("no plan engine")
	}

	declared, enabled, err := p.declaredPlan(ctx, project)
	if err != nil {
		// A failed read opens **nothing**. Composing a fallback would build something other than what
		// the project declares, under a release's tag — and nobody would know it is not the right one.
		return 0, false, fmt.Errorf("reading the automation: %w", err)
	}
	if !enabled {
		return 0, false, errors.New("the project's automation is switched off")
	}

	// A release produced from another runs what its source built, re-tagged or built again: not the
	// declared plan, which would build anew and verify what was already verified.
	kinds := project.ReleaseKinds
	if len(kinds) == 0 {
		kinds = release.DefaultCycle()
	}
	if spec, err := kinds.Find(record.Kind); err == nil &&
		spec.Mode == release.ModeFromSource && record.SourceReleaseID != nil {
		source, derived, err := p.derivedPlan(ctx, *record.SourceReleaseID)
		if err != nil {
			return 0, false, err
		}
		declared, revision = derived, source.GitCommit
		if revision == "" {
			revision = source.GitRef
		}
	} else if declared.Empty() {
		return 0, true, errNothingDeclared
	}

	if strings.TrimSpace(revision) == "" {
		// Nothing starts without a reference. Building "what the branch names at this instant" is
		// exactly what the version invariant forbids, and an empty revision would produce a clone on
		// the pipeline's default: hence an image nobody could say where it came from.
		return 0, false, errors.New("no Git reference to build")
	}

	projectID := project.ID
	releaseID := record.ID
	started := p.engine.Trigger(ctx, store.PlanRun{
		Trigger:   releaseTrigger,
		EventKey:  event.ReleaseCreatedKey(record.ID),
		ProjectID: &projectID,
		ReleaseID: &releaseID,
		Plan:      declared,
		Context:   releaseContext(record, revision),
	})
	if started == 0 {
		return 0, false, errors.New("no step could start")
	}
	return started, false, nil
}

// SourcePlans reads what a release's source ran and produced.
type SourcePlans interface {
	GetReleaseByID(ctx context.Context, id int64) (store.Release, error)
	PlanRunByEvent(ctx context.Context, trigger, eventKey string) (store.PlanRun, error)
	PlanArtifacts(ctx context.Context, runID int64) ([]store.PlanArtifact, error)
}

// WithSources has a release produced from another run what its source built.
func (p *ReleasePlanner) WithSources(sources SourcePlans) *ReleasePlanner {
	p.sources = sources
	return p
}

// derivedPlan composes the plan of a release produced from its source: one stage, each build step
// of the source's plan carried over.
//
// A step whose pipeline declares how it promotes, a Docker build, becomes one promotion per image it
// published, the development image excepted: re-tagged bit for bit, never rebuilt. Any other step,
// a Go build for instance, is built again from the source's commit, under the release's own tag.
func (p *ReleasePlanner) derivedPlan(ctx context.Context, sourceID int64) (store.Release, plan.Plan, error) {
	if p.sources == nil || p.catalog == nil || p.catalog.Current() == nil {
		return store.Release{}, plan.Plan{}, errors.New("nothing reads a source's plan")
	}
	source, err := p.sources.GetReleaseByID(ctx, sourceID)
	if err != nil {
		return store.Release{}, plan.Plan{}, fmt.Errorf("reading the source: %w", err)
	}
	run, err := p.sources.PlanRunByEvent(ctx, releaseTrigger, event.ReleaseCreatedKey(source.ID))
	if err != nil {
		return source, plan.Plan{}, fmt.Errorf("the plan release %s ran: %w", source.Tag, err)
	}
	artifacts, err := p.sources.PlanArtifacts(ctx, run.ID)
	if err != nil {
		return source, plan.Plan{}, fmt.Errorf("what release %s produced: %w", source.Tag, err)
	}
	published := map[store.GesteRef][]store.PlanArtifact{}
	for _, artifact := range artifacts {
		if artifact.Name == "image" && artifact.Kind == string(catalog.ProductImage) {
			ref := store.GesteRef{Stage: artifact.Stage, Position: artifact.Position}
			published[ref] = append(published[ref], artifact)
		}
	}

	pipelines := p.catalog.Current()
	var gestes []plan.Geste
	for stageIndex, stage := range run.Plan.Stages {
		if stage.Role != plan.RoleBuild {
			continue
		}
		for position, geste := range stage.Gestes {
			if geste.Kind != plan.GestePipeline || !geste.Promoted() {
				continue
			}
			pipeline, found := pipelines.Find(geste.Ref)
			if !found {
				return source, plan.Plan{}, fmt.Errorf("%s, which built release %s, is no longer in the catalogue",
					geste.Ref, source.Tag)
			}
			if pipeline.Promote == nil {
				gestes = append(gestes, geste)
				continue
			}
			for _, artifact := range published[store.GesteRef{Stage: stageIndex, Position: position}] {
				if !artifact.Promoted {
					continue
				}
				name := images.Parse(artifact.Reference).Name
				params := make(map[string]string, len(pipeline.Promote.Params))
				for key, value := range pipeline.Promote.Params {
					params[key] = action.Expand(value, map[string]string{"artifact.name": name})
				}
				gestes = append(gestes, plan.Geste{
					Kind: plan.GestePipeline, Ref: pipeline.Promote.Kind, Label: name, Params: params,
				})
			}
		}
	}
	if len(gestes) == 0 {
		return source, plan.Plan{}, fmt.Errorf("release %s built nothing a release carries", source.Tag)
	}
	return source, plan.Plan{Stages: []plan.Stage{{
		Role: plan.RoleBuild, Label: "Produire depuis " + source.Tag, Gestes: gestes,
	}}}, nil
}

// declaredPlan returns the plan the project declared, and whether its automation is switched on.
//
// A missing automation returns an empty plan and **no error**: that is the state of a fresh
// project, which the worker's catch-up will fill at its next start-up. Treating it as an
// outage would send someone hunting for a defect where there is only a declaration to write.
func (p *ReleasePlanner) declaredPlan(
	ctx context.Context, project store.Project,
) (plan.Plan, bool, error) {
	if p.automations == nil {
		return plan.Plan{}, true, nil
	}

	entry, err := p.automations.ReleaseAutomation(ctx, project.ID)
	if errors.Is(err, store.ErrNoAutomation) {
		return plan.Plan{}, true, nil
	}
	if err != nil {
		return plan.Plan{}, false, err
	}
	return entry.Plan, entry.Enabled, nil
}

// ProductOf returns the kind of what a catalogue kind leaves behind it.
//
// The **first** product declared, and empty when the kind declares none or declares `none`. A
// pipeline that builds then tests returns two; keeping the first is enough to name the kind of
// its artifacts, and choosing per artifact would require of the catalogue a declaration per
// result that it does not carry.
func ProductOf(registry catalog.Source) func(ref string) string {
	return func(ref string) string {
		if registry == nil || registry.Current() == nil {
			return ""
		}
		pipeline, found := registry.Current().Find(ref)
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
