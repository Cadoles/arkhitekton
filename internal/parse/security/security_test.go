package security

import (
	"encoding/json"
	"testing"
)

func TestAFindingsFileSkipsTheLinesItCannotRead(t *testing.T) {
	raw := []byte(`{"fingerprint":"a","severity":"HIGH","tool":{"name":"trivy"},"location":{"file":"composer.lock","start_line":3}}
not json

{"fingerprint":"b","severity":"LOW","suppression":{"reason":"accepted","expires":"2027-01-01"}}
`)

	findings := UnmarshalFindings(raw)
	if len(findings) != 2 {
		t.Fatalf("findings = %d, expected 2", len(findings))
	}
	if findings[0].Tool.Name != "trivy" || findings[0].Location.StartLine != 3 {
		t.Errorf("first finding = %+v", findings[0])
	}
	if findings[0].Suppressed() || !findings[1].Suppressed() {
		t.Error("suppression read from the wrong finding")
	}
}

func TestTheSummaryCarriesTheFailedScanners(t *testing.T) {
	raw := []byte(`{"run_id":"r1","total":4,"by_severity":{"CRITICAL":1,"HIGH":3},
		"delta":{"new":2,"existing":2,"resolved":1},"failed_scanners":["trivy"]}`)

	var summary Summary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.BySeverity[string(SeverityCritical)] != 1 || summary.Delta.Resolved != 1 {
		t.Errorf("summary = %+v", summary)
	}
	if len(summary.FailedScanners) != 1 || summary.FailedScanners[0] != "trivy" {
		t.Errorf("failed scanners = %v", summary.FailedScanners)
	}
}
