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

// Le cœur de la décision : l'état vient du **statut** de la condition, pas d'une
// liste de motifs.
//
// Les deux premiers cas sont relevés sur le cluster de développement, et le
// premier suffit à condamner l'approche par énumération : Tekton conclut un run
// réussi avec le motif `Completed`, et non `Succeeded`. Une liste écrite de
// mémoire aurait laissé ces runs en cours indéfiniment.
func TestStatusComesFromConditionStatusNotReason(t *testing.T) {
	cases := []struct {
		name     string
		status   corev1.ConditionStatus
		reason   string
		started  bool
		expected Status
	}{
		{"réussite, motif Completed (relevé sur le cluster)", corev1.ConditionTrue, "Completed", true, StatusSucceeded},
		{"échec, motif Failed (relevé sur le cluster)", corev1.ConditionFalse, "Failed", true, StatusFailed},
		{"réussite, motif Succeeded", corev1.ConditionTrue, "Succeeded", true, StatusSucceeded},

		// Le motif qui avait piégé le précédent outil de suivi.
		{"échec, motif ParameterMissing", corev1.ConditionFalse, "ParameterMissing", false, StatusFailed},
		{"échec, motif inconnu", corev1.ConditionFalse, "MotifQueTektonInventeraDemain", true, StatusFailed},
		{"échec, motif vide", corev1.ConditionFalse, "", true, StatusFailed},

		{"annulation", corev1.ConditionFalse, "Cancelled", true, StatusCancelled},
		{"annulation, ancien motif", corev1.ConditionFalse, "PipelineRunCancelled", true, StatusCancelled},
		{"dépassement de délai", corev1.ConditionFalse, "PipelineRunTimeout", true, StatusTimedOut},

		{"en cours", corev1.ConditionUnknown, "Running", true, StatusRunning},
		{"en cours, motif inconnu", corev1.ConditionUnknown, "MotifInattendu", true, StatusRunning},
		{"pas encore démarré", corev1.ConditionUnknown, "Started", false, StatusPending},
	}

	for _, c := range cases {
		got := ProjectRun(runWith(condition(c.status, c.reason), c.started)).Status
		if got != c.expected {
			t.Errorf("%s: statut = %q, attendu %q", c.name, got, c.expected)
		}
	}
}

// Un objet que Tekton n'a pas encore réconcilié n'a aucune condition. Le traiter
// comme un échec ferait afficher des runs rouges à chaque création.
func TestRunWithoutConditionIsPending(t *testing.T) {
	if got := ProjectRun(runWith(nil, false)).Status; got != StatusPending {
		t.Errorf("statut = %q, attendu %q", got, StatusPending)
	}
}

// La garde qui protège de la resynchronisation : les événements d'informer
// n'arrivent pas dans l'ordre, et un run terminé ne doit jamais redevenir en
// cours.
func TestTerminalStatuses(t *testing.T) {
	terminal := []Status{StatusSucceeded, StatusFailed, StatusCancelled, StatusTimedOut}
	transient := []Status{StatusPending, StatusRunning}

	for _, status := range terminal {
		if !status.Terminal() {
			t.Errorf("%q devrait être terminal", status)
		}
	}
	for _, status := range transient {
		if status.Terminal() {
			t.Errorf("%q ne devrait pas être terminal", status)
		}
	}
}

// L'absence du label de paternité vaut « observé ». Les 28 PipelineRun présents
// sur le cluster de développement sont dans ce cas : ils viennent de
// l'EventListener et des CronJobs de `mse-tekton`, et le dashboard doit les
// afficher sans jamais prétendre les piloter.
func TestRunWithoutJobLabelIsObserved(t *testing.T) {
	run := ProjectRun(runWith(condition(corev1.ConditionTrue, "Completed"), true))

	if !run.Observed {
		t.Error("un run sans label de job doit être marqué observé")
	}
	if run.JobID != 0 {
		t.Errorf("JobID = %d, attendu 0", run.JobID)
	}
}

func TestRunWithJobLabelIsPiloted(t *testing.T) {
	pr := runWith(condition(corev1.ConditionUnknown, "Running"), true)
	pr.Labels[LabelJobID] = "4242"

	run := ProjectRun(pr)
	if run.Observed {
		t.Error("un run portant le label de job ne doit pas être marqué observé")
	}
	if run.JobID != 4242 {
		t.Errorf("JobID = %d, attendu 4242", run.JobID)
	}
}

// Un label illisible ne doit pas faire passer un run piloté pour un run observé
// avec un identifiant fantaisiste : mieux vaut le traiter comme observé, donc en
// lecture seule, que d'agir sur un job qui n'existe pas.
func TestUnparsableJobLabelFallsBackToObserved(t *testing.T) {
	pr := runWith(condition(corev1.ConditionUnknown, "Running"), true)
	pr.Labels[LabelJobID] = "pas-un-nombre"

	run := ProjectRun(pr)
	if run.JobID != 0 || !run.Observed {
		t.Errorf("JobID = %d, Observed = %v ; attendu 0 et true", run.JobID, run.Observed)
	}
}

func TestPipelineNameFallsBackToRefThenPlaceholder(t *testing.T) {
	withLabel := runWith(nil, false)
	if got := ProjectRun(withLabel).Pipeline; got != "php-static-tests" {
		t.Errorf("depuis le label: %q", got)
	}

	withRef := runWith(nil, false)
	withRef.Labels = nil
	withRef.Spec.PipelineRef = &tektonv1.PipelineRef{Name: "security-audit"}
	if got := ProjectRun(withRef).Pipeline; got != "security-audit" {
		t.Errorf("depuis pipelineRef: %q", got)
	}

	inline := runWith(nil, false)
	inline.Labels = nil
	if got := ProjectRun(inline).Pipeline; got != "(en ligne)" {
		t.Errorf("pipeline en ligne: %q, attendu un repli explicite", got)
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
	// Le nom du pod est ce qui rend la collecte de logs possible : sans lui, il
	// n'y a rien à lire.
	if projection.PodName == "" {
		t.Error("PodName vide")
	}
	if projection.Status != StatusRunning {
		t.Errorf("statut = %q", projection.Status)
	}

	if len(projection.Steps) != 3 {
		t.Fatalf("%d étape(s), attendu 3", len(projection.Steps))
	}
	expected := []Status{StatusSucceeded, StatusRunning, StatusPending}
	for i, step := range projection.Steps {
		if step.Status != expected[i] {
			t.Errorf("étape %s: statut = %q, attendu %q", step.Name, step.Status, expected[i])
		}
		// Le nom du conteneur, et non celui de l'étape : c'est ce que l'API des
		// pods attend, et Tekton les préfixe par `step-`.
		if step.Container == step.Name {
			t.Errorf("étape %s: nom de conteneur non distingué du nom d'étape", step.Name)
		}
	}

	if code := projection.Steps[0].ExitCode; code == nil || *code != 0 {
		t.Errorf("code de sortie de la première étape = %v", code)
	}
}

// Un échec de conteneur doit remonter comme échec d'étape, avec son code : c'est
// ce qui permet de désigner l'étape fautive sans lire les logs.
func TestStepFailureCarriesExitCode(t *testing.T) {
	tr := &tektonv1.TaskRun{ObjectMeta: metav1.ObjectMeta{Name: "run-x"}}
	tr.Status.Steps = []tektonv1.StepState{{
		Name: "phpunit", Container: "step-phpunit",
		ContainerState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2}},
	}}

	step := ProjectTaskRun(tr).Steps[0]
	if step.Status != StatusFailed {
		t.Errorf("statut = %q, attendu %q", step.Status, StatusFailed)
	}
	if step.ExitCode == nil || *step.ExitCode != 2 {
		t.Errorf("code de sortie = %v, attendu 2", step.ExitCode)
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
		t.Errorf("résultats = %v", results)
	}
}
