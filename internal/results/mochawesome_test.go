package results

import "testing"

const mochaJSON = `{
  "stats": {"suites": 2, "tests": 4, "duration": 12500},
  "results": [{
    "file": "cypress/e2e/panier.cy.js",
    "suites": [{
      "title": "Panier",
      "file": "cypress/e2e/panier.cy.js",
      "tests": [
        {"title": "ajoute un article", "fullTitle": "Panier ajoute un article",
         "duration": 1200, "pass": true, "fail": false, "pending": false, "currentRetry": 0},
        {"title": "valide la commande", "fullTitle": "Panier valide la commande",
         "duration": 3400, "pass": false, "fail": true, "pending": false, "currentRetry": 2,
         "err": {"message": "Timed out retrying after 4000ms\nExpected to find element", "estack": "at Context.<anonymous>"}},
        {"title": "vide le panier", "fullTitle": "Panier vide le panier",
         "duration": 800, "pass": true, "fail": false, "pending": false, "currentRetry": 1,
         "err": {"message": "Timed out retrying after 4000ms"}},
        {"title": "à écrire", "fullTitle": "Panier à écrire",
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
		t.Fatalf("lecture: %v", err)
	}
	if report.Total != 4 || report.Passed != 1 || report.Failed != 1 || report.Skipped != 1 {
		t.Errorf("comptes = %+v", report)
	}
	if report.DurationMS != 12500 {
		t.Errorf("durée = %d", report.DurationMS)
	}
}

// Un cas repassé après reprise porte `currentRetry > 0` **et** `pass: true`.
// Le lire comme un succès effacerait le seul signal d'instabilité de la
// campagne E2E.
func TestARetriedPassIsFlaky(t *testing.T) {
	report, _ := ParseMochawesome([]byte(mochaJSON))

	for _, testCase := range report.Cases {
		if testCase.Name == "Panier vide le panier" {
			if testCase.Status != StatusFlaky {
				t.Errorf("statut = %q, attendu flaky", testCase.Status)
			}
			return
		}
	}
	t.Error("cas introuvable")
}

// Renommer un `describe` ne doit pas rendre tous ses tests « nouveaux » à la
// comparaison suivante : la suite est identifiée par son fichier.
func TestSuiteIdentityIsAnchoredOnTheFile(t *testing.T) {
	report, _ := ParseMochawesome([]byte(mochaJSON))

	for _, testCase := range report.Cases {
		if testCase.Suite == "" {
			t.Errorf("cas sans suite: %+v", testCase)
		}
		if testCase.Suite[:len("cypress/e2e")] != "cypress/e2e" {
			t.Errorf("suite = %q, attendue ancrée sur le fichier", testCase.Suite)
		}
	}
}

// Cypress produit cet état quand la spec meurt avant d'exécuter le cas :
// l'ignorer perdrait un test.
func TestACaseThatNeverRanIsKept(t *testing.T) {
	const orphan = `{"stats":{"duration":0},"results":[{"file":"a.cy.js","tests":[
	  {"title":"orphelin","fullTitle":"orphelin","pass":false,"fail":false,"pending":false}
	],"suites":[]}]}`

	report, err := ParseMochawesome([]byte(orphan))
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if report.Total != 1 || report.Skipped != 1 {
		t.Errorf("comptes = %+v", report)
	}
	if report.Cases[0].Message == "" {
		t.Error("le motif doit être dit")
	}
}

func TestParseMochawesomeRejectsGarbage(t *testing.T) {
	if _, err := ParseMochawesome([]byte("nope")); err == nil {
		t.Error("un rapport illisible doit être refusé")
	}
}
