package native

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
)

func TestARoleWithoutDossiersSaysSo(t *testing.T) {
	_, err := NewHost(HostDeps{}, "mse").GetReleaseDossier(context.Background(), &pluginv1.ReleaseByTag{Tag: "1.0.0"})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("err = %v", err)
	}
}

func TestADossierCarriesTheScreensVerdicts(t *testing.T) {
	deps := HostDeps{Dossier: func(context.Context, int64, string) (Dossier, error) {
		return Dossier{
			Score: results.Breakdown{Score: 80, Measured: true, Signals: []results.SignalScore{
				{Key: "unit", Value: 80, Weight: 100, Measured: true},
			}},
			Audit: &store.SecurityAudit{Total: 2, FailedScanners: []string{"trivy"}},
		}, nil
	}}
	dossier, err := NewHost(deps, "mse").GetReleaseDossier(context.Background(), &pluginv1.ReleaseByTag{Tag: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if dossier.GetScore().GetHealth() == "" || dossier.GetScore().GetSignals()[0].GetHealth() == "" {
		t.Errorf("score = %v: the verdicts are the core's to compute", dossier.GetScore())
	}
	// A scanner that did not conclude: the audit grades nothing.
	if dossier.GetAudit().GetMeasured() {
		t.Errorf("audit = %v", dossier.GetAudit())
	}
}

func TestAMissingReleaseIsNotFoundForThePlugin(t *testing.T) {
	deps := HostDeps{Dossier: func(context.Context, int64, string) (Dossier, error) {
		return Dossier{}, store.ErrReleaseNotFound
	}}
	_, err := NewHost(deps, "mse").GetReleaseDossier(context.Background(), &pluginv1.ReleaseByTag{Tag: "1.0.0"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("err = %v", err)
	}
}
