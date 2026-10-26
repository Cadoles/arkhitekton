// Package parsers decodes the artifacts a pipeline publishes into the records of the plugin
// contract (sdk/parse).
//
// Parsing belongs to the dashboard: a known format is decoded once, here, for every plugin that
// uses it, and a fix to a parser reaches all of them.
package parsers

import (
	"encoding/json"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse/security"
)

// Parser decodes an artifact's bytes into its record payload.
type Parser func(raw []byte) (any, error)

var parsers = map[parse.ID]Parser{
	parse.Raw:             func(raw []byte) (any, error) { return parse.Bytes(raw), nil },
	parse.JUnitXML:        func(raw []byte) (any, error) { return ParseJUnit(raw) },
	parse.MochawesomeJSON: func(raw []byte) (any, error) { return ParseMochawesome(raw) },
	parse.PHPStanJSON:     func(raw []byte) (any, error) { return ParsePHPStan(raw) },
	parse.CSFixerJSON:     func(raw []byte) (any, error) { return ParseCSFixer(raw) },
	parse.K6SummaryJSON:   func(raw []byte) (any, error) { return ParseK6(raw) },
	parse.SecuritySummaryJSON: func(raw []byte) (any, error) {
		var summary security.Summary
		err := json.Unmarshal(raw, &summary)
		return summary, err
	},
	parse.SecurityFindingsJSONL: func(raw []byte) (any, error) { return UnmarshalFindings(raw), nil },
}

// Lookup returns a parser by ID.
func Lookup(id parse.ID) (Parser, bool) {
	parser, found := parsers[id]
	return parser, found
}
