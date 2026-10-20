// Package parse declares the records a run interpreter reads: what the dashboard's parsers make
// of the artifacts a pipeline publishes.
//
// Parsing belongs to the dashboard by default: a known format (JUnit, a k6 summary...) is decoded
// once, by the host, for every plugin that uses it, and a fix to a parser reaches all of them. An
// interpreter then never handles bytes, only records.
//
// A format the dashboard does not know goes through [Raw]: the interpreter receives the bytes and
// parses them itself. The host still bounds their size.
//
// This package is part of the contract plugins compile against, and imports only the standard
// library.
package parse

import "strings"

// ID names a parser.
type ID string

// Raw hands the artifact over as it is. Its payload is a [Bytes].
const Raw ID = "raw"

// Record is one parsed member of an artifact.
type Record struct {
	// Artifact is the name the capability gave the artifact; Member is the file inside an archive,
	// empty for a plain file.
	Artifact string
	Member   string
	// Object is the storage key the artifact was read from.
	Object string
	Parser ID
	// Tool is what the capability declared for this member.
	Tool    string
	Payload any
	// Err is the parsing failure, if any. The record is still handed over: an unreadable report is
	// something the interpreter must be able to say, not something the host hides.
	Err error
}

// Bytes is the payload of [Raw].
type Bytes []byte

// The security report's two files, whose payloads are a security.Summary and a []security.Finding.
const (
	SecuritySummaryJSON   ID = "security-summary-json"
	SecurityFindingsJSONL ID = "security-findings-jsonl"
)

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
