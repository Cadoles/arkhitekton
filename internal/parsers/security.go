package parsers

import (
	"encoding/json"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse/security"
)

// UnmarshalFindings reads a findings file.
//
// An unreadable line is skipped rather than failing the whole file: one corrupt finding must not
// cost the audit.
func UnmarshalFindings(raw []byte) []security.Finding {
	findings := make([]security.Finding, 0)
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var finding security.Finding
		if err := json.Unmarshal([]byte(trimmed), &finding); err != nil {
			continue
		}
		findings = append(findings, finding)
	}
	return findings
}
