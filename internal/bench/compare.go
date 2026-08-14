package bench

import "fmt"

// Verdict est le jugement porté sur une mesure.
type Verdict string

const (
	// VerdictPass : aussi bon que la baseline, ou meilleur.
	VerdictPass Verdict = "pass"
	// VerdictWarn : dégradé de moins de 10 %.
	VerdictWarn Verdict = "warn"
	// VerdictFail : dégradé au-delà.
	VerdictFail Verdict = "fail"
	// VerdictNA : rien à comparer, ou mesure non comparable.
	VerdictNA Verdict = "na"
)

// rank ordonne les verdicts du plus clément au plus sévère. Sert à ce qu'un
// second critère puisse **dégrader** un verdict sans jamais l'améliorer.
var rank = map[Verdict]int{VerdictNA: 0, VerdictPass: 1, VerdictWarn: 2, VerdictFail: 3}

// Seuil de tolérance. 10 % : repris de la maquette, où il est déjà appliqué et
// validé par le client. Le redéfinir ici ferait diverger l'interface portée de
// l'interface validée.
const warnThreshold = 0.9

// Conditions décrit dans quelles conditions une mesure a été prise.
//
// Sans elles, une mesure n'est qu'un nombre. Le cluster qui héberge Tekton est
// aussi celui de la cible : deux mesures prises l'une pendant un build et l'autre
// dans un cluster au repos ne se comparent pas, et les mettre sur la même courbe
// produit une tendance inventée ([B16]).
type Conditions struct {
	// Exclusive vaut true quand la barrière `exclusive-cluster` était tenue : le
	// dashboard n'a laissé tourner aucun autre job.
	Exclusive bool
	// ConcurrentJobs est le nombre de runs constatés au démarrage. Zéro avec
	// Exclusive vrai est le cas nominal ; non nul, il chiffre la perturbation.
	ConcurrentJobs int
}

// Comparable dit si la mesure peut entrer dans une comparaison à la baseline.
//
// La règle est volontairement stricte : l'exclusivité seule ne suffit pas si des
// runs tournaient encore au démarrage. La barrière draine avant de lancer, donc
// les deux devraient coïncider — mais s'ils divergent, c'est le constat qui fait
// foi, pas l'intention.
func (c Conditions) Comparable() bool {
	return c.Exclusive && c.ConcurrentJobs == 0
}

// Why explique en français pourquoi une mesure n'est pas comparable.
//
// L'interface doit pouvoir le dire : une mesure affichée sans verdict et sans
// motif se lit comme une panne de mesure, alors que c'est une mesure valide dont
// on refuse seulement la comparaison.
func (c Conditions) Why() string {
	switch {
	case c.Comparable():
		return ""
	case !c.Exclusive && c.ConcurrentJobs > 0:
		return fmt.Sprintf(
			"mesure prise sans réservation du cluster, %d exécution(s) concurrente(s) constatée(s)",
			c.ConcurrentJobs)
	case !c.Exclusive:
		return "mesure prise sans réservation du cluster"
	default:
		return fmt.Sprintf(
			"%d exécution(s) tournaient encore au démarrage malgré la réservation", c.ConcurrentJobs)
	}
}

// Comparison confronte une mesure à sa baseline.
type Comparison struct {
	Measure  Measure
	Baseline *float64
	Verdict  Verdict
	// Ratio vaut 1 à parité, au-dessus si la mesure est meilleure. Rendu pour que
	// l'interface affiche l'écart plutôt qu'un simple verdict.
	Ratio float64
	// Comparable est false quand les conditions de mesure interdisent la
	// comparaison. La mesure reste affichée : la cacher ferait croire qu'elle
	// n'a pas eu lieu.
	Comparable bool
	// Reason porte le motif du refus de comparaison, vide sinon.
	Reason string
}

// Compare confronte une campagne à ses baselines.
//
// Les mesures non comparables reçoivent [VerdictNA] et leur motif — elles ne sont
// ni écartées ni jugées. Les écarter reviendrait à cacher une campagne qui a bien
// eu lieu ; les juger reviendrait à imputer à une release une lenteur causée par
// un build voisin.
func Compare(summary Summary, baselines map[string]float64, conditions Conditions) []Comparison {
	comparable := conditions.Comparable()
	reason := conditions.Why()

	// Le volume total peut dégrader le verdict des autres mesures, jamais
	// l'améliorer : un débit conforme obtenu en traitant moins de requêtes n'est
	// pas un succès. Son propre verdict est donc calculé d'abord.
	volume := VerdictNA
	for _, measure := range summary.Measures {
		if !measure.Volume {
			continue
		}
		if baseline, known := baselines[measure.Metric]; known && comparable {
			volume = judge(measure.Value, baseline, measure.LowerBetter)
		}
	}

	comparisons := make([]Comparison, 0, len(summary.Measures))
	for _, measure := range summary.Measures {
		comparison := Comparison{
			Measure: measure, Verdict: VerdictNA, Comparable: comparable, Reason: reason,
		}

		baseline, known := baselines[measure.Metric]
		if known {
			value := baseline
			comparison.Baseline = &value
		}

		if known && comparable {
			comparison.Ratio = ratio(measure.Value, baseline, measure.LowerBetter)
			comparison.Verdict = judge(measure.Value, baseline, measure.LowerBetter)

			// Le volume dégrade les autres, pas lui-même.
			if !measure.Volume && rank[volume] > rank[comparison.Verdict] {
				comparison.Verdict = volume
			}
		}

		comparisons = append(comparisons, comparison)
	}
	return comparisons
}

func ratio(value, baseline float64, lowerBetter bool) float64 {
	if baseline == 0 || value == 0 {
		// Une baseline nulle rendrait tout ratio infini ; une mesure nulle sur
		// une métrique où plus bas vaut mieux est parfaite mais indivisible.
		if lowerBetter && value == 0 {
			return 1
		}
		return 0
	}
	if lowerBetter {
		return baseline / value
	}
	return value / baseline
}

func judge(value, baseline float64, lowerBetter bool) Verdict {
	r := ratio(value, baseline, lowerBetter)
	switch {
	case r >= 1:
		return VerdictPass
	case r >= warnThreshold:
		return VerdictWarn
	default:
		return VerdictFail
	}
}

// Worst rend le verdict le plus sévère d'un ensemble.
//
// C'est ce qui alimente le signal de performance du score de qualité : une
// campagne vaut son point le plus faible, et non sa moyenne — une moyenne
// noierait une régression sur une métrique dans la conformité des autres.
func Worst(comparisons []Comparison) Verdict {
	worst := VerdictNA
	for _, comparison := range comparisons {
		if rank[comparison.Verdict] > rank[worst] {
			worst = comparison.Verdict
		}
	}
	return worst
}
