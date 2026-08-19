package tekton

import (
	"strconv"
	"strings"
	"time"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	"knative.dev/pkg/apis"
)

// Labels posés par le dashboard sur les PipelineRun qu'il crée. Leur absence est
// ce qui distingue un run observé — déclenché par un webhook Gitea ou un CronJob —
// d'un run piloté.
const (
	LabelJobID = "mse.dashboard/job-id"
	// LabelInstance porte l'identité de la base qui a lancé le run.
	//
	// Le seul `job-id` ne suffit pas : les identifiants repartent à 1 sur une
	// base neuve, les PipelineRun survivent dans le cluster, et un run resté là
	// concluait le job homonyme de la nouvelle base. Constaté — `mse-static-
	// tests-7`, échoué la veille, a fait échouer le job 7 d'une base créée depuis,
	// qui n'avait pas encore été lancé.
	LabelInstance = "mse.dashboard/instance"
	LabelKind     = "mse.dashboard/kind"
	LabelRelease  = "mse.dashboard/release"
	LabelPipeline = "tekton.dev/pipeline"
)

// Status est l'état d'un run dans le modèle du dashboard.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusTimedOut  Status = "timedout"
)

// Terminal dit si l'état ne peut plus changer.
//
// Sert de garde à la projection : un événement d'informer arrivé en retard — une
// resynchronisation périodique livre des objets dans un ordre quelconque — ne doit
// pas ramener un run terminé à `running`.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusTimedOut:
		return true
	default:
		return false
	}
}

// Run est la projection d'un PipelineRun.
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
	// JobID vaut 0 pour un run observé, c'est-à-dire non lancé par le dashboard.
	JobID int64
	// Instance est la base qui a lancé le run. Vide sur un run observé, et sur
	// ceux lancés avant que ce label existe.
	Instance string
	Observed bool
}

// TaskRunProjection est la projection d'un TaskRun.
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

// Step décrit une étape et son conteneur. Le nom du conteneur est ce que l'API
// des pods attend pour lire les logs.
type Step struct {
	Name      string
	Container string
	Status    Status
	ExitCode  *int32
}

// ProjectRun traduit un PipelineRun.
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

	run.Instance = pr.Labels[LabelInstance]
	if raw, ok := pr.Labels[LabelJobID]; ok {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			run.JobID = id
		}
	}
	// Observé plutôt que piloté : le label est la seule marque de paternité, et
	// un run observé s'affiche en lecture seule — le dashboard ne relance ni
	// n'annule ce qu'il n'a pas lancé.
	run.Observed = run.JobID == 0

	return run
}

// ProjectTaskRun traduit un TaskRun.
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

// succeededCondition extrait la condition `Succeeded`, la seule que Tekton
// renseigne pour dire si un run a abouti.
func succeededCondition(conditions []apis.Condition) *apis.Condition {
	for i := range conditions {
		if conditions[i].Type == "Succeeded" {
			return &conditions[i]
		}
	}
	return nil
}

// statusFromCondition dérive l'état à partir du **statut** de la condition, et
// non d'une énumération de motifs.
//
// C'est délibéré, et l'expérience l'impose : un précédent outil de suivi filtrait
// sur une liste de motifs connus (`Succeeded`, `Failed`, `Completed`…) et
// attendait indéfiniment sur un run terminé en `ParameterMissing`, motif absent de
// la liste. Tekton en produit des dizaines et en ajoute à chaque version.
//
// Le motif ne sert donc qu'à distinguer annulation et dépassement de délai parmi
// les échecs, et un motif inconnu retombe sur `failed` — jamais sur `running`. Se
// tromper dans ce sens laisse un run bloqué et gèle sa classe d'exclusion ; dans
// l'autre, il affiche un échec qu'un coup d'œil au motif corrige.
func statusFromCondition(condition *apis.Condition, started bool) Status {
	if condition == nil {
		// Aucune condition : Tekton n'a pas encore réconcilié l'objet.
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
		// Unknown : en cours, ou pas encore démarré. La présence de startTime
		// tranche sans dépendre du motif.
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
	// Un pipeline défini en ligne n'a pas de nom. Le laisser vide serait pire
	// qu'un repli explicite : l'interface afficherait une case blanche.
	return "(en ligne)"
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
