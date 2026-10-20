package tekton

import (
	"testing"
	"time"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	duckv1 "knative.dev/pkg/apis/duck/v1"
)

func condition(status corev1.ConditionStatus, reason string) duckv1.Conditions {
	return duckv1.Conditions{{
		Type:   "Succeeded",
		Status: status,
		Reason: reason,
	}}
}

func runWith(conditions duckv1.Conditions, started bool) *tektonv1.PipelineRun {
	pr := &tektonv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "php-static-tests-run-abc",
			Namespace: "mse-tekton-pipelines",
			Labels:    map[string]string{LabelPipeline: "php-static-tests"},
		},
	}
	pr.Status.Conditions = conditions
	if started {
		start := metav1.NewTime(time.Now().Add(-time.Minute))
		pr.Status.StartTime = &start
	}
	return pr
}

// The heart of the decision: the state comes from the condition's status, not from a
// list of reasons.
//
// The first two cases were observed on the development cluster, and the first alone
// condemns the enumeration approach: Tekton concludes a successful run with the
// reason `Completed`, not `Succeeded`. A list written from memory would have left
// those runs in flight indefinitely.
func TestStatusComesFromConditionStatusNotReason(t *testing.T) {
	cases := []struct {
		name     string
		status   corev1.ConditionStatus
		reason   string
		started  bool
		expected Status
	}{
		{"success, reason Completed (observed on the cluster)", corev1.ConditionTrue, "Completed", true, StatusSucceeded},
		{"failure, reason Failed (observed on the cluster)", corev1.ConditionFalse, "Failed", true, StatusFailed},
		{"success, reason Succeeded", corev1.ConditionTrue, "Succeeded", true, StatusSucceeded},

		// The reason that trapped the earlier tracking tool.
		{"failure, reason ParameterMissing", corev1.ConditionFalse, "ParameterMissing", false, StatusFailed},
		{"failure, unknown reason", corev1.ConditionFalse, "MotifQueTektonInventeraDemain", true, StatusFailed},
		{"failure, empty reason", corev1.ConditionFalse, "", true, StatusFailed},

		{"cancellation", corev1.ConditionFalse, "Cancelled", true, StatusCancelled},
		{"cancellation, old reason", corev1.ConditionFalse, "PipelineRunCancelled", true, StatusCancelled},
		{"timed out", corev1.ConditionFalse, "PipelineRunTimeout", true, StatusTimedOut},

		{"en cours", corev1.ConditionUnknown, "Running", true, StatusRunning},
		{"running, unknown reason", corev1.ConditionUnknown, "MotifInattendu", true, StatusRunning},
		{"not started yet", corev1.ConditionUnknown, "Started", false, StatusPending},
	}

	for _, c := range cases {
		got := ProjectRun(runWith(condition(c.status, c.reason), c.started)).Status
		if got != c.expected {
			t.Errorf("%s: status = %q, expected %q", c.name, got, c.expected)
		}
	}
}

// An object Tekton has not yet reconciled has no condition. Treating it as a failure
// would show red runs on every creation.
func TestRunWithoutConditionIsPending(t *testing.T) {
	if got := ProjectRun(runWith(nil, false)).Status; got != StatusPending {
		t.Errorf("status = %q, expected %q", got, StatusPending)
	}
}

// The guard against resynchronisation: informer events do not arrive in order, and a
// finished run must never go back to running.
func TestTerminalStatuses(t *testing.T) {
	terminal := []Status{StatusSucceeded, StatusFailed, StatusCancelled, StatusTimedOut}
	transient := []Status{StatusPending, StatusRunning}

	for _, status := range terminal {
		if !status.Terminal() {
			t.Errorf("%q should be terminal", status)
		}
	}
	for _, status := range transient {
		if status.Terminal() {
			t.Errorf("%q should not be terminal", status)
		}
	}
}

// The absence of the paternity label means "observed". The PipelineRuns present on
// the development cluster are in that case: they come from an event listener and
// cron jobs, and the dashboard must display them without ever claiming to drive
// them.
func TestRunWithoutJobLabelIsObserved(t *testing.T) {
	run := ProjectRun(runWith(condition(corev1.ConditionTrue, "Completed"), true))

	if !run.Observed {
		t.Error("a run with no job label must be marked observed")
	}
	if run.JobID != 0 {
		t.Errorf("JobID = %d, expected 0", run.JobID)
	}
}

func TestRunWithJobLabelIsPiloted(t *testing.T) {
	pr := runWith(condition(corev1.ConditionUnknown, "Running"), true)
	pr.Labels[LabelJobID] = "4242"

	run := ProjectRun(pr)
	if run.Observed {
		t.Error("a run carrying the job label must not be marked observed")
	}
	if run.JobID != 4242 {
		t.Errorf("JobID = %d, expected 4242", run.JobID)
	}
}

// An unreadable label must not make a driven run pass for an observed one with a
// fanciful identifier: better to treat it as observed, hence read-only, than to act
// on a job that does not exist.
func TestUnparsableJobLabelFallsBackToObserved(t *testing.T) {
	pr := runWith(condition(corev1.ConditionUnknown, "Running"), true)
	pr.Labels[LabelJobID] = "not-a-number"

	run := ProjectRun(pr)
	if run.JobID != 0 || !run.Observed {
		t.Errorf("JobID = %d, Observed = %v; expected 0 and true", run.JobID, run.Observed)
	}
}

func TestPipelineNameFallsBackToRefThenPlaceholder(t *testing.T) {
	withLabel := runWith(nil, false)
	if got := ProjectRun(withLabel).Pipeline; got != "php-static-tests" {
		t.Errorf("from the label: %q", got)
	}

	withRef := runWith(nil, false)
	withRef.Labels = nil
	withRef.Spec.PipelineRef = &tektonv1.PipelineRef{Name: "security-audit"}
	if got := ProjectRun(withRef).Pipeline; got != "security-audit" {
		t.Errorf("from pipelineRef: %q", got)
	}

	inline := runWith(nil, false)
	inline.Labels = nil
	if got := ProjectRun(inline).Pipeline; got != "(inline)" {
		t.Errorf("inline pipeline: %q, expected an explicit fallback", got)
	}
}

func TestTaskRunProjectionCarriesPodAndSteps(t *testing.T) {
	tr := &tektonv1.TaskRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: "php-static-tests-run-abc-unit-test",
			Labels: map[string]string{
				"tekton.dev/pipelineRun":  "php-static-tests-run-abc",
				"tekton.dev/pipelineTask": "unit-test",
			},
		},
	}
	tr.Status.PodName = "php-static-tests-run-abc-unit-test-pod"
	tr.Status.Conditions = condition(corev1.ConditionUnknown, "Running")
	start := metav1.NewTime(time.Now())
	tr.Status.StartTime = &start
	tr.Status.Steps = []tektonv1.StepState{
		{Name: "composer-install", Container: "step-composer-install",
			ContainerState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		{Name: "phpunit", Container: "step-phpunit",
			ContainerState: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "publish", Container: "step-publish"},
	}

	projection := ProjectTaskRun(tr)

	if projection.RunName != "php-static-tests-run-abc" {
		t.Errorf("RunName = %q", projection.RunName)
	}
	if projection.DisplayName != "unit-test" {
		t.Errorf("DisplayName = %q", projection.DisplayName)
	}
	// The pod's name is what makes log collection possible: without it there is nothing
	// to read.
	if projection.PodName == "" {
		t.Error("PodName empty")
	}
	if projection.Status != StatusRunning {
		t.Errorf("status = %q", projection.Status)
	}

	if len(projection.Steps) != 3 {
		t.Fatalf("%d step(s), expected 3", len(projection.Steps))
	}
	expected := []Status{StatusSucceeded, StatusRunning, StatusPending}
	for i, step := range projection.Steps {
		if step.Status != expected[i] {
			t.Errorf("step %s: status = %q, expected %q", step.Name, step.Status, expected[i])
		}
		// The container's name, not the step's: that is what the pods API expects, and Tekton
		// prefixes them with `step-`.
		if step.Container == step.Name {
			t.Errorf("step %s: container name not told apart from the step name", step.Name)
		}
	}

	if code := projection.Steps[0].ExitCode; code == nil || *code != 0 {
		t.Errorf("exit code of the first step = %v", code)
	}
}

// A container failure must surface as a step failure, with its code: that is what
// allows naming the offending step without reading the logs.
func TestStepFailureCarriesExitCode(t *testing.T) {
	tr := &tektonv1.TaskRun{ObjectMeta: metav1.ObjectMeta{Name: "run-x"}}
	tr.Status.Steps = []tektonv1.StepState{{
		Name: "phpunit", Container: "step-phpunit",
		ContainerState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2}},
	}}

	step := ProjectTaskRun(tr).Steps[0]
	if step.Status != StatusFailed {
		t.Errorf("status = %q, expected %q", step.Status, StatusFailed)
	}
	if step.ExitCode == nil || *step.ExitCode != 2 {
		t.Errorf("exit code = %v, expected 2", step.ExitCode)
	}
}

func TestRunResultsAreCollected(t *testing.T) {
	pr := runWith(condition(corev1.ConditionTrue, "Completed"), true)
	pr.Status.Results = []tektonv1.PipelineRunResult{
		{Name: "tag", Value: *tektonv1.NewStructuredValues("1.8.0-rc1")},
		{Name: "digest", Value: *tektonv1.NewStructuredValues("sha256:abc")},
	}

	results := ProjectRun(pr).Results
	if results["tag"] != "1.8.0-rc1" || results["digest"] != "sha256:abc" {
		t.Errorf("results = %v", results)
	}
}
