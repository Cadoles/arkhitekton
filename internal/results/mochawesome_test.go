package results

import "testing"

const mochaJSON = `{
  "stats": {"suites": 2, "tests": 4, "duration": 12500},
  "results": [{
    "file": "cypress/e2e/cart.cy.js",
    "suites": [{
      "title": "Cart",
      "file": "cypress/e2e/cart.cy.js",
      "tests": [
        {"title": "adds an item", "fullTitle": "Cart adds an item",
         "duration": 1200, "pass": true, "fail": false, "pending": false, "currentRetry": 0},
        {"title": "confirms the order", "fullTitle": "Cart confirms the order",
         "duration": 3400, "pass": false, "fail": true, "pending": false, "currentRetry": 2,
         "err": {"message": "Timed out retrying after 4000ms\nExpected to find element", "estack": "at Context.<anonymous>"}},
        {"title": "empties the cart", "fullTitle": "Cart empties the cart",
         "duration": 800, "pass": true, "fail": false, "pending": false, "currentRetry": 1,
         "err": {"message": "Timed out retrying after 4000ms"}},
        {"title": "to be written", "fullTitle": "Cart to be written",
         "duration": 0, "pass": false, "fail": false, "pending": true, "currentRetry": 0}
      ],
      "suites": []
    }],
    "tests": []
  }]
}`

func TestParseMochawesome(t *testing.T) {
	report, err := ParseMochawesome([]byte(mochaJSON))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if report.Total != 4 || report.Passed != 1 || report.Failed != 1 || report.Skipped != 1 {
		t.Errorf("counts = %+v", report)
	}
	if report.DurationMS != 12500 {
		t.Errorf("duration = %d", report.DurationMS)
	}
}

// A case that passed on retry carries `currentRetry > 0` and `pass: true`. Reading
// it as a success would erase the campaign's only signal of instability.
func TestARetriedPassIsFlaky(t *testing.T) {
	report, _ := ParseMochawesome([]byte(mochaJSON))

	for _, testCase := range report.Cases {
		if testCase.Name == "Cart empties the cart" {
			if testCase.Status != StatusFlaky {
				t.Errorf("status = %q, expected flaky", testCase.Status)
			}
			return
		}
	}
	t.Error("case not found")
}

// Renaming a `describe` must not make all its tests "new" at the next comparison:
// the suite is identified by its file.
func TestSuiteIdentityIsAnchoredOnTheFile(t *testing.T) {
	report, _ := ParseMochawesome([]byte(mochaJSON))

	for _, testCase := range report.Cases {
		if testCase.Suite == "" {
			t.Errorf("case with no suite: %+v", testCase)
		}
		if testCase.Suite[:len("cypress/e2e")] != "cypress/e2e" {
			t.Errorf("suite = %q, expected to be anchored on the file", testCase.Suite)
		}
	}
}

// Cypress produces this state when the spec dies before running the case: ignoring
// it would lose a test.
func TestACaseThatNeverRanIsKept(t *testing.T) {
	const orphan = `{"stats":{"duration":0},"results":[{"file":"a.cy.js","tests":[
	  {"title":"orphelin","fullTitle":"orphelin","pass":false,"fail":false,"pending":false}
	],"suites":[]}]}`

	report, err := ParseMochawesome([]byte(orphan))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if report.Total != 1 || report.Skipped != 1 {
		t.Errorf("counts = %+v", report)
	}
	if report.Cases[0].Message == "" {
		t.Error("the reason must be stated")
	}
}

func TestParseMochawesomeRejectsGarbage(t *testing.T) {
	if _, err := ParseMochawesome([]byte("nope")); err == nil {
		t.Error("an unreadable report must be refused")
	}
}
