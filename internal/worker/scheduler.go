package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
)

// tickInterval bounds the wait between two rounds of the loop.
//
// A wake-up through `LISTEN/NOTIFY` with a safety tick is foreseen. Only the tick is
// implemented for now: two seconds of latency at launch are of no consequence for a pipeline
// lasting minutes, whereas a lost notification would leave the queue still until the next tick
// anyway. The notification stays a refinement, not a prerequisite.
const tickInterval = 2 * time.Second

// Queue is what the scheduler needs from the database.
type Queue interface {
	ClaimNext(ctx context.Context, limits store.Limits, runName func(kind string, id int64) string) (store.Job, error)
	Requeue(ctx context.Context, id int64, reason string) (bool, error)
	FinishJob(ctx context.Context, id int64, state, reason string) error
	RecordCapability(ctx context.Context, id int64, capability, version string) error
	ExplainQueue(ctx context.Context, limits store.Limits) error
	PendingCancellations(ctx context.Context) ([]store.RunningJob, error)
	// QueueLimits returns the caps set by hand. Re-read at every round: a setting must take effect
	// without restarting the worker, otherwise nobody uses it.
	QueueLimits(ctx context.Context) (map[string]int, error)
}

// LocalTask runs a job inside the process, without going through Tekton.
//
// Not everything the queue carries becomes a PipelineRun. Generating a delivery note reads the
// database and writes an object: making it cross Tekton would cost a pod and a workspace to
// produce two pages. Going through the queue anyway gives scheduling, a trace and visibility —
// an export appears in the queue like the rest, and its failure is read in the same place.
type LocalTask interface {
	// Kind is the `kind` handled. These kinds are absent from the catalogue, which describes what
	// becomes a PipelineRun.
	Kind() string
	Run(ctx context.Context, job store.Job) error
}

// Presigner mints the write URLs of the artifacts.
//
// The layout belongs to the dashboard: it computes the exact key of each artifact **before**
// creating the run, and supplies the matching URL as a parameter. The pipeline therefore has
// no bucket, no prefix and no credentials — and the reader has nothing to look for by prefix
// afterwards.
type Presigner interface {
	PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error)
}

// uploadTTL bounds the validity of a write URL.
//
// It must outlive the longest pipeline — an E2E campaign exceeds the hour — without becoming a
// lasting write permission. Six hours: beyond that, a leaked URL would stay usable after the
// end of the run that justified it.
const uploadTTL = 6 * time.Hour

// Scheduler turns the queued jobs into PipelineRuns.
type Scheduler struct {
	// version gives a plugin's version in force, the one a run is launched with.
	version func(plugin string) string
	// availability says what the cluster cannot run, kept up to date by the capability reconciler.
	availability availability
	// pluginGate refuses a job whose plugin is switched off.
	pluginGate func(ctx context.Context, projectID *int64, plugin string) error
	queue      Queue
	cluster    *tekton.Client
	catalog    catalog.Source
	config     map[string]string
	local      map[string]LocalTask
	// uploads mints the presigned URLs. Nil when storage is not available: the runs then start
	// with no publication, and say so.
	uploads Presigner
	keys    blob.Keys
	// instance identifies the database. It enters the runs' names so that a reset does not produce
	// namesakes of PipelineRuns left in the cluster.
	instance string
	// projects returns a project's repository, so that its pipelines clone its own. Nil: the
	// instance's repository holds for all.
	projects ProjectRepositories
	// projectSSHPort is the forge's SSH port, which the HTTPS URL does not state.
	projectSSHPort string
	// projectSettings returns the configuration values a plugin adds for a project — MSE's
	// kustomization, its overlay. Nil: the instance's configuration alone.
	projectSettings func(ctx context.Context, projectID int64) (map[string]string, error)
	// deployKeys returns the name of the secret carrying a project's deploy key. Nil when no
	// per-project key is in service: the runs then mount the shared key.
	deployKeys ProjectKeys
	logger     *slog.Logger
}

// ProjectDeployKey is the configuration key naming the secret of the project's own deploy key.
//
// Distinct from `pipeline.gitSecret`, the deployment's clone credential: a project's key opens the
// project's repository and nothing else, so it cannot stand for the clone of another one, like
// the end-to-end campaign's. The catalogue binds whichever a pipeline clones.
const ProjectDeployKey = "project.deployKeySecret"

// binds says whether a pipeline mounts the secret a configuration key names.
func binds(pipeline catalog.Pipeline, configKey string) bool {
	for _, workspace := range pipeline.Workspaces {
		if workspace.Secret != nil && workspace.Secret.ConfigKey == configKey {
			return true
		}
	}
	return false
}

// ProjectKeys says which secret carries a project's deploy key.
//
// A narrow interface rather than the whole store: the scheduler needs only that answer, and
// giving it access to the projects would invite it to read something else there.
type ProjectKeys interface {
	// DeployKeySecret returns the secret's name, and false when the project has no active key of
	// its own. The absence **stops the run**: there is no shared key left to fall back on, and
	// building with another project's is precisely what was made impossible.
	DeployKeySecret(ctx context.Context, projectID int64) (string, bool, error)
	// RegistrySecret returns the secret the project **names** in order to push its images, and
	// false when it names none. Named and not created: a registry credential comes from a human,
	// and the deployment provisions it.
	RegistrySecret(ctx context.Context, projectID int64) (string, bool, error)
}

// WithProjectKeys wires the per-project deploy keys.
func (s *Scheduler) WithProjectKeys(keys ProjectKeys) *Scheduler {
	s.deployKeys = keys
	return s
}

// WithInstance declares the database's identity.
//
// Separate from the constructor so as not to propagate a database read into every caller: it
// is optional, and its absence falls back to the old naming — enough for a test, never for a
// deployment that has already run.
func (s *Scheduler) WithInstance(id string) *Scheduler {
	s.instance = id
	return s
}

// WithUploads wires the generation of the artifact URLs.
func (s *Scheduler) WithUploads(presigner Presigner) *Scheduler {
	s.uploads = presigner
	return s
}

// projectRepoKey is the key under which the catalogue names the repository to clone.
const projectRepoKey = "project.repoUrl"

// NewSchedulerFromCatalog assembles the scheduler. `config` supplies the values of the
// parameters whose source is `config`.
func NewSchedulerFromCatalog(
	queue Queue, cluster *tekton.Client, pipelines catalog.Source,
	config map[string]string, logger *slog.Logger,
) *Scheduler {
	return &Scheduler{
		queue: queue, cluster: cluster, catalog: pipelines,
		config: config, local: map[string]LocalTask{}, logger: logger,
	}
}

// ProjectRepositories returns a project's declared repository.
type ProjectRepositories interface {
	GetProject(ctx context.Context, id int64) (store.Project, error)
}

// WithProjectRepositories wires the resolution of a project's repository.
//
// Without it, every project clones the configuration's repository: that is the previous state,
// and the state of a job naming no project.
func (s *Scheduler) WithProjectRepositories(projects ProjectRepositories, sshPort string) *Scheduler {
	s.projects = projects
	s.projectSSHPort = sshPort
	return s
}

// WithProjectSettings wires the settings a plugin adds per project.
func (s *Scheduler) WithProjectSettings(
	settings func(ctx context.Context, projectID int64) (map[string]string, error),
) *Scheduler {
	s.projectSettings = settings
	return s
}

// WithPluginGate wires the check that a capability's plugin is switched on, for the job's project
// or, without one, for the instance. Without it, every plugin counts as switched on.
func (s *Scheduler) WithPluginGate(gate func(ctx context.Context, projectID *int64, plugin string) error) *Scheduler {
	s.pluginGate = gate
	return s
}

// Handle registers a task run inside the process.
func (s *Scheduler) Handle(task LocalTask) {
	s.local[task.Kind()] = task
}

// Run runs the loop until cancellation.
//
// A single instance must run it: that is what the lease guarantees. The reservation's `skip
// locked` covers the rest — a double deployment launch in production cannot be undone.
func (s *Scheduler) Run(ctx context.Context) error {
	s.logger.Info("scheduler started",
		"cap", catalog.Of(s.catalog).MaxConcurrentRuns, "kinds", catalog.Of(s.catalog).Kinds())

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		// The caps are re-read once per round, and not per job: a setting changed while a round empties
		// the queue will apply at the next round, which is amply enough, whereas a read per job would
		// multiply the round trips to the database for a value that moves once a month.
		limits := s.limits(ctx)

		// Each round empties the queue as far as the rules allow: serving a single job per tick would
		// make four eligible jobs start over eight seconds, for no reason.
		for s.dispatchOne(ctx, limits) {
			if ctx.Err() != nil {
				return nil
			}
		}

		s.applyCancellations(ctx)

		if err := s.queue.ExplainQueue(ctx, limits); err != nil {
			s.logger.Error("queue explanation", "error", err)
		}

		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return nil
		case <-ticker.C:
		}
	}
}

// limits composes the effective caps: the catalogue's, overridden by the recorded settings.
//
// A failed read does not stop the queue: the catalogue's caps then apply alone. Freezing
// because a setting could not be read would cost more than running one round with the default
// values.
func (s *Scheduler) limits(ctx context.Context) store.Limits {
	limits := store.Limits{
		PerKind: catalog.Of(s.catalog).Concurrency(),
		Default: catalog.DefaultKindConcurrency,
		Global:  catalog.Of(s.catalog).MaxConcurrentRuns,
	}

	overrides, err := s.queue.QueueLimits(ctx)
	if err != nil {
		s.logger.Error("reading the queue caps", "error", err)
		return limits
	}
	for kind, limit := range overrides {
		limits.PerKind[kind] = limit
	}
	return limits
}

// dispatchOne reserves and launches a job. Returns false when there is nothing left to do for
// this round.
func (s *Scheduler) dispatchOne(ctx context.Context, limits store.Limits) bool {
	job, err := s.queue.ClaimNext(ctx, limits,
		func(kind string, id int64) string { return RunName(s.instance, kind, id) })
	if err != nil {
		if !errors.Is(err, store.ErrJobNotFound) {
			s.logger.Error("reserving a job", "error", err)
		}
		return false
	}

	// The local tasks are consulted before the catalogue: their `kind` does not appear there, and
	// its absence from the catalogue would conclude the job as failed.
	if task, local := s.local[job.Kind]; local {
		s.runLocal(ctx, task, job)
		return true
	}

	pipeline, found := catalog.Of(s.catalog).Find(job.Kind)
	if !found {
		// The catalogue changed between the queueing and the launch. Putting the job back in the queue
		// would make it loop: better to conclude it as failed with a reason that names the cause.
		s.logger.Error("kind missing from the catalogue", "job", job.ID, "kind", job.Kind)
		s.retry(ctx, job.ID, fmt.Sprintf("kind %q missing from the catalogue", job.Kind))
		return false
	}
	unavailable := pipeline.Unavailable
	if unavailable == "" {
		unavailable = s.availability.reason(job.Kind)
	}
	if unavailable != "" {
		// The catalogue does not bind what the pipeline needs, or the cluster lacks its definition.
		// Retrying would change nothing until an operator acts: the job ends now, with the reason,
		// instead of failing in the cluster.
		reason := fmt.Sprintf("kind %q unavailable: %s", job.Kind, unavailable)
		s.logger.Error("pipeline unavailable", "job", job.ID, "kind", job.Kind, "reason", unavailable)
		if err := s.queue.FinishJob(ctx, job.ID, store.JobFailed, reason); err != nil {
			s.logger.Error("concluding an unavailable job", "job", job.ID, "error", err)
		}
		return true
	}

	if pipeline.Capability != "" {
		plugin, _, _ := strings.Cut(pipeline.Capability, "/")
		if s.pluginGate != nil {
			if err := s.pluginGate(ctx, job.ProjectID, plugin); err != nil {
				// Switched off by an administrator: retrying changes nothing until someone switches it
				// back on, and the reason says who can.
				reason := fmt.Sprintf("kind %q unavailable: %v", job.Kind, err)
				s.logger.Warn("plugin switched off", "job", job.ID, "kind", job.Kind, "plugin", plugin)
				if err := s.queue.FinishJob(ctx, job.ID, store.JobFailed, reason); err != nil {
					s.logger.Error("concluding a job of a disabled plugin", "job", job.ID, "error", err)
				}
				return true
			}
		}
		if err := s.queue.RecordCapability(ctx, job.ID, pipeline.Capability, s.pluginVersion(plugin)); err != nil {
			s.logger.Error("recording the capability", "job", job.ID, "error", err)
			s.retry(ctx, job.ID, err.Error())
			return false
		}
	}

	// The parameters and the secret names are resolved in the same chain, and from the same
	// settings: they are two ways for the catalogue to name a configuration value, and handling
	// them separately would start a run with some resolved by the project and the others not.
	settings, err := s.settingsFor(ctx, job, pipeline)
	var params map[string]string
	if err == nil {
		params, err = s.resolveParams(pipeline, job.Params, settings)
	}
	if err == nil {
		err = s.addUploadURLs(ctx, pipeline, job.RunName, params)
	}
	resolved := pipeline
	if err == nil {
		resolved, err = pipeline.ResolveSecrets(settings)
	}
	if err != nil {
		s.logger.Error("job parameters", "job", job.ID, "error", err)
		s.retry(ctx, job.ID, err.Error())
		return false
	}

	// The creation comes **after** the transition is committed in the database. If it fails, the
	// job goes back to `queued` with one more attempt; the reverse order would produce a run the
	// dashboard could not attach.
	if _, err := s.cluster.Create(ctx, tekton.Request{
		Name:     job.RunName,
		JobID:    job.ID,
		Instance: s.instance,
		Pipeline: resolved,
		Params:   params,
	}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The run already exists under that name: an earlier attempt completed in the cluster without
			// the worker learning it — dead between the creation and its being taken into account. Putting
			// it back in the queue would create a second one, or block the queue indefinitely. The
			// intended state is reached: carry on.
			s.logger.Warn("run already present, an earlier attempt completed",
				"job", job.ID, "run", job.RunName)
			return true
		}

		s.logger.Error("creating the PipelineRun", "job", job.ID, "run", job.RunName, "error", err)
		s.retry(ctx, job.ID, err.Error())
		return false
	}

	s.logger.Info("run launched",
		"job", job.ID, "kind", job.Kind, "run", job.RunName, "file", limits.Of(job.Kind))
	return true
}

// WithPluginVersions sets how the version recorded on the jobs it launches is read: from the
// plugins in force, which an update changes.
func (s *Scheduler) WithPluginVersions(version func(plugin string) string) *Scheduler {
	s.version = version
	return s
}

func (s *Scheduler) pluginVersion(plugin string) string {
	if s.version == nil {
		return ""
	}
	return s.version(plugin)
}

// SetAvailability records what the cluster cannot run.
func (s *Scheduler) SetAvailability(unavailable map[string]string) {
	s.availability.set(unavailable)
}

// addUploadURLs adds one presigned URL per declared artifact.
//
// Done here, before the run is created, and not by the pipeline: that is what lets the
// dashboard know each artifact's key without looking for it, and what takes the MinIO secret
// out of the test Tasks.
//
// An undeclared artifact receives no URL, and its publication is simply ignored by the Task.
// That is the intended degradation: a pipeline producing more than the catalogue announces
// must not fail for that.
func (s *Scheduler) addUploadURLs(
	ctx context.Context, pipeline catalog.Pipeline, runName string, params map[string]string,
) error {
	if len(pipeline.Uploads) == 0 {
		return nil
	}
	if s.uploads == nil {
		// Without storage, the run starts with no publication. Refusing it would deprive the user of a
		// run that stays useful — the logs go by another path.
		s.logger.Warn("storage unavailable, artifacts not published", "run", runName)
		return nil
	}

	for name, upload := range pipeline.Uploads {
		if upload.Param == "" || upload.Artifact == "" {
			return fmt.Errorf("artifact %q: `param` and `artifact` are required to mint a URL", name)
		}

		key, err := s.keys.RawArtifact(runName, upload.Artifact)
		if err != nil {
			return fmt.Errorf("artefact %q: %w", name, err)
		}

		signed, err := s.uploads.PresignPut(ctx, key, uploadTTL)
		if err != nil {
			return fmt.Errorf("artefact %q: %w", name, err)
		}
		params[upload.Param] = signed.String()
	}
	return nil
}

// runLocal runs a task of the process and concludes its job.
//
// The conclusion is immediate and not delegated to the informer: no PipelineRun exists for this
// job, so nothing else will come to finish it. A local job left `running` would block its class
// indefinitely.
func (s *Scheduler) runLocal(ctx context.Context, task LocalTask, job store.Job) {
	if err := task.Run(ctx, job); err != nil {
		s.logger.Error("local task", "job", job.ID, "kind", job.Kind, "error", err)
		// Final failure rather than re-queueing: these tasks fail on causes that do not fix themselves
		// — a release with no image, an unknown format. Retrying would produce the same error in a
		// loop.
		if err := s.queue.FinishJob(ctx, job.ID, store.JobFailed, err.Error()); err != nil {
			s.logger.Error("concluding the job", "job", job.ID, "error", err)
		}
		return
	}

	if err := s.queue.FinishJob(ctx, job.ID, store.JobSucceeded, ""); err != nil {
		s.logger.Error("concluding the job", "job", job.ID, "error", err)
	}
	s.logger.Info("local task finished", "job", job.ID, "kind", job.Kind)
}

// applyCancellations carries out the stop requests the api recorded.
//
// The Tekton patch is idempotent: replaying it on a run already cancelled costs only a call.
// That is what makes it unnecessary to keep extra state to know whether the request was passed
// on — the informer will eventually see the end, and the request will disappear with the job.
func (s *Scheduler) applyCancellations(ctx context.Context) {
	pending, err := s.queue.PendingCancellations(ctx)
	if err != nil {
		s.logger.Error("reading the requested cancellations", "error", err)
		return
	}

	for _, job := range pending {
		if err := s.cluster.Cancel(ctx, job.RunName); err != nil {
			s.logger.Error("cancellation in the cluster",
				"job", job.ID, "run", job.RunName, "error", err)
			continue
		}
		s.logger.Info("cancellation passed to the cluster", "job", job.ID, "run", job.RunName)
	}
}

// resolveParams composes the run's parameters from the catalogue and the request.
//
// The three sources do not mix: `input` comes from the user, `config` from the dashboard's
// configuration, `literal` from the catalogue. Letting a request supply a `config` parameter
// would allow pointing a pipeline at a repository other than the one intended.
//
// What is **not** declared does pass through as it is, and that is what makes a customised run
// possible: the form accepts extra parameters, which the catalogue ignores and the pipeline may
// know. The rule above is not weakened by it — a declared parameter keeps its source, and a
// value supplied under the name of a `config` parameter is set aside by the loop that follows,
// not picked up by the one after.
//
// An extra parameter the Pipeline does not declare fails the creation on Tekton's side, with a
// message naming it. That is the intended behaviour: an explicit refusal is better than a
// parameter silently lost.
func (s *Scheduler) resolveParams(
	pipeline catalog.Pipeline, provided map[string]string, settings map[string]string,
) (map[string]string, error) {
	params := make(map[string]string, len(pipeline.Params))

	for name, declared := range pipeline.Params {
		switch declared.Source {
		case catalog.SourceLiteral:
			params[name] = declared.Value

		case catalog.SourceConfig:
			value, present := settings[declared.Key]
			if !present || (value == "" && declared.Required) {
				return nil, fmt.Errorf("parameter %q: configuration key %q missing", name, declared.Key)
			}
			params[name] = value

		case catalog.SourceInput:
			value, present := provided[name]
			if !present || value == "" {
				if declared.Required {
					return nil, fmt.Errorf("parameter %q required and not supplied", name)
				}
				continue
			}
			params[name] = value
		}
	}

	for name, value := range provided {
		if _, declared := pipeline.Params[name]; declared {
			// Already handled by its source. Picking it up here would overwrite a configuration value with
			// a user value, which the whole separation of sources exists to prevent.
			continue
		}
		if value == "" {
			continue
		}
		params[name] = value
	}

	return params, nil
}

// RunName composes the name of a job's PipelineRun.
//
// It carries the job's identifier: the link is readable in `kubectl` without consulting the
// database, and two attempts of the same job give the same name. That is what makes retrying
// safe — a second creation meets a conflict, which the scheduler reads as "it was already
// done" rather than creating a duplicate.
//
// It **also** carries the database's identity, and that is what was missing. Job identifiers
// start again at 1 on a fresh database, whereas PipelineRuns survive in the cluster: each new
// job attached itself to an existing run, the conflict was read as a success, and nothing
// started. One looked at the failure of a run hours old believing one was looking at one's own.
//
// An empty identity is tolerated so that tests need not supply it; in production it is read at
// start-up and is never empty.
func RunName(instance, kind string, id int64) string {
	// A capability's kind is `plugin/name`, and a Kubernetes name has no `/`.
	kind = strings.ReplaceAll(kind, "/", "-")
	if instance == "" {
		return fmt.Sprintf("mse-%s-%d", kind, id)
	}
	return fmt.Sprintf("mse-%s-%s-%d", instance, kind, id)
}

// retry puts a job back in the queue, or concludes it as failed when it has insisted too much.
//
// The distinction is what stops an unsatisfiable job freezing the queue. A retry serves
// transient failures; a missing parameter, on the other hand, will never materialise — the
// parameters are frozen at queueing, and the catalogue changes. With no cap, the scheduler
// retries the same job indefinitely, always the oldest, and never considers the next ones.
func (s *Scheduler) retry(ctx context.Context, jobID int64, reason string) {
	again, err := s.queue.Requeue(ctx, jobID, reason)
	if err != nil {
		s.logger.Error("requeueing", "job", jobID, "error", err)
		return
	}
	if again {
		return
	}

	// The last attempt's reason is kept: without it, the user would read "too many attempts"
	// without knowing of what.
	final := fmt.Sprintf("given up after %d attempts: %s", store.MaxJobAttempts, reason)
	s.logger.Error("job given up", "job", jobID, "attempts", store.MaxJobAttempts, "reason", reason)
	if err := s.queue.FinishJob(ctx, jobID, store.JobFailed, final); err != nil {
		s.logger.Error("concluding the job given up", "job", jobID, "error", err)
	}
}

// settingsFor returns the run's settings, the project's replacing the deployment's where it
// names any.
//
// A **copy** layered over the global configuration, never a write into it: the map is shared by
// every run, and writing into it would have the next job carry the previous one's key — a
// repository built with another client's key, with no error reporting it.
//
// # A project with no key of its own no longer builds
//
// A missing key fell back to a **shared** key, the deployment's. That fallback is removed: a
// single key added to several repositories makes its revocation an outage for all of them at
// once, and says nothing about who cloned what.
//
// The refusal is therefore explicit, and its message names the missing act — pasting the public
// half into the repository's settings, then declaring it. Without it, the clone would fail in
// the pipeline with an SSH error, very far from its cause.
//
// Only for a pipeline that clones the project's repository: the key is named under
// [ProjectDeployKey], and a pipeline cloning another repository binds the deployment's
// credential instead.
//
// The registry secret keeps its fallback, and that is no inconsistency: it is **named** and not
// created, provisioned by the deployment for the whole instance, and a project naming none in
// particular legitimately pushes where the others push.
func (s *Scheduler) settingsFor(ctx context.Context, job store.Job, pipeline catalog.Pipeline) (map[string]string, error) {
	if job.ProjectID == nil {
		// A job predating projects clones with the deployment's key: there is no project key to
		// name.
		settings := make(map[string]string, len(s.config)+1)
		maps.Copy(settings, s.config)
		settings[ProjectDeployKey] = s.config["pipeline.gitSecret"]
		return settings, nil
	}

	overrides := map[string]string{}

	// The project's repository, the one its pipelines clone. Declared in the database, and not in
	// the configuration: a second project does not build the first one's repository.
	if s.projects != nil {
		project, err := s.projects.GetProject(ctx, *job.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("repository of project %d: %w", *job.ProjectID, err)
		}
		if clone := config.CloneURLFor(project.ForgeURL, project.Repository, s.projectSSHPort); clone != "" {
			overrides[projectRepoKey] = clone
		}
	}

	// What a plugin adds for this project, before the secrets: the two bear on distinct keys, and
	// the order only says which is the more specific should names collide in future.
	if s.projectSettings != nil {
		values, err := s.projectSettings(ctx, *job.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("settings of project %d: %w", *job.ProjectID, err)
		}
		maps.Copy(overrides, values)
	}

	if s.deployKeys == nil {
		settings := make(map[string]string, len(s.config)+len(overrides))
		maps.Copy(settings, s.config)
		maps.Copy(settings, overrides)
		return settings, nil
	}

	// Two settings, read separately: a project may have its key without naming a registry, and the
	// reverse. Handling them together would lose one when the other is missing.

	gitSecret, found, err := s.deployKeys.DeployKeySecret(ctx, *job.ProjectID)
	switch {
	case err == nil && !found && !binds(pipeline, ProjectDeployKey):
		// This pipeline clones no repository of the project's: it needs no key of its own.
	case err != nil:
		// A failed read does not disguise itself as a missing key: the message would send someone to
		// enable a key that may already exist. The retry covers the transient case, and the attempt cap
		// concludes otherwise.
		return nil, fmt.Errorf("reading the deploy key of project %d: %w",
			*job.ProjectID, err)
	case !found:
		return nil, fmt.Errorf(
			"project %d has no active deploy key: paste its public half "+
				"into the repository settings, then declare it active from "+
				"the project screen", *job.ProjectID)
	}
	if found {
		overrides[ProjectDeployKey] = gitSecret
	}

	registrySecret, found, err := s.deployKeys.RegistrySecret(ctx, *job.ProjectID)
	if err != nil {
		// This one does not fail the run: the deployment's secret stays valid, and that is where the
		// projects naming none push.
		s.logger.Error("project's registry secret",
			"job", job.ID, "project", *job.ProjectID, "error", err)
	} else if found {
		overrides["pipeline.registrySecret"] = registrySecret
	}

	// A **copy** layered over, never a write into the shared map.
	settings := make(map[string]string, len(s.config))
	maps.Copy(settings, s.config)
	maps.Copy(settings, overrides)
	return settings, nil
}
