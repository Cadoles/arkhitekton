package store

import (
	"context"
	"testing"
)

func loadFixture(t *testing.T, db *DB) (int64, int64) {
	t.Helper()
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	jobID, err := db.Enqueue(ctx, JobRequest{
		Kind: "load-test", RequestedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("job: %v", err)
	}

	var runID int64
	err = db.pool.QueryRow(ctx,
		`insert into runs (job_id, pipeline_name, pipelinerun_name, namespace, status, started_at)
		 values ($1, 'load-test', $2, 'mse-tekton', 'succeeded', now()) returning id`,
		jobID, "load-test-1").Scan(&runID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return releaseID, runID
}

func yes() *bool { ok := true; return &ok }
func no() *bool  { ko := false; return &ko }

func TestSaveLoadResultKeepsTheMeasuredConditions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	resultID, err := db.SaveLoadResult(ctx,
		LoadResult{
			RunID: runID, ReleaseID: &releaseID, Scenario: "panier",
			Exclusive: true, ConcurrentJobs: 0, DurationMS: 60000, Summary: "60 s",
		},
		[]BenchMeasure{
			{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
				Value: 310.2, LowerBetter: true, ThresholdOK: yes()},
			{Metric: "http_reqs:count", Label: "Requêtes", Unit: "req", Value: 24000, Volume: true},
		})
	if err != nil {
		t.Fatalf("enregistrement: %v", err)
	}

	measures, err := db.ListResultMeasures(ctx, resultID)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(measures) != 2 {
		t.Fatalf("%d mesure(s)", len(measures))
	}
	// Les conditions accompagnent chaque mesure : sans elles, une mesure n'est
	// qu'un nombre.
	if !measures[0].Exclusive || measures[0].ConcurrentAt != 0 {
		t.Errorf("conditions = %+v", measures[0])
	}
	if measures[0].ThresholdOK == nil || !*measures[0].ThresholdOK {
		t.Errorf("seuil = %v", measures[0].ThresholdOK)
	}
	if !measures[1].Volume {
		t.Error("le drapeau de volume est perdu")
	}
}

// Le statut figé de la campagne suit les seuils déclarés dans k6 et non la
// baseline : une colonne figée qui dépendrait d'une référence mobile deviendrait
// fausse au premier ré-épinglage.
func TestLoadStatusFollowsTheDeclaredThresholds(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	resultID, err := db.SaveLoadResult(ctx,
		LoadResult{RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
		[]BenchMeasure{
			{Metric: "a:p(95)", Label: "A", Unit: "ms", Value: 1, LowerBetter: true, ThresholdOK: yes()},
			{Metric: "b:rate", Label: "B", Unit: "%", Value: 2, LowerBetter: true, ThresholdOK: no()},
		})
	if err != nil {
		t.Fatalf("enregistrement: %v", err)
	}

	var status string
	if err := db.pool.QueryRow(ctx, `select status from test_results where id = $1`, resultID).Scan(&status); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if status != "fail" {
		t.Errorf("statut = %q, attendu fail", status)
	}
}

// Une campagne sans seuil déclaré ne prononce aucun verdict sur elle-même.
func TestLoadStatusWithoutDeclaredThresholds(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	resultID, _ := db.SaveLoadResult(ctx,
		LoadResult{RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
		[]BenchMeasure{{Metric: "a:p(95)", Label: "A", Unit: "ms", Value: 1, LowerBetter: true}})

	var status string
	db.pool.QueryRow(ctx, `select status from test_results where id = $1`, resultID).Scan(&status)
	if status != "na" {
		t.Errorf("statut = %q, attendu na", status)
	}
}

func TestBaselinesArePinnedNotComputed(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	first, _ := db.SaveLoadResult(ctx,
		LoadResult{RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
		[]BenchMeasure{{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
			Value: 300, LowerBetter: true}})
	second, _ := db.SaveLoadResult(ctx,
		LoadResult{RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
		[]BenchMeasure{{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
			Value: 450, LowerBetter: true}})

	firstMeasures, _ := db.ListResultMeasures(ctx, first)
	secondMeasures, _ := db.ListResultMeasures(ctx, second)

	if err := db.PinBaseline(ctx, firstMeasures[0].ID, "vcarroy"); err != nil {
		t.Fatalf("épinglage: %v", err)
	}

	baselines, err := db.Baselines(ctx, "panier")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	// La référence est celle qui a été désignée, et une campagne plus récente ne
	// la déplace pas d'elle-même. C'est ce qui rend la dérive visible.
	if baselines["http_req_duration:p(95)"].Value != 300 {
		t.Errorf("baseline = %v, attendu 300", baselines["http_req_duration:p(95)"].Value)
	}

	// Ré-épingler est permis : une baseline vieillit.
	if err := db.PinBaseline(ctx, secondMeasures[0].ID, "vcarroy"); err != nil {
		t.Fatalf("ré-épinglage: %v", err)
	}
	baselines, _ = db.Baselines(ctx, "panier")
	if baselines["http_req_duration:p(95)"].Value != 450 {
		t.Errorf("baseline = %v, attendu 450", baselines["http_req_duration:p(95)"].Value)
	}
	if baselines["http_req_duration:p(95)"].PinnedBy != "vcarroy" {
		t.Error("le geste doit être tracé")
	}
}

func TestScenarioHistoryIsMostRecentFirst(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	for _, value := range []float64{300, 320, 290} {
		if _, err := db.SaveLoadResult(ctx,
			LoadResult{RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
			[]BenchMeasure{{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
				Value: value, LowerBetter: true}}); err != nil {
			t.Fatalf("enregistrement: %v", err)
		}
	}

	history, err := db.ListScenarioMeasures(ctx, "panier", "http_req_duration:p(95)", 6)
	if err != nil {
		t.Fatalf("historique: %v", err)
	}
	if len(history) != 3 || history[0].Value != 290 {
		t.Errorf("historique = %+v", history)
	}
}

// Relevées à la réservation : minutes plus tard, plus rien ne dit ce qui
// tournait au moment de la mesure.
func TestClaimRecordsTheStartConditions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.Enqueue(ctx, JobRequest{
		Kind: "load-test", RequestedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("mise en file: %v", err)
	}

	// Une file à une place : c'est ce qui rend la mesure exclusive, la barrière
	// du modèle précédent ayant disparu.
	limits := Limits{PerKind: map[string]int{"load-test": 1}, Default: 5, Global: 4}
	job, err := db.ClaimNext(ctx, limits, func(kind string, id int64) string { return "run-1" })
	if err != nil {
		t.Fatalf("réservation: %v", err)
	}

	exclusive, concurrent, err := db.StartConditions(ctx, job.ID)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if !exclusive || concurrent != 0 {
		t.Errorf("conditions = exclusive %v, concurrents %d", exclusive, concurrent)
	}
}

// Un job ordinaire n'est pas exclusif, quoi qu'il arrive : c'est la classe qui en
// décide, pas la chance d'avoir trouvé le cluster vide.
func TestAnOrdinaryJobIsNeverMarkedExclusive(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.Enqueue(ctx, JobRequest{
		Kind: "static-tests", RequestedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("mise en file: %v", err)
	}

	job, _ := db.ClaimNext(ctx, caps(4, nil), func(kind string, id int64) string { return "run-2" })
	exclusive, _, err := db.StartConditions(ctx, job.ID)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if exclusive {
		t.Error("un job hors barrière ne doit jamais être marqué exclusif")
	}
}

// Non relevées : traitées comme non exclusives. Se tromper dans ce sens écarte
// une mesure valide ; dans l'autre, il ferait entrer une mesure faussée dans la
// comparaison.
func TestUnrecordedConditionsDefaultToNonExclusive(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id, err := db.Enqueue(ctx, JobRequest{Kind: "x", RequestedBy: "v"})
	if err != nil {
		t.Fatalf("mise en file: %v", err)
	}

	exclusive, concurrent, err := db.StartConditions(ctx, id)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if exclusive || concurrent != 0 {
		t.Errorf("conditions = %v / %d", exclusive, concurrent)
	}
}
