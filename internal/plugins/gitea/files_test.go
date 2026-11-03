package gitea

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// contentsForge answers the contents API for one file, and keeps what was committed.
type contentsForge struct {
	content   string
	sha       string
	committed map[string]any
}

func (f *contentsForge) serve(t *testing.T) *Writer {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/team/app/contents/version.txt":
			if r.URL.Query().Get("ref") != "main" {
				t.Errorf("read on %q", r.URL.Query().Get("ref"))
			}
			if f.sha == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"type": "file", "sha": f.sha, "content": base64.StdEncoding.EncodeToString([]byte(f.content)),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/team/app/contents":
			_ = json.NewDecoder(r.Body).Decode(&f.committed)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": "c0ffee"}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(server.Close)
	writer, err := NewWriter(Options{URL: server.URL, Token: "t", Repository: "team/app", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

func TestAVersionFileIsUpdatedInOneCommit(t *testing.T) {
	forge := &contentsForge{content: "latest\n", sha: "blob1"}
	commit, err := forge.serve(t).CommitFile(context.Background(), "main", "version.txt", "1.0.0-rc1\n", "chore: version 1.0.0-rc1")
	if err != nil || commit != "c0ffee" {
		t.Fatalf("commit %q, %v", commit, err)
	}
	files := forge.committed["files"].([]any)
	change := files[0].(map[string]any)
	decoded, _ := base64.StdEncoding.DecodeString(change["content"].(string))
	if forge.committed["branch"] != "main" || change["operation"] != "update" || change["sha"] != "blob1" ||
		string(decoded) != "1.0.0-rc1\n" {
		t.Errorf("committed %+v", forge.committed)
	}
}

func TestAMissingVersionFileIsCreated(t *testing.T) {
	forge := &contentsForge{}
	if _, err := forge.serve(t).CommitFile(context.Background(), "main", "version.txt", "1.0.0\n", "m"); err != nil {
		t.Fatal(err)
	}
	change := forge.committed["files"].([]any)[0].(map[string]any)
	if change["operation"] != "create" {
		t.Errorf("operation %v", change["operation"])
	}
}

func TestAnUnchangedVersionFileCommitsNothing(t *testing.T) {
	forge := &contentsForge{content: "1.0.0\n", sha: "blob1"}
	commit, err := forge.serve(t).CommitFile(context.Background(), "main", "version.txt", "1.0.0\n", "m")
	if err != nil || commit != "" || forge.committed != nil {
		t.Errorf("commit %q, %v, committed %+v", commit, err, forge.committed)
	}
}
