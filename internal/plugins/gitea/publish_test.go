package gitea

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// threadForge holds a pull request's thread and records what is written to it.
type threadForge struct {
	comments []map[string]any
	written  []string
}

func (f *threadForge) serve(t *testing.T) *Writer {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "arkhitekton"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/team/app/issues/12/comments":
			_ = json.NewEncoder(w).Encode(f.comments)
		default:
			f.written = append(f.written, r.Method+" "+r.URL.Path+" "+string(raw))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(server.Close)
	writer, err := NewWriter(Options{URL: server.URL, Token: "t", Repository: "team/app", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

func comment(id int64, login, body string) map[string]any {
	return map[string]any{"id": id, "body": body, "user": map[string]string{"login": login}}
}

// The summary is posted once, then edited: the edit targets the comment this account posted with
// the marker, never another's.
func TestASummaryIsPostedThenKeptUpToDate(t *testing.T) {
	forge := &threadForge{comments: []map[string]any{
		comment(1, "someone", "Merci <!-- arkhitekton:pr-summary -->"),
	}}
	writer := forge.serve(t)
	if err := writer.UpsertComment(context.Background(), 12, "pr-summary", "Résumé 1"); err != nil {
		t.Fatal(err)
	}
	if len(forge.written) != 1 || !strings.HasPrefix(forge.written[0], "POST /api/v1/repos/team/app/issues/12/comments") ||
		!strings.Contains(forge.written[0], "arkhitekton:pr-summary") {
		t.Fatalf("written %v", forge.written)
	}

	forge.comments = append(forge.comments, comment(2, "arkhitekton", "Résumé 1\n\n<!-- arkhitekton:pr-summary -->"))
	forge.written = nil
	if err := writer.UpsertComment(context.Background(), 12, "pr-summary", "Résumé 2"); err != nil {
		t.Fatal(err)
	}
	if len(forge.written) != 1 || !strings.HasPrefix(forge.written[0], "PATCH /api/v1/repos/team/app/issues/comments/2 ") ||
		!strings.Contains(forge.written[0], "Résumé 2") {
		t.Errorf("written %v", forge.written)
	}
}

// A release is published on its tag, marked pre-release when asked.
func TestAReleaseIsPublishedOnItsTag(t *testing.T) {
	forge := &threadForge{}
	if err := forge.serve(t).CreateRelease(context.Background(), "1.0.0-rc1", "", "- #1", true); err != nil {
		t.Fatal(err)
	}
	if len(forge.written) != 1 || !strings.Contains(forge.written[0], `"tag_name":"1.0.0-rc1"`) ||
		!strings.Contains(forge.written[0], `"prerelease":true`) || !strings.Contains(forge.written[0], `"name":"1.0.0-rc1"`) {
		t.Errorf("written %v", forge.written)
	}
}

// The writer edits comments, and nothing else.
func TestTheWriterEditsNothingButComments(t *testing.T) {
	writer := (&threadForge{}).serve(t)
	if _, _, err := writer.call(context.Background(), http.MethodPatch, "/api/v1/repos/team/app/pulls/12", map[string]string{}); err == nil {
		t.Error("a pull request was edited")
	}
	if _, _, err := writer.call(context.Background(), http.MethodDelete, "/api/v1/repos/team/app/issues/comments/2", nil); err == nil {
		t.Error("a comment was deleted")
	}
}
