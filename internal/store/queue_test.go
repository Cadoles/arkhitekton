package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

/*
 * La file, une par type de job.
 *
 * Le modèle précédent groupait les jobs en classes d'exclusion : `parallel`
 * n'excluait rien, `exclusive-<nom>` excluait sa propre classe,
 * `exclusive-cluster` excluait tout. Trois régimes pour une seule question —
 * combien de ces exécutions peuvent tourner ensemble — et une réponse qu'il
 * fallait reconstituer en cherchant quels types partageaient une classe.
 *
 * Ces tests décrivent le modèle qui l'a remplacé : un plafond par type, un
 * plafond global au-dessus, rien d'autre.
 */

func naming(kind string, id int64) string { return fmt.Sprintf("%s-run-%d", kind, id) }

func enqueue(t *testing.T, db *DB, kind string, priority int) int64 {
	t.Helper()

	id, err := db.Enqueue(context.Background(), JobRequest{
		Kind:        kind,
		Priority:    priority,
		Params:      map[string]string{"revision": "abc"},
		RequestedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("mise en file de %s: %v", kind, err)
	}
	return id
}

// caps compose des plafonds : ceux nommés, un défaut large pour les autres.
func caps(global int, perKind map[string]int) Limits {
	if perKind == nil {
		perKind = map[string]int{}
	}
	return Limits{PerKind: perKind, Default: 5, Global: global}
}

func claim(t *testing.T, db *DB, limits Limits) (Job, error) {
	t.Helper()
	return db.ClaimNext(context.Background(), limits, naming)
}

func TestClaimServesByPriorityThenArrival(t *testing.T) {
	db := migrated(t)

	enqueue(t, db, "build", 100)
	urgent := enqueue(t, db, "promote", 10)
	enqueue(t, db, "audit", 100)

	job, err := claim(t, db, caps(4, nil))
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

// Une file pleine n'écarte que son type. C'est la règle centrale du modèle : un
// audit de sécurité doit pouvoir tourner pendant des tests E2E, deux campagnes
// E2E non.
func TestUneFilePleineNecartequeSonType(t *testing.T) {
	db := migrated(t)

	limits := caps(4, map[string]int{"e2e": 1})

	enqueue(t, db, "e2e", 100)
	enqueue(t, db, "e2e", 100)
	enqueue(t, db, "audit", 100)

	first, err := claim(t, db, limits)
	if err != nil {
		t.Fatalf("premier: %v", err)
	}
	if first.Kind != "e2e" {
		t.Fatalf("premier job = %q", first.Kind)
	}

	second, err := claim(t, db, limits)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// Le second E2E est sauté, l'audit passe. Un job dont la file est pleine ne
	// doit pas bloquer ceux qui le suivent — sans quoi une intégration en cours
	// retiendrait tous les tests unitaires derrière elle.
	if second.Kind != "audit" {
		t.Errorf("second job = %q, attendu audit: une file pleine bloque au-delà d'elle-même", second.Kind)
	}

	if _, err := claim(t, db, limits); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("le second E2E ne devait pas être servi: %v", err)
	}
}

// Un plafond supérieur à un laisse plusieurs exécutions du même type, et s'arrête
// à sa valeur. C'est ce que le modèle précédent ne savait pas exprimer : une
// classe `parallel` n'avait pour limite que le plafond global.
func TestUneFileSertJusquASonPlafond(t *testing.T) {
	db := migrated(t)

	limits := caps(10, map[string]int{"unit-tests": 2})

	for range 3 {
		enqueue(t, db, "unit-tests", 100)
	}

	for i := range 2 {
		if _, err := claim(t, db, limits); err != nil {
			t.Fatalf("réservation %d: %v", i, err)
		}
	}

	if _, err := claim(t, db, limits); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("le plafond de 2 n'a pas été respecté: %v", err)
	}
}

// Un type absent des plafonds — une tâche locale, un type retiré du catalogue
// depuis la mise en file — prend le défaut. Le faire attendre un réglage qui
// n'existera jamais l'immobiliserait pour toujours.
func TestUnTypeInconnuPrendLeDefaut(t *testing.T) {
	db := migrated(t)

	limits := Limits{PerKind: map[string]int{"build": 5}, Default: 1, Global: 10}

	enqueue(t, db, "delivery-note", 100)
	enqueue(t, db, "delivery-note", 100)

	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("premier: %v", err)
	}
	// Défaut à 1 : le second attend, ce qui prouve que le défaut s'applique
	// bien plutôt qu'une absence de limite.
	if _, err := claim(t, db, limits); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("le défaut n'a pas été appliqué au type inconnu: %v", err)
	}
}

// Le plafond global prime : il protège le cluster, et aucune file ne peut le
// dépasser même si la sienne a de la place.
func TestLePlafondGlobalPrimeSurLesFiles(t *testing.T) {
	db := migrated(t)

	limits := caps(2, map[string]int{"build": 5, "audit": 5})

	enqueue(t, db, "build", 100)
	enqueue(t, db, "build", 100)
	enqueue(t, db, "audit", 100)

	for i := range 2 {
		if _, err := claim(t, db, limits); err != nil {
			t.Fatalf("réservation %d: %v", i, err)
		}
	}

	if _, err := claim(t, db, limits); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("le plafond global de 2 n'a pas été respecté: %v", err)
	}
}

// L'exclusivité constatée n'est plus une barrière tenue mais une file à une
// place. Elle est relevée à la réservation parce que c'est le seul instant où
// elle existe, et sert à juger la comparabilité d'une mesure de charge (B16).
func TestUneFileAUnePlaceEstConstateeExclusive(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	limits := caps(10, map[string]int{"load-test": 1, "build": 5})

	seul := enqueue(t, db, "load-test", 100)
	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("réservation: %v", err)
	}

	exclusive, concurrent, err := db.StartConditions(ctx, seul)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if !exclusive {
		t.Error("une file à une place doit être constatée exclusive")
	}
	// Rien ne tournait : la mesure est comparable. C'est cette seconde condition
	// qui porte désormais l'essentiel de B16, la barrière ayant disparu.
	if concurrent != 0 {
		t.Errorf("%d exécution(s) concurrente(s), attendu 0", concurrent)
	}

	partage := enqueue(t, db, "build", 100)
	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("seconde réservation: %v", err)
	}
	exclusive, concurrent, err = db.StartConditions(ctx, partage)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if exclusive {
		t.Error("une file à cinq places n'est pas exclusive")
	}
	if concurrent != 1 {
		t.Errorf("%d exécution(s) concurrente(s), attendu 1", concurrent)
	}
}

// Le nom du run est décidé avant la création dans le cluster. S'il échoue, le job
// doit repartir en file avec un essai de plus — et sans conserver un nom qui ne
// désigne rien.
func TestRequeueClearsTheRunNameAndCountsTheAttempt(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", 100)
	if _, err := claim(t, db, caps(4, nil)); err != nil {
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
func TestExplainQueueNommeLaFilePleine(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	limits := caps(4, map[string]int{"e2e": 1})

	enqueue(t, db, "e2e", 100)
	blocked := enqueue(t, db, "e2e", 100)
	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("réservation: %v", err)
	}

	if err := db.ExplainQueue(ctx, limits); err != nil {
		t.Fatalf("explication: %v", err)
	}

	job, err := db.GetJob(ctx, blocked)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.QueueReason != ReasonKindBusy {
		t.Errorf("motif = %q, attendu %q", job.QueueReason, ReasonKindBusy)
	}
}

// Le plafond global prime dans l'explication comme dans le service : dire « file
// pleine » à un job retenu par le plafond ferait chercher la place au mauvais
// endroit, et régler la file n'y changerait rien.
func TestExplainQueuePrefereLePlafondGlobal(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	limits := caps(1, map[string]int{"audit": 1, "build": 5})

	enqueue(t, db, "audit", 100)
	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("réservation: %v", err)
	}
	blocked := enqueue(t, db, "build", 100)

	if err := db.ExplainQueue(ctx, limits); err != nil {
		t.Fatalf("explication: %v", err)
	}

	job, err := db.GetJob(ctx, blocked)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.QueueReason != ReasonCapacity {
		t.Errorf("motif = %q, attendu %q", job.QueueReason, ReasonCapacity)
	}
}

// Un réglage remplace le plafond du catalogue, et son retrait le rend.
func TestUnReglageRemplaceEtSeRetire(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.SetQueueLimit(ctx, "unit-tests", 2, "vcarroy"); err != nil {
		t.Fatalf("réglage: %v", err)
	}
	// Le même type réglé deux fois ne crée pas deux lignes : c'est un réglage,
	// pas un journal.
	if err := db.SetQueueLimit(ctx, "unit-tests", 3, "vcarroy"); err != nil {
		t.Fatalf("second réglage: %v", err)
	}

	limits, err := db.QueueLimits(ctx)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(limits) != 1 || limits["unit-tests"] != 3 {
		t.Errorf("réglages = %v, attendu unit-tests à 3", limits)
	}

	// Un plafond nul ou négatif est refusé : il immobiliserait la file pour
	// toujours, sans que rien ne le dise.
	if err := db.SetQueueLimit(ctx, "unit-tests", 0, "vcarroy"); err == nil {
		t.Error("un plafond nul devrait être refusé")
	}

	if err := db.ClearQueueLimit(ctx, "unit-tests"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	limits, _ = db.QueueLimits(ctx)
	if len(limits) != 0 {
		t.Errorf("réglages après retrait = %v", limits)
	}
}

// L'occupation est comptée en base : un décompte fait sur une liste tronquée se
// tromperait, et c'est le nombre de places prises qui décide de l'attente.
func TestLoccupationEstCompteeParType(t *testing.T) {
	db := migrated(t)

	limits := caps(10, map[string]int{"build": 1})

	enqueue(t, db, "build", 100)
	enqueue(t, db, "build", 100)
	enqueue(t, db, "audit", 100)

	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("réservation: %v", err)
	}

	counts, err := db.QueueOccupancy(context.Background())
	if err != nil {
		t.Fatalf("occupation: %v", err)
	}
	if counts["build"].Running != 1 || counts["build"].Queued != 1 {
		t.Errorf("build = %+v, attendu 1 en cours et 1 en attente", counts["build"])
	}
	if counts["audit"].Running != 0 || counts["audit"].Queued != 1 {
		t.Errorf("audit = %+v, attendu 1 en attente", counts["audit"])
	}
}

func TestCancelQueuedJobOnlyTouchesQueuedOnes(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", 100)

	cancelled, err := db.CancelQueuedJob(ctx, id, "demande utilisateur")
	if err != nil {
		t.Fatalf("annulation: %v", err)
	}
	if !cancelled {
		t.Fatal("un job en file doit pouvoir être annulé")
	}

	running := enqueue(t, db, "audit", 100)
	if _, err := claim(t, db, caps(4, nil)); err != nil {
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

	enqueue(t, db, "build", 100)
	enqueue(t, db, "promote", 10)
	if _, err := claim(t, db, caps(4, nil)); err != nil {
		t.Fatalf("réservation: %v", err)
	}
	enqueue(t, db, "audit", 50)

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

	id := enqueue(t, db, "build", 100)
	if _, err := claim(t, db, caps(4, nil)); err != nil {
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

// Un job insatisfiable ne doit pas être repris indéfiniment : il immobiliserait la
// file, l'ordonnanceur reprenant toujours le plus ancien avant de considérer les
// suivants. Constaté en conditions réelles à deux cent trente essais.
func TestUneRepriseSarreteAuPlafond(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", 100)

	// `attempt` compte la tentative en cours, pas les reprises : un job enfilé
	// est déjà à un essai. Le plafond borne donc le total, et la boucle doit
	// s'arrêter d'elle-même.
	reprises := 0
	for reprises < 50 {
		if _, err := claim(t, db, caps(4, nil)); err != nil {
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

	id := enqueue(t, db, "build", 100)

	again, err := db.Requeue(ctx, id, "jamais démarré")
	if err != nil {
		t.Fatalf("remise en file: %v", err)
	}
	if again {
		t.Error("aucune reprise ne devrait être annoncée sur un job non courant")
	}
}
