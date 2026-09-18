package results

import "testing"

const phpunitXML = `<?xml version="1.0"?>
<testsuites>
  <testsuite name="Unit" tests="4" time="1.250">
    <testsuite name="App\Tests\LoginTest" time="0.750">
      <testcase name="testLogin" classname="App\Tests\LoginTest" time="0.500"/>
      <testcase name="testLogout" classname="App\Tests\LoginTest" time="0.250">
        <failure message="Failed asserting that false is true.&#10;in LoginTest.php:42" type="PHPUnit\Framework\ExpectationFailedException">
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

// Cypress sometimes emits a bare <testsuite>. Handling only <testsuites> would
// read zero tests from a successful end-to-end campaign.
const bareSuiteXML = `<testsuite name="e2e" tests="1" time="3.0">
  <testcase name="visits the home page" time="3.0"/>
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

// Two `testLogin` tests in different classes are two tests. Merging them would
// report a resolved failure where nothing moved.
func TestHomonymousCasesStayDistinct(t *testing.T) {
	report, _ := ParseJUnit(FamilyUnit, "phpunit", []byte(phpunitXML))

	keys := map[string]bool{}
	for _, testCase := range report.Cases {
		if keys[testCase.Key()] {
			t.Errorf("duplicate identity: %q", testCase.Key())
		}
		keys[testCase.Key()] = true
	}
	if !keys[`App\Tests\LoginTest::testLogin`] || !keys[`App\Tests\CartTest::testLogin`] {
		t.Errorf("identities = %v", keys)
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

// JUnit time is in floating-point seconds. Taking it for milliseconds would display
// three-millisecond campaigns.
func TestJUnitTimeIsSeconds(t *testing.T) {
	report, _ := ParseJUnit(FamilyE2E, "cypress", []byte(bareSuiteXML))
	if report.Cases[0].DurationMS != 3000 {
		t.Errorf("duration = %d ms, expected 3000", report.Cases[0].DurationMS)
	}
}

// The failure message fits on one line in the table; the trace unfolds.
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
			t.Error("the trace must be kept apart")
		}
		return
	}
	t.Error("no failure found")
}

// A case that passed on retry is neither a success nor a failure: treating it as a
// success would erase the only signal of instability.
func TestRerunCasesAreFlaky(t *testing.T) {
	const xml = `<testsuites><testsuite name="e2e">
	  <testcase name="cart" classname="Cart" time="1.0">
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
	// Flaky degrades without blocking.
	if report.Status != ReportWarn {
		t.Errorf("status = %q, expected warn", report.Status)
	}
}

// A JUnit report announces tests="12" while listing only ten cases when the process
// dies mid-run. Counting what is actually there avoids two phantom passing tests.
func TestTotalsAreRecountedNotTrusted(t *testing.T) {
	const xml = `<testsuites><testsuite name="Unit" tests="12">
	  <testcase name="a" classname="X"/>
	</testsuite></testsuites>`

	report, _ := ParseJUnit(FamilyUnit, "phpunit", []byte(xml))
	if report.Total != 1 {
		t.Errorf("total = %d, expected the real number of cases", report.Total)
	}
}

func TestParseJUnitRejectsGarbage(t *testing.T) {
	if _, err := ParseJUnit(FamilyUnit, "phpunit", []byte("not xml")); err == nil {
		t.Error("an unreadable report must be refused")
	}
}

// An empty report is not a successful report: nobody ran anything.
func TestAnEmptyReportIsNotAPass(t *testing.T) {
	report, err := ParseJUnit(FamilyUnit, "phpunit", []byte(`<testsuites></testsuites>`))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if report.Status != ReportNA {
		t.Errorf("status = %q, expected na", report.Status)
	}
}
