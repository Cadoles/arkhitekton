package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// migrated rend une base au schéma à jour. Les tests de ce fichier travaillent
// tous sur des runs, sans dépendance à un référentiel.
func migratedBare(t *testing.T) *DB {
	t.Helper()

	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func runningRecord(name string) RunRecord {
	started := time.Now().Add(-2 * time.Minute)
	return RunRecord{
		Name:      name,
		Namespace: "mse-tekton-pipelines",
		Pipeline:  "php-static-tests",
		Status:    "running",
		Reason:    "Running",
		StartedAt: &started,
		Observed:  true,
	}
}

func TestUpsertRunIsIdempotent(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	first, err := db.UpsertRun(ctx, runningRecord("run-idempotent"))
	if err != nil {
		t.Fatalf("premier enregistrement: %v", err)
	}
	second, err := db.UpsertRun(ctx, runningRecord("run-idempotent"))
	if err != nil {
		t.Fatalf("second enregistrement: %v", err)
	}
	if first != second {
		t.Errorf("identifiants différents: %d puis %d", first, second)
	}
}

// L'invariant central de la projection. Une resynchronisation d'informer rejoue
// tous les objets dans un ordre quelconque : sans cette garde, un run terminé
// repasserait `running` et gèlerait sa classe d'exclusion, sans que rien
// n'explique pourquoi la file n'avance plus.
func TestTerminalStatusIsNeverOverwritten(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-terminal")); err != nil {
		t.Fatalf("enregistrement initial: %v", err)
	}

	completed := time.Now()
	final := runningRecord("run-terminal")
	final.Status = "failed"
	final.Reason = "Failed"
	final.CompletedAt = &completed
	if _, err := db.UpsertRun(ctx, final); err != nil {
		t.Fatalf("passage en échec: %v", err)
	}

	// L'événement en retard : même objet, état antérieur.
	if _, err := db.UpsertRun(ctx, runningRecord("run-terminal")); err != nil {
		t.Fatalf("événement en retard: %v", err)
	}

	var status, reason string
	var completedAt *time.Time
	err := db.Pool().QueryRow(ctx,
		`select status, coalesce(reason, ''), completed_at from runs where pipelinerun_name = 'run-terminal'`).
		Scan(&status, &reason, &completedAt)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}

	if status != "failed" {
		t.Errorf("statut = %q, attendu failed: un état terminal a été écrasé", status)
	}
	if reason != "Failed" {
		t.Errorf("motif = %q, attendu Failed", reason)
	}
	if completedAt == nil {
		t.Error("completed_at effacé par un événement plus ancien")
	}
}

func TestTaskRunAttachesToItsRun(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-avec-task")); err != nil {
		t.Fatalf("run: %v", err)
	}

	record := TaskRunRecord{
		RunName:     "run-avec-task",
		Name:        "run-avec-task-unit-test",
		DisplayName: "unit-test",
		Status:      "running",
		PodName:     "run-avec-task-unit-test-pod",
	}
	if err := db.UpsertTaskRun(ctx, record); err != nil {
		t.Fatalf("task: %v", err)
	}

	var count int
	if err := db.Pool().QueryRow(ctx,
		`select count(*) from task_runs t join runs r on r.id = t.run_id
		 where r.pipelinerun_name = 'run-avec-task'`).Scan(&count); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if count != 1 {
		t.Errorf("%d task(s) rattachée(s), attendu 1", count)
	}
}

// Un TaskRun peut arriver avant son PipelineRun : l'informer ne garantit aucun
// ordre entre les deux ressources. L'écriture doit alors ne rien faire, sans
// erreur — le prochain événement la reprendra.
func TestTaskRunWithoutItsRunIsIgnoredWithoutError(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	err := db.UpsertTaskRun(ctx, TaskRunRecord{
		RunName: "run-jamais-vu",
		Name:    "run-jamais-vu-task",
		Status:  "running",
	})
	if err != nil {
		t.Fatalf("une task orpheline ne doit pas produire d'erreur: %v", err)
	}

	var count int
	if err := db.Pool().QueryRow(ctx, `select count(*) from task_runs`).Scan(&count); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if count != 0 {
		t.Errorf("%d task(s) écrite(s) sans run parent", count)
	}
}

// Le pod disparaît avant le TaskRun, et un événement tardif ne le porte plus.
// L'effacer rendrait les logs illisibles alors qu'ils sont peut-être encore
// capturables — ou déjà scellés sur MinIO sous une clé dérivée du pod.
func TestPodNameIsNeverErased(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-pod")); err != nil {
		t.Fatalf("run: %v", err)
	}

	withPod := TaskRunRecord{
		RunName: "run-pod", Name: "run-pod-task", Status: "running",
		PodName: "run-pod-task-pod",
	}
	if err := db.UpsertTaskRun(ctx, withPod); err != nil {
		t.Fatalf("task avec pod: %v", err)
	}

	withoutPod := TaskRunRecord{RunName: "run-pod", Name: "run-pod-task", Status: "running"}
	if err := db.UpsertTaskRun(ctx, withoutPod); err != nil {
		t.Fatalf("task sans pod: %v", err)
	}

	var podName *string
	if err := db.Pool().QueryRow(ctx,
		`select pod_name from task_runs where name = 'run-pod-task'`).Scan(&podName); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if podName == nil || *podName != "run-pod-task-pod" {
		t.Errorf("pod_name = %v, attendu conservé", podName)
	}
}

func TestUnfinishedRunsExcludesTerminalOnes(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	completed := time.Now()
	for name, status := range map[string]string{
		"run-en-cours":   "running",
		"run-en-attente": "pending",
		"run-reussi":     "succeeded",
		"run-annule":     "cancelled",
	} {
		record := runningRecord(name)
		record.Status = status
		if status == "succeeded" || status == "cancelled" {
			record.CompletedAt = &completed
		}
		if _, err := db.UpsertRun(ctx, record); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	unfinished, err := db.UnfinishedRuns(ctx, "mse-tekton-pipelines")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(unfinished) != 2 {
		t.Fatalf("%d run(s) non terminé(s), attendu 2: %+v", len(unfinished), unfinished)
	}
}

// La conclusion d'un run disparu, cœur de la réconciliation au démarrage.
func TestMarkRunVanishedConcludesTheRun(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id, err := db.UpsertRun(ctx, runningRecord("run-disparu"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	const reason = "PipelineRunDisparu"
	if err := db.MarkRunVanished(ctx, id, reason); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	var status, got string
	var completedAt *time.Time
	if err := db.Pool().QueryRow(ctx,
		`select status, coalesce(reason, ''), completed_at from runs where id = $1`, id).
		Scan(&status, &got, &completedAt); err != nil {
		t.Fatalf("relecture: %v", err)
	}

	if status != "failed" {
		t.Errorf("statut = %q, attendu failed", status)
	}
	// Le motif doit dire « disparu » et non « échoué » : la distinction évite de
	// faire chercher un log qui n'a jamais existé.
	if got != reason {
		t.Errorf("motif = %q, attendu %q", got, reason)
	}
	if completedAt == nil {
		t.Error("completed_at non renseigné")
	}
}

// Un run déjà conclu ne doit pas être réécrit par la réconciliation : son motif
// réel vaut mieux que « disparu ».
func TestMarkRunVanishedLeavesTerminalRunsAlone(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	completed := time.Now()
	record := runningRecord("run-deja-conclu")
	record.Status = "succeeded"
	record.Reason = "Completed"
	record.CompletedAt = &completed
	id, err := db.UpsertRun(ctx, record)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if err := db.MarkRunVanished(ctx, id, "PipelineRunDisparu"); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	var status, reason string
	if err := db.Pool().QueryRow(ctx,
		`select status, coalesce(reason, '') from runs where id = $1`, id).Scan(&status, &reason); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if status != "succeeded" || reason != "Completed" {
		t.Errorf("run terminal réécrit: statut = %q, motif = %q", status, reason)
	}
}

// Un run que Tekton n'a pas encore réconcilié n'a pas de date de début. Il doit
// apparaître en tête de liste et non en queue, sans quoi le run qu'on vient de
// lancer est le plus dur à trouver.
func TestListRunsPlacesUnstartedRunsFirst(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	old := runningRecord("run-ancien")
	ancient := time.Now().Add(-24 * time.Hour)
	old.StartedAt = &ancient
	if _, err := db.UpsertRun(ctx, old); err != nil {
		t.Fatalf("run ancien: %v", err)
	}

	fresh := runningRecord("run-tout-neuf")
	fresh.Status = "pending"
	fresh.StartedAt = nil
	if _, err := db.UpsertRun(ctx, fresh); err != nil {
		t.Fatalf("run neuf: %v", err)
	}

	runs, err := db.ListRuns(ctx, 10)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("%d run(s), attendu 2", len(runs))
	}
	if runs[0].Name != "run-tout-neuf" {
		t.Errorf("premier run = %q, attendu run-tout-neuf", runs[0].Name)
	}
}

func TestUpsertRunStoresResultsAndJobID(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	record := runningRecord("run-resultats")
	record.Status = "succeeded"
	completed := time.Now()
	record.CompletedAt = &completed
	record.Results = map[string]string{"tag": "1.8.0-rc1"}
	record.JobID = insertJob(t, db)
	record.Observed = false

	if _, err := db.UpsertRun(ctx, record); err != nil {
		t.Fatalf("run: %v", err)
	}

	var results map[string]string
	var jobID *int64
	var observed bool
	if err := db.Pool().QueryRow(ctx,
		`select results, job_id, observed from runs where pipelinerun_name = 'run-resultats'`).
		Scan(&results, &jobID, &observed); err != nil {
		t.Fatalf("relecture: %v", err)
	}

	if results["tag"] != "1.8.0-rc1" {
		t.Errorf("résultats = %v", results)
	}
	if jobID == nil || *jobID != record.JobID {
		t.Errorf("job_id = %v, attendu %d", jobID, record.JobID)
	}
	if observed {
		t.Error("un run portant un job_id ne doit pas être marqué observé")
	}
}

// insertJob crée un job minimal. La colonne runs.job_id le référence : un test
// qui inventerait un identifiant vérifierait la contrainte, pas la projection.
func insertJob(t *testing.T, db *DB) int64 {
	t.Helper()

	var id int64
	err := db.Pool().QueryRow(context.Background(),
		`insert into jobs (kind, queue_class, state, params, requested_by)
		 values ('php-static-tests', 'parallel', 'running', '{}'::jsonb, 'test')
		 returning id`).Scan(&id)
	if err != nil {
		t.Fatalf("création du job: %v", err)
	}
	return id
}

// Un label de job pointant sur un job inexistant ne doit pas faire perdre
// l'observation : la contrainte référentielle ferait échouer l'insertion, et le
// run n'apparaîtrait nulle part. Il est enregistré sans lien, donc en lecture
// seule.
func TestUnknownJobIDDoesNotLoseTheRun(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	record := runningRecord("run-job-fantome")
	record.JobID = 999999
	record.Observed = false

	if _, err := db.UpsertRun(ctx, record); err != nil {
		t.Fatalf("un job inconnu ne doit pas faire échouer l'enregistrement: %v", err)
	}

	var jobID *int64
	var observed bool
	if err := db.Pool().QueryRow(ctx,
		`select job_id, observed from runs where pipelinerun_name = 'run-job-fantome'`).
		Scan(&jobID, &observed); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if jobID != nil {
		t.Errorf("job_id = %v, attendu null", *jobID)
	}
	if !observed {
		t.Error("un run sans lien de job valide doit être en lecture seule")
	}
}

func TestGetRunReturnsItsTasksInExecutionOrder(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-detail")); err != nil {
		t.Fatalf("run: %v", err)
	}

	early := time.Now().Add(-3 * time.Minute)
	late := time.Now().Add(-time.Minute)
	tasks := []TaskRunRecord{
		// Insérées à l'envers de l'ordre d'exécution, et nommées de sorte que
		// l'ordre alphabétique ne sauve pas le test.
		{RunName: "run-detail", Name: "run-detail-zebre", DisplayName: "zebre", Status: "running", StartedAt: &late},
		{RunName: "run-detail", Name: "run-detail-alpha", DisplayName: "alpha", Status: "succeeded", StartedAt: &early},
		{RunName: "run-detail", Name: "run-detail-attente", DisplayName: "attente", Status: "pending"},
	}
	for _, task := range tasks {
		if err := db.UpsertTaskRun(ctx, task); err != nil {
			t.Fatalf("task %s: %v", task.Name, err)
		}
	}

	detail, err := db.GetRun(ctx, "run-detail")
	if err != nil {
		t.Fatalf("détail: %v", err)
	}
	if detail.Run.Name != "run-detail" {
		t.Errorf("run = %q", detail.Run.Name)
	}
	if len(detail.Tasks) != 3 {
		t.Fatalf("%d task(s), attendu 3", len(detail.Tasks))
	}

	order := []string{detail.Tasks[0].DisplayName, detail.Tasks[1].DisplayName, detail.Tasks[2].DisplayName}
	expected := []string{"alpha", "zebre", "attente"}
	for i := range expected {
		if order[i] != expected[i] {
			t.Errorf("ordre = %v, attendu %v (la task non démarrée doit finir la liste)", order, expected)
			break
		}
	}
}

// Un run purgé par les CronJobs de nettoyage est un cas normal : l'erreur doit
// être distinguable pour que l'interface réponde 404 plutôt que 500.
func TestGetRunDistinguishesAbsence(t *testing.T) {
	db := migrated(t)

	_, err := db.GetRun(context.Background(), "run-qui-n-existe-pas")
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("erreur = %v, attendu ErrRunNotFound", err)
	}
}

func TestRunsUpdatedSinceReturnsOnlyChanges(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-avant")); err != nil {
		t.Fatalf("run: %v", err)
	}

	_, watermark, err := db.RunsUpdatedSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("première lecture: %v", err)
	}

	// Rien n'a changé : le flux ne doit rien retransmettre, sinon chaque tour
	// renverrait des centaines de runs figés.
	unchanged, _, err := db.RunsUpdatedSince(ctx, watermark)
	if err != nil {
		t.Fatalf("lecture à vide: %v", err)
	}
	if len(unchanged) != 0 {
		t.Errorf("%d run(s) retransmis sans changement", len(unchanged))
	}

	if _, err := db.UpsertRun(ctx, runningRecord("run-apres")); err != nil {
		t.Fatalf("second run: %v", err)
	}

	changed, _, err := db.RunsUpdatedSince(ctx, watermark)
	if err != nil {
		t.Fatalf("lecture après changement: %v", err)
	}
	if len(changed) != 1 || changed[0].Name != "run-apres" {
		t.Errorf("changements = %+v", changed)
	}
}

// testProjectID est le projet créé par [migrated]. Constante et non variable :
// le schéma est recréé à chaque test, et la séquence repart de un.
const testProjectID int64 = 1

// testProjectSlug est le nom que ce projet porte dans les URL.
const testProjectSlug = "test"

// migrated rend une base au schéma à jour, avec un projet d'accueil.
//
// Sans lui, aucune version ni aucune release ne peut exister depuis la migration
// 0025. Créé ici plutôt que dans chaque test : c'est un prérequis du schéma, pas
// un sujet d'épreuve. Les tests qui **comptent** les projets — amorçage,
// administration des dépôts — prennent [migratedBare], qui n'en pose aucun.
//
// L'identifiant est prévisible parce que `resetSchema` recrée le schéma : la
// séquence repart de un.
func migrated(t *testing.T) *DB {
	t.Helper()

	db := migratedBare(t)
	if _, err := db.Pool().Exec(context.Background(),
		// Avec un suivi : la plupart de ces tests portent sur le rapprochement
		// entre releases et versions d'un outil de gestion de projet, qui n'a de
		// sens que pour un projet suivi. Le cas sans suivi a son propre test.
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context,
		                       tracker_project, created_by)
		 values ($1, 'vcarroy/test', 'Test', 'https://forge.example', 'Dockerfile', '.',
		         'projet-suivi', 'test')`,
		testProjectSlug); err != nil {
		t.Fatalf("projet de test: %v", err)
	}
	return db
}
