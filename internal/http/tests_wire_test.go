package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// wireTestStore serves one release with one report and its cases.
type wireTestStore struct{ TestStore }

func (wireTestStore) ProjectBySlug(context.Context, string) (store.Project, error) {
	return store.Project{ID: 7, Slug: "mse"}, nil
}

func (wireTestStore) GetRelease(context.Context, int64, string) (store.Release, error) {
	return store.Release{ID: 3, Tag: "1.0.0"}, nil
}

func (wireTestStore) ListReleaseTestResults(context.Context, int64) ([]store.TestResult, error) {
	return []store.TestResult{{
		ID: 11, Group: "unit", Family: "unit", Shape: "cases", Producer: "php@1.0.0", Tool: "phpunit",
		Status: "fail", Total: 2, Passed: 1, Failed: 1, RunName: "mse-unit-3", CreatedAt: time.Now(),
	}}, nil
}

func (wireTestStore) ListTestCases(context.Context, int64, store.CasePage) ([]store.TestCase, int, error) {
	return []store.TestCase{{Suite: "A", Name: "b", Status: "failed"}}, 1, nil
}

func (wireTestStore) PreviousRelease(context.Context, int64) (int64, error) {
	return 0, store.ErrReleaseNotFound
}

func (wireTestStore) LatestSecurityAudit(context.Context, int64) (store.SecurityAudit, error) {
	return store.SecurityAudit{}, store.ErrAuditNotFound
}

func (wireTestStore) ListReleaseLoadResults(context.Context, int64) ([]store.LoadResult, error) {
	return nil, nil
}

func serveTests(t *testing.T, path, pattern string, handler stdhttp.HandlerFunc) map[string]any {
	t.Helper()
	mux := stdhttp.NewServeMux()
	mux.HandleFunc(pattern, handler)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, path, nil))
	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// The tests screen reads results by group and opens them by shape: these keys are its contract.
func TestTheTestsResponseCarriesTheKeysTheScreenReads(t *testing.T) {
	handler := NewTestsHandler(wireTestStore{}, nil)
	body := serveTests(t, "/projects/mse/releases/1.0.0/tests",
		"GET /projects/{project}/releases/{tag}/tests", handler.Tests)

	reports, _ := body["results"].([]any)
	if len(reports) != 1 {
		t.Fatalf("results = %v", body["results"])
	}
	report := reports[0].(map[string]any)
	for _, key := range []string{"group", "shape", "producer", "tool", "status"} {
		if _, found := report[key]; !found {
			t.Errorf("result without %q: %v", key, report)
		}
	}

	score, _ := body["score"].(map[string]any)
	signals, _ := score["signals"].([]any)
	var unit map[string]any
	for _, raw := range signals {
		if signal := raw.(map[string]any); signal["group"] == "unit" {
			unit = signal
		}
	}
	if unit == nil || unit["shape"] != "cases" {
		t.Errorf("unit signal = %v among %v", unit, signals)
	}
}

func TestTheGroupResponseCarriesTheKeysTheDetailReads(t *testing.T) {
	handler := NewTestsHandler(wireTestStore{}, nil)
	body := serveTests(t, "/projects/mse/releases/1.0.0/tests/unit",
		"GET /projects/{project}/releases/{tag}/tests/{group}", handler.Group)

	if body["group"] != "unit" || body["tool"] != "phpunit" {
		t.Errorf("body = %v", body)
	}
	if cases, _ := body["cases"].([]any); len(cases) != 1 {
		t.Errorf("cases = %v", body["cases"])
	}
}
