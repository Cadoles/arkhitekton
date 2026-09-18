package worker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
)

// fakeVerificationStore keeps what is queued after a build.
type fakeVerificationStore struct {
	enqueued  []store.JobRequest
	enqueueEr error
	// state simulates the release and its conditional write.
	state    string
	advances int
	// jobRelease associates a job with its release. Nil, every job returns an empty one — the
	// behaviour for the tests that take no interest in it.
	jobRelease map[int64]store.JobReleaseContext
	// builds is the build count returned to the observer. Zero value: a single successful build,
	// which is the case for the tests that take no interest in the multiplicity of the images.
	builds store.BuildTally
}

func (f *fakeVerificationStore) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	if f.enqueueEr != nil {
		return 0, f.enqueueEr
	}
	f.enqueued = append(f.enqueued, request)
	return int64(len(f.enqueued)), nil
}

func verificationCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Pipelines: []catalog.Pipeline{
			{
				Kind: "static-tests", Verification: true, Params: map[string]catalog.Param{
					"revision": {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			// The unit tests have their own pipeline: two distinct questions, two signals the score weighs
			// separately.
			{
				Kind: "unit-tests", Verification: true, Params: map[string]catalog.Param{
					"revision": {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			{
				Kind: "security-audit", Verification: true, Params: map[string]catalog.Param{
					// The audit names its revision differently from the static tests, and asks for the images
					// already published rather than rebuilding them.
					// Three names for three kinds: it is the declaration, and not the parameter's name, that says
					// what the chaining must put there.
					"app-revision": {
						Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					"portal-image": {
						Source: catalog.SourceInput, Required: true,
						Suggest: catalog.SuggestImage, Image: "portal"},
					"consumer-image": {
						Source: catalog.SourceInput, Required: true,
						Suggest: catalog.SuggestImage, Image: "portal-cli"},
					"app-repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			// The deployment infrastructure's revision comes from the configuration: that is what makes
			// this pipeline chainable. While it was an input, the dashboard did not know how to fill it in.
			{
				Kind: "integration-tests", Verification: true, Params: map[string]catalog.Param{
					"mse-revision": {
						Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					"mse-kustom-revision": {Source: catalog.SourceConfig, Key: "forge.mseKustom.revision"},
					// The pipeline deploys images already published: it receives their tag, never what is needed to
					// rebuild them.
					"image-tag": {
						Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestReleaseTag},
				},
			},
		},
	}
}

// sinkFor stands up the observer for the tests that still drive the old path.
//
// TODO: these tests target `concludeVerification` and `advanceRelease`, dead code since a
// release's plan carries the build and the verification. They remain to be repointed at the
// plan, as those of `launchVerification` just above have been.
func sinkFor(store *fakeVerificationStore, pipelines *catalog.Catalog) *runSink {
	sink := &runSink{
		logger: quietLogger(), catalog: pipelines,
		registryProject: "reg.cadoles.com/vcarroy",
	}
	sink.verification = store
	sink.engine = NewVerificationEngine(sink, newFakePlanStore(), nil, quietLogger())
	return sink
}

// plan opens a release's plan and returns the number of steps started.
//
// # These tests changed driver, not assertions
//
// They called `launchVerification`, which composed the downstream half of the chain in the
// observer. That half has become **the verification stage of a release's plan**, and it is what
// they now exercise.
//
// The project declares **no target**: the build stage is therefore empty, the verification
// stage becomes the first, and it starts immediately — which is what `launchVerification` did
// when called on an already concluded build. It is the same situation, expressed in the model
// rather than beside it.
func openPlan(
	t *testing.T, queue *fakeVerificationStore, pipelines *catalog.Catalog,
	tag, revision string,
) int {
	t.Helper()

	projectRow := store.Project{ID: 1, Slug: "mse"}

	planner := NewReleasePlanner(
		NewEngine(newFakePlanStore(),
			NewReleaseRunner(pipelines, queue, quietLogger()), quietLogger()),
		pipelines,
		// The automation the catch-up would have composed for this project. The plan is now **read**
		// and not composed when opened: passing nil would return an empty plan, and these tests would
		// observe zero steps started with nothing saying it is the read that is missing.
		testAutomation{plan: DefaultAutomation(pipelines, projectRow, nil)},
		"reg.cadoles.com/vcarroy", quietLogger())

	return planner.OpenReleasePlan(context.Background(), projectRow,
		store.Release{ID: 7, Tag: tag}, revision)
}

// testAutomation returns an already declared plan.
type testAutomation struct{ plan plan.Plan }

func (a testAutomation) ReleaseAutomation(
	context.Context, int64,
) (store.ReleaseAutomation, error) {
	return store.ReleaseAutomation{Plan: a.plan}, nil
}

// A successful build chains the verifications on **the commit built**, never on the branch it
// comes from: that branch may have moved since the build, and the tests would then bear on
// something other than the image.
func TestTheVerificationBearsOnTheCommitBuilt(t *testing.T) {
	fake := &fakeVerificationStore{}

	launched := openPlan(t, fake, verificationCatalog(), "3.2.0-rc1", "abc123def456")

	if launched != 4 {
		t.Fatalf("%d verifications launched, expected 4", launched)
	}

	kinds := map[string]store.JobRequest{}
	for _, job := range fake.enqueued {
		kinds[job.Kind] = job
	}
	if _, ok := kinds["static-tests"]; !ok {
		t.Error("static-tests should be launched")
	}
	if _, ok := kinds["unit-tests"]; !ok {
		t.Error("unit-tests should be launched")
	}
	if _, ok := kinds["security-audit"]; !ok {
		t.Error("security-audit should be launched")
	}
	integration, ok := kinds["integration-tests"]
	if !ok {
		t.Error("integration-tests should be launched")
	}
	// The integration pipeline names its revision `mse-revision`. It is still the commit built: the
	// deployment it exercises must be the release's, not that of the branch it came from.
	if integration.Params["mse-revision"] != "abc123def456" {
		t.Errorf("params of integration-tests: %v", integration.Params)
	}
	// `mse-kustom-revision` comes from the configuration, not from the chaining: the scheduler
	// resolves it at launch, and supplying it here would freeze it at the moment of the build.
	if _, present := integration.Params["mse-kustom-revision"]; present {
		t.Error("mse-kustom-revision must not be filled in by the chaining")
	}
	// The tag of the images to deploy is the release's: they are the ones its build has just
	// published, and not a rebuild.
	if integration.Params["image-tag"] != "3.2.0-rc1" {
		t.Errorf("image-tag %q, expected the release's tag", integration.Params["image-tag"])
	}

	static := kinds["static-tests"]
	if static.Params["revision"] != "abc123def456" {
		t.Errorf("params of static-tests: %v — expected the commit, not the branch", static.Params)
	}

	// The audit names its revision `app-revision`. Without that name, it fell back to its default
	// and scanned `CNOUS/mse` at `k8s-unstable` — therefore something other than the release,
	// returning a verdict attributed to the wrong thing.
	audit := kinds["security-audit"]
	if audit.Params["app-revision"] != "abc123def456" {
		t.Errorf("params of security-audit: %v — expected the commit in app-revision", audit.Params)
	}
	// The audit analyses the images **published** by the build, at the release's tag. Rebuilding in
	// order to scan would produce an artifact different from the one that will be deployed.
	if got := audit.Params["portal-image"]; got != "reg.cadoles.com/vcarroy/portal:3.2.0-rc1" {
		t.Errorf("image portal %q", got)
	}
	// `portal-cli` since `mse`'s FrankenPHP migration: the Dockerfile's `consumer` target
	// disappeared, replaced by `cli`, and the image it publishes was renamed with it.
	if got := audit.Params["consumer-image"]; got != "reg.cadoles.com/vcarroy/portal-cli:3.2.0-rc1" {
		t.Errorf("image consumer %q", got)
	}
	if static.ReleaseID == nil || *static.ReleaseID != 7 {
		t.Error("the verifications must carry their release, otherwise their conclusion advances nothing")
	}
}

// With no resolved commit, the verification falls back to the reference: less good, but better
// than launching nothing.
func TestWithNoCommitTheVerificationFallsBackToTheReference(t *testing.T) {
	fake := &fakeVerificationStore{}

	// The reference stands in for the revision: the caller has already made the fallback, and the
	// plan knows only one revision.
	launched := openPlan(t, fake, verificationCatalog(), "3.2.0-rc1", "main")

	if launched != 4 {
		t.Fatalf("%d verifications launched", launched)
	}
	for _, job := range fake.enqueued {
		if job.Kind == "static-tests" && job.Params["revision"] != "main" {
			t.Errorf("revision %q, expected the fallback to the reference", job.Params["revision"])
		}
	}
}

// No reference at all: nothing to test, and saying so is better than queueing a run that will
// fail at the clone.
func TestWithNoReferenceNoVerificationStarts(t *testing.T) {
	fake := &fakeVerificationStore{}

	if launched := openPlan(t, fake, verificationCatalog(), "3.2.0-rc1", ""); launched != 0 {
		t.Errorf("%d verifications launched, expected 0", launched)
	}
	if len(fake.enqueued) != 0 {
		t.Errorf("jobs queued: %+v", fake.enqueued)
	}
}

// A queueing failure is logged, not propagated: the build succeeded, and losing that fact
// because a test could not be queued would be worse.
func TestAQueueingFailureDoesNotFailTheConclusion(t *testing.T) {
	fake := &fakeVerificationStore{enqueueEr: errors.New("file indisponible")}

	if launched := openPlan(t, fake, verificationCatalog(), "3.2.0-rc1", "abc123"); launched != 0 {
		t.Errorf("%d verifications launched despite the failure", launched)
	}
}

// With no catalogue wired, nothing starts — and observation continues. An observer falling over
// for want of a catalogue would also stop projecting the runs, which is far worse than not
// chaining.
func TestWithNoCatalogueNothingStarts(t *testing.T) {
	fake := &fakeVerificationStore{}

	if launched := openPlan(t, fake, nil, "3.2.0-rc1", "abc123"); launched != 0 {
		t.Errorf("%d verifications launched with no catalogue", launched)
	}
}

func (f *fakeVerificationStore) AdvanceReleaseState(_ context.Context, _ int64, from, to string) (bool, error) {
	if f.state != from {
		return false, nil
	}
	f.state = to
	f.advances++
	return true, nil
}

func (f *fakeVerificationStore) CountBuilds(
	_ context.Context, _ int64, _ string,
) (store.BuildTally, error) {
	if f.builds == (store.BuildTally{}) {
		return store.BuildTally{Succeeded: 1}, nil
	}
	return f.builds, nil
}

func (f *fakeVerificationStore) JobRelease(_ context.Context, jobID int64) (store.JobReleaseContext, error) {
	if f.jobRelease == nil {
		return store.JobReleaseContext{}, nil
	}
	linked, ok := f.jobRelease[jobID]
	if !ok {
		return store.JobReleaseContext{}, store.ErrJobWithoutRelease
	}
	return linked, nil
}

// The observer redelivers finished runs: informer resynchronisation, worker restart. One build
// therefore concludes several times, and the verifications must start only once.
//
// Observed before the fix: five pairs of tests for a single build, because the launch preceded
// the transition check.
func TestABuildLaunchesItsVerificationsOnlyOnce(t *testing.T) {
	fake := &fakeVerificationStore{state: "building"}
	pipelines := verificationCatalog()

	projectRow := store.Project{ID: 1, Slug: "mse"}

	// A single planner, therefore a single plan store: it is what carries the uniqueness per
	// (trigger, fact), like the `plan_runs` table.
	planner := NewReleasePlanner(
		NewEngine(newFakePlanStore(),
			NewReleaseRunner(pipelines, fake, quietLogger()), quietLogger()),
		pipelines,
		testAutomation{plan: DefaultAutomation(pipelines, projectRow, nil)},
		"reg.cadoles.com/vcarroy", quietLogger())

	ouvrir := func() int {
		return planner.OpenReleasePlan(context.Background(), projectRow,
			store.Release{ID: 7, Tag: "3.2.0-rc1"}, "abc123")
	}

	if premier := ouvrir(); premier != 4 {
		t.Fatalf("%d verifications launched, expected 4", premier)
	}

	// Redeliveries. The uniqueness no longer comes from a transition check written by hand but from
	// the run itself: `CreatePlanRun` returns the one that exists, and its steps are no longer
	// waiting.
	for i := range 3 {
		if reparti := ouvrir(); reparti != 0 {
			t.Errorf("redelivery %d: %d step(s) started again", i, reparti)
		}
	}

	if len(fake.enqueued) != 4 {
		t.Errorf("%d verifications in all, expected 4 — the redeliveries relaunched",
			len(fake.enqueued))
	}
}
func TestARunOfAnotherDatabaseConcludesNoJob(t *testing.T) {
	sink := &runSink{logger: quietLogger(), instance: "1d82ae"}

	disowned := sink.disown(tekton.Run{
		Name: "mse-static-tests-7", JobID: 7, Instance: "",
		Status: tekton.StatusFailed, Reason: "Failed",
	})

	if disowned.JobID != 0 {
		t.Errorf("job %d still attached", disowned.JobID)
	}
	if !disowned.Observed {
		t.Error("a run of another database is observed, not driven")
	}
	// The run stays projected: it existed, its logs have value, and hiding it would make what the
	// cluster still holds disappear from the history.
	if disowned.Name != "mse-static-tests-7" || disowned.Status != tekton.StatusFailed {
		t.Errorf("the run was altered beyond the attachment: %+v", disowned)
	}
}

// A run of the current database keeps its attachment, without which nothing would advance any
// more.
func TestARunOfTheCurrentDatabaseStaysAttached(t *testing.T) {
	sink := &runSink{logger: quietLogger(), instance: "1d82ae"}

	run := tekton.Run{
		Name: "mse-1d82ae-static-tests-7", JobID: 7, Instance: "1d82ae",
		Status: tekton.StatusSucceeded,
	}
	if got := sink.disown(run); got.JobID != 7 || got.Observed {
		t.Errorf("rattachement perdu: %+v", got)
	}
}

// A run that was never driven — Gitea webhook, CronJob — passes through unchanged, whatever the
// database.
func TestAnObservedRunPassesThroughUnchanged(t *testing.T) {
	sink := &runSink{logger: quietLogger(), instance: "1d82ae"}

	run := tekton.Run{Name: "e2e-test-run-f8m8c", JobID: 0, Observed: true}
	if got := sink.disown(run); got.JobID != 0 || !got.Observed {
		t.Errorf("observed run altered: %+v", got)
	}
}

// The catalogue actually shipped, set against what the chaining launches.
//
// A verification kind that declares no artifact runs, fails or succeeds, and the dashboard
// knows nothing of it: its signal stays "not measured" while ten scanners or three hundred
// tests run. That was the case of `security-audit` and of `integration-tests`, which published
// into the pipelines' bucket — where the reader does not look.
//
// The rule holds for everything that is chained: launching a verification whose verdict will
// not be read amounts to not launching it, only more expensively.
func TestEverythingChainedReturnsAReadableResult(t *testing.T) {
	shipped := "../../deploy/base/config/catalog.yaml"
	if _, err := os.Stat(shipped); err != nil {
		t.Skip("shipped catalogue not found")
	}

	pipelines, err := catalog.Load(shipped)
	if err != nil {
		t.Fatalf("shipped catalogue: %v", err)
	}

	kinds := pipelines.VerificationKinds()
	if len(kinds) == 0 {
		t.Fatal("the shipped catalogue chains no verification: a successful build would conclude on nothing")
	}

	// The rule "a verification declares an artifact" is now held by the catalogue's validation,
	// which `Load` applies: this test would fail earlier, at loading, if the file shipped broke it.
	// Checking it a second time here would measure the same code twice.

	// The point the validation cannot settle on its own: what builds or re-tags must not be chained.
	// A build that chained itself would loop endlessly, publishing an image at each round.
	for _, kind := range []string{"build", "retag"} {
		if pipelines.IsVerification(kind) {
			t.Errorf("%s is declared a verification: a build would chain itself", kind)
		}
	}
}

// And the counterpart: a declared artifact must name a parameter and an object name, without
// which the URL is minted with nobody knowing where to send it — the scheduler then refuses the
// run, and the release stays waiting for a test that will not start.
func TestEachArtifactDeclaresItsParameterAndItsName(t *testing.T) {
	shipped := "../../deploy/base/config/catalog.yaml"
	if _, err := os.Stat(shipped); err != nil {
		t.Skip("shipped catalogue not found")
	}

	pipelines, err := catalog.Load(shipped)
	if err != nil {
		t.Fatalf("shipped catalogue: %v", err)
	}

	for _, pipeline := range pipelines.Pipelines {
		for name, upload := range pipeline.Uploads {
			if upload.Param == "" || upload.Artifact == "" {
				t.Errorf("%s/%s: param=%q artifact=%q — both are required",
					pipeline.Kind, name, upload.Param, upload.Artifact)
			}
			// The object's extension must say its content: it is what decides, at reading, whether an
			// archive must be unpacked.
			archive := strings.HasSuffix(upload.Artifact, ".tar.gz") ||
				strings.HasSuffix(upload.Artifact, ".tgz")
			if (upload.Format == "archive") != archive {
				t.Errorf("%s/%s: format=%q but artifact=%q — the reader will unpack or not, wrongly",
					pipeline.Kind, name, upload.Format, upload.Artifact)
			}
		}
	}
}

// The defect that emptied a release's screen: the reports were indeed ingested — nine rows in
// the database — but with a null `release_id`. The screen looks for them by release and found
// none, leaving the score "not evaluated" on a release whose tests had run and passed.
//
// The cause was one field not filled in at the call: `LoadRun` does carry `ReleaseID`, and the
// three ingestions left it at zero.
func TestAReportIsAttachedToTheReleaseItsRunVerifies(t *testing.T) {
	fake := &fakeVerificationStore{
		jobRelease: map[int64]store.JobReleaseContext{
			42: {ReleaseID: 7, Tag: "3.2.0-rc1", Kind: "static-tests"},
		},
	}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	release := sink.releaseOf(context.Background(), tekton.Run{Name: "mse-static-tests-42", JobID: 42})

	if release == nil {
		t.Fatal("no release returned: the reports would be recorded orphaned")
	}
	if *release != 7 {
		t.Errorf("release %d, expected 7", *release)
	}
}

// A run the dashboard did not launch — Gitea webhook, nightly CronJob — verifies no release. It
// is a normal case: its reports stay consultable from the run, and inventing a release for them
// would make them appear in someone else's score.
func TestAnObservedRunAttachesItsReportsToNoRelease(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	if release := sink.releaseOf(context.Background(),
		tekton.Run{Name: "php-static-tests-run-abc", JobID: 0}); release != nil {
		t.Errorf("release %d returned for an observed run", *release)
	}
}

// Nor does a job with no release — a manual launch from the queue.
func TestAManualLaunchAttachesNothing(t *testing.T) {
	fake := &fakeVerificationStore{jobRelease: map[int64]store.JobReleaseContext{}}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	if release := sink.releaseOf(context.Background(),
		tekton.Run{Name: "mse-static-tests-9", JobID: 9}); release != nil {
		t.Errorf("release %d returned for a job with no release", *release)
	}
}

// The automatic chaining and the re-run of a single kind bear on the same kinds, and the
// catalogue is now their **single** source. What follows therefore no longer compares two lists
// — it checks that the file shipped does name those expected, the declaration being now what
// one can get wrong in writing.
func TestTheShippedCatalogueChainsTheFourExpectedKinds(t *testing.T) {
	shipped := "../../deploy/base/config/catalog.yaml"
	if _, err := os.Stat(shipped); err != nil {
		t.Skip("shipped catalogue not found")
	}

	pipelines, err := catalog.Load(shipped)
	if err != nil {
		t.Fatalf("shipped catalogue: %v", err)
	}

	want := []string{"security-audit", "static-tests", "unit-tests", "integration-tests"}
	got := pipelines.VerificationKinds()

	if len(got) != len(want) {
		t.Fatalf("families chained = %v, expected %v", got, want)
	}
	for index, kind := range want {
		// The order is that of declaration, and it becomes that of queueing: runs changing rank at every
		// build make it impossible to compare two releases at a glance.
		if got[index] != kind {
			t.Errorf("family %d = %q, expected %q", index, got[index], kind)
		}
	}
}

/*
 * A release builds several images, and the pipeline handles only one per run: the conclusion is
 * therefore called once per image.
 *
 * It must advance only on the last. Advancing on the first would start the verifications on a
 * release two of whose three images do not yet exist in the registry — the audit and the
 * integration would fail on an impossible pull, and the failure would name the test instead of
 * the build.
 */
