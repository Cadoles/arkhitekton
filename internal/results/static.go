package results

import (
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
)

/*
 * Static analysis — PHPStan and PHP-CS-Fixer.
 *
 * These tools do not produce "tests" but **violations**. Projecting them into test cases anyway is
 * no contrivance: the question asked is the same — what appeared, what disappeared — and a single
 * model lets it be asked once, in a single query, for every family.
 *
 * A violation's identity is its **file + its rule**, never its line number. A line moves at every
 * commit: attaching it there would report one violation resolved and a new one appearing every
 * time a line is inserted above.
 */

// ParsePHPStan reads PHPStan's JSON output into its report.
func ParsePHPStan(raw []byte) (Report, error) {
	report := Report{Family: FamilyStatic, Tool: "phpstan"}

	source, err := parse.ParsePHPStan(raw)
	if err != nil {
		return report, ErrUnsupported{Tool: "phpstan", Detail: err.Error()}
	}

	// Errors outside a file are failures of the tool, not of the code. Counting them as violations
	// would send someone looking for a problem in the code that is not there.
	for _, message := range source.Errors {
		report.Cases = append(report.Cases, Case{
			Suite: "phpstan", Name: "erreur d'analyse",
			Status: StatusFailed, Message: firstLine(message),
		})
	}

	for _, file := range source.Files {
		for _, message := range file.Messages {
			rule := message.Identifier
			if rule == "" {
				// Older versions emit no identifier: the message stands in for the rule. Less stable,
				// but better than an identity based on the line number.
				rule = firstLine(message.Message)
			}
			report.Cases = append(report.Cases, Case{
				Suite: file.Path, Name: rule, Status: StatusFailed,
				Message: fmt.Sprintf("line %d: %s", message.Line, firstLine(message.Message)),
			})
		}
	}

	report.tally()
	// A clean static analysis has no cases. The derived verdict would be `na`, meaning "did not
	// run", whereas it did run and found nothing, which is the best possible result.
	if report.Total == 0 {
		report.Status = ReportPass
	}
	report.Summary = summarizeViolations(report, "violation(s)")
	return report, nil
}

// ParseCSFixer reads PHP-CS-Fixer's JSON output into its report.
//
// The tool runs in --dry-run in the pipeline: every file listed is one that would have been
// modified, hence a style deviation.
func ParseCSFixer(raw []byte) (Report, error) {
	report := Report{Family: FamilyStatic, Tool: "cs-fixer"}

	source, err := parse.ParseCSFixer(raw)
	if err != nil {
		return report, ErrUnsupported{Tool: "cs-fixer", Detail: err.Error()}
	}

	for _, file := range source.Files {
		fixers := file.AppliedFixers
		if len(fixers) == 0 {
			fixers = []string{"style"}
		}
		for _, fixer := range fixers {
			// One entry per rule rather than per file: that is the granularity at which one fixes,
			// and at which "it is done" means something.
			report.Cases = append(report.Cases, Case{
				Suite: file.Name, Name: fixer, Status: StatusFailed,
				Message: "style deviation: " + fixer,
				Trace:   file.Diff,
			})
		}
	}

	report.tally()
	if report.Total == 0 {
		report.Status = ReportPass
	}
	report.Summary = summarizeViolations(report, "style deviation(s)")
	return report, nil
}

func summarizeViolations(report Report, noun string) string {
	if report.Total == 0 {
		return "no " + strings.TrimSuffix(noun, "(s)")
	}
	files := map[string]bool{}
	for _, testCase := range report.Cases {
		files[testCase.Suite] = true
	}
	return fmt.Sprintf("%d %s in %d file(s)", report.Total, noun, len(files))
}
