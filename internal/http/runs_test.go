package http

import (
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// Tekton ne crée un TaskRun qu'au démarrage d'une tâche : un pipeline de huit
// étapes n'en montrait donc que deux au début, et rien ne disait ce qu'il
// restait à faire.
func TestLesTachesNonDemarreesSontListees(t *testing.T) {
	// Une tâche observée porte deux noms : celui de son TaskRun et, dans
	// `DisplayName`, celui de la tâche du pipeline qu'elle exécute. C'est le
	// second qui rapproche de la déclaration.
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
		t.Fatalf("%d tâches, attendu les 4 déclarées: %+v", len(tasks), tasks)
	}
	// L'ordre est celui du graphe : une tâche non démarrée n'a pas de date, et
	// trier par ce qu'elle n'a pas la rejetterait en fin de liste.
	order := []string{"Clone mse", "composer-install", "Run phpstan", "save-results"}
	for i, label := range order {
		if tasks[i].DisplayName != label {
			t.Errorf("position %d: %q, attendu %q", i, tasks[i].DisplayName, label)
		}
	}
	// Le nom du TaskRun est conservé sur ce qui a démarré : c'est par lui que les
	// journaux se lisent.
	if tasks[0].Name != "mse-unit-tests-1-git-clone" {
		t.Errorf("nom du TaskRun perdu: %q", tasks[0].Name)
	}
	if tasks[2].Status != "pending" || tasks[3].Status != "pending" {
		t.Errorf("statuts: %q, %q — attendu pending", tasks[2].Status, tasks[3].Status)
	}
	// Le libellé déclaré est repris quand il existe, le nom technique sinon.
	if tasks[2].DisplayName != "Run phpstan" || tasks[3].DisplayName != "save-results" {
		t.Errorf("libellés: %q, %q", tasks[2].DisplayName, tasks[3].DisplayName)
	}
	// Le défaut constaté : la même étape apparaissait deux fois, exécutée sous
	// son nom technique et « en attente » sous son libellé.
	seen := map[string]int{}
	for _, task := range tasks {
		seen[task.DisplayName]++
	}
	for label, count := range seen {
		if count > 1 {
			t.Errorf("%q apparaît %d fois", label, count)
		}
	}
	// Ce qui a démarré garde son statut réel.
	if tasks[0].Status != "succeeded" || tasks[1].Status != "running" {
		t.Errorf("statuts observés perdus: %+v", tasks[:2])
	}
}

// Une tâche qui a tourné sans figurer dans la déclaration — pipeline modifié
// entre-temps, run projeté avant l'enregistrement des déclarations — reste
// affichée. La taire perdrait une exécution réelle au profit d'une définition.
func TestUneTacheObserveeHorsDeclarationResteAffichee(t *testing.T) {
	tasks := mergeTasks(store.RunDetail{
		Tasks: []store.TaskSummary{
			{Name: "mse-run-1-tache-disparue", DisplayName: "tache-disparue", Status: "failed"},
		},
		Declared: []store.DeclaredTask{{Name: "git-clone"}},
	})

	if len(tasks) != 2 {
		t.Fatalf("%d tâches: %+v", len(tasks), tasks)
	}
	if tasks[0].DisplayName != "tache-disparue" || tasks[0].Status != "failed" {
		t.Errorf("la tâche observée devrait passer en tête: %+v", tasks[0])
	}
}

// Sans déclaration — run projeté avant que la colonne existe — le comportement
// d'avant subsiste : les seules tâches démarrées.
func TestSansDeclarationSeulesLesTachesDemarreesSontRendues(t *testing.T) {
	tasks := mergeTasks(store.RunDetail{
		Tasks: []store.TaskSummary{
			{Name: "mse-run-1-git-clone", DisplayName: "git-clone", Status: "succeeded"},
		},
	})

	if len(tasks) != 1 || tasks[0].DisplayName != "git-clone" {
		t.Errorf("tâches: %+v", tasks)
	}
}

// Le lien vers le dashboard Tekton, et les deux cas où il ne doit pas exister.
func TestLeLienTektonEstOmisQuandIlSeraitMort(t *testing.T) {
	run := store.RunSummary{Name: "mse-unit-tests-1", Namespace: "mse-tekton-pipelines"}

	configured := NewRunsHandler(nil).WithTektonDashboard("https://tekton.example.fr/")
	want := "https://tekton.example.fr/#/namespaces/mse-tekton-pipelines/pipelineruns/mse-unit-tests-1"
	if got := configured.dashboardLink(run); got != want {
		t.Errorf("lien: %q, attendu %q", got, want)
	}

	// Sans dashboard configuré, pas de lien : le déployer n'est pas une exigence
	// de ce dashboard-ci.
	if got := NewRunsHandler(nil).dashboardLink(run); got != "" {
		t.Errorf("lien sans configuration: %q", got)
	}

	// Sans namespace, l'URL s'ouvrirait sur une page d'erreur. Mieux vaut ne rien
	// proposer.
	orphan := store.RunSummary{Name: "mse-unit-tests-1"}
	if got := configured.dashboardLink(orphan); got != "" {
		t.Errorf("lien sans namespace: %q", got)
	}
}
