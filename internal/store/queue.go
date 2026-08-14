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
	ReasonClassBusy = "class-busy"
	ReasonCapacity  = "capacity"
	ReasonDraining  = "draining"
)

// ClassBarrier doit rester aligné sur catalog.ClassBarrier. La duplication évite
// à `store` de dépendre du catalogue pour une constante.
const ClassBarrier = "exclusive-cluster"

// JobRequest décrit une demande de lancement.
type JobRequest struct {
	Kind        string
	QueueClass  string
	Priority    int
	Params      map[string]string
	RequestedBy string
	ReleaseID   *int64
	Environment *string
}

// Job est un job de la file.
type Job struct {
	ID          int64
	Kind        string
	QueueClass  string
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
		`insert into jobs (kind, queue_class, priority, state, params, requested_by, release_id, environment_id)
		 values ($1, $2, $3, 'queued', $4, $5, $6, $7)
		 returning id`,
		request.Kind, request.QueueClass, request.Priority, params,
		request.RequestedBy, request.ReleaseID, request.Environment).Scan(&id)
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
// L'ordre des conditions traduit trois règles :
//
//  1. le plafond global protège le cluster, quelles que soient les classes ;
//  2. la barrière ne démarre que **seule**, et une fois en tête de file elle gèle
//     tout — sans quoi elle serait indéfiniment doublée par des jobs plus courts
//     et n'obtiendrait jamais le cluster. Le gel ne commence qu'en tête : une
//     barrière déposée derrière vingt jobs attend son tour comme les autres ;
//  3. une classe exclusive déjà occupée est écartée, mais n'empêche pas les
//     autres : un audit de sécurité peut tourner pendant des tests E2E.
//
// Rend `ErrJobNotFound` quand rien n'est éligible — cas courant, pas une erreur.
func (db *DB) ClaimNext(ctx context.Context, maxConcurrent int, runName func(kind string, id int64) string) (Job, error) {
	var job Job

	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return job, fmt.Errorf("ouverture de transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	const query = `
		with running as (
		    select queue_class from jobs where state = 'running'
		),
		capacity as (
		    select count(*) as total from running
		),
		-- La barrière ne gèle la file que lorsqu'elle est **en tête**, et non dès
		-- qu'elle est mise en file. Bloquer sur sa seule présence figerait des
		-- jobs qui la précèdent légitimement : une barrière déposée derrière vingt
		-- jobs les arrêterait tous sur-le-champ, au lieu d'attendre son tour.
		head as (
		    select queue_class from jobs
		    where state = 'queued'
		    order by priority, requested_at
		    limit 1
		),
		barrier as (
		    select exists(select 1 from head where queue_class = $2) as leading
		)
		select j.id, j.kind, j.queue_class, j.priority, j.params, j.requested_by,
		       j.requested_at, j.attempt, j.release_id
		from jobs j, capacity, barrier
		where j.state = 'queued'
		  and capacity.total < $1
		  and (
		      case
		          -- La barrière démarre quand elle est en tête et le cluster vide.
		          when j.queue_class = $2 then barrier.leading and capacity.total = 0
		          -- Les autres s'effacent devant une barrière en tête : c'est le
		          -- drainage, qui laisse les runs en cours finir sans en lancer.
		          when barrier.leading then false
		          when j.queue_class = 'parallel' then true
		          else j.queue_class not in (select queue_class from running)
		      end
		  )
		order by j.priority, j.requested_at
		for update of j skip locked
		limit 1`

	var params []byte
	err = transaction.QueryRow(ctx, query, maxConcurrent, ClassBarrier).Scan(
		&job.ID, &job.Kind, &job.QueueClass, &job.Priority, &params,
		&job.RequestedBy, &job.RequestedAt, &job.Attempt, &job.ReleaseID)
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

	_, err = transaction.Exec(ctx,
		`update jobs
		 set state = 'running', started_at = now(), queue_reason = null,
		     run_name = $2, updated_at = now()
		 where id = $1`, job.ID, job.RunName)
	if err != nil {
		return job, fmt.Errorf("réservation du job %d: %w", job.ID, err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return job, fmt.Errorf("validation de la réservation du job %d: %w", job.ID, err)
	}

	job.State = JobRunning
	return job, nil
}

// Requeue remet un job en file après un échec de création dans le cluster.
func (db *DB) Requeue(ctx context.Context, id int64, reason string) error {
	_, err := db.pool.Exec(ctx,
		`update jobs
		 set state = 'queued', started_at = null, run_name = null,
		     attempt = attempt + 1, error = $2, updated_at = now()
		 where id = $1 and state = 'running'`, id, reason)
	if err != nil {
		return fmt.Errorf("remise en file du job %d: %w", id, err)
	}
	return nil
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
		`select id, kind, queue_class, priority, state, params, requested_by,
		        requested_at, started_at, finished_at, attempt, queue_reason, error, run_name
		 from jobs where id = $1`, id).
		Scan(&job.ID, &job.Kind, &job.QueueClass, &job.Priority, &job.State, &params,
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
		`select id, kind, queue_class, priority, state, params, requested_by,
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

		if err := rows.Scan(&job.ID, &job.Kind, &job.QueueClass, &job.Priority, &job.State,
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
func (db *DB) ExplainQueue(ctx context.Context, maxConcurrent int) error {
	const query = `
		with running as (
		    select queue_class from jobs where state = 'running'
		),
		capacity as (
		    select count(*) as total from running
		),
		head as (
		    select queue_class from jobs
		    where state = 'queued'
		    order by priority, requested_at
		    limit 1
		),
		barrier as (
		    select exists(select 1 from head where queue_class = $2) as leading
		)
		update jobs j
		set queue_reason = case
		        -- L'ordre des branches est celui de la cause dominante : un job
		        -- retenu par une barrière ne doit pas afficher « plafond atteint »,
		        -- qui ferait attendre la fin d'un run alors que c'est la barrière
		        -- qui décide.
		        when barrier.leading and j.queue_class <> $2 then 'draining'
		        when j.queue_class = $2 and capacity.total > 0 then 'draining'
		        when capacity.total >= $1 then 'capacity'
		        when j.queue_class <> 'parallel'
		             and j.queue_class in (select queue_class from running) then 'class-busy'
		        else null
		    end,
		    updated_at = now()
		from capacity, barrier
		where j.state = 'queued'`

	if _, err := db.pool.Exec(ctx, query, maxConcurrent, ClassBarrier); err != nil {
		return fmt.Errorf("explication de la file: %w", err)
	}
	return nil
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
