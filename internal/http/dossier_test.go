package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// A deliverable attests to what the release's screen shows: the same digests, the same score.
func TestADossierSaysWhatTheReleaseScreenShows(t *testing.T) {
	fake := newFakeStore()
	fake.registry = fakeRegistry{"1.8.0-rc3": "sha256:aaa"}
	edited := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	fake.releases["1.8.0"] = store.Release{
		ID: 8, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
		ManualActions: "- vider le cache", ManualActionsUpdatedBy: "sub-alice", ManualActionsUpdatedAt: &edited,
	}
	fake.produits = []store.PlanArtifact{{
		RunID: 3, Name: "image", Kind: "image", Reference: "reg.example.fr/vcarroy/portal:1.8.0-rc3",
	}}
	fake.notes = []store.NoteEntry{
		{ExternalID: "12", Subject: "Recherche", Known: true},
		{ExternalID: "13", Subject: "Plantage", Anomaly: true, Known: true},
	}
	fake.results = []store.TestResult{{ID: 1, Group: "unit", Status: "pass", Total: 4, Passed: 4}}
	handler := newReleasesHandler(t, fake)

	dossier, err := handler.Dossier(context.Background(), 1, "1.8.0")
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0", nil)
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()
	handler.Get(recorder, asRole(request, auth.RoleViewer))
	var screen releasePayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &screen); err != nil {
		t.Fatal(err)
	}

	if len(dossier.Artifacts) != 1 || dossier.Artifacts[0].Digest != screen.Artifacts[0].Digest ||
		dossier.Artifacts[0].Digest != "sha256:aaa" {
		t.Errorf("artifacts = %+v, the screen shows %+v", dossier.Artifacts, screen.Artifacts)
	}
	if screen.Score == nil || dossier.Score.Score != screen.Score.Value || dossier.Score.Measured != screen.Score.Measured {
		t.Errorf("score = %+v, the screen shows %+v", dossier.Score, screen.Score)
	}
	if len(dossier.Notes) != 2 || !dossier.Notes[1].Anomaly {
		t.Errorf("notes = %+v", dossier.Notes)
	}
	if dossier.ManualActions == nil || dossier.ManualActions.Markdown != "- vider le cache" ||
		dossier.ManualActions.UpdatedBy != screen.ManualAction.UpdatedBy {
		t.Errorf("manual actions = %+v, the screen shows %+v", dossier.ManualActions, screen.ManualAction)
	}
	if dossier.Version != "1.8.0" || len(dossier.Results) != 1 {
		t.Errorf("version %q, %d result(s)", dossier.Version, len(dossier.Results))
	}
	// No audit ran: absent, not zeroed, which would read as "found nothing".
	if dossier.Audit != nil {
		t.Errorf("audit = %+v", dossier.Audit)
	}
}

func TestADossierOfAnUnknownReleaseIsNotFound(t *testing.T) {
	handler := newReleasesHandler(t, newFakeStore())
	if _, err := handler.Dossier(context.Background(), 1, "9.9.9"); !errors.Is(err, store.ErrReleaseNotFound) {
		t.Errorf("err = %v", err)
	}
}
