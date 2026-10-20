package http

import (
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// Tekton creates a TaskRun only when a task starts: an eight-stage pipeline therefore
// showed two at first, and nothing said what was left to do.
func TestTasksNotStartedAreListed(t *testing.T) {
	// An observed task carries two names: its TaskRun's and, in DisplayName, that of the
	// pipeline task it runs. The second is what matches the declaration.
	tasks := mergeTasks(store.RunDetail{
		Tasks: []store.TaskSummary{
			{Name: "mse-unit-tests-1-git-clone", DisplayName: "git-clone", Status: "succeeded"},
			{Name: "mse-unit-tests-1-composer-install", DisplayName: "composer-install", Status: "running"},
		},
		Declared: []store.DeclaredTask{
			{Name: "git-clone", DisplayName: "Clone mse"},
			{Name: "composer-install"},
			{Name: "run-phpstan", DisplayName: "Run phpstan"},
			{Name: "save-results", Finally: true},
		},
	})

	if len(tasks) != 4 {
		t.Fatalf("%d tasks, expected the 4 declared: %+v", len(tasks), tasks)
	}
	// The order is the graph's: an unstarted task has no date, and sorting by what it does not
	// have would push it to the end of the list.
	order := []string{"Clone mse", "composer-install", "Run phpstan", "save-results"}
	for i, label := range order {
		if tasks[i].DisplayName != label {
			t.Errorf("position %d: %q, expected %q", i, tasks[i].DisplayName, label)
		}
	}
	// The TaskRun's name is kept on what started: it is how the logs are read.
	if tasks[0].Name != "mse-unit-tests-1-git-clone" {
		t.Errorf("TaskRun name lost: %q", tasks[0].Name)
	}
	if tasks[2].Status != "pending" || tasks[3].Status != "pending" {
		t.Errorf("statuses: %q, %q — expected pending", tasks[2].Status, tasks[3].Status)
	}
	// The declared label is used where it exists, the technical name otherwise.
	if tasks[2].DisplayName != "Run phpstan" || tasks[3].DisplayName != "save-results" {
		t.Errorf("labels: %q, %q", tasks[2].DisplayName, tasks[3].DisplayName)
	}
	// The observed defect: the same step appeared twice, run under its technical name and
	// "pending" under its label.
	seen := map[string]int{}
	for _, task := range tasks {
		seen[task.DisplayName]++
	}
	for label, count := range seen {
		if count > 1 {
			t.Errorf("%q appears %d times", label, count)
		}
	}
	// What started keeps its real status.
	if tasks[0].Status != "succeeded" || tasks[1].Status != "running" {
		t.Errorf("observed statuses lost: %+v", tasks[:2])
	}
}

// A task that ran without appearing in the declaration — a pipeline modified meanwhile, a
// run projected before the declarations were recorded — stays displayed. Hiding it would
// lose a real run in favour of a definition.
func TestAnObservedTaskOutsideTheDeclarationStaysDisplayed(t *testing.T) {
	tasks := mergeTasks(store.RunDetail{
		Tasks: []store.TaskSummary{
			{Name: "mse-run-1-tache-disparue", DisplayName: "tache-disparue", Status: "failed"},
		},
		Declared: []store.DeclaredTask{{Name: "git-clone"}},
	})

	if len(tasks) != 2 {
		t.Fatalf("%d tasks: %+v", len(tasks), tasks)
	}
	if tasks[0].DisplayName != "tache-disparue" || tasks[0].Status != "failed" {
		t.Errorf("the observed task should move to the front: %+v", tasks[0])
	}
}

// With no declaration — a run projected before the column existed — the previous behaviour
// holds: only the started tasks.
func TestWithoutADeclarationOnlyStartedTasksAreReturned(t *testing.T) {
	tasks := mergeTasks(store.RunDetail{
		Tasks: []store.TaskSummary{
			{Name: "mse-run-1-git-clone", DisplayName: "git-clone", Status: "succeeded"},
		},
	})

	if len(tasks) != 1 || tasks[0].DisplayName != "git-clone" {
		t.Errorf("tasks: %+v", tasks)
	}
}

// The link to the Tekton dashboard, and the two cases where it must not exist.
func TestTheTektonLinkIsOmittedWhenItWouldBeDead(t *testing.T) {
	run := store.RunSummary{Name: "mse-unit-tests-1", Namespace: "mse-tekton-pipelines"}

	configured := NewRunsHandler(nil).WithTektonDashboard("https://tekton.example.fr/")
	want := "https://tekton.example.fr/#/namespaces/mse-tekton-pipelines/pipelineruns/mse-unit-tests-1"
	if got := configured.dashboardLink(run); got != want {
		t.Errorf("link: %q, expected %q", got, want)
	}

	// With no dashboard configured, no link: deploying one is not a requirement of this
	// dashboard.
	if got := NewRunsHandler(nil).dashboardLink(run); got != "" {
		t.Errorf("link with no configuration: %q", got)
	}

	// With no namespace, the URL would open on an error page. Better to offer nothing.
	orphan := store.RunSummary{Name: "mse-unit-tests-1"}
	if got := configured.dashboardLink(orphan); got != "" {
		t.Errorf("link with no namespace: %q", got)
	}
}
