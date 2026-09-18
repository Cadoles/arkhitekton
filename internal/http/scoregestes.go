package http

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/bench"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Le score, geste par geste.
 *
 * # Ce qui change d'axe
 *
 * Le score pesait six **signaux** de poids fixes. Deux campagnes E2E distinctes
 * s'y confondaient donc en une, et aucune pondération ne pouvait les séparer. Le
 * composant est désormais le **geste** de l'étape de vérification : deux
 * campagnes sont deux gestes, chacun avec son poids (doc 15 §15.6).
 *
 * Le signal ne disparaît pas. Il dit toujours **comment** noter ce qui a été
 * mesuré — un rapport de tests ne se note pas comme un audit — et sert encore à
 * choisir une icône. Il a seulement cessé d'être l'identité.
 *
 * # D'où vient le rattachement
 *
 * Rien n'a été ajouté au schéma : un résultat porte son run, un run porte son
 * job, et un geste est le seul à porter ce job. `ResultGestes` suit cette chaîne.
 *
 * # Les poids sont ceux du plan qui a tourné
 *
 * Ils sont lus dans `plan_runs.plan`, figé à l'ouverture de l'exécution. Rejouer
 * le score d'une release livrée il y a six mois emploie donc les poids de ce
 * jour-là. Un chiffre communiqué ne se réécrit pas.
 */

// planScoreStore est ce que le score par geste attend de la base, en plus du
// reste.
type planScoreStore interface {
	PlanRunByEvent(ctx context.Context, trigger, eventKey string) (store.PlanRun, error)
	ResultGestes(ctx context.Context, releaseID int64) (map[int64]store.GesteRef, error)
}

// gesteMeasures rend un composant de score par geste de vérification.
//
// Rend `nil, nil` quand la release n'a pas de plan, ou qu'il ne vérifie rien :
// l'appelant retombe alors sur les familles. Ce repli n'est pas une commodité —
// les releases antérieures au plan d'une release (doc 15 §15.2) n'en ont pas, et
// leur score doit rester lisible.
func gesteMeasures(
	ctx context.Context, source scoreStore, releaseID int64,
	reports []store.TestResult, registry *catalog.Catalog,
) ([]results.Measure, error) {
	plans, ok := source.(planScoreStore)
	if !ok {
		return nil, nil
	}

	run, err := plans.PlanRunByEvent(ctx,
		plan.ReleaseTrigger, event.ReleaseCreatedKey(releaseID))
	if err != nil {
		if errors.Is(err, store.ErrPlanRunNotFound) {
			return nil, nil
		}
		return nil, err
	}

	// La **dernière** étape de vérification, comme le cycle de vie : un plan peut
	// vérifier en deux temps, et c'est la conclusion du dernier qui dit que la
	// release est vérifiée.
	stage := run.Plan.LastWithRole(plan.RoleTest)
	if stage < 0 || len(run.Plan.Stages[stage].Gestes) == 0 {
		return nil, nil
	}

	byResult, err := plans.ResultGestes(ctx, releaseID)
	if err != nil {
		return nil, err
	}

	reportsByGeste := map[store.GesteRef][]store.TestResult{}
	for _, report := range reports {
		if ref, attribue := byResult[report.ID]; attribue {
			reportsByGeste[ref] = append(reportsByGeste[ref], report)
		}
	}

	audit, auditGeste, err := auditDuGeste(ctx, source, releaseID, byResult)
	if err != nil {
		return nil, err
	}
	campagne, campagneGeste, err := campagneDuGeste(ctx, source, releaseID, byResult)
	if err != nil {
		return nil, err
	}

	measures := make([]results.Measure, 0, len(run.Plan.Stages[stage].Gestes))
	for position, geste := range run.Plan.Stages[stage].Gestes {
		ref := store.GesteRef{Stage: stage, Position: position}
		siens := reportsByGeste[ref]

		famille := familleDuGeste(geste, siens, registry)
		signal, connu := results.SignalOf(famille)

		measure := results.Measure{
			Key: gesteKey(ref), Label: geste.Name(), Signal: signal,
			// Ce qui a produit la mesure : le pipeline que « Rejouer » relance, et
			// la famille de rapports que le détail ouvre.
			Ref: geste.Ref, Family: famille,
			Weight: results.WeightOf(geste.Weight, signal),
		}

		switch {
		case !connu:
			// Un geste dont on ne sait pas lire ce qu'il produit. Son poids reste
			// affiché — sinon on croirait qu'il ne compte pas — mais il ne note
			// rien, et le détail dit pourquoi.
			measure.Detail = "aucune famille de résultats connue pour ce pipeline"
		case famille == results.FamilySecurity:
			measure.Measured, measure.Value, measure.Detail =
				valeurDe(mesureAudit(audit, auditGeste == ref))
		case famille == results.FamilyLoad:
			mesure, err := mesureCampagne(ctx, source, campagne, campagneGeste == ref)
			if err != nil {
				return nil, err
			}
			measure.Measured, measure.Value, measure.Detail = valeurDe(mesure)
		case famille == results.FamilyStatic:
			combine, ran := cumule(siens)
			// Les violations d'analyse statique se comptent en cas, pas en tests
			// réussis : la règle de score n'est pas la même.
			measure.Measured, measure.Value, measure.Detail =
				valeurDe(results.FromStatic(combine.Failed, ran))
		default:
			combine, ran := cumule(siens)
			measure.Measured, measure.Value, measure.Detail =
				valeurDe(results.FromReport(signal, combine, ran))
		}

		measures = append(measures, measure)
	}
	return measures, nil
}

// gesteKey compose la clé d'un composant porté par un geste.
//
// Sa position dans le plan figé, et non son libellé : deux gestes peuvent le
// partager, et un plan renommé ne doit pas casser le rapprochement d'un score
// avec la case qui l'affiche.
func gesteKey(ref store.GesteRef) string {
	return "geste:" + strconv.Itoa(ref.Stage) + "." + strconv.Itoa(ref.Position)
}

// familleDuGeste rend la famille de résultats que ce geste alimente.
//
// **Ce qu'il a produit d'abord**, le catalogue ensuite. L'ordre compte : un geste
// qui a tourné dit lui-même ce qu'il a rendu, et le catalogue peut avoir changé
// depuis. Le catalogue sert au geste qui n'a rien produit — c'est le seul moyen
// de savoir quel poids par défaut lui donner, et donc de l'afficher plutôt que de
// le taire.
func familleDuGeste(
	geste plan.Geste, siens []store.TestResult, registry *catalog.Catalog,
) results.Family {
	for _, report := range siens {
		if report.Family != "" {
			return results.Family(report.Family)
		}
	}
	if registry == nil {
		return ""
	}
	pipeline, found := registry.Find(geste.Ref)
	if !found {
		return ""
	}
	return results.Family(pipeline.Family())
}

// cumule somme les rapports d'un geste, un seul par outil.
//
// Retenir un seul rapport perdrait les pipelines à plusieurs outils. `static` en
// compte deux — PHPStan et PHP-CS-Fixer — et seul le plus récent entrait au
// score : une release avec quarante erreurs PHPStan et un cs-fixer propre
// affichait 100 dès lors que cs-fixer avait conclu en dernier.
func cumule(siens []store.TestResult) (results.Report, bool) {
	combine := results.Report{}
	vus := map[string]bool{}
	ran := false

	for _, report := range siens {
		// La liste est antichronologique : le premier rencontré pour un outil est
		// le plus récent, les suivants sont son historique.
		if vus[report.Tool] {
			continue
		}
		vus[report.Tool] = true
		ran = true

		combine.Total += report.Total
		combine.Passed += report.Passed
		combine.Failed += report.Failed
		combine.Skipped += report.Skipped
		combine.Flaky += report.Flaky
		// Le statut le plus sévère l'emporte : deux outils d'un même geste rendent
		// un verdict commun, et le plus clément masquerait l'autre.
		combine.Status = worstStatus(combine.Status, report.Status)
	}
	return combine, ran
}

// valeurDe extrait ce qu'une mesure dit, pour le reporter sur un composant.
func valeurDe(measure results.Measure) (bool, float64, string) {
	return measure.Measured, measure.Value, measure.Detail
}

/*
 * L'audit et la campagne, rattachés au geste qui les a produits.
 *
 * # Une limite assumée
 *
 * Les deux se lisent « le plus récent de la release », et non « celui de ce
 * geste ». Deux gestes de sécurité dans un même plan ne seraient donc pas notés
 * séparément : le second se déclarerait non mesuré, en le disant, plutôt que
 * d'afficher la valeur du premier sous son nom.
 *
 * Ce n'est pas la forme définitive. C'est la seule qui soit **vraie** sans
 * requête nouvelle, et le cas qu'elle laisse de côté n'existe dans aucun
 * catalogue déployé.
 */

func auditDuGeste(
	ctx context.Context, source scoreStore, releaseID int64,
	byResult map[int64]store.GesteRef,
) (store.SecurityAudit, store.GesteRef, error) {
	audit, err := source.LatestSecurityAudit(ctx, releaseID)
	if err != nil {
		if errors.Is(err, store.ErrAuditNotFound) {
			return store.SecurityAudit{}, store.GesteRef{Stage: -1}, nil
		}
		return store.SecurityAudit{}, store.GesteRef{}, err
	}
	ref, attribue := byResult[audit.TestResultID]
	if !attribue {
		return audit, store.GesteRef{Stage: -1}, nil
	}
	return audit, ref, nil
}

func campagneDuGeste(
	ctx context.Context, source scoreStore, releaseID int64,
	byResult map[int64]store.GesteRef,
) (*store.LoadResult, store.GesteRef, error) {
	campagnes, err := source.ListReleaseLoadResults(ctx, releaseID)
	if err != nil {
		return nil, store.GesteRef{}, err
	}
	if len(campagnes) == 0 {
		return nil, store.GesteRef{Stage: -1}, nil
	}
	ref, attribue := byResult[campagnes[0].ID]
	if !attribue {
		return &campagnes[0], store.GesteRef{Stage: -1}, nil
	}
	return &campagnes[0], ref, nil
}

// mesureAudit note la sécurité depuis l'audit d'un geste.
//
// L'audit décide lui-même s'il est mesurable : un scanner en échec produit zéro
// finding, exactement comme un audit propre, et seul son descripteur les
// distingue. Le compter comme conforme serait l'erreur la plus chère possible
// sur ce signal (P2, B11).
func mesureAudit(audit store.SecurityAudit, sien bool) results.Measure {
	if !sien {
		return results.FromSecurity(0, 0, false)
	}
	if !audit.Measured() {
		return results.Measure{Signal: results.SignalSecurity,
			Detail: "audit incomplet : " + strings.Join(audit.FailedScanners, ", ") +
				" n'a pas conclu"}
	}
	// Les compteurs viennent de `summary.json`, qui exclut déjà les findings
	// supprimés par exception. Une exception est une décision humaine datée : la
	// respecter ici est ce qui rend le score actionnable plutôt que subi.
	return results.FromSecurity(audit.Critical, audit.High, true)
}

// mesureCampagne note la performance depuis la campagne d'un geste.
func mesureCampagne(
	ctx context.Context, source scoreStore, campagne *store.LoadResult, sien bool,
) (results.Measure, error) {
	if !sien || campagne == nil {
		return results.FromBench("", false), nil
	}

	conditions := bench.Conditions{
		Exclusive: campagne.Exclusive, ConcurrentJobs: campagne.ConcurrentJobs,
	}
	if !conditions.Comparable() {
		// Une campagne non comparable n'est pas une campagne absente, mais elle ne
		// peut pas noter la performance : la juger imputerait à la release une
		// lenteur causée par un voisin (B16).
		return results.FromBench("", false), nil
	}

	measures, err := source.ListResultMeasures(ctx, campagne.ID)
	if err != nil {
		return results.Measure{}, err
	}
	baselines, err := source.Baselines(ctx, campagne.Scenario)
	if err != nil {
		return results.Measure{}, err
	}

	summary := bench.Summary{Scenario: campagne.Scenario}
	for _, measure := range measures {
		summary.Measures = append(summary.Measures, bench.Measure{
			Metric: measure.Metric, Value: measure.Value,
			LowerBetter: measure.LowerBetter, Volume: measure.Volume,
		})
	}
	values := map[string]float64{}
	for metric, baseline := range baselines {
		values[metric] = baseline.Value
	}

	return results.FromBench(string(bench.Worst(
		bench.Compare(summary, values, conditions))), true), nil
}
