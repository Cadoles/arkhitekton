package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/connect"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// listingForge lists one branch and one tag.
type listingForge struct{ provider.Forge }

func (listingForge) DefaultBranch(context.Context) (string, error) { return "main", nil }
func (listingForge) ListBranches(context.Context) ([]provider.Ref, bool, error) {
	return []provider.Ref{{Name: "main", SHA: "abc"}}, false, nil
}
func (listingForge) ListTags(context.Context) ([]provider.Ref, bool, error) {
	return []provider.Ref{{Name: "1.0.0", SHA: "def"}}, false, nil
}

// projectForges answers per project, as the plugins enabled for each would.
type projectForges map[int64]error

func (f projectForges) Forge(_ context.Context, projectID int64) (provider.Forge, error) {
	if err := f[projectID]; err != nil {
		return nil, err
	}
	return listingForge{}, nil
}

func (f projectForges) TagWriter(context.Context, int64) (provider.TagWriter, error) {
	return nil, connect.ErrNotConfigured
}

func refs(t *testing.T, handler *ForgeHandler, project string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/projects/"+project+"/forge/refs", nil)
	request.SetPathValue("project", project)
	recorder := httptest.NewRecorder()
	handler.Refs(recorder, request)
	var payload map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return recorder.Code, payload
}

// The references come from the project's own forge: a project with none configured falls back to
// free-text entry, one whose forge is down says so.
func TestTheReferencesComeFromTheProjectsForge(t *testing.T) {
	handler := NewForgeHandler(slugResolver{"mse": 1, "tekton": 2, "down": 3}, projectForges{
		2: connect.ErrNotConfigured,
		3: errors.New("connection refused"),
	})

	code, payload := refs(t, handler, "mse")
	if code != http.StatusOK || payload["configured"] != true || payload["default_branch"] != "main" {
		t.Errorf("configured project: %d %v", code, payload)
	}
	code, payload = refs(t, handler, "tekton")
	if code != http.StatusOK || payload["configured"] != false {
		t.Errorf("project with no forge: %d %v", code, payload)
	}
	if code, _ = refs(t, handler, "down"); code != http.StatusBadGateway {
		t.Errorf("forge down: %d", code)
	}
}
