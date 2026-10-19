package parse

import (
	"encoding/json"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse/security"
)

// The security report's two files.
const (
	SecuritySummaryJSON   ID = "security-summary-json"
	SecurityFindingsJSONL ID = "security-findings-jsonl"
)

// Parser decodes an artifact's bytes into its record payload.
type Parser func(raw []byte) (any, error)

var parsers = map[ID]Parser{
	Raw:             func(raw []byte) (any, error) { return Bytes(raw), nil },
	JUnitXML:        func(raw []byte) (any, error) { return ParseJUnit(raw) },
	MochawesomeJSON: func(raw []byte) (any, error) { return ParseMochawesome(raw) },
	PHPStanJSON:     func(raw []byte) (any, error) { return ParsePHPStan(raw) },
	CSFixerJSON:     func(raw []byte) (any, error) { return ParseCSFixer(raw) },
	K6SummaryJSON:   func(raw []byte) (any, error) { return ParseK6(raw) },
	SecuritySummaryJSON: func(raw []byte) (any, error) {
		var summary security.Summary
		err := json.Unmarshal(raw, &summary)
		return summary, err
	},
	SecurityFindingsJSONL: func(raw []byte) (any, error) { return security.UnmarshalFindings(raw), nil },
}

// Lookup returns a parser by ID.
func Lookup(id ID) (Parser, bool) {
	parser, found := parsers[id]
	return parser, found
}

// FirstLine returns the first non-empty line of a text, trimmed.
//
// Tools' failure messages often run to ten lines, the first carrying the substance and the rest
// being the trace. Showing all ten in a table makes the table unreadable.
func FirstLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
