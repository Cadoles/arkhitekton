package results

import (
	"encoding/json"
	"strings"
)

/*
 * Stream markers — `##META##` and `##SUMMARY##`, possibly prefixed.
 *
 * Two pieces of information must surface **before the end of the run**: the run descriptor and
 * the summary line. They are too small to justify an artifact, and too useful to wait — an
 * operator watching a forty-minute campaign must see "PHPStan: 12 violations" as soon as PHPStan
 * has finished, without waiting for the E2E.
 *
 * The channel carries only kilobytes. The complete payloads go through the presigned URLs:
 * container logs are subject to the kubelet's rotation and are not a reliable transport for
 * several megabytes.
 *
 * **The descriptor is produced by the pipeline, never deduced here.** It alone can tell "the tool
 * failed" from "the tool found nothing" — both produce zero violations, and confusing them would
 * make a crashed scanner pass for healthy code. It is the best property of the existing
 * `security-audit`, and it is taken as it is.
 */

// MarkerSet names the two markers a stream uses.
//
// One token rather than two free strings: both markers of a protocol must carry the
// same mark, and letting the caller compose them would allow a `##ACME-META##`
// beside a `##SUMMARY##`, with nothing saying they belong together.
type MarkerSet struct {
	Meta    string
	Summary string
}

// DefaultMarkers is the set with no deployment token.
//
// A deployment's name inside a protocol is a different kind of coupling than inside
// an interface: a generic pipeline emitting a branded marker stays branded in its
// own source.
var DefaultMarkers = NewMarkerSet("")

// NewMarkerSet composes the markers of a deployment token.
//
// An empty token gives the neutral form, `##META##`. A token gives the historical
// form, which lets a deployment already emitting it carry on without touching its
// pipelines.
func NewMarkerSet(token string) MarkerSet {
	token = strings.TrimSpace(strings.Trim(strings.TrimSpace(token), "-"))
	if token != "" {
		token += "-"
	}
	return MarkerSet{Meta: "##" + token + "META##", Summary: "##" + token + "SUMMARY##"}
}

// Meta is a tool's execution descriptor.
type Meta struct {
	Tool    string `json:"tool"`
	Version string `json:"version,omitempty"`
	// ExitCode is the tool's own. Distinct from Status: a non-zero exit code may be
	// normal — PHPStan exits 1 as soon as it finds a violation, which is a success of
	// the tool and a failure of the code.
	ExitCode int `json:"exit_code"`
	// Status is `ok` when the tool did its job, whatever the outcome. Any other value
	// says the tool could not conclude, and the corresponding signal becomes "not
	// measured" rather than "clean".
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Ran says whether the tool concluded.
//
// An absent descriptor means "did not conclude": the pipeline is supposed to emit
// one, and its absence is itself information. Erring this way shows "not measured"
// for a tool that ran; erring the other way would show "clean" for a scanner that
// crashed.
func (m Meta) Ran() bool { return strings.EqualFold(m.Status, "ok") }

// Markers gathers what a stream delivered.
type Markers struct {
	// Metas is keyed by tool. A tool emitting several — a retry after failure — has the
	// last one prevail: it describes the run that was kept.
	Metas map[string]Meta
	// Summaries keeps the order of appearance, which is the order of execution.
	Summaries []string
}

// ScanMarkers extracts markers from a log stream.
//
// Tolerant about shape: a malformed marker is ignored rather than failing the whole
// log read. The log has other uses, and losing it entirely over a missing brace
// would be disproportionate.
func (m MarkerSet) ScanMarkers(text string) Markers {
	markers := Markers{Metas: map[string]Meta{}}

	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, m.Meta):
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, m.Meta))
			var meta Meta
			if err := json.Unmarshal([]byte(payload), &meta); err != nil || meta.Tool == "" {
				continue
			}
			markers.Metas[meta.Tool] = meta

		case strings.HasPrefix(trimmed, m.Summary):
			summary := strings.TrimSpace(strings.TrimPrefix(trimmed, m.Summary))
			if summary != "" {
				markers.Summaries = append(markers.Summaries, summary)
			}
		}
	}

	return markers
}

// StripMarkers removes marker lines from a log.
//
// The displayed log is what a person reads: leaving protocol lines in it adds noise
// without teaching anything, since what they carry is displayed alongside,
// formatted.
func (m MarkerSet) StripMarkers(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, m.Meta) || strings.HasPrefix(trimmed, m.Summary) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
