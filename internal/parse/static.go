package parse

import (
	"encoding/json"
	"sort"
)

// PHPStanJSON parses PHPStan's JSON output into a [PHPStan].
const PHPStanJSON ID = "phpstan-json"

// PHPStan is PHPStan's report.
type PHPStan struct {
	// Errors are outside any file: unreadable configuration, a missing extension. Failures of the
	// tool, not of the code.
	Errors []string
	// Files are sorted by path, so that two reads of one report give one order.
	Files []PHPStanFile
}

// PHPStanFile is one analysed file and its messages.
type PHPStanFile struct {
	Path     string
	Messages []PHPStanMessage
}

// PHPStanMessage is one violation.
type PHPStanMessage struct {
	Message string
	Line    int
	// Identifier is the rule; older versions emit none.
	Identifier string
	Ignorable  bool
}

type phpstanReport struct {
	// Files is keyed by path. It may be `[]` when there is no error: PHP serialises an empty array
	// where an object is expected, which json.RawMessage absorbs.
	Files  json.RawMessage `json:"files"`
	Errors []string        `json:"errors"`
}

type phpstanFile struct {
	Messages []struct {
		Message    string `json:"message"`
		Line       int    `json:"line"`
		Identifier string `json:"identifier"`
		Ignorable  bool   `json:"ignorable"`
	} `json:"messages"`
}

// ParsePHPStan reads PHPStan's JSON output. The error is the decoder's, unwrapped.
func ParsePHPStan(raw []byte) (PHPStan, error) {
	var source phpstanReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return PHPStan{}, err
	}

	files := map[string]phpstanFile{}
	if len(source.Files) > 0 {
		// Deliberately ignored: `files: []` is an empty array, not an object, and means "no
		// violation".
		_ = json.Unmarshal(source.Files, &files)
	}

	report := PHPStan{Errors: source.Errors}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		file := PHPStanFile{Path: path}
		for _, message := range files[path].Messages {
			file.Messages = append(file.Messages, PHPStanMessage{
				Message: message.Message, Line: message.Line,
				Identifier: message.Identifier, Ignorable: message.Ignorable,
			})
		}
		report.Files = append(report.Files, file)
	}
	return report, nil
}

// CSFixerJSON parses PHP-CS-Fixer's JSON output into a [CSFixer].
const CSFixerJSON ID = "cs-fixer-json"

// CSFixer is PHP-CS-Fixer's report. In a dry run, every file listed is one that would have been
// modified.
type CSFixer struct {
	Files []CSFixerFile
}

// CSFixerFile is a file and the fixers that would change it.
type CSFixerFile struct {
	Name          string
	AppliedFixers []string
	Diff          string
}

type csFixerReport struct {
	Files []struct {
		Name          string   `json:"name"`
		AppliedFixers []string `json:"appliedFixers"`
		Diff          string   `json:"diff"`
	} `json:"files"`
}

// ParseCSFixer reads PHP-CS-Fixer's JSON output. The error is the decoder's, unwrapped.
func ParseCSFixer(raw []byte) (CSFixer, error) {
	var source csFixerReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return CSFixer{}, err
	}

	var report CSFixer
	for _, file := range source.Files {
		report.Files = append(report.Files, CSFixerFile{
			Name: file.Name, AppliedFixers: file.AppliedFixers, Diff: file.Diff,
		})
	}
	return report, nil
}
