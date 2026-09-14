package http

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Le score, geste par geste.
 *
 * Ce que ces tests tiennent est le **changement d'axe** : deux gestes de même
 * nature comptent séparément, et chacun pèse ce qu'il déclare. Le modèle par
 * signal ne savait dire ni l'un ni l'autre.
 */

// scoreDeTest réalise ce dont le score a besoin, et rien d'autre.
type scoreDeTest struct {
	run     store.PlanRun
	sansRun bool
	// gestes rattache un résultat à la position du geste qui l'a produit, comme
	// le ferait la chaîne résultat → run → job → geste.
	gestes map[int64]store.GesteRef
}

func (s *scoreDeTest) PlanRunByEvent(_ context.Context, _, _ string) (store.PlanRun, error) {
	if s.sansRun {
		return store.PlanRun{}, store.ErrPlanRunNotFound
	}
	return s.run, nil
}

func (s *scoreDeTest) ResultGestes(context.Context, int64) (map[int64]store.GesteRef, error) {
	return s.gestes, nil
}

func (s *scoreDeTest) LatestSecurityAudit(context.Context, int64) (store.SecurityAudit, error) {
	return store.SecurityAudit{}, store.ErrAuditNotFound
}

func (s *scoreDeTest) ListReleaseLoadResults(context.Context, int64) ([]store.LoadResult, error) {
	return nil, nil
}

func (s *scoreDeTest) ListResultMeasures(context.Context, int64) ([]store.BenchMeasure, error) {
	return nil, nil
}

func (s *scoreDeTest) Baselines(context.Context, string) (map[string]store.Baseline, error) {
	return nil, nil
}

// planDeVerification rend un plan à une étape de vérification.
func planDeVerification(gestes ...plan.Geste) store.PlanRun {
	return store.PlanRun{
		Trigger: plan.ReleaseTrigger, EventKey: event.ReleaseCreatedKey(7),
		Plan: plan.Plan{Stages: []plan.Stage{
			{Role: plan.RoleBuild, Gestes: []plan.Geste{
				{Kind: plan.GestePipeline, Ref: "build", Label: "portal"},
			}},
			{Role: plan.RoleTest, Gestes: gestes},
		}},
	}
}

func rapport(id int64, family string, total, failed int) store.TestResult {
	return store.TestResult{
		ID: id, Family: family, Tool: family, Status: "passed",
		Total: total, Passed: total - failed, Failed: failed,
	}
}

func poidsDe(value float64) *float64 { return &value }

// Deux campagnes de même nature comptent séparément, et pèsent chacune la sienne.
//
// C'est le cas que le modèle par signal ne savait pas exprimer : les deux se
// confondaient dans un composant unique, et aucune pondération ne pouvait les
// séparer (doc 15 §15.6).
func TestDeuxGestesDeMemeNatureComptentSeparement(t *testing.T) {
	source := &scoreDeTest{
		run: planDeVerification(
			plan.Geste{Kind: plan.GestePipeline, Ref: "e2e-tests",
				Label: "E2E portail", Weight: poidsDe(90)},
			plan.Geste{Kind: plan.GestePipeline, Ref: "e2e-tests",
				Label: "E2E back-office", Weight: poidsDe(10)},
		),
		// Le premier rapport vient du geste 1.0, le second du geste 1.1.
		gestes: map[int64]store.GesteRef{
			1: {Stage: 1, Position: 0},
			2: {Stage: 1, Position: 1},
		},
	}

	// Le portail passe, le back-office échoue entièrement.
	reports := []store.TestResult{
		rapport(1, "e2e", 10, 0),
		rapport(2, "e2e", 10, 10),
	}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if len(breakdown.Signals) != 2 {
		t.Fatalf("%d composant(s), attendu deux gestes", len(breakdown.Signals))
	}
	if breakdown.Signals[0].Label != "E2E portail" ||
		breakdown.Signals[1].Label != "E2E back-office" {
		t.Errorf("libellés = %q / %q — l'ordre du plan n'est pas conservé",
			breakdown.Signals[0].Label, breakdown.Signals[1].Label)
	}
	// 90 % à 100 et 10 % à 0. Un axe par signal aurait cumulé les deux rapports
	// en un seul — 10 échecs sur 20 — et rendu 50.
	if breakdown.Score != 90 {
		t.Errorf("score = %d, attendu 90", breakdown.Score)
	}
}

// Un geste qui ne déclare rien pèse ce que sa nature pesait (B11).
//
// Personne ne perd la pondération convenue en ne saisissant rien.
func TestUnGesteSansPoidsPeseLaPonderationDorigine(t *testing.T) {
	source := &scoreDeTest{
		run: planDeVerification(
			plan.Geste{Kind: plan.GestePipeline, Ref: "unit-tests", Label: "Tests unitaires"},
			plan.Geste{Kind: plan.GestePipeline, Ref: "e2e-tests", Label: "Tests E2E"},
		),
		gestes: map[int64]store.GesteRef{
			1: {Stage: 1, Position: 0},
			2: {Stage: 1, Position: 1},
		},
	}
	reports := []store.TestResult{rapport(1, "unit", 10, 0), rapport(2, "e2e", 10, 0)}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}

	attendus := map[string]float64{"Tests unitaires": 25, "Tests E2E": 20}
	for _, composant := range breakdown.Signals {
		if attendu, connu := attendus[composant.Label]; connu && composant.Declared != attendu {
			t.Errorf("%s: poids déclaré = %v, attendu %v (pondération d'origine)",
				composant.Label, composant.Declared, attendu)
		}
	}
}

// Un geste qui n'a rien produit reste affiché, et son poids se lit.
//
// Le catalogue dit ce qu'il aurait mesuré : sans lui, la case disparaîtrait au
// lieu de dire qu'elle attend.
func TestUnGesteSansResultatResteAfficheAvecSonPoids(t *testing.T) {
	registry := &catalog.Catalog{Pipelines: []catalog.Pipeline{{
		Kind: "unit-tests", PipelineRef: "unit",
		Produces: []catalog.Product{catalog.ProductResults},
		Uploads: map[string]catalog.Upload{
			"results": {Param: "results-upload-url", Artifact: "unit.xml", Family: "unit"},
		},
	}}}

	source := &scoreDeTest{
		run: planDeVerification(plan.Geste{
			Kind: plan.GestePipeline, Ref: "unit-tests", Label: "Tests unitaires",
		}),
		gestes: map[int64]store.GesteRef{},
	}

	breakdown, err := computeScore(context.Background(), source, 7, nil, registry)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if len(breakdown.Signals) != 1 {
		t.Fatalf("%d composant(s)", len(breakdown.Signals))
	}
	composant := breakdown.Signals[0]
	if composant.Measured {
		t.Error("un geste sans résultat se déclare mesuré")
	}
	// Le poids vient du signal, que seul le catalogue pouvait nommer ici.
	if composant.Declared != 25 {
		t.Errorf("poids déclaré = %v, attendu 25 — le catalogue n'a pas nommé sa nature",
			composant.Declared)
	}
	if len(breakdown.Redistributed) != 1 {
		t.Errorf("redistribués = %v", breakdown.Redistributed)
	}
}

// Un poids nul retire le geste du score, et le dit.
func TestUnGesteAPoidsNulNeNotePas(t *testing.T) {
	source := &scoreDeTest{
		run: planDeVerification(
			plan.Geste{Kind: plan.GestePipeline, Ref: "unit-tests",
				Label: "Tests unitaires", Weight: poidsDe(100)},
			plan.Geste{Kind: plan.GestePipeline, Ref: "e2e-tests",
				Label: "E2E indicatif", Weight: poidsDe(0)},
		),
		gestes: map[int64]store.GesteRef{
			1: {Stage: 1, Position: 0},
			2: {Stage: 1, Position: 1},
		},
	}
	// Les E2E échouent entièrement, et ne doivent pas peser.
	reports := []store.TestResult{rapport(1, "unit", 10, 0), rapport(2, "e2e", 10, 10)}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if breakdown.Score != 100 {
		t.Errorf("score = %d, attendu 100 — le geste à poids nul a pesé", breakdown.Score)
	}
	if len(breakdown.Signals) != 2 {
		t.Errorf("%d composant(s) — celui à poids nul a disparu de l'écran",
			len(breakdown.Signals))
	}
}

// Une release sans plan garde son score, calculé par famille.
//
// Les releases antérieures au plan d'une release n'en ont pas. Perdre leur score
// serait pire que le calculer sur un axe qui n'existe plus.
func TestUneReleaseSansPlanGardeSonScoreParFamille(t *testing.T) {
	source := &scoreDeTest{sansRun: true}
	reports := []store.TestResult{rapport(1, "unit", 10, 0)}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if !breakdown.Measured || breakdown.Score != 100 {
		t.Errorf("score = %d (mesuré: %v)", breakdown.Score, breakdown.Measured)
	}
	// Six composants, un par nature, et leurs clés le disent.
	if len(breakdown.Signals) != 6 {
		t.Fatalf("%d composant(s), attendu les six natures", len(breakdown.Signals))
	}
	if breakdown.Signals[0].Key != results.SignalKey(results.SignalUnit) {
		t.Errorf("clé = %q, attendue celle d'un composant par famille",
			breakdown.Signals[0].Key)
	}
}

// Un résultat qu'aucun geste n'a produit n'entre pas au score.
//
// Il vient d'un lancement manuel depuis la file, qu'aucun plan n'a causé. Le
// compter ferait noter une release sur une exécution que son automatisation n'a
// pas demandée.
func TestUnResultatHorsPlanNentrePasAuScore(t *testing.T) {
	source := &scoreDeTest{
		run: planDeVerification(plan.Geste{
			Kind: plan.GestePipeline, Ref: "unit-tests",
			Label: "Tests unitaires", Weight: poidsDe(100),
		}),
		gestes: map[int64]store.GesteRef{1: {Stage: 1, Position: 0}},
	}
	reports := []store.TestResult{
		rapport(1, "unit", 10, 0),
		// Relancé à la main, et catastrophique. Il ne doit pas peser.
		rapport(2, "unit", 10, 10),
	}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if breakdown.Score != 100 {
		t.Errorf("score = %d — un résultat hors plan a pesé", breakdown.Score)
	}
}
