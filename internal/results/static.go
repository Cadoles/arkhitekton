package results

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

/*
Analyse statique — PHPStan et PHP-CS-Fixer.

Ces outils ne produisent pas des « tests » mais des **violations**. Les projeter
malgré tout en cas de test n'est pas un artifice : la question posée est la même —
qu'est-ce qui est apparu, qu'est-ce qui a disparu — et un modèle unique permet de
la poser une seule fois, dans une seule requête, pour toutes les familles.

L'identité d'une violation est son **fichier + sa règle**, jamais son numéro de
ligne. Une ligne bouge à chaque commit : l'y attacher signalerait une violation
résolue et une nouvelle apparue à chaque fois qu'on insère une ligne au-dessus.
*/

type phpstanReport struct {
	Totals struct {
		Errors     int `json:"errors"`
		FileErrors int `json:"file_errors"`
	} `json:"totals"`
	// Files is keyed by path. `json:"files"` may be `[]` when there is no error — PHP
	// serialises an empty array where an object is expected — which json.RawMessage
	// absorbs.
	Files  json.RawMessage `json:"files"`
	Errors []string        `json:"errors"`
}

type phpstanFile struct {
	Errors   int `json:"errors"`
	Messages []struct {
		Message    string `json:"message"`
		Line       int    `json:"line"`
		Identifier string `json:"identifier"`
		Ignorable  bool   `json:"ignorable"`
	} `json:"messages"`
}

// ParsePHPStan lit la sortie JSON de PHPStan.
func ParsePHPStan(raw []byte) (Report, error) {
	report := Report{Family: FamilyStatic, Tool: "phpstan"}

	var source phpstanReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return report, ErrUnsupported{Tool: "phpstan", Detail: err.Error()}
	}

	// Errors outside a file — unreadable configuration, a missing extension — are
	// failures of the tool, not of the code. Counting them as violations would send
	// someone looking for a problem in the code that is not there.
	for _, message := range source.Errors {
		report.Cases = append(report.Cases, Case{
			Suite: "phpstan", Name: "erreur d'analyse",
			Status: StatusFailed, Message: firstLine(message),
		})
	}

	files := map[string]phpstanFile{}
	if len(source.Files) > 0 {
		// Deliberately ignored: `files: []` is an empty array, not an object, and means "no
		// violation".
		_ = json.Unmarshal(source.Files, &files)
	}

	for _, path := range sortedKeys(files) {
		for _, message := range files[path].Messages {
			rule := message.Identifier
			if rule == "" {
				// Older versions emit no identifier: the message stands in for the rule. Less
				// stable, but better than an identity based on the line number.
				rule = firstLine(message.Message)
			}
			report.Cases = append(report.Cases, Case{
				Suite: path, Name: rule, Status: StatusFailed,
				Message: fmt.Sprintf("ligne %d: %s", message.Line, firstLine(message.Message)),
			})
		}
	}

	report.tally()
	// A clean static analysis has no cases. The derived verdict would be `na`, meaning
	// "did not run" — whereas it did run and found nothing, which is the best possible
	// result.
	if report.Total == 0 {
		report.Status = ReportPass
	}
	report.Summary = summarizeViolations(report, "violation(s)")
	return report, nil
}

type csFixerReport struct {
	Files []struct {
		Name        string   `json:"name"`
		AppliedFixe []string `json:"appliedFixers"`
		Diff        string   `json:"diff"`
	} `json:"files"`
}

// ParseCSFixer reads PHP-CS-Fixer's JSON output.
//
// The tool runs in --dry-run in the pipeline: every file listed is one that would
// have been modified, hence a style deviation.
func ParseCSFixer(raw []byte) (Report, error) {
	report := Report{Family: FamilyStatic, Tool: "cs-fixer"}

	var source csFixerReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return report, ErrUnsupported{Tool: "cs-fixer", Detail: err.Error()}
	}

	for _, file := range source.Files {
		fixers := file.AppliedFixe
		if len(fixers) == 0 {
			fixers = []string{"style"}
		}
		for _, fixer := range fixers {
			// One entry per rule rather than per file: that is the granularity at which one
			// fixes, and at which "it is done" means something.
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

// sortedKeys returns the keys, sorted.
//
// A Go map's iteration order is random: without sorting, two reads of the same
// report would produce two orders of cases, and any comparison of outputs would
// become unreadable.
func sortedKeys[V any](source map[string]V) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
