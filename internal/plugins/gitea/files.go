package gitea

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

var _ provider.FileWriter = (*Writer)(nil)

// CommitFile writes a file on a branch through the contents API, in one commit. The file's current
// blob is read first: Gitea needs it to update a file, and identical content commits nothing.
func (w *Writer) CommitFile(ctx context.Context, branch, path, content, message string) (string, error) {
	branch, path = strings.TrimSpace(branch), strings.Trim(strings.TrimSpace(path), "/")
	if branch == "" || path == "" {
		return "", fmt.Errorf("gitea: a branch and a path are required to commit a file")
	}
	geste := fmt.Sprintf("committing %s on %s", path, branch)

	current, found, err := w.fileAt(ctx, branch, path)
	if err != nil {
		return "", err
	}
	if found && current.content == content {
		return "", nil
	}

	change := map[string]string{
		"operation": "create", "path": path,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
	}
	if found {
		change["operation"], change["sha"] = "update", current.sha
	}
	status, body, err := w.call(ctx, http.MethodPost, "/api/v1/repos/"+w.owner+"/"+w.repo+"/contents",
		map[string]any{"branch": branch, "message": message, "files": []map[string]string{change}})
	if err != nil {
		return "", fmt.Errorf("gitea: %s: %w", geste, err)
	}
	if err := judge(status, geste); err != nil {
		return "", err
	}
	var answer struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Commit.SHA == "" {
		return "", fmt.Errorf("gitea: %s: the forge did not say which commit it made", geste)
	}
	return answer.Commit.SHA, nil
}

type fileBlob struct {
	sha     string
	content string
}

// fileAt reads a file on a branch. A file the branch does not carry is not an error: it is created.
func (w *Writer) fileAt(ctx context.Context, branch, path string) (fileBlob, bool, error) {
	route := "/api/v1/repos/" + w.owner + "/" + w.repo + "/contents/" + path + "?ref=" + url.QueryEscape(branch)
	status, body, err := w.call(ctx, http.MethodGet, route, nil)
	if err != nil {
		return fileBlob{}, false, fmt.Errorf("gitea: reading %s on %s: %w", path, branch, err)
	}
	if status == http.StatusNotFound {
		return fileBlob{}, false, nil
	}
	if err := judge(status, "reading "+path+" on "+branch); err != nil {
		return fileBlob{}, false, err
	}
	var answer struct {
		Type    string `json:"type"`
		SHA     string `json:"sha"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Type != "file" {
		return fileBlob{}, false, fmt.Errorf("gitea: %s on %s is not a file", path, branch)
	}
	decoded, err := base64.StdEncoding.DecodeString(answer.Content)
	if err != nil {
		return fileBlob{}, false, fmt.Errorf("gitea: reading %s on %s: %w", path, branch, err)
	}
	return fileBlob{sha: answer.SHA, content: string(decoded)}, true, nil
}

// call sends a request with an optional JSON body, and returns the status and the body.
func (w *Writer) call(ctx context.Context, method, route string, payload any) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	target, err := w.baseURL.Parse(strings.TrimRight(w.baseURL.Path, "/") + route)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := w.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, body, err
}

// judge turns a status into the error the writing helpers return.
func judge(status int, geste string) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusForbidden, status == http.StatusUnauthorized:
		return fmt.Errorf("gitea: %s refused (%d) — does the token have the write:repository scope?", geste, status)
	case status == http.StatusNotFound:
		return fmt.Errorf("gitea: %s: not found (404) — the target or the repository is out of the token's reach", geste)
	default:
		return fmt.Errorf("gitea: %s: the forge answered %d", geste, status)
	}
}
