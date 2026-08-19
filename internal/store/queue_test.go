package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func naming(kind string, id int64) string { return fmt.Sprintf("%s-run-%d", kind, id) }

func enqueue(t *testing.T, db *DB, kind, class string, priority int) int64 {
	t.Helper()

	id, err := db.Enqueue(context.Background(), JobRequest{
		Kind:        kind,
		QueueClass:  class,
		Priority:    priority,
		Params:      map[string]string{"revision": "abc"},
		RequestedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("mise en file de %s: %v", kind, err)
	}
	return id
}

func claim(t *testing.T, db *DB, max int) (Job, error) {
	t.Helper()
	return db.ClaimNext(context.Background(), max, naming)
}

func TestClaimServesByPriorityThenArrival(t *testing.T) {
	db := migrated(t)

	enqueue(t, db, "build", "parallel", 100)
	urgent := enqueue(t, db, "promote", "parallel", 10)
	enqueue(t, db, "audit", "parallel", 100)

	job, err := claim(t, db, 4)
	if err != nil {
		t.Fatalf("réservation: %v", err)
	}
	if job.ID != urgent {
		t.Errorf("job réservé = %d, attendu le plus prioritaire %d", job.ID, urgent)
	}
	if job.RunName != naming("promote", urgent) {
		t.Errorf("nom de run = %q", job.RunName)
	}
	if job.State != JobRunning {
		t.Errorf("état = %q", job.State)
	}
}

// L'exclusion est **par classe** et non globale : un audit de sécurité doit
// pouvoir tourner pendant des tests E2E, deux campagnes E2E non.
func TestExclusiveClassBlocksOnlyItsOwnClass(t *testing.T) {
	db := migrated(t)

	enqueue(t, db, "e2e", "exclusive-e2e", 100)
	enqueue(t, db, "e2e-bis", "exclusive-e2e", 100)
	enqueue(t, db, "audit", "parallel", 100)

	first, err := claim(t, db, 4)
	if err != nil {
		t.Fatalf("premier: %v", err)
	}
	if first.Kind != "e2e" {
		t.Fatalf("premier job = %q", first.Kind)
	}

	second, err := claim(t, db, 4)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// Le second E2E est écarté, l'audit passe.
	if second.Kind != "audit" {
		t.Errorf("second job = %q, attendu audit: la classe occupée bloque au-delà d'elle-même", second.Kind)
	}

	if _, err := claim(t, db, 4); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("le second E2E ne devait pas être servi: %v", err)
	}
}

// La barrière est le mécanisme le plus délicat de la file : elle doit obtenir le
// cluster pour elle seule, sans être indéfiniment doublée par des jobs plus
// courts.
func TestBarrierDrainsThenRunsAlone(t *testing.T) {
	db := migrated(t)

	// L'ordre de dépôt compte : le build précède la barrière, il doit donc
	// pouvoir démarrer avant elle.
	enqueue(t, db, "audit", "parallel", 100)
	enqueue(t, db, "build", "parallel", 100)
	barrier := enqueue(t, db, "load", ClassBarrier, 100)

	// L'audit démarre : la barrière n'est pas encore en tête.
	running, err := claim(t, db, 4)
	if err != nil {
		t.Fatalf("premier: %v", err)
	}
	if running.Kind != "audit" {
		t.Fatalf("premier job = %q", running.Kind)
	}

	// La barrière n'est pas en tête : le build la précède et démarre normalement.
	// Bloquer dès la mise en file arrêterait des jobs qui la précèdent
	// légitimement — une barrière déposée derrière vingt jobs les figerait tous.
	build, err := claim(t, db, 4)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if build.Kind != "build" {
		t.Fatalf("second job = %q", build.Kind)
	}

	// Le cluster se vide. La barrière devient alors la tête de file.
	ctx := context.Background()
	if err := db.FinishJob(ctx, running.ID, JobSucceeded, ""); err != nil {
		t.Fatalf("fin de l'audit: %v", err)
	}
	if err := db.FinishJob(ctx, build.ID, JobSucceeded, ""); err != nil {
		t.Fatalf("fin du build: %v", err)
	}

	next, err := claim(t, db, 4)
	if err != nil {
		t.Fatalf("après drainage: %v", err)
	}
	if next.ID != barrier {
		t.Errorf("job servi = %d, attendu la barrière %d", next.ID, barrier)
	}

	// Et elle tourne seule : rien d'autre ne démarre à côté.
	if _, err := claim(t, db, 4); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("un job a démarré à côté de la barrière: %v", err)
	}
}

func TestConcurrencyCapIsEnforced(t *testing.T) {
	db := migrated(t)

	for i := range 4 {
		enqueue(t, db, fmt.Sprintf("build-%d", i), "parallel", 100)
	}

	for i := range 2 {
		if _, err := claim(t, db, 2); err != nil {
			t.Fatalf("réservation %d: %v", i, err)
		}
	}

	if _, err := claim(t, db, 2); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("le plafond de 2 n'a pas été respecté: %v", err)
	}
}

// Le nom du run est décidé avant la création dans le cluster. S'il échoue, le job
// doit repartir en file avec un essai de plus — et sans conserver un nom qui ne
// désigne rien.
func TestRequeueClearsTheRunNameAndCountsTheAttempt(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", "parallel", 100)
	if _, err := claim(t, db, 4); err != nil {
		t.Fatalf("réservation: %v", err)
	}

	again, err := db.Requeue(ctx, id, "création refusée par le cluster")
	if err != nil {
		t.Fatalf("remise en file: %v", err)
	}
	if !again {
		t.Error("une première reprise doit être permise")
	}

	job, err := db.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.State != JobQueued {
		t.Errorf("état = %q, attendu queued", job.State)
	}
	if job.Attempt != 2 {
		t.Errorf("essais = %d, attendu 2", job.Attempt)
	}
	if job.RunName != "" {
		t.Errorf("nom de run conservé après échec: %q", job.RunName)
	}
}

// Une file qui n'explique pas son attente engendre des relances inutiles.
func TestExplainQueueNamesTheDominantCause(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	enqueue(t, db, "e2e", "exclusive-e2e", 100)
	blocked := enqueue(t, db, "e2e-bis", "exclusive-e2e", 100)
	if _, err := claim(t, db, 4); err != nil {
		t.Fatalf("réservation: %v", err)
	}

	if err := db.ExplainQueue(ctx, 4); err != nil {
		t.Fatalf("explication: %v", err)
	}

	job, err := db.GetJob(ctx, blocked)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.QueueReason != ReasonClassBusy {
		t.Errorf("motif = %q, attendu %q", job.QueueReason, ReasonClassBusy)
	}
}

// Un job retenu par une barrière ne doit pas afficher « plafond atteint » : cela
// ferait attendre la fin d'un run alors que c'est la barrière qui décide.
func TestExplainQueuePrefersDrainingOverCapacity(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	enqueue(t, db, "audit", "parallel", 100)
	if _, err := claim(t, db, 1); err != nil {
		t.Fatalf("réservation: %v", err)
	}
	enqueue(t, db, "load", ClassBarrier, 100)
	blocked := enqueue(t, db, "build", "parallel", 100)

	// Plafond atteint **et** barrière en attente : la barrière doit l'emporter.
	if err := db.ExplainQueue(ctx, 1); err != nil {
		t.Fatalf("explication: %v", err)
	}

	job, err := db.GetJob(ctx, blocked)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.QueueReason != ReasonDraining {
		t.Errorf("motif = %q, attendu %q", job.QueueReason, ReasonDraining)
	}
}

func TestCancelQueuedJobOnlyTouchesQueuedOnes(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", "parallel", 100)

	cancelled, err := db.CancelQueuedJob(ctx, id, "demande utilisateur")
	if err != nil {
		t.Fatalf("annulation: %v", err)
	}
	if !cancelled {
		t.Fatal("un job en file doit pouvoir être annulé")
	}

	running := enqueue(t, db, "audit", "parallel", 100)
	if _, err := claim(t, db, 4); err != nil {
		t.Fatalf("réservation: %v", err)
	}

	// Un job en cours ne s'annule pas en base : il faut passer par le cluster,
	// et l'appelant doit pouvoir le distinguer.
	cancelled, err = db.CancelQueuedJob(ctx, running, "demande utilisateur")
	if err != nil {
		t.Fatalf("annulation: %v", err)
	}
	if cancelled {
		t.Error("un job en cours ne doit pas être annulé par cette voie")
	}
}

// L'ordre affiché doit être celui du service : une file qui s'affiche autrement
// fait attendre le mauvais job.
func TestListQueueShowsRunningFirstThenServiceOrder(t *testing.T) {
	db := migrated(t)

	enqueue(t, db, "build", "parallel", 100)
	enqueue(t, db, "promote", "parallel", 10)
	if _, err := claim(t, db, 4); err != nil {
		t.Fatalf("réservation: %v", err)
	}
	enqueue(t, db, "audit", "parallel", 50)

	jobs, err := db.ListQueue(context.Background())
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("%d job(s), attendu 3", len(jobs))
	}

	if jobs[0].State != JobRunning || jobs[0].Kind != "promote" {
		t.Errorf("premier = %s/%s, attendu le job en cours", jobs[0].Kind, jobs[0].State)
	}
	if jobs[1].Kind != "audit" || jobs[2].Kind != "build" {
		t.Errorf("ordre d'attente = %s puis %s, attendu audit puis build", jobs[1].Kind, jobs[2].Kind)
	}
}

func TestFinishJobIsIdempotentOnTerminalJobs(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", "parallel", 100)
	if _, err := claim(t, db, 4); err != nil {
		t.Fatalf("réservation: %v", err)
	}

	if err := db.FinishJob(ctx, id, JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	// Un second événement d'informer ne doit pas réécrire l'issue.
	if err := db.FinishJob(ctx, id, JobFailed, "trop tard"); err != nil {
		t.Fatalf("seconde conclusion: %v", err)
	}

	job, err := db.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.State != JobSucceeded {
		t.Errorf("état = %q, attendu succeeded: une issue terminale a été réécrite", job.State)
	}
}

// Le drainage proprement dit : une fois la barrière en tête, plus aucun job ne
// démarre, y compris un job parallèle largement sous le plafond.
func TestLeadingBarrierFreezesTheQueue(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	enqueue(t, db, "audit", "parallel", 100)
	running, err := claim(t, db, 4)
	if err != nil {
		t.Fatalf("réservation: %v", err)
	}

	// La barrière passe en tête grâce à sa priorité, devant un build déposé avant.
	enqueue(t, db, "build", "parallel", 100)
	enqueue(t, db, "load", ClassBarrier, 10)

	if _, err := claim(t, db, 4); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("un job a démarré pendant le drainage: %v", err)
	}

	if err := db.ExplainQueue(ctx, 4); err != nil {
		t.Fatalf("explication: %v", err)
	}
	jobs, err := db.ListQueue(ctx)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	for _, job := range jobs {
		if job.State != JobQueued {
			continue
		}
		if job.QueueReason != ReasonDraining {
			t.Errorf("job %s: motif = %q, attendu %q", job.Kind, job.QueueReason, ReasonDraining)
		}
	}

	if err := db.FinishJob(ctx, running.ID, JobSucceeded, ""); err != nil {
		t.Fatalf("fin: %v", err)
	}
	next, err := claim(t, db, 4)
	if err != nil {
		t.Fatalf("après drainage: %v", err)
	}
	if next.QueueClass != ClassBarrier {
		t.Errorf("job servi = %q, attendu la barrière", next.Kind)
	}
}

// Un job insatisfiable ne doit pas être repris indéfiniment : il immobiliserait la
// file, l'ordonnanceur reprenant toujours le plus ancien avant de considérer les
// suivants. Constaté en conditions réelles à deux cent trente essais.
func TestUneRepriseSarreteAuPlafond(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", "parallel", 100)

	// `attempt` compte la tentative en cours, pas les reprises : un job enfilé
	// est déjà à un essai. Le plafond borne donc le total, et la boucle doit
	// s'arrêter d'elle-même.
	reprises := 0
	for reprises < 50 {
		if _, err := claim(t, db, 4); err != nil {
			t.Fatalf("réservation: %v", err)
		}
		again, err := db.Requeue(ctx, id, "cause qui ne disparaîtra pas")
		if err != nil {
			t.Fatalf("reprise %d: %v", reprises, err)
		}
		reprises++
		if !again {
			break
		}
	}

	if reprises >= 50 {
		t.Fatal("la reprise ne s'arrête jamais: la file resterait immobilisée")
	}

	job, err := db.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.Attempt != MaxJobAttempts {
		t.Errorf("%d tentatives au total, attendu le plafond %d", job.Attempt, MaxJobAttempts)
	}
}

// Un job qui n'est plus `running` — annulé entre-temps — n'est pas une erreur de
// reprise : il n'y a simplement plus rien à reprendre.
func TestUneRepriseSurUnJobNonCourantNestPasUneErreur(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", "parallel", 100)

	again, err := db.Requeue(ctx, id, "jamais démarré")
	if err != nil {
		t.Fatalf("remise en file: %v", err)
	}
	if again {
		t.Error("aucune reprise ne devrait être annoncée sur un job non courant")
	}
}
