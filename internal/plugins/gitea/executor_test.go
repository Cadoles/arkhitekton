package gitea

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * The forge's actions, against a fake forge: what is sent, where, and what a refusal says.
 */

// writingForge records what the executor sends to the forge.
type writingForge struct {
	server *httptest.Server

	mu     sync.Mutex
	method string
	path   string
	auth   string
	body   map[string]string
	status int
}

func newWritingForge(t *testing.T) *writingForge {
	t.Helper()
	forge := &writingForge{status: http.StatusCreated}
	forge.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		forge.mu.Lock()
		forge.method, forge.path = r.Method, r.URL.Path
		forge.auth = r.Header.Get("Authorization")
		forge.body = map[string]string{}
		_ = json.Unmarshal(raw, &forge.body)
		code := forge.status
		forge.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(forge.server.Close)
	return forge
}

func (f *writingForge) seenParams() (string, string, map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.method, f.path, f.body
}

func writeTarget(url string) capability.Target {
	return capability.Target{
		Project: "mse", Repository: "equipe/mse", Timeout: 2 * time.Second,
		Settings: capability.Settings{"url": url, "token": "forge-token"},
	}
}

// A status goes to the commit, with the plugin's token in the header.
func TestACommitStatusGoesToTheCommit(t *testing.T) {
	forge := newWritingForge(t)

	err := executor{}.Execute(context.Background(), writeTarget(forge.server.URL), "status", map[string]string{
		"commit": "9f1c0deadbeef", "state": "success",
		"context": "arkhitekton/verifications", "description": "1.8.0-rc1 verified",
	})
	if err != nil {
		t.Fatalf("%v", err)
	}

	method, route, payload := forge.seenParams()
	if method != http.MethodPost {
		t.Errorf("method %q", method)
	}
	if route != "/api/v1/repos/equipe/mse/statuses/9f1c0deadbeef" {
		t.Errorf("path %q", route)
	}
	if payload["state"] != "success" || payload["context"] != "arkhitekton/verifications" ||
		payload["description"] != "1.8.0-rc1 verified" {
		t.Errorf("body %v", payload)
	}
	forge.mu.Lock()
	auth := forge.auth
	forge.mu.Unlock()
	if auth != "token forge-token" {
		t.Errorf("authentication header %q", auth)
	}
}

// A comment goes on the request's thread, with the number the fact carries.
func TestACommentGoesOnTheRequestTheFactNames(t *testing.T) {
	forge := newWritingForge(t)

	err := executor{}.Execute(context.Background(), writeTarget(forge.server.URL), "comment",
		map[string]string{"number": "128", "body": "Verified in 1.8.0-rc1."})
	if err != nil {
		t.Fatalf("%v", err)
	}

	_, route, payload := forge.seenParams()
	// A pull request's thread lives under the issues route: in Gitea, a request **is** an
	// augmented issue.
	if route != "/api/v1/repos/equipe/mse/issues/128/comments" {
		t.Errorf("path %q", route)
	}
	if payload["body"] != "Verified in 1.8.0-rc1." {
		t.Errorf("body %q", payload["body"])
	}
}

// A template left literal says so by quoting its value.
//
// It is the common case, and it is worth naming this way: a reaction wired to `release.built`
// has no `{number}`, and "invalid number" without quoting the value would send someone hunting
// for a typo where it is the fact that does not carry the variable.
func TestAnUninterpolatedNumberQuotesWhatItBecame(t *testing.T) {
	forge := newWritingForge(t)

	err := executor{}.Execute(context.Background(), writeTarget(forge.server.URL), "comment",
		map[string]string{"number": "{number}", "body": "bonjour"})

	if err == nil || !strings.Contains(err.Error(), "{number}") {
		t.Fatalf("%v: the message must quote what the template became", err)
	}
	if method, _, _ := forge.seenParams(); method != "" {
		t.Errorf("the forge was called (%s) on an unreadable number", method)
	}
}

// A refusal from the forge says what is needed to act.
func TestAForgeRefusalNamesTheExpectedScope(t *testing.T) {
	forge := newWritingForge(t)
	forge.status = http.StatusForbidden

	err := executor{}.Execute(context.Background(), writeTarget(forge.server.URL), "status",
		map[string]string{"commit": "9f1c0deadbeef", "state": "success", "context": "c"})

	if err == nil {
		t.Fatal("a 403 must fail the action")
	}
	// The token's scope is the most frequent cause, and naming it avoids hunting for a repository
	// permission that is already there.
	if !strings.Contains(err.Error(), "write:repository") {
		t.Errorf("message %q: it must name the missing scope", err)
	}
}
