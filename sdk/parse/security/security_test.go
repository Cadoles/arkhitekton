package security

import (
	"encoding/json"
	"testing"
)

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
