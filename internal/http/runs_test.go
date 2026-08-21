package http

import (
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// Tekton ne crée un TaskRun qu'au démarrage d'une tâche : un pipeline de huit
// étapes n'en montrait donc que deux au début, et rien ne disait ce qu'il
// restait à faire.
func TestLesTachesNonDemarreesSontListees(t *testing.T) {
	tasks := mergeTasks(store.RunDetail{
		Tasks: []store.TaskSummary{
			{Name: "git-clone", Status: "succeeded"},
			{Name: "composer-install", Status: "running"},
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
	order := []string{"git-clone", "composer-install", "run-phpstan", "save-results"}
	for i, name := range order {
		if tasks[i].Name != name {
			t.Errorf("position %d: %q, attendu %q", i, tasks[i].Name, name)
		}
	}
	if tasks[2].Status != "pending" || tasks[3].Status != "pending" {
		t.Errorf("statuts: %q, %q — attendu pending", tasks[2].Status, tasks[3].Status)
	}
	// Le libellé déclaré est repris quand il existe, le nom technique sinon.
	if tasks[2].DisplayName != "Run phpstan" || tasks[3].DisplayName != "save-results" {
		t.Errorf("libellés: %q, %q", tasks[2].DisplayName, tasks[3].DisplayName)
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
		Tasks:    []store.TaskSummary{{Name: "tache-disparue", Status: "failed"}},
		Declared: []store.DeclaredTask{{Name: "git-clone"}},
	})

	if len(tasks) != 2 {
		t.Fatalf("%d tâches: %+v", len(tasks), tasks)
	}
	if tasks[0].Name != "tache-disparue" || tasks[0].Status != "failed" {
		t.Errorf("la tâche observée devrait passer en tête: %+v", tasks[0])
	}
}

// Sans déclaration — run projeté avant que la colonne existe — le comportement
// d'avant subsiste : les seules tâches démarrées.
func TestSansDeclarationSeulesLesTachesDemarreesSontRendues(t *testing.T) {
	tasks := mergeTasks(store.RunDetail{
		Tasks: []store.TaskSummary{{Name: "git-clone", Status: "succeeded"}},
	})

	if len(tasks) != 1 || tasks[0].Name != "git-clone" {
		t.Errorf("tâches: %+v", tasks)
	}
}
