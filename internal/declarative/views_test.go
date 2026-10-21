package declarative

import (
	"os"
	"path/filepath"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/parsers"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// parsed reads one of the reference reports the interpreters are recorded against.
func parsed(t *testing.T, parser parse.ID, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "arkhitekton", "testdata", "reports", name))
	if err != nil {
		t.Fatal(err)
	}
	parsing, _ := parsers.Lookup(parser)
	payload, err := parsing(raw)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// Every row carries every field its view announces, and nothing else: a declaration is checked
// against the announced fields, and a row lacking one would render it empty without a word.
func TestEveryRowCarriesTheAnnouncedFields(t *testing.T) {
	samples := map[string]any{
		"junit.cases":      parsed(t, parse.JUnitXML, "junit.xml"),
		"mocha.tests":      parsed(t, parse.MochawesomeJSON, "mochawesome.json"),
		"phpstan.messages": parsed(t, parse.PHPStanJSON, "phpstan.json"),
		"phpstan.errors":   parse.PHPStan{Errors: []string{"Ignored error pattern was not matched"}},
		"csfixer.fixers":   parsed(t, parse.CSFixerJSON, "cs-fixer.json"),
	}
	for _, view := range Views() {
		payload, found := samples[view.Name]
		if !found {
			t.Errorf("view %s has no sample", view.Name)
			continue
		}
		rows := view.Rows(payload)
		if len(rows) == 0 {
			t.Errorf("view %s: no row from its sample", view.Name)
		}
		for _, row := range rows {
			for _, field := range view.Fields {
				if _, present := row[field]; !present && !optional(view.Name, field) {
					t.Errorf("view %s: row without %q: %v", view.Name, field, row)
				}
			}
			for field := range row {
				if !contains(view.Fields, field) {
					t.Errorf("view %s: unannounced field %q", view.Name, field)
				}
			}
			for field, values := range view.Enums {
				if !contains(values, row[field]) {
					t.Errorf("view %s: %s %q outside %v", view.Name, field, row[field], values)
				}
			}
		}
	}
}

// A JUnit case carries its detail only when it failed.
func optional(view, field string) bool {
	return view == "junit.cases" && (field == "detail" || field == "detail_type" || field == "trace")
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// A file listed by PHP-CS-Fixer with no fixer still deviates: it gets a row of its own.
func TestAFileWithNoFixerStillGetsARow(t *testing.T) {
	view, _ := Lookup("csfixer.fixers")
	rows := view.Rows(parse.CSFixer{Files: []parse.CSFixerFile{{Name: "src/A.php", Diff: "-a\n+b"}}})
	if len(rows) != 1 || rows[0]["file"] != "src/A.php" || rows[0]["fixer"] != "" {
		t.Errorf("rows = %v", rows)
	}
}
