package worker

import (
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
)

// A step still running has no exit code; recording it as 0 would read as a success.
func TestAStepWithoutExitCodeStaysWithout(t *testing.T) {
	code := int32(1)
	records := stepRecords([]tekton.Step{
		{Name: "analysis", Status: tekton.StatusFailed, ExitCode: &code},
		{Name: "upload", Status: tekton.StatusRunning},
	})

	if len(records) != 2 {
		t.Fatalf("records = %d, expected 2", len(records))
	}
	if records[0].ExitCode == nil || *records[0].ExitCode != 1 || records[0].Status != "failed" {
		t.Errorf("first step = %+v", records[0])
	}
	if records[1].ExitCode != nil {
		t.Errorf("running step carries an exit code: %d", *records[1].ExitCode)
	}
}
