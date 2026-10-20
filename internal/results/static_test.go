package results

import "testing"

const phpstanJSON = `{
  "totals": {"errors": 1, "file_errors": 2},
  "files": {
    "/app/src/Controller/HomeController.php": {
      "errors": 2,
      "messages": [
        {"message": "Method has no return type specified.", "line": 42,
         "identifier": "missingType.return", "ignorable": true},
        {"message": "Undefined variable: $user", "line": 88,
         "identifier": "variable.undefined", "ignorable": true}
      ]
    }
  },
  "errors": ["Configuration file not found: phpstan.neon"]
}`

func TestParsePHPStanProjectsViolationsAsCases(t *testing.T) {
	report, err := ParsePHPStan([]byte(phpstanJSON))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Two file violations plus the analysis error.
	if report.Total != 3 || report.Failed != 3 {
		t.Errorf("counts = %+v", report)
	}
	if report.Status != ReportFail {
		t.Errorf("status = %q", report.Status)
	}
}

// The identity is file + rule, never the line number: a line moves with every
// commit, and tying identity to it would report one violation resolved and one new
// at every insertion above.
func TestViolationIdentityIgnoresTheLineNumber(t *testing.T) {
	report, _ := ParsePHPStan([]byte(phpstanJSON))

	for _, testCase := range report.Cases {
		if testCase.Name == "missingType.return" {
			if testCase.Key() != "/app/src/Controller/HomeController.php::missingType.return" {
				t.Errorf("identity = %q", testCase.Key())
			}
			// The line stays in the message: useful to read, without entering the identity.
			if testCase.Message == "" {
				t.Error("the line must stay readable in the message")
			}
			return
		}
	}
	t.Error("violation not found")
}

// An error outside a file is a failure of the tool, not of the code: counting it as
// a violation would send someone looking for a problem in the code that is not
// there.
func TestAnalysisErrorsAreDistinguished(t *testing.T) {
	report, _ := ParsePHPStan([]byte(phpstanJSON))

	for _, testCase := range report.Cases {
		if testCase.Suite == "phpstan" && testCase.Name == "erreur d'analyse" {
			return
		}
	}
	t.Error("the analysis error must be told apart from violations")
}

// PHP serialises an empty object as an empty array. Refusing `files: []` would fail
// the read of a perfectly clean analysis.
func TestPHPStanAcceptsAnEmptyFilesArray(t *testing.T) {
	report, err := ParsePHPStan([]byte(`{"totals":{"errors":0,"file_errors":0},"files":[],"errors":[]}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Zero cases, but the tool ran: `pass`, not `na`.
	if report.Status != ReportPass {
		t.Errorf("status = %q, expected pass", report.Status)
	}
}

func TestParsePHPStanRejectsGarbage(t *testing.T) {
	if _, err := ParsePHPStan([]byte("<html>")); err == nil {
		t.Error("an unreadable report must be refused")
	}
}

const csFixerJSON = `{
  "files": [
    {"name": "src/Kernel.php", "appliedFixers": ["no_unused_imports", "blank_line_after_opening_tag"],
     "diff": "--- a\n+++ b\n"},
    {"name": "src/Legacy.php", "appliedFixers": []}
  ]
}`

// One entry per rule rather than per file: that is the granularity at which one
// fixes, and at which "it is done" means something.
func TestCSFixerReportsOneCasePerRule(t *testing.T) {
	report, err := ParseCSFixer([]byte(csFixerJSON))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if report.Total != 3 {
		t.Errorf("total = %d, expected 3", report.Total)
	}

	keys := map[string]bool{}
	for _, testCase := range report.Cases {
		keys[testCase.Key()] = true
	}
	if !keys["src/Kernel.php::no_unused_imports"] {
		t.Errorf("identities = %v", keys)
	}
	// A file with no named rule is still reported.
	if !keys["src/Legacy.php::style"] {
		t.Errorf("identities = %v", keys)
	}
}

func TestCSFixerCleanRunIsAPass(t *testing.T) {
	report, err := ParseCSFixer([]byte(`{"files":[]}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if report.Status != ReportPass {
		t.Errorf("status = %q", report.Status)
	}
}

// A Go map's iteration order is random: without sorting, two reads of the same
// report would give two different orders.
func TestPHPStanOutputIsStable(t *testing.T) {
	const multi = `{"totals":{"errors":0,"file_errors":3},"files":{
	  "c.php":{"errors":1,"messages":[{"message":"m","line":1,"identifier":"r"}]},
	  "a.php":{"errors":1,"messages":[{"message":"m","line":1,"identifier":"r"}]},
	  "b.php":{"errors":1,"messages":[{"message":"m","line":1,"identifier":"r"}]}
	},"errors":[]}`

	first, _ := ParsePHPStan([]byte(multi))
	for range 5 {
		next, _ := ParsePHPStan([]byte(multi))
		for index := range first.Cases {
			if first.Cases[index].Key() != next.Cases[index].Key() {
				t.Fatalf("unstable order: %q vs %q",
					first.Cases[index].Key(), next.Cases[index].Key())
			}
		}
	}
	if first.Cases[0].Suite != "a.php" {
		t.Errorf("first case = %q, expected alphabetical order", first.Cases[0].Suite)
	}
}
