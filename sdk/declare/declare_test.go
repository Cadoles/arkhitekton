package declare

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func mustTemplate(t *testing.T, source string) Template {
	t.Helper()
	template, err := ParseTemplate(source)
	if err != nil {
		t.Fatalf("%q: %v", source, err)
	}
	return template
}

// Each filter does one thing, and they chain left to right.
func TestTemplatesProjectFieldsThroughFilters(t *testing.T) {
	row := map[string]string{
		"message": "\n  expected 200\nbut got 500", "identifier": "", "file": "src/A.php",
		"line": "12", "name": "Login fails at line 42",
	}
	for source, expected := range map[string]string{
		"{file}":                                 "src/A.php",
		"line {line}: {message | firstline}":     "line 12: expected 200",
		"{identifier | or: message | firstline}": "expected 200",
		"{identifier | or: 'style'}":             "style",
		"{name | replace: ' at line \\d+' ''}":   "Login fails",
		"{name | lower}":                         "login fails at line 42",
		"{message | trim}":                       "expected 200\nbut got 500",
		"{absent}":                               "",
		"plain text":                             "plain text",
	} {
		if got := mustTemplate(t, source).Render(row); got != expected {
			t.Errorf("%q gives %q, expected %q", source, got, expected)
		}
	}
}

func TestATemplateNamesTheFieldsItReads(t *testing.T) {
	fields := mustTemplate(t, "{identifier | or: message | firstline} at {line}").Fields()
	if !slices.Equal(fields, []string{"identifier", "message", "line"}) {
		t.Errorf("fields = %v", fields)
	}
}

// The language is closed: what it does not know is refused when read, not ignored when run.
func TestAMalformedTemplateIsRefused(t *testing.T) {
	for _, source := range []string{
		"{file", "file}", "{File}", "{file | upper}", "{file | or}", "{file | firstline: x}",
		"{file | replace: '(' ''}", "{file | replace: x y}", "{file | or: 'unclosed}",
	} {
		if _, err := ParseTemplate(source); err == nil {
			t.Errorf("%q accepted", source)
		}
	}
}

const staticTests = `{
  "apiVersion": "arkhitekton.reading/v1",
  "tools": [{
    "tool": "cs-fixer", "task": "run-cs-fixer", "group": "static",
    "summary": {"violations": "style deviation(s)"}, "empty": "pass",
    "exitCodes": {"results": [0, 8]},
    "cases": [{
      "from": "csfixer.fixers", "suite": "{file}", "name": "{fixer | or: 'style'}",
      "status": "failed", "message": "style deviation: {fixer | or: 'style'}", "trace": "{diff}"
    }]
  }, {
    "tool": "phpunit", "task": "run-unit-tests", "group": "unit", "summary": "tests",
    "duration": "{duration_ms}",
    "cases": [{
      "from": "junit.cases", "suite": "{classname | or: path}", "name": "{name}",
      "status": {"from": "outcome", "map": {"passed": "passed", "failed": "failed"}}
    }]
  }]
}`

func TestADeclarationIsRead(t *testing.T) {
	reading, err := Decode([]byte(staticTests))
	if err != nil {
		t.Fatal(err)
	}
	fixer, unit := reading.Tools[0], reading.Tools[1]
	if fixer.Summary.Violations != "style deviation(s)" || !unit.Summary.Tests ||
		fixer.ExitCodes.Results[1] != 8 || fixer.Cases[0].Status.Fixed != "failed" ||
		unit.Cases[0].Status.Map["failed"] != "failed" {
		t.Errorf("reading = %+v", reading)
	}
	if fields := unit.Cases[0].Fields(); !slices.Contains(fields, "outcome") || !slices.Contains(fields, "path") {
		t.Errorf("fields = %v", fields)
	}
}

// A misspelt key is a rule that would silently not apply: it is refused.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	if _, err := Decode([]byte(strings.Replace(staticTests, `"trace"`, `"traces"`, 1))); err == nil {
		t.Error("an unknown key accepted")
	}
}

func TestAnInconsistentDeclarationIsRefused(t *testing.T) {
	for label, broken := range map[string]string{
		"version":   strings.Replace(staticTests, "reading/v1", "reading/v9", 1),
		"from":      strings.Replace(staticTests, `"csfixer.fixers"`, `"fixers"`, 1),
		"status":    strings.Replace(staticTests, `"status": "failed"`, `"status": "broken"`, 1),
		"verdict":   strings.Replace(staticTests, `"empty": "pass"`, `"empty": "green"`, 1),
		"summary":   strings.Replace(staticTests, `"summary": "tests"`, `"summary": "cases"`, 1),
		"exitCodes": strings.Replace(staticTests, `"task": "run-cs-fixer", `, ``, 1),
		"template":  strings.Replace(staticTests, `"{diff}"`, `"{diff"`, 1),
	} {
		if _, err := Decode([]byte(broken)); err == nil {
			t.Errorf("%s: accepted", label)
		}
	}
}

// The browser renders display templates with its own implementation of the grammar: both read the
// same cases.
func TestDisplayTemplatesReadAsTheBrowserReadsThem(t *testing.T) {
	raw, err := os.ReadFile("testdata/display.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Parse []struct {
			Template string   `json:"template"`
			Fields   []string `json:"fields"`
			Error    bool     `json:"error"`
		} `json:"parse"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases.Parse {
		template, err := ParseDisplayTemplate(c.Template)
		switch {
		case c.Error && err == nil:
			t.Errorf("%q accepted", c.Template)
		case !c.Error && err != nil:
			t.Errorf("%q: %v", c.Template, err)
		case !c.Error && !slices.Equal(template.Fields(), c.Fields) && len(c.Fields)+len(template.Fields()) > 0:
			t.Errorf("%q reads %v, expected %v", c.Template, template.Fields(), c.Fields)
		}
	}
}
