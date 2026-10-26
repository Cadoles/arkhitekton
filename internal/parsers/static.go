package parsers

import (
	"encoding/json"
	"sort"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

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
func ParsePHPStan(raw []byte) (parse.PHPStan, error) {
	var source phpstanReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return parse.PHPStan{}, err
	}

	files := map[string]phpstanFile{}
	if len(source.Files) > 0 {
		// Deliberately ignored: `files: []` is an empty array, not an object, and means "no
		// violation".
		_ = json.Unmarshal(source.Files, &files)
	}

	report := parse.PHPStan{Errors: source.Errors}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		file := parse.PHPStanFile{Path: path}
		for _, message := range files[path].Messages {
			file.Messages = append(file.Messages, parse.PHPStanMessage{
				Message: message.Message, Line: message.Line,
				Identifier: message.Identifier, Ignorable: message.Ignorable,
			})
		}
		report.Files = append(report.Files, file)
	}
	return report, nil
}

type csFixerReport struct {
	Files []struct {
		Name          string   `json:"name"`
		AppliedFixers []string `json:"appliedFixers"`
		Diff          string   `json:"diff"`
	} `json:"files"`
}

// ParseCSFixer reads PHP-CS-Fixer's JSON output. The error is the decoder's, unwrapped.
func ParseCSFixer(raw []byte) (parse.CSFixer, error) {
	var source csFixerReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return parse.CSFixer{}, err
	}

	var report parse.CSFixer
	for _, file := range source.Files {
		report.Files = append(report.Files, parse.CSFixerFile{
			Name: file.Name, AppliedFixers: file.AppliedFixers, Diff: file.Diff,
		})
	}
	return report, nil
}
