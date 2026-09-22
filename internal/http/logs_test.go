package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/logs"
)

/*
 * The log as text: what serves the raw view and the download.
 *
 * The point that deserves a test is the pagination. The reader bounds each call, and an
 * integration log goes well beyond that bound: serving the first chunk and stopping would give a
 * truncated file with nothing reporting it.
 */

// chunkedReader returns the log in chunks, like the real bounded reader.
type chunkedReader struct {
	content []byte
	chunk   int64
	// calls counts the reads: that is what tells real pagination from a lucky case where
	// everything fitted in the first call.
	calls int
	err   error
}

func (r *chunkedReader) Tail(context.Context, string, string, int64) (logs.Segment, error) {
	return logs.Segment{}, errors.New("Tail has no business serving the raw text")
}

// LinesBefore does not serve the raw view — it returns the log as it is, without numbering —
// but the interface requires it.
func (r *chunkedReader) LinesBefore(_ context.Context, _, _ string, offset int64) (int64, error) {
	if r.err != nil {
		return 0, r.err
	}
	if offset <= 0 || offset > int64(len(r.content)) {
		return 0, nil
	}
	return int64(bytes.Count(r.content[:offset], []byte{'\n'})), nil
}

func (r *chunkedReader) From(_ context.Context, _, _ string, offset, _ int64) (logs.Segment, error) {
	if r.err != nil {
		return logs.Segment{}, r.err
	}
	r.calls++

	size := int64(len(r.content))
	if offset > size {
		offset = size
	}
	end := offset + r.chunk
	if end > size {
		end = size
	}
	return logs.Segment{
		Content: r.content[offset:end], Offset: offset, NextOffset: end, Size: size, Sealed: true,
	}, nil
}

func rawRequest(t *testing.T, reader LogReader, query string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/runs/mse-run-1/tasks/git-clone/log.txt"+query, nil)
	request.SetPathValue("run", "mse-run-1")
	request.SetPathValue("task", "git-clone")

	recorder := httptest.NewRecorder()
	NewLogsHandler(reader).Raw(recorder, request)
	return recorder
}

// A log longer than a single read must come out whole.
func TestTheRawLogIsServedWhole(t *testing.T) {
	reader := &chunkedReader{content: []byte("one line\ntwo lines\nthree lines\n"), chunk: 8}

	recorder := rawRequest(t, reader, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != string(reader.content) {
		t.Errorf("truncated body: %q", got)
	}
	if reader.calls < 2 {
		t.Errorf("%d read(s) — the test proves nothing without pagination", reader.calls)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("type: %q", got)
	}
	// Without `download`, the response displays in the tab: an attachment disposition would
	// save it, which is the other use.
	if got := recorder.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("disposition set with no request: %q", got)
	}
}

// `download` switches the same resource to a file, named from both identifiers: a folder
// where ten files are called `log.txt` is useless.
func TestTheDownloadNamesTheFile(t *testing.T) {
	recorder := rawRequest(t, &chunkedReader{content: []byte("bonjour\n"), chunk: 64}, "?download=1")

	want := `attachment; filename="mse-run-1-git-clone.txt"`
	if got := recorder.Header().Get("Content-Disposition"); got != want {
		t.Errorf("disposition: %q, expected %q", got, want)
	}
}

// An empty log is not an error, and the loop must stop on it.
func TestAnEmptyLogDoesNotLoop(t *testing.T) {
	recorder := rawRequest(t, &chunkedReader{content: nil, chunk: 64}, "")

	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("code %d, %d octet(s)", recorder.Code, recorder.Body.Len())
	}
}

// A failure on the first read gives an error code: it is the only moment one is left to
// give, the header not having gone yet.
func TestAReadFailureReturnsAnError(t *testing.T) {
	recorder := rawRequest(t, &chunkedReader{err: errors.New("minio injoignable")}, "")

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("code %d", recorder.Code)
	}
}

// The file name keeps only what crosses a header unharmed.
func TestTheFileNameIsSanitised(t *testing.T) {
	if got := sanitizeFilename(`mse "run"/1`); got != "mse--run--1" {
		t.Errorf("nom: %q", got)
	}
}
