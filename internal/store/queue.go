package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// États d'un job.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Motifs d'attente, contraints par le schéma.
//
// Une file qui n'explique pas son attente engendre systématiquement des relances
// inutiles : l'utilisateur voit un job immobile et en lance un second.
const (
	// ReasonKindBusy : la file du type est pleine. C'est le motif le plus
	// courant, et le seul qu'un réglage puisse lever.
	ReasonKindBusy = "kind-busy"
	// ReasonCapacity : le plafond global est atteint, toutes files confondues.
	// Il protège le cluster et prime sur les plafonds par type.
	ReasonCapacity = "capacity"
)

// Limits porte les plafonds effectifs, par type.
//
// Composés par l'appelant à partir du catalogue et des réglages : `store` ne
// connaît ni l'un ni les autres, et les recomposer ici le lierait au catalogue
// pour une addition.
type Limits struct {
	// PerKind donne le plafond d'un type. Un type absent prend Default.
	PerKind map[string]int
	// Default s'applique aux types que le catalogue ne décrit pas — les tâches
	// locales, un type retiré depuis la mise en file.
	Default int
	// Global borne le total, toutes files confondues. Sans lui, sept files à
	// cinq places autoriseraient trente-cinq exécutions simultanées, ce qu'aucun
	// cluster de ce projet ne tient.
	Global int
}

// Of rend le plafond effectif d'un type.
func (l Limits) Of(kind string) int {
	if limit, declared := l.PerKind[kind]; declared && limit > 0 {
		return limit
	}
	return l.Default
}

// arrays rend les plafonds sous la forme attendue par `unnest`.
//
// Deux tableaux parallèles plutôt qu'un JSON : la jointure se lit en SQL sans
// extraction, et Postgres sait les indexer.
func (l Limits) arrays() ([]string, []int32) {
	kinds := make([]string, 0, len(l.PerKind))
	limits := make([]int32, 0, len(l.PerKind))
	for kind, limit := range l.PerKind {
		kinds = append(kinds, kind)
		limits = append(limits, int32(limit))
	}
	return kinds, limits
}

// JobRequest décrit une demande de lancement.
type JobRequest struct {
	Kind        string
	Priority    int
	Params      map[string]string
	RequestedBy string
	ReleaseID   *int64
	Environment *string
	// ProjectID rattache le job à son dépôt. Nil pour ce qui n'en vise aucun.
	//
	// L'ordonnanceur s'en sert pour monter la clé de déploiement du projet
	// plutôt que la clé partagée : sans lui, il ne peut que retomber sur celle-ci
	// et annuler le bénéfice de la séparation (migration 0035, doc 12 §12.7).
	ProjectID *int64
}

// Job est un job de la file.
type Job struct {
	ID          int64
	Kind        string
	Priority    int
	State       string
	Params      map[string]string
	RequestedBy string
	RequestedAt time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Attempt     int
	QueueReason string
	Error       string
	RunName     string
	// ReleaseID rattache le job à une release. Renseigné pour les promotions et
	// les exports, dont l'exécution part de la release et non de paramètres.
	ReleaseID *int64
	// ProjectID rattache le job à son dépôt. Nil pour les jobs antérieurs à la
	// migration 0035 qui ne visaient aucune release : leur en inventer un
	// rendrait un historique faux plutôt qu'incomplet.
	ProjectID *int64
	// ConcurrentAtStart est le nombre de runs constatés au moment de la
	// réservation. Renseigné par `ClaimNext` seule : c'est le seul instant où
	// l'information existe, et c'est elle qui décide si une mesure de charge est
	// comparable à sa baseline (B16).
	ConcurrentAtStart int
}

// ErrJobNotFound signale un job absent.
var ErrJobNotFound = errors.New("job introuvable")

// Enqueue met un job en file.
func (db *DB) Enqueue(ctx context.Context, request JobRequest) (int64, error) {
	params, err := json.Marshal(request.Params)
	if err != nil {
		return 0, fmt.Errorf("sérialisation des paramètres: %w", err)
	}
	if request.Priority == 0 {
		request.Priority = 100
	}

	var id int64
	err = db.pool.QueryRow(ctx,
		// Le projet se **dérive** de la release quand l'appelant ne l'a pas
		// donné. Huit sites enfilent des jobs, et exiger de chacun qu'il pense au
		// projet garantissait qu'un l'oublierait : le run serait alors parti avec
		// la clé partagée, sans erreur, ce qui est précisément l'écart que le lot
		// G ferme. Ici, la règle est écrite une fois.
		`insert into jobs (kind, priority, state, params, requested_by, release_id,
		                   environment_id, project_id)
		 values ($1, $2, 'queued', $3, $4, $5, $6,
		         coalesce($7, (select project_id from releases where id = $5)))
		 returning id`,
		request.Kind, request.Priority, params,
		request.RequestedBy, request.ReleaseID, request.Environment,
		request.ProjectID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("mise en file de %q: %w", request.Kind, err)
	}
	return id, nil
}

// ClaimNext réserve le prochain job éligible et le passe en `running`.
//
// Toute la sémantique de file est dans cette requête, et c'est délibéré : la
// répartir entre du SQL et du Go ouvrirait une fenêtre entre la lecture et
// l'écriture, pendant laquelle un second ordonnanceur pourrait choisir le même
// job. `for update ... skip locked` la referme — ceinture et bretelles avec
// l'élection de leader, parce qu'un double lancement de déploiement en production
// n'est pas rattrapable.
//
// Deux conditions, et plus trois régimes :
//
//  1. le plafond global protège le cluster, toutes files confondues ;
//  2. la file du type doit avoir une place libre.
//
// Chaque type a sa file. Un type sans plafond déclaré prend le défaut, ce qui
// vaut pour les tâches locales — absentes du catalogue — comme pour un type
// retiré depuis la mise en file : le job avance au lieu d'attendre un réglage
// qui n'existera jamais.
//
// L'ordre reste le même pour tous, priorité puis ancienneté : c'est la file
// unique qui décide de qui est examiné en premier, les plafonds ne font
// qu'écarter. Un job dont la file est pleine est sauté, pas bloquant — sans quoi
// une intégration en cours retiendrait tous les tests unitaires derrière elle.
//
// Rend `ErrJobNotFound` quand rien n'est éligible — cas courant, pas une erreur.
func (db *DB) ClaimNext(
	ctx context.Context, limits Limits, runName func(kind string, id int64) string,
) (Job, error) {
	var job Job

	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return job, fmt.Errorf("ouverture de transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	const query = `
		with limits as (
		    select * from unnest($2::text[], $3::int[]) as l(kind, max_concurrent)
		),
		running as (
		    select kind from jobs where state = 'running'
		)
		select j.id, j.kind, j.priority, j.params, j.requested_by,
		       j.requested_at, j.attempt, j.release_id, j.project_id,
		       -- Constat des conditions de démarrage, relevé **ici** et pas plus
		       -- tard : minutes après, plus rien ne dit ce qui tournait au moment
		       -- de la mesure. La CTE running est calculée avant la réservation,
		       -- donc ce compte exclut le job réservé (B16).
		       (select count(*) from running)
		from jobs j
		where j.state = 'queued'
		  and (select count(*) from running) < $1
		  and (select count(*) from running r where r.kind = j.kind)
		      < coalesce((select l.max_concurrent from limits l where l.kind = j.kind), $4)
		order by j.priority, j.requested_at
		for update of j skip locked
		limit 1`

	kinds, perKind := limits.arrays()

	var params []byte
	err = transaction.QueryRow(ctx, query, limits.Global, kinds, perKind, limits.Default).Scan(
		&job.ID, &job.Kind, &job.Priority, &params,
		&job.RequestedBy, &job.RequestedAt, &job.Attempt, &job.ReleaseID,
		&job.ProjectID, &job.ConcurrentAtStart)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return job, ErrJobNotFound
		}
		return job, fmt.Errorf("sélection du prochain job: %w", err)
	}

	if len(params) > 0 {
		if err := json.Unmarshal(params, &job.Params); err != nil {
			return job, fmt.Errorf("paramètres du job %d: %w", job.ID, err)
		}
	}

	// Le nom du PipelineRun est décidé **avant** la création dans le cluster, et
	// écrit dans la même transaction que la transition. C'est ce qui rend un run
	// orphelin impossible : si la création échoue ensuite, le job repasse
	// `queued` et le nom est réattribué au prochain essai (doc 06 §6.3).
	job.RunName = runName(job.Kind, job.ID)

	// L'exclusivité constatée n'est plus une barrière tenue mais une file à une
	// place : le type ne tolérait aucune exécution jumelle. Elle ne suffit pas à
	// rendre une mesure comparable — `concurrent_at_start` doit être nul, donc
	// rien d'autre ne tournait non plus — et c'est cette seconde condition qui
	// porte désormais l'essentiel de B16.
	exclusive := limits.Of(job.Kind) == 1

	_, err = transaction.Exec(ctx,
		`update jobs
		 set state = 'running', started_at = now(), queue_reason = null,
		     -- Conditions constatées, figées dans la même transaction que la
		     -- réservation : c'est le seul instant où elles existent.
		     exclusive_at_start = $3,
		     concurrent_at_start = $4,
		     run_name = $2, updated_at = now()
		 where id = $1`, job.ID, job.RunName, exclusive, job.ConcurrentAtStart)
	if err != nil {
		return job, fmt.Errorf("réservation du job %d: %w", job.ID, err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return job, fmt.Errorf("validation de la réservation du job %d: %w", job.ID, err)
	}

	job.State = JobRunning
	return job, nil
}

// MaxJobAttempts borne les reprises d'un job.
//
// Une reprise sert aux échecs transitoires — cluster momentanément indisponible,
// nœud saturé. Elle ne répare pas un job devenu insatisfiable : un paramètre que
// le catalogue exige et que le job ne porte pas ne se matérialisera jamais, parce
// que les paramètres sont figés à la mise en file et le catalogue évolue.
//
// Sans plafond, un tel job est repris indéfiniment. Constaté : deux cent trente
// essais en quelques minutes, et la file entière immobilisée derrière lui —
// l'ordonnanceur reprenait toujours le même, le plus ancien, avant de considérer
// les suivants.
const MaxJobAttempts = 5

// Requeue remet un job en file après un échec de création dans le cluster.
//
// Rend `false` quand le plafond est atteint : l'appelant doit alors conclure le
// job en échec plutôt que d'insister. Rendre l'information plutôt que conclure
// ici garde la décision là où le motif est connu — c'est l'ordonnanceur qui sait
// pourquoi la reprise a échoué, et c'est lui qui l'écrit.
func (db *DB) Requeue(ctx context.Context, id int64, reason string) (bool, error) {
	var attempt int
	err := db.pool.QueryRow(ctx,
		`update jobs
		 set state = 'queued', started_at = null, run_name = null,
		     attempt = attempt + 1, error = $2, updated_at = now()
		 where id = $1 and state = 'running'
		 returning attempt`, id, reason).Scan(&attempt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Le job n'était plus `running` : annulé entre-temps, ou déjà repris
			// par une autre passe. Rien à faire, et ce n'est pas une erreur.
			return false, nil
		}
		return false, fmt.Errorf("remise en file du job %d: %w", id, err)
	}
	return attempt < MaxJobAttempts, nil
}

// FinishJob conclut un job.
func (db *DB) FinishJob(ctx context.Context, id int64, state, reason string) error {
	_, err := db.pool.Exec(ctx,
		`update jobs
		 set state = $2, finished_at = now(), error = nullif($3, ''), updated_at = now()
		 where id = $1 and state not in ('succeeded', 'failed', 'cancelled')`,
		id, state, reason)
	if err != nil {
		return fmt.Errorf("conclusion du job %d: %w", id, err)
	}
	return nil
}

// CancelQueuedJob annule un job encore en file.
//
// Rend false si le job n'était plus en file : l'appelant doit alors passer par le
// cluster, ce qui n'est pas la même opération et ne peut pas être décidé ici.
func (db *DB) CancelQueuedJob(ctx context.Context, id int64, reason string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update jobs
		 set state = 'cancelled', finished_at = now(), cancel_reason = $2, updated_at = now()
		 where id = $1 and state = 'queued'`, id, reason)
	if err != nil {
		return false, fmt.Errorf("annulation du job %d: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// RunningJob décrit un job en cours, pour l'annulation et la réconciliation.
type RunningJob struct {
	ID      int64
	Kind    string
	RunName string
}

// GetJob rend un job.
func (db *DB) GetJob(ctx context.Context, id int64) (Job, error) {
	var job Job
	var params []byte
	var reason, failure, runName *string

	err := db.pool.QueryRow(ctx,
		`select id, kind, priority, state, params, requested_by,
		        requested_at, started_at, finished_at, attempt, queue_reason, error, run_name
		 from jobs where id = $1`, id).
		Scan(&job.ID, &job.Kind, &job.Priority, &job.State, &params,
			&job.RequestedBy, &job.RequestedAt, &job.StartedAt, &job.FinishedAt,
			&job.Attempt, &reason, &failure, &runName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return job, ErrJobNotFound
		}
		return job, fmt.Errorf("lecture du job %d: %w", id, err)
	}

	if len(params) > 0 {
		_ = json.Unmarshal(params, &job.Params)
	}
	job.QueueReason = value(reason)
	job.Error = value(failure)
	job.RunName = value(runName)
	return job, nil
}

// ListQueue rend les jobs actifs : en cours d'abord, puis en attente dans l'ordre
// réel de traitement.
//
// L'ordre affiché est celui de l'ordonnanceur, et pas un autre. Une file qui
// s'affiche dans un ordre différent de celui où elle sera servie est pire
// qu'absente : elle fait attendre le mauvais job.
func (db *DB) ListQueue(ctx context.Context) ([]Job, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, priority, state, params, requested_by,
		        requested_at, started_at, attempt, queue_reason, run_name
		 from jobs
		 where state in ('queued', 'running')
		 order by case state when 'running' then 0 else 1 end,
		          priority, requested_at`)
	if err != nil {
		return nil, fmt.Errorf("lecture de la file: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var job Job
		var params []byte
		var reason, runName *string

		if err := rows.Scan(&job.ID, &job.Kind, &job.Priority, &job.State,
			&params, &job.RequestedBy, &job.RequestedAt, &job.StartedAt, &job.Attempt,
			&reason, &runName); err != nil {
			return nil, fmt.Errorf("lecture de la file: %w", err)
		}
		if len(params) > 0 {
			_ = json.Unmarshal(params, &job.Params)
		}
		job.QueueReason = value(reason)
		job.RunName = value(runName)
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// ExplainQueue inscrit sur chaque job en attente la raison pour laquelle il
// n'avance pas.
//
// Calculé en base et non à l'affichage : la raison dépend de l'état global de la
// file à un instant donné, et deux clients qui la recalculeraient chacun de leur
// côté afficheraient des explications différentes du même blocage.
func (db *DB) ExplainQueue(ctx context.Context, limits Limits) error {
	const query = `
		with limits as (
		    select * from unnest($2::text[], $3::int[]) as l(kind, max_concurrent)
		),
		running as (
		    select kind from jobs where state = 'running'
		),
		capacity as (
		    select count(*) as total from running
		)
		update jobs j
		set queue_reason = case
		        -- L'ordre des branches est celui de la cause dominante : le
		        -- plafond global prime, parce qu'aucun réglage de file ne le
		        -- lèvera. Dire « file pleine » à un job retenu par le plafond
		        -- ferait chercher la place au mauvais endroit.
		        when capacity.total >= $1 then 'capacity'
		        when (select count(*) from running r where r.kind = j.kind)
		             >= coalesce((select l.max_concurrent from limits l where l.kind = j.kind), $4)
		             then 'kind-busy'
		        else null
		    end,
		    updated_at = now()
		from capacity
		where j.state = 'queued'`

	kinds, perKind := limits.arrays()
	if _, err := db.pool.Exec(ctx, query, limits.Global, kinds, perKind, limits.Default); err != nil {
		return fmt.Errorf("explication de la file: %w", err)
	}
	return nil
}

// QueueLimits rend les plafonds réglés à la main, par type.
//
// Seuls les réglages **modifiés** y figurent : recopier le catalogue en base
// figerait sa valeur, et changer un défaut dans le ConfigMap n'aurait plus
// d'effet sans que rien ne le dise.
func (db *DB) QueueLimits(ctx context.Context) (map[string]int, error) {
	rows, err := db.pool.Query(ctx, `select kind, max_concurrent from queue_limits`)
	if err != nil {
		return nil, fmt.Errorf("lecture des plafonds de file: %w", err)
	}
	defer rows.Close()

	limits := map[string]int{}
	for rows.Next() {
		var kind string
		var limit int
		if err := rows.Scan(&kind, &limit); err != nil {
			return nil, fmt.Errorf("lecture des plafonds de file: %w", err)
		}
		limits[kind] = limit
	}
	return limits, rows.Err()
}

// SetQueueLimit règle le plafond d'une file.
func (db *DB) SetQueueLimit(ctx context.Context, kind string, max int, by string) error {
	if max < 1 {
		return fmt.Errorf("plafond de %q: %d, attendu >= 1", kind, max)
	}
	_, err := db.pool.Exec(ctx,
		`insert into queue_limits (kind, max_concurrent, updated_by)
		 values ($1, $2, $3)
		 on conflict (kind) do update
		     set max_concurrent = excluded.max_concurrent,
		         updated_by = excluded.updated_by,
		         updated_at = now()`, kind, max, by)
	if err != nil {
		return fmt.Errorf("réglage du plafond de %q: %w", kind, err)
	}
	return nil
}

// ClearQueueLimit rend une file à son plafond de catalogue.
func (db *DB) ClearQueueLimit(ctx context.Context, kind string) error {
	if _, err := db.pool.Exec(ctx, `delete from queue_limits where kind = $1`, kind); err != nil {
		return fmt.Errorf("retrait du plafond de %q: %w", kind, err)
	}
	return nil
}

// QueueOccupancy compte les jobs actifs par type et par état.
//
// Compté en base et non à l'affichage : l'écran des files doit dire combien de
// places sont prises **maintenant**, et un décompte fait sur la liste paginée
// des jobs se tromperait dès qu'elle est tronquée.
func (db *DB) QueueOccupancy(ctx context.Context) (map[string]QueueCount, error) {
	rows, err := db.pool.Query(ctx,
		`select kind,
		        count(*) filter (where state = 'running') as running,
		        count(*) filter (where state = 'queued')  as queued
		 from jobs
		 where state in ('queued', 'running')
		 group by kind`)
	if err != nil {
		return nil, fmt.Errorf("occupation des files: %w", err)
	}
	defer rows.Close()

	counts := map[string]QueueCount{}
	for rows.Next() {
		var kind string
		var count QueueCount
		if err := rows.Scan(&kind, &count.Running, &count.Queued); err != nil {
			return nil, fmt.Errorf("occupation des files: %w", err)
		}
		counts[kind] = count
	}
	return counts, rows.Err()
}

// QueueCount est l'occupation d'une file.
type QueueCount struct {
	Running int
	Queued  int
}

// RunningJobs liste les jobs en cours, pour la réconciliation au démarrage.
func (db *DB) RunningJobs(ctx context.Context) ([]RunningJob, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, coalesce(run_name, '') from jobs where state = 'running' order by id`)
	if err != nil {
		return nil, fmt.Errorf("lecture des jobs en cours: %w", err)
	}
	defer rows.Close()

	var jobs []RunningJob
	for rows.Next() {
		var job RunningJob
		if err := rows.Scan(&job.ID, &job.Kind, &job.RunName); err != nil {
			return nil, fmt.Errorf("lecture des jobs en cours: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

// RequestCancel inscrit une demande d'annulation sur un job en cours.
//
// L'api ne parle pas à Kubernetes — son compte de service ne monte même pas de
// jeton (doc 07 §7.3). L'annulation d'un run en cours est donc enregistrée comme
// une intention, que l'ordonnanceur exécute au tour suivant. Le détour coûte deux
// secondes et évite de donner à l'`api` un accès au cluster pour une opération
// qu'elle fait rarement.
func (db *DB) RequestCancel(ctx context.Context, id int64, reason string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update jobs set cancel_reason = $2, updated_at = now()
		 where id = $1 and state = 'running' and cancel_reason is null`, id, reason)
	if err != nil {
		return false, fmt.Errorf("demande d'annulation du job %d: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// PendingCancellations liste les runs dont l'arrêt a été demandé.
func (db *DB) PendingCancellations(ctx context.Context) ([]RunningJob, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, run_name from jobs
		 where state = 'running' and cancel_reason is not null and run_name is not null
		 order by id`)
	if err != nil {
		return nil, fmt.Errorf("lecture des annulations demandées: %w", err)
	}
	defer rows.Close()

	var jobs []RunningJob
	for rows.Next() {
		var job RunningJob
		if err := rows.Scan(&job.ID, &job.Kind, &job.RunName); err != nil {
			return nil, fmt.Errorf("lecture des annulations demandées: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// InstanceID rend l'identité de cette base.
//
// Elle entre dans le nom des PipelineRun. Sans elle, les identifiants de jobs —
// qui repartent à 1 sur une base neuve — produisaient des noms déjà pris par des
// runs restés dans le cluster : la création se heurtait à un conflit, lu comme
// « c'était déjà fait », et aucun run ne partait. L'interface montrait alors
// l'échec d'une exécution vieille de plusieurs heures.
func (db *DB) InstanceID(ctx context.Context) (string, error) {
	var id string
	if err := db.pool.QueryRow(ctx, `select id from instance`).Scan(&id); err != nil {
		return "", fmt.Errorf("identité de la base: %w", err)
	}
	return id, nil
}
