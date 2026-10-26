package parsers

import (
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
