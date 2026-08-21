package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RunRecord est ce que la projection d'un PipelineRun dépose en base.
//
// Le type est défini ici et non dans `internal/tekton` pour que `store` ne
// dépende pas de client-go : la couche de persistance n'a aucune raison de
// connaître Kubernetes.
type RunRecord struct {
	Name        string
	Namespace   string
	Pipeline    string
	Status      string
	Reason      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Results     map[string]string
	JobID       int64
	Observed    bool
	// DeclaredTasks énumère les tâches du pipeline, telles qu'il les déclare.
	// Sert à montrer ce qui n'a pas encore démarré : Tekton ne crée un TaskRun
	// qu'au démarrage d'une tâche.
	DeclaredTasks []DeclaredTask
}

// DeclaredTask est une tâche telle que le pipeline la déclare.
//
// Redit ici plutôt qu'importé de `tekton` : ce paquet ignore Tekton, et
// l'adaptateur de l'observateur fait la traduction. C'est la même règle qui fait
// que `tekton` ignore PostgreSQL, et elle se paie une fois — six champs de
// recopie — plutôt qu'en dépendance croisée entre deux paquets qui n'ont pas à
// se connaître.
type DeclaredTask struct {
	Name string `json:"name"`
	// DisplayName porte le label `tekton.dev/pipelineTask`, c'est-à-dire le nom
	// de la **tâche du pipeline** — `git-clone` — et non un libellé humain. Le
	// nom est un accident d'histoire, et il a coûté un défaut : la même étape
	// s'affichait deux fois, une fois exécutée sous ce nom et une fois « en
	// attente » sous le libellé de la déclaration. C'est par lui que les deux se
	// rapprochent.
	DisplayName string `json:"display_name,omitempty"`
	Finally     bool   `json:"finally,omitempty"`
}

// TaskRunRecord est la projection d'un TaskRun.
type TaskRunRecord struct {
	RunName     string
	Name        string
	DisplayName string
	Status      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	PodName     string
}

// terminalStatuses doit rester aligné sur tekton.Status.Terminal(). La
// duplication est assumée : la faire disparaître obligerait `store` à importer le
// paquet Kubernetes, pour une liste de cinq chaînes.
const terminalStatuses = `('succeeded', 'failed', 'cancelled', 'timedout')`

// UpsertRun enregistre ou met à jour un run, et rend son identifiant.
//
// Deux protections sont dans la requête, et non dans le code appelant :
//
//  1. **Un état terminal ne redevient jamais transitoire.** Les événements
//     d'informer n'arrivent pas dans l'ordre — une resynchronisation périodique
//     rejoue tous les objets — et un run terminé qui repasserait `running`
//     gèlerait sa classe d'exclusion sans que rien ne l'explique.
//  2. **Les dates ne régressent pas.** `completed_at` déjà connu n'est pas
//     effacé par un événement plus ancien qui ne le porte pas encore.
//
// Les mettre en SQL plutôt qu'en Go les rend vraies même en cas d'écriture
// concurrente par deux workers, situation que l'élection de leader rend rare
// mais pas impossible pendant une bascule.
func (db *DB) UpsertRun(ctx context.Context, record RunRecord) (int64, error) {
	results, err := marshalResults(record.Results)
	if err != nil {
		return 0, err
	}

	var jobID any
	if record.JobID != 0 {
		jobID = record.JobID
	}

	const query = `
		insert into runs (
			pipeline_name, pipelinerun_name, namespace, status, reason,
			started_at, completed_at, results, observed, job_id, declared_tasks
		)
		-- job_id passe par une sous-requête plutôt que d'être inséré tel quel.
		-- La colonne référence la table jobs, et un PipelineRun portant un
		-- identifiant de job inconnu — base restaurée, label recopié à la main —
		-- ferait échouer l'insertion sur la contrainte. L'observation serait
		-- perdue pour une raison d'intégrité référentielle, ce qui est le mauvais
		-- arbitrage : mieux vaut un run affiché sans lien vers son job.
		--
		-- observed suit la même logique : sans lien valide, le run reste en
		-- lecture seule. C'est le côté sûr — le dashboard ne prétend pas piloter
		-- ce qu'il ne peut pas rattacher.
		select $1, $2, $3, $4, $5, $6, $7, $8,
		       $9 or job.id is null,
		       job.id, $11
		from (select 1) as always
		left join (select id from jobs where id = $10) as job on true
		on conflict (pipelinerun_name) do update set
			status = case
				when runs.status in ` + terminalStatuses + ` then runs.status
				else excluded.status
			end,
			reason = case
				when runs.status in ` + terminalStatuses + ` then runs.reason
				else excluded.reason
			end,
			started_at   = coalesce(runs.started_at, excluded.started_at),
			completed_at = coalesce(runs.completed_at, excluded.completed_at),
			results      = coalesce(excluded.results, runs.results),
			job_id       = coalesce(runs.job_id, excluded.job_id),
			-- La déclaration ne se perd pas : Tekton la retire du statut d'un run
			-- terminé, et une redélivrance tardive viderait sinon la liste d'un
			-- run qu'on regarde encore.
			declared_tasks = case
				when jsonb_array_length(excluded.declared_tasks) > 0 then excluded.declared_tasks
				else runs.declared_tasks
			end,
			updated_at   = now()
		returning id`

	declared, err := json.Marshal(record.DeclaredTasks)
	if err != nil {
		return 0, fmt.Errorf("tâches déclarées du run %q: %w", record.Name, err)
	}
	if record.DeclaredTasks == nil {
		declared = []byte("[]")
	}

	var id int64
	err = db.pool.QueryRow(ctx, query,
		record.Pipeline, record.Name, record.Namespace, record.Status, nullString(record.Reason),
		record.StartedAt, record.CompletedAt, results, record.Observed, jobID, declared,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("enregistrement du run %q: %w", record.Name, err)
	}
	return id, nil
}

// UpsertTaskRun enregistre ou met à jour une task, rattachée à son run.
//
// Le rattachement se fait par nom de PipelineRun : un TaskRun peut être observé
// avant son PipelineRun, l'informer ne garantissant aucun ordre entre les deux
// ressources. Dans ce cas la task est ignorée et le prochain événement — ou la
// resynchronisation — la reprendra.
func (db *DB) UpsertTaskRun(ctx context.Context, record TaskRunRecord) error {
	const query = `
		insert into task_runs (
			run_id, name, display_name, status, started_at, completed_at, pod_name
		)
		select id, $2, $3, $4, $5, $6, $7 from runs where pipelinerun_name = $1
		on conflict (run_id, name) do update set
			status = case
				when task_runs.status in ` + terminalStatuses + ` then task_runs.status
				else excluded.status
			end,
			display_name = coalesce(excluded.display_name, task_runs.display_name),
			started_at   = coalesce(task_runs.started_at, excluded.started_at),
			completed_at = coalesce(task_runs.completed_at, excluded.completed_at),
			-- Le nom du pod ne doit jamais être effacé : c'est la seule prise
			-- pour lire les logs, et le pod disparaît avant le TaskRun.
			pod_name     = coalesce(task_runs.pod_name, excluded.pod_name),
			updated_at   = now()`

	_, err := db.pool.Exec(ctx, query,
		record.RunName, record.Name, nullString(record.DisplayName), record.Status,
		record.StartedAt, record.CompletedAt, nullString(record.PodName),
	)
	if err != nil {
		return fmt.Errorf("enregistrement de la task %q: %w", record.Name, err)
	}
	return nil
}

// UnfinishedRun décrit un run que la base croit en cours.
type UnfinishedRun struct {
	ID     int64
	Name   string
	Status string
}

// UnfinishedRuns liste les runs non terminés d'un namespace.
//
// Point d'entrée de la réconciliation au démarrage : c'est cette liste qui est
// confrontée à ce que le cluster détient réellement.
func (db *DB) UnfinishedRuns(ctx context.Context, namespace string) ([]UnfinishedRun, error) {
	rows, err := db.pool.Query(ctx,
		`select id, pipelinerun_name, status from runs
		 where namespace = $1 and status not in `+terminalStatuses+`
		 order by id`, namespace)
	if err != nil {
		return nil, fmt.Errorf("lecture des runs non terminés: %w", err)
	}
	defer rows.Close()

	var runs []UnfinishedRun
	for rows.Next() {
		var run UnfinishedRun
		if err := rows.Scan(&run.ID, &run.Name, &run.Status); err != nil {
			return nil, fmt.Errorf("lecture des runs non terminés: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// MarkRunVanished conclut un run dont le PipelineRun a disparu du cluster.
//
// Sans cette conclusion, un redémarrage laisse des runs bloqués en `running` qui
// gèlent leur classe d'exclusion — le mode de panne le plus probable de tout le
// dispositif (doc 06 §6.4). Le motif est explicite : « disparu » n'est pas
// « échoué », et la distinction évite de faire chercher un log qui n'existe pas.
func (db *DB) MarkRunVanished(ctx context.Context, id int64, reason string) error {
	_, err := db.pool.Exec(ctx,
		`update runs
		 set status = 'failed', reason = $2,
		     completed_at = coalesce(completed_at, now()), updated_at = now()
		 where id = $1 and status not in `+terminalStatuses, id, reason)
	if err != nil {
		return fmt.Errorf("conclusion du run %d: %w", id, err)
	}
	return nil
}

// RunSummary est la vue de liste d'un run.
type RunSummary struct {
	ID          int64
	Name        string
	Pipeline    string
	Status      string
	Reason      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Observed    bool
	// Namespace est celui du PipelineRun. Sert à composer le lien vers le
	// dashboard Tekton, qui range ses objets par namespace.
	Namespace string
	// ReleaseTag et RequestedBy disent d'où vient l'exécution. Vides sur un run
	// observé — déclenché hors dashboard, il n'a ni job ni demandeur — et c'est
	// une information en soi : personne ici ne l'a lancé.
	//
	// Renseignés par GetRun seulement : la liste n'en a pas l'usage, et la
	// jointure la ferait payer à chaque ligne.
	ReleaseTag  string
	RequestedBy string
}

// ListRuns rend les runs les plus récents d'abord.
func (db *DB) ListRuns(ctx context.Context, limit int) ([]RunSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := db.pool.Query(ctx,
		`select id, pipelinerun_name, pipeline_name, status, coalesce(reason, ''),
		        started_at, completed_at, observed, namespace
		 from runs
		 -- Les runs sans date de début — créés mais pas encore réconciliés par
		 -- Tekton — doivent apparaître en tête et non en fin de liste.
		 order by coalesce(started_at, created_at) desc
		 limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("liste des runs: %w", err)
	}
	defer rows.Close()

	var runs []RunSummary
	for rows.Next() {
		var run RunSummary
		if err := rows.Scan(&run.ID, &run.Name, &run.Pipeline, &run.Status, &run.Reason,
			&run.StartedAt, &run.CompletedAt, &run.Observed, &run.Namespace); err != nil {
			return nil, fmt.Errorf("liste des runs: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func marshalResults(results map[string]string) (any, error) {
	if len(results) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		return nil, fmt.Errorf("sérialisation des résultats: %w", err)
	}
	return encoded, nil
}

// nullString évite d'écrire des chaînes vides là où l'absence est la bonne
// information : `reason` vide et `reason` inconnu ne se distinguent plus ensuite.
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// SealTaskLog enregistre l'objet de journal d'une task et la marque scellée.
//
// Appelé après l'écriture de l'objet, jamais avant : l'ordre inverse annoncerait
// un journal lisible que personne ne pourrait ouvrir.
func (db *DB) SealTaskLog(ctx context.Context, runName, taskName, object string) error {
	_, err := db.pool.Exec(ctx,
		`update task_runs t
		 set log_object = $3, log_sealed = true, updated_at = now()
		 from runs r
		 where r.id = t.run_id and r.pipelinerun_name = $1 and t.name = $2`,
		runName, taskName, object)
	if err != nil {
		return fmt.Errorf("scellement du journal de %q: %w", taskName, err)
	}
	return nil
}

// TaskLogLocation décrit où lire le journal d'une task.
type TaskLogLocation struct {
	RunName  string
	TaskName string
	PodName  string
	Object   string
	Sealed   bool
}

// TaskLog rend l'emplacement du journal d'une task.
func (db *DB) TaskLog(ctx context.Context, runName, taskName string) (TaskLogLocation, error) {
	location := TaskLogLocation{RunName: runName, TaskName: taskName}

	var podName, object *string
	err := db.pool.QueryRow(ctx,
		`select t.pod_name, t.log_object, t.log_sealed
		 from task_runs t join runs r on r.id = t.run_id
		 where r.pipelinerun_name = $1 and t.name = $2`, runName, taskName).
		Scan(&podName, &object, &location.Sealed)
	if err != nil {
		return location, fmt.Errorf("emplacement du journal de %q: %w", taskName, err)
	}

	if podName != nil {
		location.PodName = *podName
	}
	if object != nil {
		location.Object = *object
	}
	return location, nil
}

// RunsUpdatedSince rend les runs modifiés depuis un instant donné, et l'instant
// de la modification la plus récente.
//
// Alimente le flux d'état : renvoyer la liste entière à chaque tour ferait
// retransmettre des centaines de runs figés pour un seul changement.
func (db *DB) RunsUpdatedSince(ctx context.Context, since time.Time) ([]RunSummary, time.Time, error) {
	rows, err := db.pool.Query(ctx,
		`select id, pipelinerun_name, pipeline_name, status, coalesce(reason, ''),
		        started_at, completed_at, observed, namespace, updated_at
		 from runs
		 where updated_at > $1
		 order by updated_at
		 limit 200`, since)
	if err != nil {
		return nil, since, fmt.Errorf("runs modifiés: %w", err)
	}
	defer rows.Close()

	latest := since
	var runs []RunSummary
	for rows.Next() {
		var run RunSummary
		var updatedAt time.Time
		if err := rows.Scan(&run.ID, &run.Name, &run.Pipeline, &run.Status, &run.Reason,
			&run.StartedAt, &run.CompletedAt, &run.Observed, &run.Namespace,
			&updatedAt); err != nil {
			return nil, since, fmt.Errorf("runs modifiés: %w", err)
		}
		if updatedAt.After(latest) {
			latest = updatedAt
		}
		runs = append(runs, run)
	}
	return runs, latest, rows.Err()
}

// TaskSummary est la vue d'une task dans le détail d'un run.
type TaskSummary struct {
	Name        string
	DisplayName string
	Status      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	PodName     string
	// LogSealed dit que le journal est définitif. L'interface s'en sert pour
	// décider s'il faut suivre le flux ou se contenter d'une lecture.
	LogSealed bool
	HasLog    bool
}

// RunDetail est un run et ses tasks.
type RunDetail struct {
	Run   RunSummary
	Tasks []TaskSummary
	// Declared énumère les tâches du pipeline, dans l'ordre du graphe. Vide sur
	// un run projeté avant que cette liste ne soit enregistrée : l'appelant
	// retombe alors sur les seules tâches démarrées.
	Declared []DeclaredTask
}

// ErrRunNotFound signale un run absent.
var ErrRunNotFound = errors.New("run introuvable")

// GetRun rend un run et ses tasks, dans l'ordre d'exécution.
func (db *DB) GetRun(ctx context.Context, name string) (RunDetail, error) {
	var detail RunDetail

	var declared []byte
	// Les jointures sont externes à dessein : un run observé n'a pas de job, et
	// un job de synchronisation n'a pas de release. Une jointure interne les
	// ferait disparaître de l'écran plutôt que de les afficher sans provenance.
	err := db.pool.QueryRow(ctx,
		`select r.id, r.pipelinerun_name, r.pipeline_name, r.status, coalesce(r.reason, ''),
		        r.started_at, r.completed_at, r.observed, r.namespace, r.declared_tasks,
		        coalesce(j.requested_by, ''), coalesce(rel.tag, '')
		 from runs r
		 left join jobs j on j.id = r.job_id
		 left join releases rel on rel.id = j.release_id
		 where r.pipelinerun_name = $1`, name).
		Scan(&detail.Run.ID, &detail.Run.Name, &detail.Run.Pipeline, &detail.Run.Status,
			&detail.Run.Reason, &detail.Run.StartedAt, &detail.Run.CompletedAt,
			&detail.Run.Observed, &detail.Run.Namespace, &declared,
			&detail.Run.RequestedBy, &detail.Run.ReleaseTag)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return detail, ErrRunNotFound
		}
		return detail, fmt.Errorf("lecture du run %q: %w", name, err)
	}

	// Une déclaration illisible n'empêche pas de rendre le run : l'écran retombe
	// sur les tâches démarrées, ce qui est le comportement d'avant.
	if len(declared) > 0 {
		_ = json.Unmarshal(declared, &detail.Declared)
	}

	rows, err := db.pool.Query(ctx,
		`select name, coalesce(display_name, name), status, started_at, completed_at,
		        coalesce(pod_name, ''), log_sealed, log_object is not null
		 from task_runs
		 where run_id = $1
		 -- Les tasks sans date de début — pas encore ordonnancées — passent en
		 -- fin de liste : elles n'ont rien à montrer, contrairement aux runs où
		 -- c'est le plus récent qu'on cherche.
		 order by started_at nulls last, name`, detail.Run.ID)
	if err != nil {
		return detail, fmt.Errorf("lecture des tasks de %q: %w", name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var task TaskSummary
		if err := rows.Scan(&task.Name, &task.DisplayName, &task.Status, &task.StartedAt,
			&task.CompletedAt, &task.PodName, &task.LogSealed, &task.HasLog); err != nil {
			return detail, fmt.Errorf("lecture des tasks de %q: %w", name, err)
		}
		detail.Tasks = append(detail.Tasks, task)
	}
	return detail, rows.Err()
}
