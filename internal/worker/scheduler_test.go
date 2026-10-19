package worker

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakePresigner struct {
	signed map[string]string
	err    error
}

func (f fakePresigner) PresignPut(_ context.Context, key string, _ time.Duration) (*url.URL, error) {
	if f.err != nil {
		return nil, f.err
	}
	signed := "https://s3.example/" + key + "?X-Amz-Signature=abc"
	f.signed[key] = signed
	return url.Parse(signed)
}

func uploadPipeline() catalog.Pipeline {
	return catalog.Pipeline{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]catalog.Upload{
			"results": {Param: "results-upload-url", Artifact: "static-tests.tar.gz", Family: "static"},
		},
	}
}

// The key is computed before the run is created: that is what lets the dashboard find it again
// afterwards without sweeping a prefix.
func TestUploadURLsAreMintedBeforeTheRun(t *testing.T) {
	presigner := fakePresigner{signed: map[string]string{}}
	scheduler := &Scheduler{uploads: presigner, logger: slog.New(slog.DiscardHandler)}

	params := map[string]string{}
	if err := scheduler.addUploadURLs(context.Background(), uploadPipeline(), "static-tests-42", params); err != nil {
		t.Fatalf("URLs: %v", err)
	}

	const key = "raw/static-tests-42/static-tests.tar.gz"
	if _, signed := presigner.signed[key]; !signed {
		t.Errorf("signed keys = %v, expected %q", presigner.signed, key)
	}
	if params["results-upload-url"] != presigner.signed[key] {
		t.Errorf("parameter = %q", params["results-upload-url"])
	}
}

// Without storage, the run starts anyway: refusing it would deprive the user of a run that
// stays useful, its logs going by another path.
func TestWithoutStorageTheRunStillStarts(t *testing.T) {
	scheduler := &Scheduler{logger: slog.New(slog.DiscardHandler)}

	params := map[string]string{}
	if err := scheduler.addUploadURLs(context.Background(), uploadPipeline(), "static-tests-42", params); err != nil {
		t.Fatalf("error = %v, expected none", err)
	}
	if len(params) != 0 {
		t.Errorf("parameters = %v, expected none", params)
	}
}

func TestAnIncompleteUploadDeclarationIsRefused(t *testing.T) {
	scheduler := &Scheduler{
		uploads: fakePresigner{signed: map[string]string{}}, logger: slog.New(slog.DiscardHandler),
	}
	pipeline := catalog.Pipeline{
		Kind:    "static-tests",
		Uploads: map[string]catalog.Upload{"results": {Artifact: "x.tar.gz"}},
	}

	if err := scheduler.addUploadURLs(context.Background(), pipeline, "run-1", map[string]string{}); err == nil {
		t.Error("an artifact with no target parameter must be refused")
	}
}

// A release tag composes keys; so does a run name. Validating the segments holds here as
// elsewhere.
func TestAnUnsafeRunNameIsRefused(t *testing.T) {
	scheduler := &Scheduler{
		uploads: fakePresigner{signed: map[string]string{}}, logger: slog.New(slog.DiscardHandler),
	}

	if err := scheduler.addUploadURLs(
		context.Background(), uploadPipeline(), "../evasion", map[string]string{}); err == nil {
		t.Error("a run name outside the grammar must be refused")
	}
}

// The defect that cost three phantom builds: job identifiers start again at 1 on a fresh
// database, whereas PipelineRuns survive in the cluster. With no database identity, the new job
// carried an existing run's name, the creation met a conflict read as "it was already done",
// and nothing started — the interface displaying the failure of a run hours old.
func TestARunsNameTellsApartTwoDatabaseGenerations(t *testing.T) {
	ancienne := RunName("a1b2c3", "build", 1)
	nouvelle := RunName("d4e5f6", "build", 1)

	if ancienne == nouvelle {
		t.Errorf("the same name for two databases: %q — a reset would produce namesakes", ancienne)
	}
}

// The property the naming must keep: two attempts of the same job give the same name. It is
// what makes retrying safe — a second creation meets a conflict the scheduler reads as "it was
// already done" instead of creating a duplicate.
func TestTwoAttemptsOfTheSameJobGiveTheSameName(t *testing.T) {
	if RunName("a1b2c3", "build", 7) != RunName("a1b2c3", "build", 7) {
		t.Error("the name must stay deterministic for a given job")
	}
	// And two distinct jobs are not confused.
	if RunName("a1b2c3", "build", 7) == RunName("a1b2c3", "build", 8) {
		t.Error("two distinct jobs must not share a name")
	}
	if RunName("a1b2c3", "build", 7) == RunName("a1b2c3", "static-tests", 7) {
		t.Error("two distinct kinds must not share a name")
	}
}

// The name must stay a valid Kubernetes resource name.
func TestTheNameStaysAValidKubernetesName(t *testing.T) {
	name := RunName("a1b2c3", "security-audit", 12345)

	if len(name) > 63 {
		t.Errorf("nom trop long (%d): %q", len(name), name)
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			t.Errorf("invalid character %q in %q", r, name)
		}
	}
}

// With no identity, the old naming survives: tests need not supply it, and a deployment that
// has never run does not change behaviour.
func TestWithNoIdentityTheOldNamingSurvives(t *testing.T) {
	if got := RunName("", "build", 3); got != "mse-build-3" {
		t.Errorf("name with no identity: %q", got)
	}
}

// The parameters the catalogue does not declare pass through as they are: that is what makes a
// customised run possible, where one fills in a parameter the pipeline knows and the dashboard
// has not described.
func TestAnUndeclaredParameterPassesThroughAsItIs(t *testing.T) {
	scheduler := &Scheduler{config: map[string]string{"forge.mse.repoUrl": "ssh://git@forge/CNOUS/mse"}}

	pipeline := catalog.Pipeline{
		Kind: "unit-tests",
		Params: map[string]catalog.Param{
			"revision": {Source: catalog.SourceInput, Required: true},
			"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
		},
	}

	params, err := scheduler.resolveParams(pipeline, map[string]string{
		"revision": "unstable",
		"debug":    "true",
	}, scheduler.config)
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}
	if params["debug"] != "true" {
		t.Errorf("extra parameter lost: %v", params)
	}
	if params["revision"] != "unstable" || params["repo-url"] != "ssh://git@forge/CNOUS/mse" {
		t.Errorf("declared parameters = %v", params)
	}
}

// The pass-through does not weaken the separation of sources. Letting a request supply a
// `config` parameter would allow pointing a pipeline at a repository other than the one
// intended — exactly what this separation exists to prevent.
func TestARequestDoesNotReplaceAConfigurationParameter(t *testing.T) {
	scheduler := &Scheduler{config: map[string]string{"forge.mse.repoUrl": "ssh://git@forge/CNOUS/mse"}}

	pipeline := catalog.Pipeline{
		Kind: "unit-tests",
		Params: map[string]catalog.Param{
			"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
			"image":    {Source: catalog.SourceLiteral, Value: "reg.example.fr/mse/ci"},
		},
	}

	params, err := scheduler.resolveParams(pipeline, map[string]string{
		"repo-url": "ssh://git@ailleurs/attaquant/mse",
		"image":    "docker.io/attaquant/ci",
	}, scheduler.config)
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}
	if params["repo-url"] != "ssh://git@forge/CNOUS/mse" {
		t.Errorf("repository = %q: a request overwrote the configuration", params["repo-url"])
	}
	if params["image"] != "reg.example.fr/mse/ci" {
		t.Errorf("image = %q: a request overwrote a literal value", params["image"])
	}
}

// A project setting — the kustomization the MSE plugin supplies — replaces the instance's for
// this job, and for it alone.
func TestAProjectSettingReplacesTheInstances(t *testing.T) {
	scheduler := (&Scheduler{config: map[string]string{
		"forge.mseKustom.repoUrl": "ssh://git@forge/CNOUS/mse-kustom",
		"registry.project":        "reg.example.fr/cnous",
	}}).WithProjectSettings(func(_ context.Context, projectID int64) (map[string]string, error) {
		if projectID != 7 {
			return nil, nil
		}
		return map[string]string{"forge.mseKustom.repoUrl": "ssh://git@forge/vcarroy/kustom"}, nil
	})

	projectRow := int64(7)
	settings, err := scheduler.settingsFor(context.Background(), store.Job{ID: 1, ProjectID: &projectRow})
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings["forge.mseKustom.repoUrl"] != "ssh://git@forge/vcarroy/kustom" {
		t.Errorf("kustomization = %q, expected the project's", settings["forge.mseKustom.repoUrl"])
	}
	// What the project does not state stays the instance's.
	if settings["registry.project"] != "reg.example.fr/cnous" {
		t.Errorf("registre = %q", settings["registry.project"])
	}
	// And the instance's map has not moved: it is shared by every job.
	if scheduler.config["forge.mseKustom.repoUrl"] != "ssh://git@forge/CNOUS/mse-kustom" {
		t.Error("the instance's configuration was written to")
	}

	autre := int64(9)
	settings, err = scheduler.settingsFor(context.Background(), store.Job{ID: 2, ProjectID: &autre})
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings["forge.mseKustom.repoUrl"] != "ssh://git@forge/CNOUS/mse-kustom" {
		t.Errorf("another project inherited the setting: %q", settings["forge.mseKustom.repoUrl"])
	}
}

type depotsDeProjets map[int64]store.Project

func (d depotsDeProjets) GetProject(_ context.Context, id int64) (store.Project, error) {
	project, found := d[id]
	if !found {
		return store.Project{}, store.ErrProjectNotFound
	}
	return project, nil
}

// A project's pipelines clone **its** repository, declared in the database. Without that, a
// second project would build the first one's repository.
func TestAProjectsPipelinesCloneItsRepository(t *testing.T) {
	scheduler := (&Scheduler{config: map[string]string{
		projectRepoKey: "ssh://git@forge/CNOUS/mse.git",
		legacyRepoKey:  "ssh://git@forge/CNOUS/mse.git",
	}}).WithProjectRepositories(depotsDeProjets{
		7: {ID: 7, Repository: "vcarroy/themes", ForgeURL: "https://forge.example.fr"},
		// Declared before its forge was filled in.
		8: {ID: 8, Repository: "vcarroy/no-forge"},
	}, "2222")

	projectRow := int64(7)
	settings, err := scheduler.settingsFor(context.Background(), store.Job{ID: 1, ProjectID: &projectRow})
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	const expected = "ssh://git@forge.example.fr:2222/vcarroy/themes.git"
	if settings[projectRepoKey] != expected {
		t.Errorf("repository = %q, expected the project's", settings[projectRepoKey])
	}
	// The old key carries the same value, for as long as a catalogue names it.
	if settings[legacyRepoKey] != expected {
		t.Errorf("old key = %q", settings[legacyRepoKey])
	}

	sansForge := int64(8)
	settings, err = scheduler.settingsFor(context.Background(), store.Job{ID: 2, ProjectID: &sansForge})
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings[projectRepoKey] != "ssh://git@forge/CNOUS/mse.git" {
		t.Errorf("repository = %q, expected the instance's", settings[projectRepoKey])
	}

	// A job with no project keeps the instance's.
	settings, err = scheduler.settingsFor(context.Background(), store.Job{ID: 3})
	if err != nil || settings[projectRepoKey] != "ssh://git@forge/CNOUS/mse.git" {
		t.Errorf("job with no project: %q (%v)", settings[projectRepoKey], err)
	}
}

// unavailableQueue hands out one job and records how it ends.
type unavailableQueue struct {
	Queue
	job      store.Job
	claimed  bool
	finished []string
	requeued int
}

func (q *unavailableQueue) ClaimNext(context.Context, store.Limits, func(string, int64) string) (store.Job, error) {
	if q.claimed {
		return store.Job{}, store.ErrJobNotFound
	}
	q.claimed = true
	return q.job, nil
}

func (q *unavailableQueue) FinishJob(_ context.Context, _ int64, state, reason string) error {
	q.finished = append(q.finished, state+": "+reason)
	return nil
}

func (q *unavailableQueue) Requeue(context.Context, int64, string) (bool, error) {
	q.requeued++
	return true, nil
}

// A pipeline whose need the catalogue does not bind will not run until an operator binds it:
// retrying would only keep it at the head of the queue. It ends at once, saying what is missing.
func TestAnUnavailablePipelineIsRefusedWithItsReason(t *testing.T) {
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{{
		Kind: "integration-tests", PipelineRef: "php-integration-test",
		Unavailable: "need cluster-deploy is bound to no service account",
	}}}
	queue := &unavailableQueue{job: store.Job{ID: 4, Kind: "integration-tests", RunName: "ark-x-4"}}
	scheduler := NewSchedulerFromCatalog(queue, nil, pipelines, nil, slog.New(slog.DiscardHandler))

	scheduler.dispatchOne(context.Background(), store.Limits{})

	if queue.requeued != 0 {
		t.Errorf("requeued %d time(s): the job cannot succeed until the catalogue changes", queue.requeued)
	}
	if len(queue.finished) != 1 || !strings.Contains(queue.finished[0], "cluster-deploy") ||
		!strings.HasPrefix(queue.finished[0], store.JobFailed) {
		t.Errorf("finished = %v", queue.finished)
	}
}

// What the cluster cannot run is refused like what the catalogue does not bind.
func TestAPipelineTheClusterCannotRunIsRefused(t *testing.T) {
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{{Kind: "e2e-tests", PipelineRef: "e2e-test"}}}
	queue := &unavailableQueue{job: store.Job{ID: 5, Kind: "e2e-tests", RunName: "ark-x-5"}}
	scheduler := NewSchedulerFromCatalog(queue, nil, pipelines, nil, slog.New(slog.DiscardHandler))
	scheduler.SetAvailability(map[string]string{"e2e-tests": `Pipeline "e2e-test" is absent from the cluster`})

	scheduler.dispatchOne(context.Background(), store.Limits{})

	if len(queue.finished) != 1 || !strings.Contains(queue.finished[0], "absent from the cluster") {
		t.Errorf("finished = %v", queue.finished)
	}
}

func (q *unavailableQueue) RecordCapability(_ context.Context, _ int64, capability, version string) error {
	q.finished = append(q.finished, "recorded: "+capability+"@"+version)
	return nil
}

// A job records, before its run exists, which plugin version will read it: an upgrade during the
// run must not change how it is read.
func TestALaunchRecordsThePluginVersion(t *testing.T) {
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{{
		Kind: "php/unit-tests", Capability: "php/unit-tests", PipelineRef: "php-unit-tests",
		// Missing at launch, so the dispatch stops before reaching the cluster.
		Params: map[string]catalog.Param{"revision": {Source: catalog.SourceInput, Required: true}},
	}}}
	queue := &unavailableQueue{job: store.Job{ID: 6, Kind: "php/unit-tests", RunName: "mse-x-6"}}
	scheduler := NewSchedulerFromCatalog(queue, nil, pipelines, nil, slog.New(slog.DiscardHandler)).
		WithPluginVersions(map[string]string{"php": "1.0.0"})

	scheduler.dispatchOne(context.Background(), store.Limits{})

	if len(queue.finished) == 0 || queue.finished[0] != "recorded: php/unit-tests@1.0.0" {
		t.Errorf("recorded = %v", queue.finished)
	}
}
