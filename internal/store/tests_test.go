package store

import (
	"context"
	"fmt"
	"testing"
)

func testRun(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	var runID int64
	err := db.pool.QueryRow(context.Background(),
		`insert into runs (pipeline_name, pipelinerun_name, namespace, status, started_at)
		 values ('php-static-tests', $1, 'mse-tekton', 'succeeded', now()) returning id`,
		name).Scan(&runID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return runID
}

func saveCases(t *testing.T, db *DB, runID int64, releaseID int64, family string, cases []TestCase) {
	t.Helper()
	failed := 0
	for _, testCase := range cases {
		if testCase.Status == "failed" {
			failed++
		}
	}
	if _, err := db.SaveTestResult(context.Background(), TestResult{
		RunID: runID, ReleaseID: &releaseID, Family: family, Tool: "phpunit",
		Status: "pass", Total: len(cases), Failed: failed,
	}, cases); err != nil {
		t.Fatalf("enregistrement: %v", err)
	}
}

func releaseFor(t *testing.T, db *DB, tag string) int64 {
	t.Helper()
	ctx := context.Background()
	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	id, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: tag, CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release %s: %v", tag, err)
	}
	return id
}

// La vue qui répond à la question réellement posée en recette : qu'est-ce qui a
// changé ? Un compteur stable peut cacher un échec résolu et un échec apparu.
func TestDiffFamilySeesTheFourKindsOfChange(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	previous := releaseFor(t, db, "1.8.0-rc1")
	current := releaseFor(t, db, "1.8.0-rc2")

	saveCases(t, db, testRun(t, db, "run-prev"), previous, "unit", []TestCase{
		{Suite: "A", Name: "stable", Status: "passed"},
		{Suite: "A", Name: "corrige", Status: "failed"},
		{Suite: "A", Name: "casse", Status: "passed"},
		{Suite: "A", Name: "supprime", Status: "passed"},
	})
	saveCases(t, db, testRun(t, db, "run-curr"), current, "unit", []TestCase{
		{Suite: "A", Name: "stable", Status: "passed"},
		{Suite: "A", Name: "corrige", Status: "passed"},
		{Suite: "A", Name: "casse", Status: "failed"},
		{Suite: "A", Name: "ajoute", Status: "passed"},
	})

	diffs, err := db.DiffFamily(ctx, current, previous, "unit")
	if err != nil {
		t.Fatalf("comparaison: %v", err)
	}

	changes := map[string]string{}
	for _, diff := range diffs {
		changes[diff.Name] = diff.Change
	}

	expected := map[string]string{
		"stable":   ChangeUnchanged,
		"corrige":  ChangeFixed,
		"casse":    ChangeNewFailure,
		"ajoute":   ChangeAdded,
		"supprime": ChangeRemoved,
	}
	for name, change := range expected {
		if changes[name] != change {
			t.Errorf("%q = %q, attendu %q", name, changes[name], change)
		}
	}
}

// Un test instable est un échec du point de vue de la comparaison : passer de
// stable à instable est une régression, et la taire ferait disparaître le seul
// signal qui existe dessus.
func TestFlakyCountsAsAFailureInTheDiff(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	previous := releaseFor(t, db, "1.8.0-rc1")
	current := releaseFor(t, db, "1.8.0-rc2")

	saveCases(t, db, testRun(t, db, "p"), previous, "e2e",
		[]TestCase{{Suite: "A", Name: "panier", Status: "passed"}})
	saveCases(t, db, testRun(t, db, "c"), current, "e2e",
		[]TestCase{{Suite: "A", Name: "panier", Status: "flaky"}})

	diffs, err := db.DiffFamily(ctx, current, previous, "e2e")
	if err != nil {
		t.Fatalf("comparaison: %v", err)
	}
	if len(diffs) != 1 || diffs[0].Change != ChangeNewFailure {
		t.Errorf("comparaison = %+v", diffs)
	}
}

// Une release peut être testée plusieurs fois : c'est la dernière exécution qui
// la décrit.
func TestTheMostRecentReportWins(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	previous := releaseFor(t, db, "1.8.0-rc1")
	current := releaseFor(t, db, "1.8.0-rc2")

	saveCases(t, db, testRun(t, db, "p"), previous, "unit",
		[]TestCase{{Suite: "A", Name: "t", Status: "failed"}})
	saveCases(t, db, testRun(t, db, "c1"), current, "unit",
		[]TestCase{{Suite: "A", Name: "t", Status: "failed"}})
	saveCases(t, db, testRun(t, db, "c2"), current, "unit",
		[]TestCase{{Suite: "A", Name: "t", Status: "passed"}})

	diffs, err := db.DiffFamily(ctx, current, previous, "unit")
	if err != nil {
		t.Fatalf("comparaison: %v", err)
	}
	if len(diffs) != 1 || diffs[0].Change != ChangeFixed {
		t.Errorf("comparaison = %+v", diffs)
	}
}

func TestSaveTestResultIsIdempotentPerTool(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "unit",
		[]TestCase{{Suite: "A", Name: "t", Status: "passed"}})

	exists, err := db.TestResultExists(ctx, runID, "phpunit")
	if err != nil {
		t.Fatalf("contrôle: %v", err)
	}
	if !exists {
		t.Error("le rapport doit être reconnu comme déjà enregistré")
	}

	if other, _ := db.TestResultExists(ctx, runID, "phpstan"); other {
		t.Error("un autre outil ne doit pas être confondu")
	}
}

// Une RC se compare à la RC qui la précède, sans quoi rc2 serait confrontée à
// une release livrée des semaines plus tôt et tout apparaîtrait changé.
func TestPreviousReleaseIsTheOneJustBefore(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	first := releaseFor(t, db, "1.8.0-rc1")
	second := releaseFor(t, db, "1.8.0-rc2")
	third := releaseFor(t, db, "1.8.0-rc3")

	previous, err := db.PreviousRelease(ctx, third)
	if err != nil {
		t.Fatalf("précédente: %v", err)
	}
	if previous != second {
		t.Errorf("précédente = %d, attendu %d", previous, second)
	}

	// La première release n'a rien à quoi se comparer.
	if _, err := db.PreviousRelease(ctx, first); err == nil {
		t.Error("la première release ne doit pas avoir de précédente")
	}
}

// Le rejeu part de `raw_object` : un run dont l'objet source n'est pas
// enregistré n'est pas rejouable, et le proposer donnerait un rejeu qui efface
// sans reconstruire.
func TestReprocessTargetsRequireARawObject(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	withRaw := testRun(t, db, "avec-brut")
	withoutRaw := testRun(t, db, "sans-brut")

	if _, err := db.SaveTestResult(ctx, TestResult{
		RunID: withRaw, ReleaseID: &releaseID, Family: "unit", Tool: "phpunit",
		Status: "pass", RawObject: "raw/avec-brut/junit.xml",
	}, nil); err != nil {
		t.Fatalf("avec brut: %v", err)
	}
	if _, err := db.SaveTestResult(ctx, TestResult{
		RunID: withoutRaw, ReleaseID: &releaseID, Family: "unit", Tool: "phpunit",
		Status: "pass",
	}, nil); err != nil {
		t.Fatalf("sans brut: %v", err)
	}

	targets, err := db.ListReprocessTargets(ctx, 0)
	if err != nil {
		t.Fatalf("cibles: %v", err)
	}
	if len(targets) != 1 || targets[0].RunName != "avec-brut" {
		t.Errorf("cibles = %+v", targets)
	}
	// Les outils déjà projetés sont rendus : savoir ce qui disparaît avant de le
	// reconstruire est le seul moyen de constater qu'un parseur corrigé produit
	// désormais moins.
	if len(targets[0].Tools) != 1 || targets[0].Tools[0] != "phpunit" {
		t.Errorf("outils = %v", targets[0].Tools)
	}
}

func TestReprocessTargetsCanBeScopedToARelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	first := releaseFor(t, db, "1.8.0-rc1")
	second := releaseFor(t, db, "1.8.0-rc2")

	for _, pair := range []struct {
		release int64
		run     string
	}{{first, "run-1"}, {second, "run-2"}} {
		if _, err := db.SaveTestResult(ctx, TestResult{
			RunID: testRun(t, db, pair.run), ReleaseID: &pair.release,
			Family: "unit", Tool: "phpunit", Status: "pass",
			RawObject: "raw/" + pair.run + "/junit.xml",
		}, nil); err != nil {
			t.Fatalf("%s: %v", pair.run, err)
		}
	}

	targets, err := db.ListReprocessTargets(ctx, second)
	if err != nil {
		t.Fatalf("cibles: %v", err)
	}
	if len(targets) != 1 || targets[0].RunName != "run-2" {
		t.Errorf("cibles = %+v", targets)
	}
}

// Les cas partent avec leur rapport : ils n'ont aucun sens détachés de ce qui
// les a produits.
func TestDeleteTestResultsCascadesToCases(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "unit", []TestCase{
		{Suite: "A", Name: "a", Status: "passed"},
		{Suite: "A", Name: "b", Status: "failed"},
	})

	deleted, err := db.DeleteTestResults(ctx, runID)
	if err != nil {
		t.Fatalf("effacement: %v", err)
	}
	if deleted != 1 {
		t.Errorf("%d rapport(s) effacé(s)", deleted)
	}

	var remaining int
	if err := db.pool.QueryRow(ctx, `select count(*) from test_cases`).Scan(&remaining); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d cas subsistent après effacement du rapport", remaining)
	}
}

// reportOf rend l'identifiant du rapport d'une famille, seule entrée de
// ListTestCases.
func reportOf(t *testing.T, db *DB, releaseID int64, family string) int64 {
	t.Helper()
	reports, err := db.ListReleaseTestResults(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("rapports: %v", err)
	}
	for _, report := range reports {
		if report.Family == family {
			return report.ID
		}
	}
	t.Fatalf("aucun rapport pour la famille %s", family)
	return 0
}

// manyCases fabrique n cas nommés de façon à être triables et reconnaissables.
func manyCases(n int) []TestCase {
	cases := make([]TestCase, 0, n)
	for i := 0; i < n; i++ {
		cases = append(cases, TestCase{
			Suite:  "Suite",
			Name:   fmt.Sprintf("cas-%04d", i),
			Status: "passed",
		})
	}
	return cases
}

// Le total porte sur l'ensemble, la page sur ce qu'on demande. Sans cela
// l'interface ne peut pas savoir qu'une page suivante existe.
func TestListTestCasesRendUnePageEtLeTotal(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "e2e", manyCases(1200))
	reportID := reportOf(t, db, releaseID, "e2e")

	page, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500})
	if err != nil {
		t.Fatalf("première page: %v", err)
	}
	if total != 1200 {
		t.Errorf("total %d, attendu 1200", total)
	}
	if len(page) != 500 {
		t.Fatalf("%d cas dans la page, attendu 500", len(page))
	}
	if page[0].Name != "cas-0000" {
		t.Errorf("premier cas %q", page[0].Name)
	}
}

// Les pages ne doivent ni se recouvrir ni laisser de trou : c'est la propriété
// qui rend la pagination utilisable, et celle qu'un tri non total casse.
func TestLesPagesCouvrentToutSansRecouvrement(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "e2e", manyCases(1200))
	reportID := reportOf(t, db, releaseID, "e2e")

	seen := map[string]int{}
	for offset := 0; offset < 1200; offset += 500 {
		page, _, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Offset: offset})
		if err != nil {
			t.Fatalf("page à l'offset %d: %v", offset, err)
		}
		for _, testCase := range page {
			seen[testCase.Name]++
		}
	}

	if len(seen) != 1200 {
		t.Errorf("%d cas distincts vus, attendu 1200", len(seen))
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("cas %s vu %d fois", name, count)
		}
	}
}

// Un offset au-delà du total rend une page vide mais conserve le total : c'est ce
// qui permet à l'interface de revenir en arrière au lieu d'afficher « aucun cas ».
func TestUnOffsetTropLoinConserveLeTotal(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "e2e", manyCases(10))
	reportID := reportOf(t, db, releaseID, "e2e")

	page, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Offset: 5000})
	if err != nil {
		t.Fatalf("page hors bornes: %v", err)
	}
	if len(page) != 0 {
		t.Errorf("%d cas rendus au-delà du total", len(page))
	}
	if total != 10 {
		t.Errorf("total %d, attendu 10", total)
	}
}

// Le filtre porte sur l'ensemble et non sur la page, et le total suit. C'est la
// raison d'être du filtre côté serveur : filtrer la page transférée répondrait
// « aucun cas » pour un test présent deux pages plus loin.
func TestLeFiltrePorteSurToutesLesPages(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	cases := manyCases(1200)
	// Un cas reconnaissable, volontairement placé hors de la première page.
	cases[900].Name = "connexion-oidc"
	saveCases(t, db, runID, releaseID, "e2e", cases)
	reportID := reportOf(t, db, releaseID, "e2e")

	page, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Query: "oidc"})
	if err != nil {
		t.Fatalf("filtre: %v", err)
	}
	if total != 1 {
		t.Fatalf("total filtré %d, attendu 1", total)
	}
	if len(page) != 1 || page[0].Name != "connexion-oidc" {
		t.Errorf("page filtrée %+v", page)
	}
}

// Le filtre joint la suite et le nom : l'utilisateur tape un fragment sans savoir
// auquel des deux il appartient.
func TestLeFiltreVoitLaSuiteEtLeNom(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "e2e", []TestCase{
		{Suite: "Panier", Name: "ajoute un article", Status: "passed"},
		{Suite: "Connexion", Name: "refuse un mot de passe vide", Status: "failed"},
	})
	reportID := reportOf(t, db, releaseID, "e2e")

	bySuite, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Query: "panier"})
	if err != nil {
		t.Fatalf("filtre par suite: %v", err)
	}
	if total != 1 || len(bySuite) != 1 {
		t.Errorf("filtre par suite: %d cas, total %d", len(bySuite), total)
	}

	byName, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Query: "mot de passe"})
	if err != nil {
		t.Fatalf("filtre par nom: %v", err)
	}
	if total != 1 || len(byName) != 1 {
		t.Errorf("filtre par nom: %d cas, total %d", len(byName), total)
	}
}
