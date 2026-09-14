package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/bench"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// TestStore est ce que les handlers de résultats attendent de la base.
type TestStore interface {
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	// Les deux façons de nommer un projet : par le chemin de la requête, ou à
	// défaut par celui qui se construit. Voir internal/http/project.go.
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	BuildableProject(ctx context.Context) (store.Project, error)
	ListReleaseTestResults(ctx context.Context, releaseID int64) ([]store.TestResult, error)
	ListTestCases(ctx context.Context, resultID int64, page store.CasePage) ([]store.TestCase, int, error)
	DiffFamily(ctx context.Context, releaseID, previousID int64, family string) ([]store.CaseDiff, error)
	PreviousRelease(ctx context.Context, releaseID int64) (int64, error)
	LatestSecurityAudit(ctx context.Context, releaseID int64) (store.SecurityAudit, error)
	ListSecurityFindings(ctx context.Context, resultID int64, includeSuppressed bool) ([]store.SecurityFinding, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
}

// TestsHandler expose les résultats de tests et le score de qualité.
type TestsHandler struct {
	store TestStore
	// catalog sert à nommer la famille d'un geste qui n'a **rien** produit : sans
	// lui, un pipeline qui n'a pas tourné n'aurait ni signal ni poids par défaut,
	// et sa case disparaîtrait au lieu de dire qu'elle attend.
	catalog *catalog.Catalog
}

// NewTestsHandler assemble le handler.
//
// La pondération n'est plus reçue ici : elle se déclare sur les gestes de
// l'automatisation, et le plan figé d'une release la porte (doc 15 §15.6). Ce qui
// reste de la pondération du catalogue est le **défaut par signal**, dans
// `results.DefaultWeights`.
func NewTestsHandler(tests TestStore, registry *catalog.Catalog) *TestsHandler {
	return &TestsHandler{store: tests, catalog: registry}
}

type testResultPayload struct {
	ID         int64     `json:"id"`
	Family     string    `json:"family"`
	Tool       string    `json:"tool"`
	Status     string    `json:"status"`
	Total      int       `json:"total"`
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	Flaky      int       `json:"flaky"`
	DurationMS int64     `json:"duration_ms"`
	Summary    string    `json:"summary,omitempty"`
	MeasuredAt time.Time `json:"measured_at"`
}

type signalPayload struct {
	// Key identifie le composant : le geste qui a mesuré — `geste:1.0` — ou la
	// famille, pour une release antérieure aux plans. C'est elle qui rend l'écran
	// dynamique : une case par composant rendu, et non six cases écrites d'avance.
	Key string `json:"key"`
	// Label le nomme, dans les mots de qui l'a déclaré.
	Label string `json:"label"`
	// Signal dit la **nature** de ce qui est mesuré, et sert encore à choisir une
	// icône. Il a cessé d'être l'identité : deux gestes peuvent le partager.
	Signal string `json:"signal"`
	// Ref est la famille de pipeline à relancer pour remesurer ce composant.
	// Vide quand rien ne le remesure depuis le dashboard.
	Ref string `json:"ref,omitempty"`
	// Family est la famille de rapports à ouvrir en détail.
	Family string  `json:"family,omitempty"`
	Value  float64 `json:"value"`
	// Weight est le poids **effectif**, après redistribution des signaux non
	// mesurés. C'est lui qui explique la contribution réelle au score.
	Weight float64 `json:"weight"`
	// Declared est le poids saisi, avant redistribution. Sans lui, une pondération
	// ne se relit pas : « 33 » ne dit pas si quelqu'un a écrit 20 ou 40.
	Declared float64 `json:"declared"`
	Measured bool    `json:"measured"`
	Detail   string  `json:"detail,omitempty"`
	// Health est le verdict du signal — `healthy`, `warn`, `critique` ou `na`.
	// Rendu par le serveur pour que l'interface ne dérive aucun seuil (B11).
	Health string `json:"health"`
}

// Tests rend les rapports d'une release et le score qu'ils composent.
func (h *TestsHandler) Tests(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	reports, err := h.store.ListReleaseTestResults(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des résultats impossible", "")
		return
	}

	payload := make([]testResultPayload, 0, len(reports))
	for _, report := range reports {
		payload = append(payload, testResultPayload{
			ID: report.ID, Family: report.Family, Tool: report.Tool, Status: report.Status,
			Total: report.Total, Passed: report.Passed, Failed: report.Failed,
			Skipped: report.Skipped, Flaky: report.Flaky, DurationMS: report.DurationMS,
			Summary: report.Summary, MeasuredAt: report.CreatedAt,
		})
	}

	breakdown, err := h.score(r.Context(), release.ID, reports)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Calcul du score impossible", "")
		return
	}

	signals := make([]signalPayload, 0, len(breakdown.Signals))
	for _, signal := range breakdown.Signals {
		signals = append(signals, signalPayload{
			Key: signal.Key, Label: signal.Label,
			Signal: string(signal.Signal), Ref: signal.Ref, Family: string(signal.Family),
			Value: signal.Value, Weight: signal.Weight,
			Declared: signal.Declared, Measured: signal.Measured, Detail: signal.Detail,
			// Le verdict d'un signal vient du serveur comme celui du score : le
			// dériver côté client en ferait un second avis, divergent au premier
			// changement de seuil (B11).
			Health: results.SignalHealth(signal),
		})
	}

	// Déjà des chaînes : ce sont les clés des composants sortis du calcul.
	redistributed := breakdown.Redistributed
	if redistributed == nil {
		redistributed = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results": payload,
		"score": map[string]any{
			"value":    breakdown.Score,
			"measured": breakdown.Measured,
			"health":   results.Health(breakdown),
			"signals":  signals,
			// Le lecteur doit savoir sur quoi porte le score qu'il lit : un 100
			// sur trois signaux n'est pas un 100 sur six.
			"redistributed": redistributed,
		},
	})
}

// severity ordonne les statuts de rapport, du plus clément au plus sévère.
var severity = map[string]int{"pass": 0, "warn": 1, "fail": 2}

// worstStatus rend le plus sévère de deux statuts.
//
// Un statut inconnu est traité comme le plus sévère : mieux vaut signaler à tort
// qu'absoudre en silence un verdict qu'on ne sait pas lire.
func worstStatus(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	rankA, knownA := severity[a]
	rankB, knownB := severity[b]
	switch {
	case !knownA:
		return a
	case !knownB:
		return b
	case rankB > rankA:
		return b
	default:
		return a
	}
}

// score compose le score d'une release à partir de ce qui a été mesuré.
//
// Chaque signal est cherché explicitement plutôt que dérivé d'une boucle sur les
// rapports : un signal absent doit produire une mesure « non exécutée », et une
// boucle sur ce qui existe ne produirait rien du tout — donc un score calculé sur
// un sous-ensemble silencieux.
func (h *TestsHandler) score(
	ctx context.Context, releaseID int64, reports []store.TestResult,
) (results.Breakdown, error) {
	return computeScore(ctx, h.store, releaseID, reports, h.catalog)
}

// scoreStore est ce dont le calcul du score a besoin.
//
// Une interface plutôt qu'un type concret parce que deux gestionnaires
// l'appellent : celui des tests, pour l'écran d'une release, et celui des
// releases, pour la pastille de la liste. Le même chiffre doit sortir des deux,
// et deux implémentations auraient divergé.
type scoreStore interface {
	LatestSecurityAudit(ctx context.Context, releaseID int64) (store.SecurityAudit, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
}

func computeScore(
	ctx context.Context, source scoreStore, releaseID int64,
	reports []store.TestResult, registry *catalog.Catalog,
) (results.Breakdown, error) {
	// Les gestes d'abord : c'est l'axe voulu, et le seul qui sache séparer deux
	// campagnes de même nature.
	measures, err := gesteMeasures(ctx, source, releaseID, reports, registry)
	if err != nil {
		return results.Breakdown{}, err
	}

	if measures == nil {
		// La release n'a pas de plan. Elle est antérieure à doc 15 §15.2, ou ses
		// tests ont été lancés à la main depuis la file. Son score se calcule par
		// **famille**, comme avant — le perdre serait pire que le calculer sur un
		// axe qui n'existe plus.
		if measures, err = familyMeasures(ctx, source, releaseID, reports); err != nil {
			return results.Breakdown{}, err
		}
	}

	return results.Score(measures), nil
}

// familyMeasures rend un composant par famille, pour une release sans plan.
//
// Chaque famille est cherchée explicitement plutôt que dérivée d'une boucle sur
// les rapports : une famille absente doit produire une mesure « non exécutée », et
// une boucle sur ce qui existe ne produirait rien du tout — donc un score calculé
// sur un sous-ensemble silencieux.
func familyMeasures(
	ctx context.Context, source scoreStore, releaseID int64, reports []store.TestResult,
) ([]results.Measure, error) {
	// Le dernier rapport de chaque **outil**, puis la somme par famille.
	//
	// Retenir un seul rapport par famille perdait les familles à plusieurs
	// outils. `static` en compte deux — PHPStan et PHP-CS-Fixer — et seul le plus
	// récent des deux entrait au score : une release avec quarante erreurs
	// PHPStan et un cs-fixer propre affichait 100 dès lors que cs-fixer avait
	// conclu en dernier.
	byFamily := map[string]results.Report{}
	seen := map[string]bool{}
	seenTool := map[string]bool{}

	for _, report := range reports {
		key := report.Family + "/" + report.Tool
		if seenTool[key] {
			continue
		}
		seenTool[key] = true
		seen[report.Family] = true

		combined := byFamily[report.Family]
		combined.Total += report.Total
		combined.Passed += report.Passed
		combined.Failed += report.Failed
		combined.Skipped += report.Skipped
		combined.Flaky += report.Flaky
		combined.Status = worstStatus(combined.Status, report.Status)
		byFamily[report.Family] = combined
	}

	// Les violations d'analyse statique se comptent en cas, pas en tests réussis :
	// la règle de score n'est pas la même.
	staticViolations, staticRan := 0, seen[string(results.FamilyStatic)]
	if staticRan {
		staticViolations = byFamily[string(results.FamilyStatic)].Failed
	}

	brutes := []results.Measure{
		results.FromReport(results.SignalUnit,
			byFamily[string(results.FamilyUnit)], seen[string(results.FamilyUnit)]),
		results.FromReport(results.SignalIntegration,
			byFamily[string(results.FamilyIntegration)], seen[string(results.FamilyIntegration)]),
		results.FromReport(results.SignalE2E,
			byFamily[string(results.FamilyE2E)], seen[string(results.FamilyE2E)]),
		results.FromStatic(staticViolations, staticRan),
	}

	security, err := securityMeasure(ctx, source, releaseID)
	if err != nil {
		return nil, err
	}
	brutes = append(brutes, security)

	performance, err := benchMeasure(ctx, source, releaseID)
	if err != nil {
		return nil, err
	}
	brutes = append(brutes, performance)

	// L'identité et le poids, que les fonctions de mesure ne posent pas : elles
	// disent ce qui a été mesuré, pas ce que cela vaut dans le score.
	measures := make([]results.Measure, 0, len(brutes))
	for _, measure := range brutes {
		measure.Key = results.SignalKey(measure.Signal)
		measure.Label = signalLabels[measure.Signal]
		measure.Weight = results.WeightOf(nil, measure.Signal)
		// Le pipeline et la famille du repli, tirés du signal : ici et nulle part
		// ailleurs, parce que c'est le seul chemin où l'on n'a pas de geste pour
		// les dire.
		measure.Ref = signalKinds[measure.Signal]
		measure.Family = signalFamilies[measure.Signal]
		measures = append(measures, measure)
	}
	return measures, nil
}

// signalLabels nomme les composants du repli par famille, en français.
//
// Écrits ici parce qu'ils n'ont pas de déclaration : ce sont des natures, pas des
// gestes. Un geste, lui, porte le libellé que quelqu'un lui a donné.
// signalKinds et signalFamilies complètent le repli par famille.
//
// La famille de pipeline à relancer, et celle des rapports à ouvrir. Vides pour
// la performance, dont les campagnes viennent d'un runner extérieur que le
// dashboard ne déclenche pas (B16).
var signalKinds = map[results.Signal]string{
	results.SignalUnit:        "unit-tests",
	results.SignalIntegration: "integration-tests",
	results.SignalE2E:         "e2e-tests",
	results.SignalCode:        "static-tests",
	results.SignalSecurity:    "security-audit",
}

var signalFamilies = map[results.Signal]results.Family{
	results.SignalUnit:        results.FamilyUnit,
	results.SignalIntegration: results.FamilyIntegration,
	results.SignalE2E:         results.FamilyE2E,
	results.SignalCode:        results.FamilyStatic,
	results.SignalSecurity:    results.FamilySecurity,
	results.SignalPerformance: results.FamilyLoad,
}

var signalLabels = map[results.Signal]string{
	results.SignalUnit:        "Tests unitaires",
	results.SignalIntegration: "Tests d'intégration",
	results.SignalE2E:         "Tests E2E",
	results.SignalCode:        "Qualité du code",
	results.SignalSecurity:    "Sécurité",
	results.SignalPerformance: "Performance",
}

// securityMeasure rend le signal de sécurité du dernier audit.
//
// L'audit décide lui-même s'il est mesurable : un scanner en échec produit zéro
// finding, exactement comme un audit propre, et seul son descripteur les
// distingue. Le compter comme conforme serait l'erreur la plus chère possible
// sur ce signal (P2, B11).
func securityMeasure(
	ctx context.Context, source scoreStore, releaseID int64,
) (results.Measure, error) {
	audit, err := source.LatestSecurityAudit(ctx, releaseID)
	if err != nil {
		if errors.Is(err, store.ErrAuditNotFound) {
			return results.FromSecurity(0, 0, false), nil
		}
		return results.Measure{}, err
	}

	if !audit.Measured() {
		return results.Measure{Signal: results.SignalSecurity,
			Detail: "audit incomplet : " + strings.Join(audit.FailedScanners, ", ") + " n'a pas conclu"}, nil
	}

	// Les compteurs viennent de `summary.json`, qui exclut déjà les findings
	// supprimés par exception. Une exception est une décision humaine datée : la
	// respecter ici est ce qui rend le score actionnable plutôt que subi.
	return results.FromSecurity(audit.Critical, audit.High, true), nil
}

// benchMeasure rend le signal de performance de la campagne la plus récente.
func benchMeasure(
	ctx context.Context, source scoreStore, releaseID int64,
) (results.Measure, error) {
	campaigns, err := source.ListReleaseLoadResults(ctx, releaseID)
	if err != nil {
		return results.Measure{}, err
	}
	if len(campaigns) == 0 {
		return results.FromBench("", false), nil
	}

	campaign := campaigns[0]
	conditions := bench.Conditions{
		Exclusive: campaign.Exclusive, ConcurrentJobs: campaign.ConcurrentJobs,
	}
	if !conditions.Comparable() {
		return results.FromBench("", false), nil
	}

	measures, err := source.ListResultMeasures(ctx, campaign.ID)
	if err != nil {
		return results.Measure{}, err
	}
	baselines, err := source.Baselines(ctx, campaign.Scenario)
	if err != nil {
		return results.Measure{}, err
	}

	summary := bench.Summary{Scenario: campaign.Scenario}
	values := map[string]float64{}
	for _, measure := range measures {
		summary.Measures = append(summary.Measures, bench.Measure{
			Metric: measure.Metric, Value: measure.Value,
			LowerBetter: measure.LowerBetter, Volume: measure.Volume,
		})
	}
	for metric, baseline := range baselines {
		values[metric] = baseline.Value
	}

	verdict := bench.Worst(bench.Compare(summary, values, conditions))
	return results.FromBench(string(verdict), true), nil
}

type caseDiffPayload struct {
	Suite  string `json:"suite"`
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
	Was    string `json:"was,omitempty"`
	Change string `json:"change"`
}

// casePageSize borne une page de cas de test (doc 08 §8.6).
//
// Cinq cents lignes tiennent dans une table utilisable et couvrent la totalité des
// familles sauf Cypress sur une release complète. La taille n'est pas négociable
// par l'appelant : un `limit` libre dans l'URL rendrait la borne décorative.
const casePageSize = 500

type casePayload struct {
	Suite      string `json:"suite"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int    `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
	Trace      string `json:"trace,omitempty"`
}

// Family rend les cas d'une famille et sa comparaison à la release précédente.
//
// La comparaison est la vue qui répond à la question réellement posée en recette
// — « qu'est-ce qui a changé ? » — et non « combien de tests passent ». Un
// compteur stable peut cacher un échec résolu et un échec apparu.
func (h *TestsHandler) Family(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	family := r.PathValue("family")
	page := store.CasePage{
		Limit:  casePageSize,
		Offset: int(intQuery(r, "offset", 0)),
		Query:  r.URL.Query().Get("q"),
	}

	reports, err := h.store.ListReleaseTestResults(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des résultats impossible", "")
		return
	}

	cases := make([]casePayload, 0, casePageSize)
	total := 0
	// Le run qui a produit le rapport affiché. Rendu avec les cas pour que le
	// lecteur puisse ouvrir l'exécution — ses journaux disent pourquoi un test a
	// échoué là où le rapport dit seulement qu'il a échoué.
	var runName, tool string
	var measuredAt time.Time
	for _, report := range reports {
		if report.Family != family {
			continue
		}
		stored, count, err := h.store.ListTestCases(r.Context(), report.ID, page)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Lecture des cas impossible", "")
			return
		}
		total = count
		for _, testCase := range stored {
			cases = append(cases, casePayload{
				Suite: testCase.Suite, Name: testCase.Name, Status: testCase.Status,
				DurationMS: testCase.DurationMS,
				Message:    testCase.FailureMessage, Trace: testCase.FailureTrace,
			})
		}
		runName, tool, measuredAt = report.RunName, report.Tool, report.CreatedAt
		// Le rapport le plus récent de la famille suffit : c'est lui qui décrit
		// l'état de la release.
		break
	}

	// La comparaison n'est pas paginée : elle est déjà réduite à ce qui a changé,
	// et c'est la partie que l'on vient lire. La tronquer reviendrait à cacher un
	// échec apparu parce qu'il tombe en deuxième page.
	response := map[string]any{
		"family": family,
		"cases":  cases,
		"total":  total,
		"limit":  casePageSize,
		"offset": page.Offset,
	}
	if runName != "" {
		response["run_name"] = runName
	}
	if tool != "" {
		response["tool"] = tool
		response["measured_at"] = measuredAt
	}

	previous, err := h.store.PreviousRelease(r.Context(), release.ID)
	if err == nil {
		diffs, err := h.store.DiffFamily(r.Context(), release.ID, previous, family)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Comparaison impossible", "")
			return
		}

		changed := make([]caseDiffPayload, 0, len(diffs))
		for _, diff := range diffs {
			// `unchanged` est le gros du volume et n'apprend rien : la vue
			// existe pour ce qui a bougé.
			if diff.Change == store.ChangeUnchanged {
				continue
			}
			changed = append(changed, caseDiffPayload{
				Suite: diff.Suite, Name: diff.Name,
				Status: diff.Status, Was: diff.Was, Change: diff.Change,
			})
		}
		response["diff"] = changed
	}
	// Pas de release précédente : pas de comparaison, et ce n'est pas une erreur.
	// La première release d'un projet n'a rien à quoi se comparer.

	writeJSON(w, http.StatusOK, response)
}

type findingPayload struct {
	Fingerprint string  `json:"fingerprint"`
	Severity    string  `json:"severity"`
	Title       string  `json:"title"`
	Tool        string  `json:"tool"`
	Category    string  `json:"category"`
	RuleID      string  `json:"rule_id"`
	CVSS        float64 `json:"cvss,omitempty"`
	Target      string  `json:"target,omitempty"`
	Location    string  `json:"location,omitempty"`
	Package     string  `json:"package,omitempty"`
	// Installed et Fixed ensemble : ce qui décide d'une action est l'écart entre
	// la version en place et celle qui corrige.
	Installed string `json:"installed,omitempty"`
	Fixed     string `json:"fixed,omitempty"`
	Fixable   bool   `json:"fixable"`
	// Status vaut `new`, `existing` ou `resolved`, établi par le manager en
	// comparant au run précédent. C'est ce qui distingue une régression d'une
	// dette connue.
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	Suppressed  bool   `json:"suppressed"`
	// Reason accompagne toujours une suppression : une exception sans motif est
	// indéfendable en revue.
	SuppressionReason  string `json:"suppression_reason,omitempty"`
	SuppressionExpires string `json:"suppression_expires,omitempty"`
}

// Security rend le dernier audit d'une release et ses findings.
func (h *TestsHandler) Security(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	audit, err := h.store.LatestSecurityAudit(r.Context(), release.ID)
	if err != nil {
		if errors.Is(err, store.ErrAuditNotFound) {
			// Aucun audit n'est un état légitime, distinct d'un audit vide : le
			// dire plutôt que rendre des compteurs à zéro, qui se liraient comme
			// « rien trouvé ».
			writeJSON(w, http.StatusOK, map[string]any{"audited": false})
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de l'audit impossible", "")
		return
	}

	// Les supprimés sont inclus : la trace de ce qui a été écarté, et par quelle
	// exception, vaut autant que la liste de ce qui reste. L'interface les
	// sépare, elle ne les cache pas.
	findings, err := h.store.ListSecurityFindings(r.Context(), audit.TestResultID, true)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des findings impossible", "")
		return
	}

	payload := make([]findingPayload, 0, len(findings))
	for _, finding := range findings {
		payload = append(payload, findingPayload{
			Fingerprint: finding.Fingerprint, Severity: finding.Severity,
			Title: finding.Title, Tool: finding.Tool, Category: finding.Category,
			RuleID: finding.RuleID, CVSS: finding.CVSS,
			Target:   finding.TargetName,
			Location: finding.Location, Package: finding.PackageName,
			Installed: finding.PackageInstalled, Fixed: finding.PackageFixed,
			Fixable: finding.Fixable, Status: finding.Status,
			Description: finding.Description, Suppressed: finding.Suppressed,
			SuppressionReason:  finding.SuppressionReason,
			SuppressionExpires: finding.SuppressionExpires,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"audited":  true,
		"run_id":   audit.RunID,
		"run_name": audit.RunName,
		// Measured dit si l'audit peut prononcer un verdict. Faux, les compteurs
		// restent affichés mais ne notent rien : un audit partiel qui afficherait
		// « conforme » est le pire résultat possible ici.
		"measured":        audit.Measured(),
		"failed_scanners": audit.FailedScanners,
		"scanner_status":  audit.ScannerStatus,
		"counts": map[string]int{
			"total": audit.Total, "critical": audit.Critical, "high": audit.High,
			"medium": audit.Medium, "low": audit.Low, "suppressed": audit.Suppressed,
		},
		"delta": map[string]int{
			"new": audit.DeltaNew, "existing": audit.DeltaExisting,
			"resolved": audit.DeltaResolved,
		},
		"previous_run_id": audit.PreviousRunID,
		"policy":          audit.Policy,
		"generated_at":    audit.GeneratedAt,
		"findings":        payload,
	})
}
