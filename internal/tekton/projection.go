package tekton

import (
	"strconv"
	"strings"
	"time"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	"knative.dev/pkg/apis"
)

// Labels the dashboard puts on the PipelineRuns it creates. Their absence is what
// distinguishes an observed run — triggered by a webhook or a cron job — from a
// driven one.
const (
	LabelJobID = "arkhitekton/job-id"
	// LabelInstance carries the identity of the database that launched the run.
	//
	// The job id alone is not enough: identifiers restart at 1 on a fresh database,
	// PipelineRuns outlive it in the cluster, and a leftover run concluded the
	// identically numbered job of the new database. Observed in practice.
	LabelInstance = "arkhitekton/instance"
	LabelKind     = "arkhitekton/kind"
	LabelRelease  = "arkhitekton/release"
	LabelPipeline = "tekton.dev/pipeline"
)

// Status is a run's state in the dashboard's model.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusTimedOut  Status = "timedout"
)

// Terminal says whether the state can still change.
//
// It guards the projection: an informer event arriving late — a periodic resync
// delivers objects in arbitrary order — must not bring a finished run back to
// `running`.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusTimedOut:
		return true
	default:
		return false
	}
}

// Run is a PipelineRun's projection.
type Run struct {
	Name        string
	Namespace   string
	Pipeline    string
	Status      Status
	Reason      string
	Message     string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Results     map[string]string
	// JobID is 0 for an observed run, that is, one the dashboard did not launch.
	JobID int64
	// Instance is the database that launched the run. Empty on an observed run, and on
	// those launched before this label existed.
	Instance string
	Observed bool
	// DeclaredTasks lists the pipeline's tasks, in graph order.
	//
	// Tekton creates a TaskRun only when a task starts: without this list, an
	// eight-stage pipeline shows two at first, and nothing says what is left to do. It
	// comes from `status.pipelineSpec`, which Tekton writes when the run is created — the
	// resolved definition is therefore known from the outset.
	DeclaredTasks []DeclaredTask
}

// DeclaredTask is a task as the pipeline declares it.
type DeclaredTask struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	// Finally marks the final tasks: they run whatever happens, and mixing them in would
	// make their position in the timeline read as one more step of the nominal path.
	Finally bool `json:"finally,omitempty"`
}

// TaskRunProjection is a TaskRun's projection.
type TaskRunProjection struct {
	Name        string
	DisplayName string
	RunName     string
	Status      Status
	Reason      string
	PodName     string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Steps       []Step
}

// Step describes a step and its container. The container's name is what the pods API
// expects in order to read logs.
type Step struct {
	Name      string
	Container string
	Status    Status
	ExitCode  *int32
}

// ProjectRun translates a PipelineRun.
func ProjectRun(pr *tektonv1.PipelineRun) Run {
	run := Run{
		Name:      pr.Name,
		Namespace: pr.Namespace,
		Pipeline:  pipelineName(pr),
		Results:   runResults(pr),
	}

	if pr.Status.StartTime != nil {
		started := pr.Status.StartTime.Time
		run.StartedAt = &started
	}
	if pr.Status.CompletionTime != nil {
		completed := pr.Status.CompletionTime.Time
		run.CompletedAt = &completed
	}

	condition := succeededCondition(pr.Status.Conditions)
	run.Status = statusFromCondition(condition, run.StartedAt != nil)
	if condition != nil {
		run.Reason = condition.Reason
		run.Message = condition.Message
	}

	// The resolved definition, when Tekton has written it. Absent on a freshly created
	// run or one whose resolution failed: the list is then empty, and the screen falls
	// back to the started tasks alone.
	if pr.Status.PipelineSpec != nil {
		for _, task := range pr.Status.PipelineSpec.Tasks {
			run.DeclaredTasks = append(run.DeclaredTasks, DeclaredTask{
				Name: task.Name, DisplayName: task.DisplayName,
			})
		}
		for _, task := range pr.Status.PipelineSpec.Finally {
			run.DeclaredTasks = append(run.DeclaredTasks, DeclaredTask{
				Name: task.Name, DisplayName: task.DisplayName, Finally: true,
			})
		}
	}

	run.Instance = pr.Labels[LabelInstance]
	if raw, ok := pr.Labels[LabelJobID]; ok {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			run.JobID = id
		}
	}
	// Observed rather than driven: the label is the only mark of paternity, and an
	// observed run displays read-only — the dashboard neither re-runs nor cancels what
	// it did not launch.
	run.Observed = run.JobID == 0

	return run
}

// ProjectTaskRun translates a TaskRun.
func ProjectTaskRun(tr *tektonv1.TaskRun) TaskRunProjection {
	projection := TaskRunProjection{
		Name:        tr.Name,
		DisplayName: tr.Labels["tekton.dev/pipelineTask"],
		RunName:     tr.Labels["tekton.dev/pipelineRun"],
		PodName:     tr.Status.PodName,
	}

	if tr.Status.StartTime != nil {
		started := tr.Status.StartTime.Time
		projection.StartedAt = &started
	}
	if tr.Status.CompletionTime != nil {
		completed := tr.Status.CompletionTime.Time
		projection.CompletedAt = &completed
	}

	condition := succeededCondition(tr.Status.Conditions)
	projection.Status = statusFromCondition(condition, projection.StartedAt != nil)
	if condition != nil {
		projection.Reason = condition.Reason
	}

	for _, step := range tr.Status.Steps {
		projection.Steps = append(projection.Steps, projectStep(step))
	}

	return projection
}

func projectStep(step tektonv1.StepState) Step {
	projected := Step{
		Name:      step.Name,
		Container: step.Container,
		Status:    StatusPending,
	}

	switch {
	case step.Terminated != nil:
		code := step.Terminated.ExitCode
		projected.ExitCode = &code
		if code == 0 {
			projected.Status = StatusSucceeded
		} else {
			projected.Status = StatusFailed
		}
	case step.Running != nil:
		projected.Status = StatusRunning
	}

	return projected
}

// succeededCondition extracts the `Succeeded` condition, the only one Tekton fills
// in to say whether a run completed.
func succeededCondition(conditions []apis.Condition) *apis.Condition {
	for i := range conditions {
		if conditions[i].Type == "Succeeded" {
			return &conditions[i]
		}
	}
	return nil
}

// statusFromCondition derives the state from the condition's status, and not from an
// enumeration of reasons.
//
// That is deliberate, and experience imposes it: an earlier tracking tool filtered on
// a list of known reasons and waited indefinitely on a run that ended with a reason
// absent from the list. Tekton produces dozens and adds more with every version.
//
// The reason therefore serves only to tell cancellation and timeout apart among
// failures, and an unknown reason falls back to `failed` — never to `running`. Erring
// this way leaves a run stuck and freezes its exclusion class; erring the other way
// shows a failure that a glance at the reason corrects.
func statusFromCondition(condition *apis.Condition, started bool) Status {
	if condition == nil {
		// No condition: Tekton has not reconciled the object yet.
		return StatusPending
	}

	switch condition.Status {
	case corev1.ConditionTrue:
		return StatusSucceeded

	case corev1.ConditionFalse:
		reason := strings.ToLower(condition.Reason)
		switch {
		case strings.Contains(reason, "cancel"):
			return StatusCancelled
		case strings.Contains(reason, "timeout"):
			return StatusTimedOut
		default:
			return StatusFailed
		}

	default:
		// Unknown: running, or not started. The presence of startTime settles it without
		// depending on the reason.
		if started {
			return StatusRunning
		}
		return StatusPending
	}
}

func pipelineName(pr *tektonv1.PipelineRun) string {
	if name, ok := pr.Labels[LabelPipeline]; ok && name != "" {
		return name
	}
	if pr.Spec.PipelineRef != nil && pr.Spec.PipelineRef.Name != "" {
		return pr.Spec.PipelineRef.Name
	}
	// A pipeline defined inline has no name. Leaving it empty would be worse than an
	// explicit fallback: the interface would show a blank cell.
	return "(inline)"
}

func runResults(pr *tektonv1.PipelineRun) map[string]string {
	if len(pr.Status.Results) == 0 {
		return nil
	}
	results := make(map[string]string, len(pr.Status.Results))
	for _, result := range pr.Status.Results {
		results[result.Name] = result.Value.StringVal
	}
	return results
}
