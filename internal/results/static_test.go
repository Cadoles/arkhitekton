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
		t.Fatalf("lecture: %v", err)
	}
	// Deux violations de fichier plus l'erreur d'analyse.
	if report.Total != 3 || report.Failed != 3 {
		t.Errorf("comptes = %+v", report)
	}
	if report.Status != ReportFail {
		t.Errorf("statut = %q", report.Status)
	}
}

// L'identité est fichier + règle, jamais le numéro de ligne : une ligne bouge à
// chaque commit, et l'y attacher signalerait une violation résolue et une
// nouvelle à chaque insertion au-dessus.
func TestViolationIdentityIgnoresTheLineNumber(t *testing.T) {
	report, _ := ParsePHPStan([]byte(phpstanJSON))

	for _, testCase := range report.Cases {
		if testCase.Name == "missingType.return" {
			if testCase.Key() != "/app/src/Controller/HomeController.php::missingType.return" {
				t.Errorf("identité = %q", testCase.Key())
			}
			// La ligne reste dans le message : utile à lire, sans entrer dans
			// l'identité.
			if testCase.Message == "" {
				t.Error("la ligne doit rester lisible dans le message")
			}
			return
		}
	}
	t.Error("violation introuvable")
}

// Une erreur hors fichier est un échec de l'outil, pas du code : la compter
// comme une violation ferait chercher un problème dans le code qui n'y est pas.
func TestAnalysisErrorsAreDistinguished(t *testing.T) {
	report, _ := ParsePHPStan([]byte(phpstanJSON))

	for _, testCase := range report.Cases {
		if testCase.Suite == "phpstan" && testCase.Name == "erreur d'analyse" {
			return
		}
	}
	t.Error("l'erreur d'analyse doit être distinguée des violations")
}

// PHP sérialise un objet vide en tableau vide. Refuser `files: []` ferait
// échouer la lecture d'une analyse parfaitement propre.
func TestPHPStanAcceptsAnEmptyFilesArray(t *testing.T) {
	report, err := ParsePHPStan([]byte(`{"totals":{"errors":0,"file_errors":0},"files":[],"errors":[]}`))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	// Zéro cas, mais l'outil a tourné : `pass` et non `na`.
	if report.Status != ReportPass {
		t.Errorf("statut = %q, attendu pass", report.Status)
	}
}

func TestParsePHPStanRejectsGarbage(t *testing.T) {
	if _, err := ParsePHPStan([]byte("<html>")); err == nil {
		t.Error("un rapport illisible doit être refusé")
	}
}

const csFixerJSON = `{
  "files": [
    {"name": "src/Kernel.php", "appliedFixers": ["no_unused_imports", "blank_line_after_opening_tag"],
     "diff": "--- a\n+++ b\n"},
    {"name": "src/Legacy.php", "appliedFixers": []}
  ]
}`

// Une entrée par règle et non par fichier : c'est la granularité à laquelle on
// corrige, et celle à laquelle « c'est réglé » a un sens.
func TestCSFixerReportsOneCasePerRule(t *testing.T) {
	report, err := ParseCSFixer([]byte(csFixerJSON))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if report.Total != 3 {
		t.Errorf("total = %d, attendu 3", report.Total)
	}

	keys := map[string]bool{}
	for _, testCase := range report.Cases {
		keys[testCase.Key()] = true
	}
	if !keys["src/Kernel.php::no_unused_imports"] {
		t.Errorf("identités = %v", keys)
	}
	// Un fichier sans règle nommée reste signalé.
	if !keys["src/Legacy.php::style"] {
		t.Errorf("identités = %v", keys)
	}
}

func TestCSFixerCleanRunIsAPass(t *testing.T) {
	report, err := ParseCSFixer([]byte(`{"files":[]}`))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if report.Status != ReportPass {
		t.Errorf("statut = %q", report.Status)
	}
}

// L'ordre d'itération d'une map est aléatoire : sans tri, deux lectures du même
// rapport donneraient deux ordres différents.
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
				t.Fatalf("ordre instable: %q vs %q",
					first.Cases[index].Key(), next.Cases[index].Key())
			}
		}
	}
	if first.Cases[0].Suite != "a.php" {
		t.Errorf("premier cas = %q, attendu l'ordre alphabétique", first.Cases[0].Suite)
	}
}
