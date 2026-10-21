// Package declarative executes the run readings plugins declare (sdk/declare).
//
// A declaration never sees a report's types: it reads **views**, the rows each kind of parsed
// record offers, with fields named and documented here. Adding a view is adding code to the host,
// with a test; a declaration only chooses among them.
package declarative

import (
	"sort"
	"strconv"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// Row is one line of a view: field names to values.
type Row = map[string]string

// View is a way to read a parsed report as rows.
type View struct {
	Name string
	// Parser is the parser whose records the view reads.
	Parser parse.ID
	// Fields are the fields every row carries.
	Fields []string
	// Enums are the closed sets of values some fields take: a status table on one of them must
	// cover every value.
	Enums map[string][]string
	rows  func(payload any) []Row
}

// Rows reads a record's payload as the view's rows, in the report's order.
func (v View) Rows(payload any) []Row { return v.rows(payload) }

// report is what a whole report says, for a tool's own fields such as its duration.
type report struct {
	fields []string
	row    func(payload any) Row
}

var junitOutcomes = []string{"passed", "failed", "errored", "skipped", "rerun"}

var views = map[string]View{
	"junit.cases": {
		Name: "junit.cases", Parser: parse.JUnitXML,
		Fields: []string{"path", "classname", "name", "duration_ms", "outcome", "detail", "detail_type", "trace"},
		Enums:  map[string][]string{"outcome": junitOutcomes},
		rows: func(payload any) []Row {
			report := payload.(parse.JUnit)
			rows := make([]Row, 0, len(report.Cases))
			for _, testCase := range report.Cases {
				row := Row{
					"path": testCase.Path, "classname": testCase.ClassName, "name": testCase.Name,
					"duration_ms": strconv.Itoa(testCase.DurationMS), "outcome": string(testCase.Outcome),
				}
				if testCase.Detail != nil {
					row["detail"], row["detail_type"], row["trace"] =
						testCase.Detail.Text(), testCase.Detail.Type, testCase.Detail.Content
				}
				rows = append(rows, row)
			}
			return rows
		},
	},
	"mocha.tests": {
		Name: "mocha.tests", Parser: parse.MochawesomeJSON,
		Fields: []string{"file", "suite", "title", "name", "duration_ms", "outcome", "retries", "message", "trace"},
		Enums:  map[string][]string{"outcome": {"passed", "failed", "pending", "rerun", "not-run"}},
		rows: func(payload any) []Row {
			report := payload.(parse.Mochawesome)
			rows := make([]Row, 0, len(report.Tests))
			for _, test := range report.Tests {
				outcome := "not-run"
				switch {
				case test.Pending:
					outcome = "pending"
				case test.Fail:
					outcome = "failed"
				case test.Pass && test.CurrentRetry > 0:
					outcome = "rerun"
				case test.Pass:
					outcome = "passed"
				}
				rows = append(rows, Row{
					"file": test.File, "suite": test.Suite(), "title": test.Title, "name": test.Name(),
					"duration_ms": strconv.Itoa(test.DurationMS), "outcome": outcome,
					"retries": strconv.Itoa(test.CurrentRetry), "message": test.ErrMessage, "trace": test.ErrStack,
				})
			}
			return rows
		},
	},
	"phpstan.messages": {
		Name: "phpstan.messages", Parser: parse.PHPStanJSON,
		Fields: []string{"file", "line", "identifier", "message", "ignorable"},
		rows: func(payload any) []Row {
			var rows []Row
			for _, file := range payload.(parse.PHPStan).Files {
				for _, message := range file.Messages {
					rows = append(rows, Row{
						"file": file.Path, "line": strconv.Itoa(message.Line), "identifier": message.Identifier,
						"message": message.Message, "ignorable": strconv.FormatBool(message.Ignorable),
					})
				}
			}
			return rows
		},
	},
	"phpstan.errors": {
		Name: "phpstan.errors", Parser: parse.PHPStanJSON,
		Fields: []string{"message"},
		rows: func(payload any) []Row {
			var rows []Row
			for _, message := range payload.(parse.PHPStan).Errors {
				rows = append(rows, Row{"message": message})
			}
			return rows
		},
	},
	"csfixer.fixers": {
		// One row per fixer a file would get, and one with an empty fixer for a file listed with
		// none: the file still deviates.
		Name: "csfixer.fixers", Parser: parse.CSFixerJSON,
		Fields: []string{"file", "fixer", "diff"},
		rows: func(payload any) []Row {
			var rows []Row
			for _, file := range payload.(parse.CSFixer).Files {
				fixers := file.AppliedFixers
				if len(fixers) == 0 {
					fixers = []string{""}
				}
				for _, fixer := range fixers {
					rows = append(rows, Row{"file": file.Name, "fixer": fixer, "diff": file.Diff})
				}
			}
			return rows
		},
	},
}

var reports = map[parse.ID]report{
	parse.JUnitXML: {fields: []string{"duration_ms"}, row: func(payload any) Row {
		return Row{"duration_ms": strconv.FormatInt(payload.(parse.JUnit).DurationMS, 10)}
	}},
	parse.MochawesomeJSON: {fields: []string{"duration_ms"}, row: func(payload any) Row {
		return Row{"duration_ms": strconv.FormatInt(payload.(parse.Mochawesome).DurationMS, 10)}
	}},
}

// Lookup returns a view by name.
func Lookup(name string) (View, bool) {
	view, found := views[name]
	return view, found
}

// Views lists the views, sorted by name.
func Views() []View {
	list := make([]View, 0, len(views))
	for _, view := range views {
		list = append(list, view)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// reportFields lists what a whole report of this parser offers.
func reportFields(parser parse.ID) []string { return reports[parser].fields }

// reportRow reads a whole report's fields; empty for a parser that offers none.
func reportRow(parser parse.ID, payload any) Row {
	if report, found := reports[parser]; found {
		return report.row(payload)
	}
	return Row{}
}
