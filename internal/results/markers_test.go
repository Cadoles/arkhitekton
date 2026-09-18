package results

import "strings"

import "testing"

const stream = `Running PHPStan...
##META## {"tool":"phpstan","version":"1.11.2","exit_code":1,"status":"ok"}
##SUMMARY## 12 violations in 4 files
Note: 12 errors found
##META## {"tool":"trivy","exit_code":0,"status":"error","error":"db download failed"}
##SUMMARY## audit interrupted
`

func TestScanMarkers(t *testing.T) {
	markers := DefaultMarkers.ScanMarkers(stream)

	if len(markers.Metas) != 2 {
		t.Fatalf("%d descriptor(s)", len(markers.Metas))
	}
	if len(markers.Summaries) != 2 || markers.Summaries[0] != "12 violations in 4 files" {
		t.Errorf("summaries = %v", markers.Summaries)
	}
}

// PHPStan exits 1 as soon as it finds a violation: a success of the tool and a
// failure of the code. Confusing the two would make every non-clean analysis read
// as "not measured".
func TestANonZeroExitCodeIsNotAToolFailure(t *testing.T) {
	markers := DefaultMarkers.ScanMarkers(stream)

	phpstan := markers.Metas["phpstan"]
	if phpstan.ExitCode != 1 {
		t.Errorf("exit code = %d", phpstan.ExitCode)
	}
	if !phpstan.Ran() {
		t.Error("phpstan concluded: its signal must be measured")
	}
}

// A crashed scanner and a scanner that finds nothing both produce zero findings.
// The descriptor is what tells them apart.
func TestAFailedToolIsNotMeasured(t *testing.T) {
	markers := DefaultMarkers.ScanMarkers(stream)

	if markers.Metas["trivy"].Ran() {
		t.Error("a tool in error must not count as measured")
	}
}

// An absent descriptor means "did not conclude". Erring this way shows "not
// measured" for a tool that ran; erring the other way, "clean" for a crashed
// scanner.
func TestAnAbsentMetaIsNotARun(t *testing.T) {
	if (Meta{}).Ran() {
		t.Error("an empty descriptor must not count as a run")
	}
}

// The log has other uses: losing it entirely over a missing brace would be
// disproportionate.
func TestMalformedMarkersAreIgnoredNotFatal(t *testing.T) {
	markers := DefaultMarkers.ScanMarkers("##META## {not json\n##SUMMARY##\nan ordinary line\n")

	if len(markers.Metas) != 0 {
		t.Errorf("descripteurs = %v", markers.Metas)
	}
	// An empty summary is not a summary.
	if len(markers.Summaries) != 0 {
		t.Errorf("summaries = %v", markers.Summaries)
	}
}

// A retry after failure emits two descriptors: the last one describes the run that
// was kept.
func TestTheLastMetaWins(t *testing.T) {
	markers := DefaultMarkers.ScanMarkers(`##META## {"tool":"phpunit","status":"error"}
##META## {"tool":"phpunit","status":"ok"}`)

	if !markers.Metas["phpunit"].Ran() {
		t.Error("the last descriptor must prevail")
	}
}

// The displayed log is what a person reads: leaving protocol lines in it adds noise
// without teaching anything.
func TestStripMarkersKeepsTheRest(t *testing.T) {
	cleaned := DefaultMarkers.StripMarkers(stream)

	if strings.Contains(cleaned, DefaultMarkers.Meta) || strings.Contains(cleaned, DefaultMarkers.Summary) {
		t.Error("markers must disappear from the displayed log")
	}
	if !strings.Contains(cleaned, "Running PHPStan...") {
		t.Error("the rest of the log must be kept")
	}
}

// A deployment token yields the historical form, which lets a deployment already
// emitting it carry on without touching its pipelines.
func TestADeploymentTokenYieldsTheHistoricalForm(t *testing.T) {
	mse := NewMarkerSet("MSE")

	if mse.Meta != "##MSE-META##" || mse.Summary != "##MSE-SUMMARY##" {
		t.Fatalf("marqueurs = %q / %q", mse.Meta, mse.Summary)
	}

	markers := mse.ScanMarkers(`##MSE-META## {"tool":"phpstan","status":"ok"}
##MSE-SUMMARY## 12 violations`)
	if !markers.Metas["phpstan"].Ran() || len(markers.Summaries) != 1 {
		t.Errorf("marqueurs lus = %+v", markers)
	}

	// And the neutral set does not read them: two distinct protocols must not be
	// conflated, otherwise the token would serve no purpose.
	if len(DefaultMarkers.ScanMarkers("##MSE-META## {\"tool\":\"phpstan\"}").Metas) != 0 {
		t.Error("the neutral set must not read another token's markers")
	}
}
