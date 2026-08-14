package results

import "testing"

const phpunitXML = `<?xml version="1.0"?>
<testsuites>
  <testsuite name="Unit" tests="4" time="1.250">
    <testsuite name="App\Tests\LoginTest" time="0.750">
      <testcase name="testLogin" classname="App\Tests\LoginTest" time="0.500"/>
      <testcase name="testLogout" classname="App\Tests\LoginTest" time="0.250">
        <failure message="Failed asserting that false is true.&#10;dans LoginTest.php:42" type="PHPUnit\Framework\ExpectationFailedException">
#0 /app/tests/LoginTest.php(42)
#1 /app/vendor/bin/phpunit(12)
        </failure>
      </testcase>
    </testsuite>
    <testsuite name="App\Tests\CartTest" time="0.500">
      <testcase name="testLogin" classname="App\Tests\CartTest" time="0.500"/>
      <testcase name="testSkipped" classname="App\Tests\CartTest"><skipped/></testcase>
    </testsuite>
  </testsuite>
</testsuites>`

// Cypress émet parfois `<testsuite>` seul. Ne gérer que `<testsuites>` ferait
// lire zéro test sur une campagne E2E réussie.
const bareSuiteXML = `<testsuite name="e2e" tests="1" time="3.0">
  <testcase name="visite la page d'accueil" time="3.0"/>
</testsuite>`

func TestParseJUnitReadsNestedSuites(t *testing.T) {
	report, err := ParseJUnit(FamilyUnit, "phpunit", []byte(phpunitXML))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}

	if report.Total != 4 || report.Passed != 2 || report.Failed != 1 || report.Skipped != 1 {
		t.Errorf("comptes = %+v", report)
	}
	if report.Status != ReportFail {
		t.Errorf("statut = %q", report.Status)
	}
}

// Deux `testLogin` dans deux classes différentes sont deux tests. Les fusionner
// signalerait un échec résolu là où rien n'a bougé.
func TestHomonymousCasesStayDistinct(t *testing.T) {
	report, _ := ParseJUnit(FamilyUnit, "phpunit", []byte(phpunitXML))

	keys := map[string]bool{}
	for _, testCase := range report.Cases {
		if keys[testCase.Key()] {
			t.Errorf("identité dupliquée: %q", testCase.Key())
		}
		keys[testCase.Key()] = true
	}
	if !keys[`App\Tests\LoginTest::testLogin`] || !keys[`App\Tests\CartTest::testLogin`] {
		t.Errorf("identités = %v", keys)
	}
}

func TestParseJUnitAcceptsABareSuite(t *testing.T) {
	report, err := ParseJUnit(FamilyE2E, "cypress", []byte(bareSuiteXML))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if report.Total != 1 || report.Status != ReportPass {
		t.Errorf("rapport = %+v", report)
	}
}

// Le temps JUnit est en secondes flottantes. Le prendre pour des millisecondes
// afficherait des campagnes de trois millisecondes.
func TestJUnitTimeIsSeconds(t *testing.T) {
	report, _ := ParseJUnit(FamilyE2E, "cypress", []byte(bareSuiteXML))
	if report.Cases[0].DurationMS != 3000 {
		t.Errorf("durée = %d ms, attendu 3000", report.Cases[0].DurationMS)
	}
}

// Le message d'échec tient sur une ligne dans le tableau ; la trace se déplie.
func TestFailureMessageIsOneLine(t *testing.T) {
	report, _ := ParseJUnit(FamilyUnit, "phpunit", []byte(phpunitXML))

	for _, testCase := range report.Cases {
		if testCase.Status != StatusFailed {
			continue
		}
		if testCase.Message != "Failed asserting that false is true." {
			t.Errorf("message = %q", testCase.Message)
		}
		if testCase.Trace == "" {
			t.Error("la trace doit être conservée à part")
		}
		return
	}
	t.Error("aucun échec trouvé")
}

// Un cas repassé après reprise n'est ni un succès ni un échec : le traiter en
// succès effacerait le seul signal d'instabilité.
func TestRerunCasesAreFlaky(t *testing.T) {
	const xml = `<testsuites><testsuite name="e2e">
	  <testcase name="panier" classname="Cart" time="1.0">
	    <flakyFailure message="timeout">trace</flakyFailure>
	  </testcase>
	</testsuite></testsuites>`

	report, err := ParseJUnit(FamilyE2E, "cypress", []byte(xml))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if report.Flaky != 1 || report.Failed != 0 {
		t.Errorf("comptes = %+v", report)
	}
	// Instable dégrade sans bloquer.
	if report.Status != ReportWarn {
		t.Errorf("statut = %q, attendu warn", report.Status)
	}
}

// Un JUnit annonce `tests="12"` en ne listant que dix cas quand le processus
// meurt en route. Compter ce qui est là évite deux tests fantômes en succès.
func TestTotalsAreRecountedNotTrusted(t *testing.T) {
	const xml = `<testsuites><testsuite name="Unit" tests="12">
	  <testcase name="a" classname="X"/>
	</testsuite></testsuites>`

	report, _ := ParseJUnit(FamilyUnit, "phpunit", []byte(xml))
	if report.Total != 1 {
		t.Errorf("total = %d, attendu le nombre réel de cas", report.Total)
	}
}

func TestParseJUnitRejectsGarbage(t *testing.T) {
	if _, err := ParseJUnit(FamilyUnit, "phpunit", []byte("pas du xml")); err == nil {
		t.Error("un rapport illisible doit être refusé")
	}
}

// Un rapport vide n'est pas un rapport réussi : personne n'a rien exécuté.
func TestAnEmptyReportIsNotAPass(t *testing.T) {
	report, err := ParseJUnit(FamilyUnit, "phpunit", []byte(`<testsuites></testsuites>`))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if report.Status != ReportNA {
		t.Errorf("statut = %q, attendu na", report.Status)
	}
}
