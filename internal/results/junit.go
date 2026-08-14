package results

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

/*
JUnit XML — le format commun de PHPUnit, Cypress et de la plupart des lanceurs.

« Commun » est un abus de langage : il n'a pas de schéma normatif, et chaque outil
en produit une variante. Trois écarts comptent ici, et chacun a une conséquence
visible s'il est ignoré.

  - **La racine varie.** PHPUnit émet `<testsuites>` englobant, Cypress émet
    parfois `<testsuite>` seul. Ne gérer que le premier ferait lire zéro test sur
    une campagne E2E réussie.

  - **Les suites s'imbriquent.** Un `<testsuite>` peut en contenir d'autres. À plat,
    les noms de classes disparaîtraient et deux tests homonymes de fichiers
    différents fusionneraient — c'est-à-dire un faux « échec résolu ».

  - **Le temps est en secondes flottantes**, pas en millisecondes, et il manque
    parfois.
*/

type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	XMLName xml.Name     `xml:"testsuite"`
	Name    string       `xml:"name,attr"`
	Time    string       `xml:"time,attr"`
	Suites  []junitSuite `xml:"testsuite"`
	Cases   []junitCase  `xml:"testcase"`
}

type junitCase struct {
	Name string `xml:"name,attr"`
	// ClassName est souvent plus précis que le nom de la suite parente : PHPUnit y
	// met la classe pleinement qualifiée, là où la suite porte le nom du fichier.
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failures  []junitDetail `xml:"failure"`
	Errors    []junitDetail `xml:"error"`
	Skipped   *struct{}     `xml:"skipped"`
	// Rerun et Flaky : Cypress et PHPUnit 10 signalent ainsi un cas repassé après
	// reprise. Les traiter en succès effacerait le seul signal d'instabilité.
	FlakyFailures []junitDetail `xml:"flakyFailure"`
	RerunFailures []junitDetail `xml:"rerunFailure"`
}

type junitDetail struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Content string `xml:",chardata"`
}

// ParseJUnit lit un rapport JUnit XML.
func ParseJUnit(family Family, tool string, raw []byte) (Report, error) {
	report := Report{Family: family, Tool: tool}

	var root junitSuites
	if err := xml.Unmarshal(raw, &root); err != nil {
		// La racine n'est pas `<testsuites>` : essayer `<testsuite>` seul avant de
		// renoncer. C'est le cas de Cypress, et renoncer ici ferait apparaître une
		// campagne E2E réussie comme non exécutée.
		var single junitSuite
		if single.unmarshal(raw) != nil {
			return report, ErrUnsupported{Tool: tool, Detail: err.Error()}
		}
		root.Suites = []junitSuite{single}
	}

	for _, suite := range root.Suites {
		collectSuite(suite, "", &report)
	}

	report.DurationMS = junitDuration(root.Suites)
	report.tally()
	return report, nil
}

func (s *junitSuite) unmarshal(raw []byte) error { return xml.Unmarshal(raw, s) }

// collectSuite descend l'arbre des suites en conservant le chemin.
//
// Le chemin est ce qui donne au cas une identité stable : deux tests `testLogin`
// dans deux classes différentes doivent rester deux tests, sans quoi la
// comparaison à la release précédente signalerait un échec résolu là où rien n'a
// bougé.
func collectSuite(suite junitSuite, path string, report *Report) {
	name := strings.TrimSpace(suite.Name)
	if path != "" && name != "" {
		name = path + " / " + name
	} else if name == "" {
		name = path
	}

	for _, testCase := range suite.Cases {
		report.Cases = append(report.Cases, junitToCase(testCase, name))
	}
	for _, nested := range suite.Suites {
		collectSuite(nested, name, report)
	}
}

func junitToCase(source junitCase, suiteName string) Case {
	// `classname` prime : PHPUnit y met la classe pleinement qualifiée, là où la
	// suite porte le nom du fichier. Un renommage de fichier ne doit pas casser
	// l'identité du test.
	suite := strings.TrimSpace(source.ClassName)
	if suite == "" {
		suite = suiteName
	}

	testCase := Case{
		Suite: suite, Name: strings.TrimSpace(source.Name),
		Status: StatusPassed, DurationMS: junitMillis(source.Time),
	}

	details := append(append([]junitDetail{}, source.Failures...), source.Errors...)
	reruns := append(append([]junitDetail{}, source.FlakyFailures...), source.RerunFailures...)

	switch {
	case len(details) > 0:
		testCase.Status = StatusFailed
		testCase.Message = firstLine(detailMessage(details[0]))
		testCase.Trace = strings.TrimSpace(details[0].Content)
	case len(reruns) > 0:
		// Le cas a fini par passer : instable, pas en échec.
		testCase.Status = StatusFlaky
		testCase.Message = firstLine(detailMessage(reruns[0]))
		testCase.Trace = strings.TrimSpace(reruns[0].Content)
	case source.Skipped != nil:
		testCase.Status = StatusSkipped
	}

	return testCase
}

func detailMessage(detail junitDetail) string {
	if message := strings.TrimSpace(detail.Message); message != "" {
		return message
	}
	// Certains outils laissent `message` vide et mettent tout dans le corps.
	return strings.TrimSpace(detail.Content)
}

// junitMillis convertit un temps JUnit en millisecondes.
//
// Le format est un flottant en **secondes**. Le prendre pour des millisecondes
// afficherait des campagnes de trois millisecondes, ce qui passe inaperçu
// longtemps.
func junitMillis(value string) int {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return int(seconds * 1000)
}

func junitDuration(suites []junitSuite) int64 {
	var total int64
	for _, suite := range suites {
		total += int64(junitMillis(suite.Time))
	}
	return total
}

// SummarizeJUnit rend la ligne de résumé d'un rapport.
func SummarizeJUnit(report Report) string {
	parts := []string{fmt.Sprintf("%d test(s)", report.Total)}
	if report.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d en échec", report.Failed))
	}
	if report.Flaky > 0 {
		parts = append(parts, fmt.Sprintf("%d instable(s)", report.Flaky))
	}
	if report.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d ignoré(s)", report.Skipped))
	}
	return strings.Join(parts, ", ")
}
