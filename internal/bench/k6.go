// Package bench lit les résultats de tests de charge et les rend comparables.
//
// Deux responsabilités, et la seconde est la moins évidente.
//
// Lire un résumé k6 est mécanique. **Décider si une mesure est comparable** ne
// l'est pas : le cluster qui héberge Tekton est aussi celui de l'environnement de
// test de charge ([B16]). Un build, un audit Trivy ou une campagne E2E qui tourne
// pendant la mesure la fausse autant que le générateur l'aurait fait — c'est un
// problème de voisinage, pas de génération, et sortir le runner du cluster ne le
// résout qu'à moitié.
//
// La conséquence est directe : une courbe de tendance sur six points dont les
// conditions varient ne veut rien dire. Ce paquet porte donc la règle qui rend la
// comparaison honnête — seules les mesures prises en exclusivité entrent dans la
// comparaison à la baseline ; les autres sont conservées, affichées, et marquées
// non comparables.
//
// Tout y est pur : ni base, ni cluster, ni réseau.
package bench

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Metric nomme une métrique k6 et la façon de la lire.
type Metric struct {
	// Name est le nom de la métrique k6 (`http_req_duration`).
	Name string
	// Stat est la statistique retenue (`p(95)`, `rate`, `count`, `avg`).
	Stat string
	// Label est ce qui s'affiche.
	Label string
	Unit  string
	// LowerBetter inverse le sens de la comparaison. Une latence baisse, un débit
	// monte : confondre les deux fait afficher « conforme » sur une régression.
	LowerBetter bool
	// Volume marque la métrique de volume total, qui peut dégrader le verdict d'une
	// autre sans jamais l'améliorer.
	Volume bool
}

// DefaultMetrics est le jeu retenu par défaut.
//
// Quatre mesures et pas davantage : la maquette affiche un panneau, pas un
// tableau de bord d'observabilité. Chacune répond à une question qu'on se pose
// avant de livrer — est-ce lent, est-ce lent pour les plus malchanceux, combien
// on encaisse, et est-ce que ça casse.
var DefaultMetrics = []Metric{
	{Name: "http_req_duration", Stat: "p(95)", Label: "Latence p95", Unit: "ms", LowerBetter: true},
	{Name: "http_req_duration", Stat: "med", Label: "Latence médiane", Unit: "ms", LowerBetter: true},
	{Name: "http_reqs", Stat: "rate", Label: "Débit", Unit: "req/s"},
	// Le volume total : un débit instantané conforme obtenu en traitant moins de
	// requêtes n'est pas un succès.
	{Name: "http_reqs", Stat: "count", Label: "Requêtes", Unit: "req", Volume: true},
	{Name: "http_req_failed", Stat: "rate", Label: "Taux d'échec", Unit: "%", LowerBetter: true},
}

// Measure est une mesure lisible, prête à être comparée.
type Measure struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
	Unit   string `json:"unit"`
	Value  float64
	// LowerBetter est recopié depuis la métrique : la mesure enregistrée doit
	// pouvoir être relue et comparée sans reconstruire le catalogue de métriques,
	// qui aura pu changer entre-temps.
	LowerBetter bool
	Volume      bool
	// ThresholdOK reprend le verdict que k6 rend lui-même sur ses seuils
	// déclarés. Distinct de la comparaison à la baseline : k6 dit « le contrat est
	// tenu », la baseline dit « c'est aussi bon qu'avant ». Les deux peuvent se
	// contredire, et l'ignorer perdrait l'information la plus directe.
	ThresholdOK *bool
}

// Summary est le résultat d'une campagne.
type Summary struct {
	Scenario string
	Measures []Measure
	// VUs est le nombre d'utilisateurs virtuels au pic. Contexte de lecture :
	// comparer deux mesures prises à des charges différentes n'a pas de sens.
	VUs int
	// ThresholdsFailed liste les seuils k6 non tenus, dans l'ordre de lecture.
	ThresholdsFailed []string
}

// k6Metric couvre les deux formes de résumé produites par k6.
//
// `--summary-export` écrit les statistiques à plat dans l'objet de la métrique ;
// `handleSummary` les range sous `values` et ajoute `type` et `thresholds`. Les
// deux existent dans la nature — une Task écrite il y a deux ans emploie la
// première —, et refuser l'une ferait échouer la collecte sur un détail
// d'invocation.
type k6Metric struct {
	Type       string                `json:"type"`
	Contains   string                `json:"contains"`
	Values     map[string]float64    `json:"values"`
	Thresholds map[string]k6Threshol `json:"thresholds"`
}

type k6Threshol struct {
	OK bool `json:"ok"`
}

type k6Summary struct {
	Metrics map[string]json.RawMessage `json:"metrics"`
}

// ErrNoMetrics signale un résumé sans métrique exploitable.
type ErrNoMetrics struct{ Scenario string }

func (e ErrNoMetrics) Error() string {
	return fmt.Sprintf("résumé k6 du scénario %q: aucune métrique exploitable", e.Scenario)
}

// ParseSummary lit un résumé k6.
//
// Une métrique attendue mais absente n'est pas une erreur : un scénario peut ne
// pas produire de requêtes HTTP. Elle est simplement omise, et le panneau
// affichera « non mesuré » — ce qui est exact, alors qu'un zéro serait faux.
func ParseSummary(scenario string, raw []byte, metrics []Metric) (Summary, error) {
	if len(metrics) == 0 {
		metrics = DefaultMetrics
	}

	var summary k6Summary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return Summary{}, fmt.Errorf("résumé k6 du scénario %q illisible: %w", scenario, err)
	}

	result := Summary{Scenario: scenario}
	failed := map[string]bool{}

	for _, metric := range metrics {
		raw, present := summary.Metrics[metric.Name]
		if !present {
			continue
		}

		values, thresholds, err := decodeMetric(raw)
		if err != nil {
			return Summary{}, fmt.Errorf("métrique %q du scénario %q: %w", metric.Name, scenario, err)
		}

		value, measured := values[metric.Stat]
		if !measured {
			continue
		}

		measure := Measure{
			Metric: metric.Name + ":" + metric.Stat,
			Label:  metric.Label, Unit: metric.Unit,
			Value: value, LowerBetter: metric.LowerBetter, Volume: metric.Volume,
		}

		// Un taux k6 vaut 0..1 ; l'afficher en pourcentage évite un « 0.02 % »
		// qui se lit comme deux centièmes de pour cent.
		if metric.Unit == "%" {
			measure.Value = value * 100
		}

		for name, threshold := range thresholds {
			ok := threshold.OK
			measure.ThresholdOK = &ok
			if !ok {
				failed[metric.Name+": "+name] = true
			}
		}

		result.Measures = append(result.Measures, measure)
	}

	if raw, present := summary.Metrics["vus_max"]; present {
		if values, _, err := decodeMetric(raw); err == nil {
			// `value` sur une jauge, `max` sur une série : les deux formes
			// existent selon la version de k6.
			if peak, found := values["value"]; found {
				result.VUs = int(peak)
			} else if peak, found := values["max"]; found {
				result.VUs = int(peak)
			}
		}
	}

	for name := range failed {
		result.ThresholdsFailed = append(result.ThresholdsFailed, name)
	}
	// Ordre stable : un ensemble de seuils identiques doit produire le même
	// document deux fois de suite, sans quoi toute comparaison de sorties est
	// faussée par l'ordre d'itération d'une map.
	sort.Strings(result.ThresholdsFailed)

	if len(result.Measures) == 0 {
		return result, ErrNoMetrics{Scenario: scenario}
	}
	return result, nil
}

// decodeMetric lit une métrique dans l'une ou l'autre des formes de k6.
func decodeMetric(raw []byte) (map[string]float64, map[string]k6Threshol, error) {
	var structured k6Metric
	if err := json.Unmarshal(raw, &structured); err == nil && structured.Values != nil {
		return structured.Values, structured.Thresholds, nil
	}

	// Forme plate : l'objet de la métrique **est** la table des statistiques.
	var flat map[string]float64
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, nil, fmt.Errorf("forme de métrique non reconnue: %w", err)
	}
	return flat, nil, nil
}

// ScenarioFromKey déduit un nom de scénario d'une clé d'objet.
//
// Le runner publie sous une clé que le dashboard a calculée ; le nom du fichier
// porte le scénario. Faute de mieux, le nom de l'objet vaut nom de scénario —
// mieux qu'un scénario vide, qui empêcherait tout historique.
func ScenarioFromKey(key string) string {
	name := key
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	name = strings.TrimSuffix(name, ".json")
	name = strings.TrimSuffix(name, "-summary")
	if name == "" {
		return "default"
	}
	return name
}
