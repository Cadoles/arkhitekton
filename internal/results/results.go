// Package results lit les sorties des outils de test et les normalise.
//
// Six outils, six formats, une seule question : **qu'est-ce qui a changé depuis
// la dernière fois ?** C'est celle qu'on pose en recette, et elle ne se répond
// que si les résultats sont comparables d'un run à l'autre. Toute la
// normalisation existe pour cela.
//
// Deux principes en découlent.
//
// **L'identité d'un cas est stable.** `suite` + `name` désignent le même test
// d'une exécution à l'autre, quel que soit son ordre, sa durée ou le nombre de
// tests autour de lui. Sans cette stabilité, « nouvel échec » et « échec
// persistant » seraient indiscernables — et c'est exactement la distinction qui
// décide si on livre.
//
// **Un outil qui n'a pas tourné n'est pas un outil sans résultat.** PHPStan qui
// plante et PHPStan qui ne trouve rien produisent tous deux zéro violation. Le
// descripteur `meta`, émis par le pipeline lui-même, tranche — le dashboard ne
// peut pas le déduire (P2, doc 10).
//
// Tout y est pur : ni base, ni réseau, ni système de fichiers.
package results

import (
	"fmt"
	"strings"
)

// Family regroupe les résultats par nature.
type Family string

const (
	FamilyUnit        Family = "unit"
	FamilyIntegration Family = "integration"
	FamilyE2E         Family = "e2e"
	FamilyStatic      Family = "static"
	FamilySecurity    Family = "security"
	FamilyLoad        Family = "load"
)

// Status est le sort d'un cas de test.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
	// StatusFlaky : le cas a fini par passer après une ou plusieurs reprises. Ni
	// un succès ni un échec — il compte pour un demi-échec dans le score (B11),
	// parce qu'un test instable coûte à chaque exécution sans jamais bloquer.
	StatusFlaky Status = "flaky"
)

// Case est un cas de test normalisé.
type Case struct {
	// Suite et Name forment l'identité du cas, stable d'un run à l'autre. C'est
	// sur ce couple que se fait la comparaison à la release précédente.
	Suite string
	Name  string
	Status
	DurationMS int
	// Message est la raison de l'échec, sur une ligne. La trace complète vit à
	// part : c'est le message qu'on lit d'abord, et la trace qu'on déplie.
	Message string
	Trace   string
}

// Key rend l'identité du cas.
//
// Fonction plutôt que champ calculé : la concaténation doit être la même partout,
// et un champ qu'on oublie de remplir donnerait deux cas identiques réputés
// distincts.
func (c Case) Key() string { return c.Suite + "::" + c.Name }

// Report est le résultat normalisé d'un outil.
type Report struct {
	Family Family
	Tool   string
	// Status est le verdict d'ensemble, dérivé des cas — sauf si le descripteur
	// `meta` dit que l'outil a échoué, auquel cas rien n'est dérivable.
	Status  string
	Total   int
	Passed  int
	Failed  int
	Skipped int
	Flaky   int
	// DurationMS est la durée cumulée. Zéro quand l'outil ne la rapporte pas —
	// distinct d'une exécution instantanée, mais l'interface ne l'affiche alors
	// pas plutôt que d'afficher « 0 ms ».
	DurationMS int64
	Summary    string
	Cases      []Case
}

// Statuts d'ensemble d'un rapport.
const (
	ReportPass = "pass"
	ReportWarn = "warn"
	ReportFail = "fail"
	ReportSkip = "skip"
	ReportNA   = "na"
)

// tally recompte les cas et pose le verdict d'ensemble.
//
// Recompté plutôt que repris des totaux annoncés par l'outil : ceux-ci mentent
// régulièrement — un JUnit rapporte `tests="12"` en ne listant que dix cas quand
// le processus meurt en cours de route. Compter ce qui est réellement là évite
// d'afficher deux tests fantômes en succès.
func (r *Report) tally() {
	r.Total, r.Passed, r.Failed, r.Skipped, r.Flaky = 0, 0, 0, 0, 0

	for _, testCase := range r.Cases {
		r.Total++
		switch testCase.Status {
		case StatusPassed:
			r.Passed++
		case StatusFailed:
			r.Failed++
		case StatusSkipped:
			r.Skipped++
		case StatusFlaky:
			r.Flaky++
		}
	}

	switch {
	case r.Total == 0:
		r.Status = ReportNA
	case r.Failed > 0:
		r.Status = ReportFail
	case r.Flaky > 0:
		// Un test instable ne bloque pas la livraison, mais le taire ferait
		// disparaître le seul signal qui existe sur lui.
		r.Status = ReportWarn
	default:
		r.Status = ReportPass
	}
}

// ErrUnsupported signale un format non reconnu.
type ErrUnsupported struct {
	Tool   string
	Detail string
}

func (e ErrUnsupported) Error() string {
	return fmt.Sprintf("format de %q non reconnu: %s", e.Tool, e.Detail)
}

// firstLine rend la première ligne non vide d'un texte.
//
// Les messages d'échec des outils PHP tiennent souvent sur dix lignes dont la
// première porte l'essentiel ; les suivantes sont la trace. Afficher les dix dans
// un tableau rend le tableau illisible.
func firstLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
