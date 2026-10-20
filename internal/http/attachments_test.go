package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// attachmentStore serves release 3 and the files given, each belonging to the release it names.
type attachmentStore struct {
	wireTestStore
	files map[int64]store.ResultAttachment
}

func (s attachmentStore) GetAttachment(_ context.Context, id int64) (store.ResultAttachment, error) {
	file, found := s.files[id]
	if !found {
		return store.ResultAttachment{}, store.ErrAttachmentNotFound
	}
	return file, nil
}

// memoryFiles serves objects from memory, seekable like the bucket's.
type memoryFiles map[string]string

type openedBytes struct{ *bytes.Reader }

func (openedBytes) Close() error { return nil }

func (f memoryFiles) Open(_ context.Context, key string) (blob.Opened, blob.ObjectInfo, error) {
	content, found := f[key]
	if !found {
		return nil, blob.ObjectInfo{}, errors.New("not found")
	}
	return openedBytes{bytes.NewReader([]byte(content))},
		blob.ObjectInfo{Key: key, Size: int64(len(content)), LastModified: time.Unix(0, 0)}, nil
}

func attachmentHandler() *TestsHandler {
	release, other := int64(3), int64(4)
	files := map[int64]store.ResultAttachment{
		1: {ID: 1, ReleaseID: &release, Name: "login.cy.js.mp4", MediaType: "video/mp4", Object: "attachments/r/a/video.mp4"},
		2: {ID: 2, ReleaseID: &release, Name: "report.html", MediaType: "text/html", Object: "attachments/r/b/report.html"},
		3: {ID: 3, ReleaseID: &release, Name: "logo.svg", MediaType: "image/svg+xml", Object: "attachments/r/c/logo.svg"},
		4: {ID: 4, ReleaseID: &other, Name: "secret.png", MediaType: "image/png", Object: "attachments/s/d/secret.png"},
	}
	objects := memoryFiles{
		"attachments/r/a/video.mp4":   "0123456789",
		"attachments/r/b/report.html": "<script>alert(document.cookie)</script>",
		"attachments/r/c/logo.svg":    "<svg onload=alert(1)/>",
		"attachments/s/d/secret.png":  "png",
	}
	return NewTestsHandler(attachmentStore{files: files}, nil).WithAttachments(objects)
}

func serveAttachment(t *testing.T, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects/{project}/releases/{tag}/attachments/{id}", attachmentHandler().Attachment)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for key, values := range header {
		request.Header[key] = values
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

// Every file is served sandboxed and unsniffed, whatever its type: an HTML report is third-party
// markup under the dashboard's origin.
func TestAnHTMLReportIsServedSandboxed(t *testing.T) {
	recorder := serveAttachment(t, "/projects/mse/releases/1.0.0/attachments/2", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	policy := recorder.Header().Get("Content-Security-Policy")
	if !strings.HasPrefix(policy, "sandbox;") || strings.Contains(policy, "allow-scripts") ||
		strings.Contains(policy, "allow-same-origin") {
		t.Errorf("policy %q", policy)
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
		recorder.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "inline") {
		t.Errorf("headers %v", recorder.Header())
	}
}

// SVG runs script in a browser: it is never shown, only downloaded as opaque bytes.
func TestAnSVGIsOnlyDownloaded(t *testing.T) {
	recorder := serveAttachment(t, "/projects/mse/releases/1.0.0/attachments/3", nil)
	if recorder.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("headers %v", recorder.Header())
	}
}

// A video is sought into: a range is answered with the part asked for.
func TestAVideoAnswersARange(t *testing.T) {
	recorder := serveAttachment(t, "/projects/mse/releases/1.0.0/attachments/1",
		http.Header{"Range": []string{"bytes=2-5"}})
	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "2345" ||
		recorder.Header().Get("Content-Type") != "video/mp4" {
		t.Errorf("code %d, body %q, headers %v", recorder.Code, recorder.Body, recorder.Header())
	}
}

// Asked for as a download, even a picture is one.
func TestADownloadIsAsked(t *testing.T) {
	recorder := serveAttachment(t, "/projects/mse/releases/1.0.0/attachments/1?download=1", nil)
	if !strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("disposition %q", recorder.Header().Get("Content-Disposition"))
	}
}

// Another release's file answers like a missing one: identifiers are sequential.
func TestAnotherReleasesFileIsNotFound(t *testing.T) {
	for _, path := range []string{
		"/projects/mse/releases/1.0.0/attachments/4",
		"/projects/mse/releases/1.0.0/attachments/99",
	} {
		if recorder := serveAttachment(t, path, nil); recorder.Code != http.StatusNotFound {
			t.Errorf("%s: code %d", path, recorder.Code)
		}
	}
}
