// Package worker carries the background loops: Tekton observation, log collection, queue
// scheduler, external synchronisations. See docs/02-architecture.md.
//
// They have one thing in common that dictates the architecture: they must run in **a single
// copy**. A duplicated scheduler launches the same pipeline twice, two collectors write to the
// same MinIO object. Hence the leader election, and not a mere `replicas: 1` — during a
// rolling update two pods briefly coexist, and that is exactly the instant that would produce
// the duplicate.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/images"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/logs"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
)

// runSink adapts the PostgreSQL store to what the observer needs.
//
// The adapter exists so that `store` ignores Tekton and `tekton` ignores PostgreSQL. The
// translation is mechanical, and is paid for once here rather than as a cross-dependency
// between two packages that have no business knowing each other.
type runSink struct {
	db *store.DB
	// load ingests a finished run's load campaigns. Nil when storage is not available: the rest of
	// the observation continues without it.
	load *LoadIngester
	// tests ingests the test reports. Same rule.
	tests *TestIngester
	// security ingests the normalised audits. Same rule.
	security *SecurityIngester
	// markers reads the run descriptors left in the logs. Nil when the log reader is not wired:
	// the rest continues, and a crashed tool then becomes indistinguishable from a clean one
	// again.
	markers *MarkerIngester
	// registryProject prefixes the published images, to name them in the audit. Empty, the kinds
	// that require them are not launchable and the log says so — better than a run that would fail
	// on a truncated reference.
	registryProject string
	// catalog serves to chain the verifications of a successful build. Nil when it is not wired:
	// observation continues, with no chaining.
	catalog *catalog.Catalog
	// verification queues the chained tests. Extracted into an interface so that the chaining can
	// be exercised without a database: it is decision logic — what to launch, with which revision
	// — and it deserves testing on its own.
	verification Enqueuer
	// pipelines says whether a catalogue kind applies to a project. Nil: everything applies, which
	// is the behaviour from before the `project_pipelines` table.
	//
	// Extracted into an interface for the same reason as `verification`: deciding what to chain is
	// logic, and it is exercised without a database.
	pipelines ProjectPipelines
	// releases carries the state advance. Extracted like `verification`: it is the conditional
	// write that makes the chaining idempotent, and it deserves exercising without a database.
	releases ReleaseAdvancer
	// instance identifies the database. A run carrying another's is not attached to a job: job
	// identifiers start again at 1 on a fresh database, and a run left in the cluster would
	// conclude its namesake.
	instance string
	// events records the observed facts. Nil: observation continues and the feed stays empty —
	// nobody reacts to it yet.
	events EventRecorder
	// engine advances the plan runs. Nil: nothing is chained, and the log says so — a mute
	// chaining would be worse than a waiting release.
	engine *Engine
	// dispatcher triggers the declared reactions of a new fact. Nil: facts are observed and nobody
	// reacts to them.
	dispatcher *Dispatcher
	// promotions composes the fact of a completed promotion. Nil: no promotion is observed, for
	// want of a plugin that promotes.
	promotions PromotionFacts
	// now dates the facts. Replaceable so that emission can be exercised without a clock: a fact
	// carries the instant at which it happened, and that is a value a test must be able to fix.
	now    func() time.Time
	logger *slog.Logger
}

// WithInstance déclare l'identité de la base.
func (s *runSink) WithInstance(id string) *runSink {
	s.instance = id
	return s
}

// disown cuts the attachment of a run launched by another database.
//
// The `job-id` label alone is not enough to identify a job: the identifiers start again at 1
// on a fresh database, whereas PipelineRuns survive in the cluster. Observed —
// `mse-static-tests-7`, failed the day before under a previous database, concluded as failed
// job 7 of a database created since, which had not even been launched yet. The run stays
// projected, it is simply no longer held to be this job's run.
func (s *runSink) disown(run tekton.Run) tekton.Run {
	if run.JobID == 0 || run.Instance == s.instance {
		return run
	}

	s.logger.Debug("run of another database, not attached",
		"run", run.Name, "job", run.JobID,
		"instance_run", run.Instance, "instance", s.instance)
	run.JobID = 0
	run.Observed = true
	return run
}

// ReleaseAdvancer is what the conclusion needs from the database.
//
// `JobRelease` appears there because two paths depend on it — the state conclusion and the
// attachment of the ingested reports — and because exercising them without a database is what
// was missing: the reports were recorded orphaned from their release with no test seeing it.
type ReleaseAdvancer interface {
	AdvanceReleaseState(ctx context.Context, id int64, from, to string) (bool, error)
	JobRelease(ctx context.Context, jobID int64) (store.JobReleaseContext, error)
	// CountBuilds says where a release's builds stand. A release builds several — one per image —
	// and only advances when all have completed.
	CountBuilds(ctx context.Context, releaseID int64, buildKind string) (store.BuildTally, error)
}

// Enqueuer is what the chaining needs from the queue.
type Enqueuer interface {
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
}

// WithCatalog wires the chaining of the verifications.
func (s *runSink) WithCatalog(pipelines *catalog.Catalog, registryProject string) *runSink {
	s.catalog = pipelines
	s.registryProject = registryProject
	return s
}

// imageRef composes the reference of an image published by the build.
//
// The image's tag is the release's: that is what `build-image` receives as `version` and
// writes into `version.txt`.
func (s *runSink) imageRef(name, tag string) string {
	return images.Reference(s.registryProject, name, tag)
}

// buildJobKind is the kind of job that builds the images.
//
// Must match the `kind` of the catalogue entry: it is on that value that a run's conclusion
// decides to advance a release.
const buildJobKind = "build"

// NewRunSink returns the adapter the Tekton observer consumes.
func NewRunSink(db *store.DB) *runSink {
	return &runSink{
		db: db, verification: db, releases: db, pipelines: db,
		now: time.Now, logger: slog.Default(),
	}
}

// WithLoadIngester wires the ingestion of the load campaigns.
//
// Triggered by the end of the run and not by a notification from the runner: the runner lives
// outside the cluster, publishes to MinIO with a presigned URL and has no return path to the
// dashboard. The only reliable signal is the one the dashboard already has.
func (s *runSink) WithLoadIngester(ingester *LoadIngester) *runSink {
	s.load = ingester
	return s
}

// WithTestIngester wires the ingestion of the test reports.
//
// Triggered by the end of the run, like the load ones: the artifacts are published as the run
// proceeds but are complete only at the end, and reading them earlier would give partial
// reports nothing would tell apart from truncated ones.
func (s *runSink) WithTestIngester(ingester *TestIngester) *runSink {
	s.tests = ingester
	return s
}

// WithMarkerIngester wires the reading of the run descriptors.
//
// Optional like the other ingestions: without it, observation continues, but a tool that
// crashed becomes indistinguishable from a tool that found nothing.
func (s *runSink) WithMarkerIngester(ingester *MarkerIngester) *runSink {
	s.markers = ingester
	return s
}

// WithSecurityIngester wires the ingestion of the security audits.
func (s *runSink) WithSecurityIngester(ingester *SecurityIngester) *runSink {
	s.security = ingester
	return s
}

func (s *runSink) SaveRun(ctx context.Context, run tekton.Run) error {
	run = s.disown(run)

	id, err := s.db.UpsertRun(ctx, store.RunRecord{
		Name:        run.Name,
		Namespace:   run.Namespace,
		Pipeline:    run.Pipeline,
		Status:      string(run.Status),
		Reason:      run.Reason,
		StartedAt:   run.StartedAt,
		CompletedAt: run.CompletedAt,
		Results:     run.Results,
		JobID:       run.JobID,
		Observed:    run.Observed,
		// Translation rather than a direct hand-over: `store` ignores Tekton, and `tekton` ignores
		// PostgreSQL. That is what this adapter exists to hold.
		DeclaredTasks: declaredTasks(run.DeclaredTasks),
	})
	if err != nil {
		return err
	}

	// The release is resolved once, here, and not in each of the three ingestions: they all carry
	// it, and three reads for one run would say the same thing three times.
	release := s.releaseOf(ctx, run)

	if err := s.ingestLoad(ctx, id, run, release); err != nil {
		return err
	}
	// The markers **before** the artifacts, and the order carries a decision: both ingestions are
	// idempotent per tool, so the first wins. The descriptor must win — it is the only one that
	// knows the tool failed, where a partial artifact would read as a complete result.
	if err := s.ingestMarkers(ctx, id, run, release); err != nil {
		return err
	}
	if err := s.ingestTests(ctx, id, run, release); err != nil {
		return err
	}
	if err := s.ingestSecurity(ctx, id, run, release); err != nil {
		return err
	}

	// The contract check comes **after** the three ingestions: it observes their combined result,
	// and doing it before would amount to concluding silence on reports not yet read.
	s.checkResultsContract(ctx, id, run)

	// The job's conclusion comes **after** the run is recorded, and the order carries a decision.
	//
	// It preceded it, with no written reason. Since concluding a step collects the run's artifacts
	// — its Tekton results — that order made them impossible to find: the collection re-read a row
	// the recording had not written yet. Observed on the cluster, three successful builds and no
	// artifact kept.
	//
	// The general rule is that of the ingestions above: **the consequence follows the
	// observation**. Concluding first meant reacting to what was not yet written.
	return s.finishJob(ctx, run)
}

// checkResultsContract reports a pipeline that announces results and leaves none.
//
// It is the failure mode that could be named without being remedied: a pipeline that does not
// honour the contract produces an empty screen, and nothing says why. Now that the catalogue
// declares what each pipeline produces, the gap between the announcement and the observation
// can be named.
//
// **A report, not a failure.** The run ran and its status is true; it is the reporting back
// that is missing. Failing it would make the release unusable over a publication defect,
// whereas the right reaction is to fix the pipeline — or its declaration.
//
// **It lives in the log, and nowhere else.** It was to surface through `pipeline.finished`;
// that fact went away, and nothing replaced it. The gap therefore remains whole: a run's
// screen still does not say why it is empty, and the log is where one must go.
func (s *runSink) checkResultsContract(ctx context.Context, runID int64, run tekton.Run) {
	if s.catalog == nil || run.JobID == 0 || !run.Status.Terminal() {
		return
	}
	// A cancelled run promised nothing: it had no time to publish, and reporting it would send
	// someone hunting for a pipeline defect where someone simply interrupted.
	if run.Status != tekton.StatusSucceeded {
		return
	}

	job, err := s.db.GetJob(ctx, run.JobID)
	if err != nil {
		s.logger.Error("family of a finished run", "run", run.Name, "job", run.JobID, "error", err)
		return
	}
	if !s.catalog.Yields(job.Kind, catalog.ProductResults) {
		return
	}

	found, err := s.db.RunHasResults(ctx, runID)
	if err != nil {
		s.logger.Error("results of a finished run", "run", run.Name, "error", err)
		return
	}
	if found {
		return
	}

	s.logger.Warn("pipeline contract not honoured",
		"run", run.Name, "kind", job.Kind,
		"expected", "test results, announced by `produces: [results]`",
		"constate", "no result recorded for this run",
		"consequence", "this release's screen will stay empty on this family",
		"piste", "artifacts not published, paths outside the results contract, "+
			"or a `produces` declaration to fix")
}

// releaseOf returns the release this run verifies, if it verifies one.
//
// Without it, the reports were recorded **orphaned**: a release's screen looks for its results
// by `release_id`, and found none although the table carried nine. The score therefore stayed
// "not evaluated" on a release whose tests had run and passed.
//
// Nil is a normal case, not an anomaly: a run triggered by a webhook or by a CronJob verifies
// no release, and its reports stay consultable from the run itself.
func (s *runSink) releaseOf(ctx context.Context, run tekton.Run) *int64 {
	if run.JobID == 0 {
		return nil
	}

	linked, err := s.releases.JobRelease(ctx, run.JobID)
	if err != nil {
		if !errors.Is(err, store.ErrJobWithoutRelease) {
			s.logger.Error("release of a run", "run", run.Name, "job", run.JobID, "error", err)
		}
		return nil
	}
	return &linked.ReleaseID
}

// declaredTasks translates a pipeline's declaration into the store's model.
func declaredTasks(tasks []tekton.DeclaredTask) []store.DeclaredTask {
	if len(tasks) == 0 {
		return nil
	}
	translated := make([]store.DeclaredTask, 0, len(tasks))
	for _, task := range tasks {
		translated = append(translated, store.DeclaredTask{
			Name: task.Name, DisplayName: task.DisplayName, Finally: task.Finally,
		})
	}
	return translated
}

// ingestSecurity records a finished run's normalised audit.
//
// The failure is not returned to the observer, for the same reason as the other two
// ingestions: it would retry the projection of the whole run although the run is correctly
// recorded.
func (s *runSink) ingestSecurity(ctx context.Context, runID int64, run tekton.Run, release *int64) error {
	if s.security == nil || !run.Status.Terminal() {
		return nil
	}

	if err := s.security.Ingest(ctx, LoadRun{ID: runID, Name: run.Name, JobID: run.JobID, ReleaseID: release}); err != nil {
		s.security.logger.Error("ingesting the security audit", "run", run.Name, "error", err)
	}
	return nil
}

// ingestMarkers records what the descriptors say about the tools that did not conclude.
//
// The failure is not returned to the observer, as for the other ingestions: it would retry the
// projection of the whole run although the run is correctly recorded, and the next visit will
// replay what is missing.
//
// The family comes from the catalogue and not from the run: a tool that published nothing has
// no artifact to deduce it from, and that is precisely the case one is trying to make visible.
func (s *runSink) ingestMarkers(ctx context.Context, runID int64, run tekton.Run, release *int64) error {
	if s.markers == nil || !run.Status.Terminal() || run.JobID == 0 {
		return nil
	}

	job, err := s.db.GetJob(ctx, run.JobID)
	if err != nil || job.Kind == "" {
		return nil
	}

	tasks := make([]string, 0, len(run.DeclaredTasks))
	for _, task := range run.DeclaredTasks {
		tasks = append(tasks, task.Name)
	}

	marked, ok := markerRunFor(s.catalog, job.Kind, MarkerRun{
		ID: runID, Name: run.Name, Tasks: tasks, ReleaseID: release,
	})
	if !ok {
		return nil
	}

	if err := s.markers.Ingest(ctx, marked); err != nil {
		s.logger.Error("reading the markers", "run", run.Name, "error", err)
	}
	return nil
}

// ingestTests records a finished run's reports.
//
// The failure is not returned to the observer, for the same reason as for the load ones: it
// would retry the projection of the whole run although the run is correctly recorded. Since
// ingestion is idempotent, the next visit will replay what is missing.
func (s *runSink) ingestTests(ctx context.Context, runID int64, run tekton.Run, release *int64) error {
	if s.tests == nil || !run.Status.Terminal() {
		return nil
	}

	if err := s.tests.Ingest(ctx, LoadRun{ID: runID, Name: run.Name, JobID: run.JobID, ReleaseID: release}); err != nil {
		s.tests.logger.Error("ingesting the test reports", "run", run.Name, "error", err)
	}
	return nil
}

// ingestLoad records the load campaigns of a finished load run.
//
// An ingestion failure is not returned to the observer: it would retry the projection of the
// whole run, although the run is correctly recorded. An unreadable summary is a problem with
// the result, not with the observation, and the run's next visit will replay it anyway —
// ingestion is idempotent.
func (s *runSink) ingestLoad(ctx context.Context, runID int64, run tekton.Run, release *int64) error {
	if s.load == nil || !run.Status.Terminal() || !isLoadRun(run) {
		return nil
	}

	var duration int64
	if run.StartedAt != nil && run.CompletedAt != nil {
		duration = run.CompletedAt.Sub(*run.StartedAt).Milliseconds()
	}

	err := s.load.Ingest(ctx, LoadRun{
		ID: runID, Name: run.Name, JobID: run.JobID, DurationMS: duration,
		ReleaseID: release,
	})
	if err != nil {
		s.load.logger.Error("ingesting the load campaigns", "run", run.Name, "error", err)
	}
	return nil
}

// isLoadRun recognises a load run by its pipeline.
//
// By the pipeline's name and not by the job's: a run triggered outside the dashboard has no
// job, and its results are worth as much as the others.
func isLoadRun(run tekton.Run) bool {
	return strings.Contains(run.Pipeline, "load-test")
}

// finishJob concludes a finished run's job.
//
// The queue learns of a run's end only through the observer: Tekton tells nobody, and without
// that conclusion the exclusion class would stay busy indefinitely — the next job would never
// get its turn.
//
// The conclusion precedes the recording of the run: were it to fail afterwards, the next
// transaction would see a finished run and a job in progress, a state no loop catches up.
func (s *runSink) finishJob(ctx context.Context, run tekton.Run) error {
	if run.JobID == 0 || !run.Status.Terminal() {
		return nil
	}

	state := store.JobFailed
	switch run.Status {
	case tekton.StatusSucceeded:
		state = store.JobSucceeded
	case tekton.StatusCancelled:
		state = store.JobCancelled
	}

	if err := s.db.FinishJob(ctx, run.JobID, state, run.Reason); err != nil {
		return err
	}
	return s.advanceReleaseForJob(ctx, run.JobID, state)
}

// advanceRelease has a release follow the conclusion of its pipeline.
//
// Only the build moves the state: a successful promotion does not make a release "ready", it
// deploys it somewhere, and the tests that follow conclude nothing until someone has read
// them. Widening this rule would advance a release on the strength of a pipeline that says
// nothing about its quality.
//
// The starting state is re-read here rather than assumed: between the queueing and the end of
// the run, a manual resume or a cancellation may have moved the release, and writing
// unconditionally would erase that decision. The conditional write returns `false` in that
// case, which is logged and not treated as an error — a pipeline arriving after the fact does
// not have the last word.
func (s *runSink) advanceReleaseForJob(ctx context.Context, jobID int64, jobState string) error {
	linked, err := s.releases.JobRelease(ctx, jobID)
	if err != nil {
		if errors.Is(err, store.ErrJobWithoutRelease) {
			// Common case: a manual launch of tests from the queue names no release.
			return nil
		}
		return err
	}
	// The step this job was carrying out, if it came from a plan. Before the state conclusion: it
	// is the plan's advance that will start the next stage, and delaying it would make the chain
	// wait a turn.
	//
	// Without effect for most jobs, which come from no plan.
	if s.engine != nil {
		s.engine.Advance(ctx, jobID, planStateOf(jobState))
	}

	// Building and verifying no longer pass through here.
	//
	// Both have become **stages of the release's plan**, and it is the settling of the stage that
	// advances the state. What that removes is worth naming: the build is no longer counted job by
	// job — the stage *is* the count — and the verifications no longer conclude through a second
	// count of kinds, kept elsewhere and having to stay in agreement with the first.
	//
	// What remains is what no plan causes: a promotion. It does not make a release "ready" — it
	// deploys it somewhere, and the tests that follow conclude nothing until someone has read
	// them. It does however constitute a **fact**, and that is the whole difference between
	// advancing a state and observing that something happened.
	if linked.Kind != buildJobKind && !s.isVerificationKind(linked.Kind) {
		s.emitReleasePromoted(ctx, linked, jobState)
	}
	return nil
}

func (s *runSink) SaveTaskRun(ctx context.Context, taskRun tekton.TaskRunProjection) error {
	return s.db.UpsertTaskRun(ctx, store.TaskRunRecord{
		RunName:     taskRun.RunName,
		Name:        taskRun.Name,
		DisplayName: taskRun.DisplayName,
		Status:      string(taskRun.Status),
		StartedAt:   taskRun.StartedAt,
		CompletedAt: taskRun.CompletedAt,
		PodName:     taskRun.PodName,
	})
}

// UnfinishedRunNames returns the runs the database believes are in progress, indexed by
// PipelineRun name — the key the informer's cache can be queried with.
func (s *runSink) UnfinishedRunNames(ctx context.Context, namespace string) (map[string]int64, error) {
	runs, err := s.db.UnfinishedRuns(ctx, namespace)
	if err != nil {
		return nil, err
	}

	names := make(map[string]int64, len(runs))
	for _, run := range runs {
		names[run.Name] = run.ID
	}
	return names, nil
}

func (s *runSink) ConcludeVanishedRun(ctx context.Context, id int64, reason string) error {
	return s.db.MarkRunVanished(ctx, id, reason)
}

// logAdapter translates the observer's notifications towards the collector.
//
// Same reason as runSink: neither `tekton` nor `logs` depends on the other, and the
// translation is paid for once here.
type logAdapter struct{ collector *logs.Collector }

// NewLogAdapter branche le collecteur sur l'observateur Tekton.
func NewLogAdapter(collector *logs.Collector) *logAdapter {
	return &logAdapter{collector: collector}
}

func (a *logAdapter) StartTaskLogs(
	ctx context.Context, runName, taskName, podName string, steps []tekton.Step,
) {
	a.collector.Start(ctx, logs.Target{
		RunName:  runName,
		TaskName: taskName,
		PodName:  podName,
		Steps:    toLogSteps(steps),
	})
}

func (a *logAdapter) FinishTaskLogs(
	ctx context.Context, runName, taskName string, steps []tekton.Step,
) error {
	return a.collector.Finish(ctx, logs.Target{
		RunName:  runName,
		TaskName: taskName,
		Steps:    toLogSteps(steps),
	})
}

// toLogSteps keeps the declared order: it is what restores the chronology at sealing, a
// TaskRun's steps running in series.
func toLogSteps(steps []tekton.Step) []logs.Step {
	converted := make([]logs.Step, 0, len(steps))
	for _, step := range steps {
		converted = append(converted, logs.Step{Name: step.Name, Container: step.Container})
	}
	return converted
}

func (s *runSink) WithProjectPipelines(pipelines ProjectPipelines) *runSink {
	s.pipelines = pipelines
	return s
}

// pipelineApplies says whether a kind is chained for this project.
//
// A failed read **lets it through**: the build succeeded, and depriving a release of its
// verifications because the database hiccupped for a second would be worse than launching one
// the project had switched off. The log says so.
func (s *runSink) pipelineApplies(ctx context.Context, projectID int64, kind string) bool {
	if s.pipelines == nil || projectID == 0 {
		return true
	}

	enabled, err := s.pipelines.PipelineEnabled(ctx, projectID, kind)
	if err != nil {
		s.logger.Error("the project's declared kinds",
			"project", projectID, "kind", kind, "error", err)
		return true
	}
	return enabled
}

func (s *runSink) isVerificationKind(kind string) bool {
	return s.catalog != nil && s.catalog.IsVerification(kind)
}
