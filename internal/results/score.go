package results

import (
	"fmt"
	"math"
)

/*
Score de qualité.

Le score n'a pas de définition évidente : la maquette en affiche un sur 100 et en
dérive le statut global, mais rien ne dit comment il est calculé ([B11]). La
pondération ci-dessous a été acceptée par le client, et **elle est donc une donnée
de configuration, pas une constante de code** — c'est ce qui permet de l'ajuster
sans redéployer.

Deux propriétés comptent autant que les chiffres.

**Le détail est toujours exposé à côté du score.** Un score dont on ne peut pas
voir la décomposition n'est pas ajustable en connaissance de cause : « 72 » ne dit
pas s'il faut corriger des tests ou des vulnérabilités.

**Un signal non mesuré sort du calcul et son poids est redistribué**, avec mention
explicite. Un 100 sur trois signaux mesurés ne doit pas ressembler à un 100 sur
six — c'est le même nombre pour deux réalités qui n'engagent pas la même décision.
*/

// Signal nomme une composante du score.
type Signal string

const (
	SignalUnit        Signal = "unit"
	SignalIntegration Signal = "integration"
	SignalE2E         Signal = "e2e"
	SignalCode        Signal = "code"
	SignalSecurity    Signal = "security"
	SignalPerformance Signal = "performance"
)

// Weights est la pondération des signaux.
//
// Type nommé plutôt que map anonyme : cette valeur vient d'une ConfigMap, et lui
// donner un nom rend visible qu'elle traverse la frontière de la configuration.
type Weights map[Signal]float64

// DefaultWeights est la pondération acceptée par le client (B11).
//
// Présente en code comme **repli**, pas comme référence : sans elle, une
// ConfigMap absente rendrait tout score incalculable, ce qui est pire qu'un score
// calculé selon la pondération d'origine et affiché avec son détail.
//
// Depuis que le poids se déclare sur le geste (doc 15 §15.6), elle sert surtout
// de **défaut par signal** : un geste qui ne dit rien pèse ce que son signal
// pesait avant que quiconque puisse le changer. Personne ne perd la pondération
// convenue en ne saisissant rien.
func DefaultWeights() Weights {
	return Weights{
		SignalUnit:        25,
		SignalIntegration: 20,
		SignalE2E:         20,
		SignalCode:        10,
		SignalSecurity:    15,
		SignalPerformance: 10,
	}
}

// SignalOf rend le signal qu'une famille de résultats alimente.
//
// La famille dit d'où sort le chiffre, le signal dit comment le noter. Les deux
// noms coexistaient déjà dans l'interface ; la correspondance, elle, vivait
// implicitement dans une liste d'appels — et devenait fausse dès qu'un geste
// choisissait sa famille au lieu de la subir.
func SignalOf(family Family) (Signal, bool) {
	switch family {
	case FamilyUnit:
		return SignalUnit, true
	case FamilyIntegration:
		return SignalIntegration, true
	case FamilyE2E:
		return SignalE2E, true
	case FamilyStatic:
		return SignalCode, true
	case FamilySecurity:
		return SignalSecurity, true
	case FamilyLoad:
		return SignalPerformance, true
	default:
		return "", false
	}
}

// WeightOf rend le poids d'un geste : celui qu'il déclare, sinon celui de son
// signal.
//
// Le pointeur porte trois états, et c'est pour cela qu'il en est un. Absent : le
// défaut du signal s'applique. Zéro : le geste est délibérément hors du score.
// Une valeur : elle s'applique.
func WeightOf(declared *float64, signal Signal) float64 {
	if declared != nil {
		return *declared
	}
	return DefaultWeights()[signal]
}

// Measure est la valeur d'un composant du score.
type Measure struct {
	// Key identifie le composant, et c'est ce qui a changé d'axe.
	//
	// Le signal faisait l'identité : six composants, un par nature, et deux
	// campagnes E2E distinctes se confondaient donc dans un seul. La clé est
	// désormais le **geste** qui a mesuré — `geste:1.0` — ou, pour une release
	// antérieure aux plans, la famille faute de mieux — `signal:unit`
	// (doc 15 §15.6).
	Key string
	// Label nomme le composant à l'écran, dans les mots de qui l'a déclaré.
	Label string
	// Signal dit **comment** mesurer, et plus comment pondérer. Il reste ce qui
	// relie un rapport à sa règle de notation (doc 13).
	Signal Signal
	// Weight est le poids déclaré de ce composant.
	//
	// Porté par la mesure et non cherché dans une table : c'est ce qui permet à
	// deux gestes de même signal de peser différemment, et c'est tout l'objet du
	// changement.
	Weight float64
	// Value vaut 0 à 100. Ignorée quand Measured est faux.
	Value float64
	// Measured distingue « mesuré à zéro » de « non mesuré ». Les deux affichent
	// zéro, et les confondre ferait tomber le score d'une release dont un pipeline
	// n'a simplement pas tourné.
	Measured bool
	// Detail explique la valeur, en français. Affiché à côté du score.
	Detail string
}

// SignalKey rend la clé d'un composant identifié par son seul signal.
//
// Le repli des releases qui n'ont pas de plan — celles d'avant doc 15 §15.2. Leur
// score reste calculable, et c'est la seule raison pour laquelle cette forme
// existe encore.
func SignalKey(signal Signal) string { return "signal:" + string(signal) }

// Breakdown est le score et sa décomposition.
type Breakdown struct {
	// Score vaut 0 à 100, arrondi à l'entier. Nul quand rien n'est mesuré — et
	// `Measured` vaut alors faux, ce qui est la seule façon de distinguer un
	// projet à zéro d'un projet non évalué.
	Score    int
	Measured bool
	Signals  []SignalScore
	// Redistributed liste les composants sortis du calcul, par leur clé. Affiché
	// tel quel : le lecteur doit savoir sur quoi porte le score qu'il lit.
	Redistributed []string
}

// SignalScore est la contribution d'un composant.
type SignalScore struct {
	// Key et Label identifient le composant. Le signal les accompagne parce qu'il
	// dit la nature de ce qui a été mesuré, et sert encore à choisir une icône.
	Key    string
	Label  string
	Signal Signal
	Value  float64
	// Weight est le poids **effectif**, après redistribution. Distinct du poids
	// déclaré : c'est lui qui explique la contribution réelle.
	Weight float64
	// Declared est le poids tel qu'il a été saisi, avant redistribution. Rendu
	// parce qu'un poids effectif seul ne permet pas de relire une pondération :
	// « 33 » ne dit pas si quelqu'un a écrit 20 ou 40.
	Declared float64
	Measured bool
	Detail   string
}

// Score calcule le score pondéré et sa décomposition.
//
// Les poids voyagent avec les mesures : un composant sait ce qu'il pèse, et deux
// composants de même signal peuvent donc peser différemment. La table des poids
// a disparu de la signature pour cette raison — la chercher par signal était
// exactement ce qui empêchait deux campagnes E2E de compter séparément.
//
// **L'ordre des mesures est conservé.** C'est celui de la déclaration, donc celui
// que l'écran doit montrer : un plan qui lance l'audit en premier l'affiche en
// premier, et le trier ici imposerait un ordre que personne n'a demandé.
func Score(measures []Measure) Breakdown {
	var totalWeight float64
	for _, measure := range measures {
		if measure.Measured {
			totalWeight += measure.Weight
		}
	}

	breakdown := Breakdown{}

	// `totalWeight` nul couvre deux cas d'un coup : rien n'a été mesuré, ou tout
	// ce qui l'a été pèse zéro. Dans les deux, aucun score n'est calculable — et
	// rendre zéro serait un jugement, le plus sévère possible sur une release
	// qu'on n'a pas évaluée.
	if totalWeight == 0 {
		for _, measure := range measures {
			breakdown.Signals = append(breakdown.Signals, retire(measure))
			breakdown.Redistributed = append(breakdown.Redistributed, measure.Key)
		}
		return breakdown
	}

	var weighted float64
	for _, measure := range measures {
		if !measure.Measured {
			breakdown.Signals = append(breakdown.Signals, retire(measure))
			breakdown.Redistributed = append(breakdown.Redistributed, measure.Key)
			continue
		}

		// Le poids effectif est la part du composant dans ce qui a été mesuré.
		// C'est la redistribution : elle se fait par renormalisation, sans
		// réattribuer arbitrairement le poids manquant à un voisin.
		effective := measure.Weight / totalWeight * 100
		weighted += measure.Value * measure.Weight / totalWeight

		breakdown.Signals = append(breakdown.Signals, SignalScore{
			Key: measure.Key, Label: measure.Label, Signal: measure.Signal,
			Value: measure.Value, Weight: effective, Declared: measure.Weight,
			Measured: true, Detail: measure.Detail,
		})
	}

	breakdown.Score = int(math.Round(weighted))
	breakdown.Measured = true
	return breakdown
}

// retire rend la ligne d'un composant sorti du calcul.
//
// Son poids déclaré est conservé : c'est ce qui permet de lire « ce geste aurait
// pesé 20 » sur un pipeline qui n'a pas tourné, plutôt qu'une case muette.
func retire(measure Measure) SignalScore {
	return SignalScore{
		Key: measure.Key, Label: measure.Label, Signal: measure.Signal,
		Declared: measure.Weight, Detail: measure.Detail,
	}
}

// FromReport rend la mesure d'un signal à partir d'un rapport de tests.
//
// La règle est celle de B11 : 100 si tout passe, sinon `100 × (1 − échecs/total)`,
// **un test instable comptant pour un demi-échec**. Un test instable coûte à
// chaque exécution sans jamais bloquer : ne pas le compter du tout reviendrait à
// dire qu'il ne coûte rien.
func FromReport(signal Signal, report Report, ran bool) Measure {
	if !ran || report.Total == 0 {
		reason := "non exécuté"
		if ran {
			reason = "aucun test"
		}
		return Measure{Signal: signal, Detail: reason}
	}

	failures := float64(report.Failed) + float64(report.Flaky)/2
	value := math.Max(0, 100*(1-failures/float64(report.Total)))

	detail := fmt.Sprintf("%d/%d réussis", report.Passed, report.Total)
	if report.Flaky > 0 {
		detail += fmt.Sprintf(", %d instable(s)", report.Flaky)
	}

	return Measure{Signal: signal, Value: value, Measured: true, Detail: detail}
}

// FromSecurity rend la mesure du signal de sécurité.
//
// Par palier et non proportionnelle : une vulnérabilité critique n'est pas « un
// peu moins bien » que zéro, elle interdit de livrer. Une moyenne la noierait
// dans le nombre de findings mineurs.
func FromSecurity(critical, high int, ran bool) Measure {
	if !ran {
		return Measure{Signal: SignalSecurity, Detail: "non exécuté"}
	}

	switch {
	case critical > 0:
		return Measure{Signal: SignalSecurity, Value: 0, Measured: true,
			Detail: fmt.Sprintf("%d finding(s) critique(s)", critical)}
	case high > 0:
		return Measure{Signal: SignalSecurity, Value: 50, Measured: true,
			Detail: fmt.Sprintf("%d finding(s) de sévérité haute", high)}
	default:
		return Measure{Signal: SignalSecurity, Value: 100, Measured: true,
			Detail: "aucun finding critique ni haut"}
	}
}

// FromBench rend la mesure du signal de performance.
func FromBench(verdict string, comparable bool) Measure {
	if !comparable {
		// Une campagne non comparable n'est pas une campagne absente, mais elle ne
		// peut pas noter la performance : la juger imputerait à la release une
		// lenteur causée par un voisin (B16).
		return Measure{Signal: SignalPerformance,
			Detail: "mesure non comparable aux conditions de référence"}
	}

	switch verdict {
	case "pass":
		return Measure{Signal: SignalPerformance, Value: 100, Measured: true, Detail: "conforme"}
	case "warn":
		return Measure{Signal: SignalPerformance, Value: 60, Measured: true, Detail: "limite"}
	case "fail":
		return Measure{Signal: SignalPerformance, Value: 0, Measured: true, Detail: "hors seuil"}
	default:
		return Measure{Signal: SignalPerformance, Detail: "non mesuré"}
	}
}

// FromStatic rend la mesure de qualité du code.
//
// Par palier : la maquette dit « 100 si PHPStan et cs-fixer sont propres, dégradé
// par palier ». Les paliers sont fixés ici parce qu'ils ne sont pas dans la
// pondération — celle-ci dit le poids du signal, pas la façon de le mesurer.
func FromStatic(violations int, ran bool) Measure {
	if !ran {
		return Measure{Signal: SignalCode, Detail: "non exécuté"}
	}

	value := 0.0
	switch {
	case violations == 0:
		value = 100
	case violations <= 10:
		value = 80
	case violations <= 50:
		value = 60
	case violations <= 200:
		value = 30
	}

	return Measure{Signal: SignalCode, Value: value, Measured: true,
		Detail: fmt.Sprintf("%d violation(s)", violations)}
}

// Health dérive le statut global du score, selon les seuils de la maquette.
func Health(breakdown Breakdown) string {
	if !breakdown.Measured {
		return "na"
	}
	return healthFromValue(float64(breakdown.Score))
}

// SignalHealth dérive le statut d'un signal, aux mêmes seuils que le score
// global.
//
// Rendu par l'API plutôt que recalculé dans le navigateur : l'interface ne
// dérive aucun verdict, sans quoi un changement de seuil ici produirait un
// second avis divergent côté client (B11). Appliquer les seuils du score à un
// signal est une extrapolation, et c'est le seul endroit où elle est faite —
// elle vaut ce que vaut l'échelle 85/70, ni plus ni moins.
func SignalHealth(signal SignalScore) string {
	if !signal.Measured {
		return "na"
	}
	return healthFromValue(signal.Value)
}

func healthFromValue(value float64) string {
	switch {
	case value >= 85:
		return "healthy"
	case value >= 70:
		return "warn"
	default:
		return "critical"
	}
}

// Ce qui a disparu ici : le tri par signal.
//
// Il imposait l'ordre de la maquette — unitaires, intégration, E2E, code,
// sécurité, performance — parce que le signal faisait l'identité et qu'une carte
// Go n'a pas d'ordre. Le composant est désormais un geste, et les gestes
// **arrivent dans l'ordre du plan** : celui que quelqu'un a déclaré, et donc
// celui qu'il s'attend à lire. Trier ici l'écraserait.
