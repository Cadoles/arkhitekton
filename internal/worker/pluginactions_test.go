package worker

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

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The actions a plugin supplies.
 *
 * What these tests hold is not the shape of the calls — the forge takes care of that — but the
 * three properties that tell a plugin action from an outgoing call: the target comes from the
 * configuration and not from the step, the token is the plugin's, and a switched-off plugin
 * refuses instead of starting.
 */

// writingForge records what the runner sends to the forge.
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

// fakePluginStore returns a plugin connection and a project.
type fakePluginStore struct {
	url       string
	enabled   bool
	repo      string
	consulted []string
}

func (f *fakePluginStore) ResolvedPlugin(
	_ context.Context, _ int64, pluginID string,
) (store.PluginSettings, error) {
	f.consulted = append(f.consulted, pluginID)
	return store.PluginSettings{
		PluginID: pluginID, Enabled: f.enabled,
		// The token is **sealed** in the database, as it really is: that is what opening must cross in
		// order to reach the forge.
		Config: map[string]string{"url": f.url, "token": "sealed:forge-token"},
	}, nil
}

func (f *fakePluginStore) GetProject(_ context.Context, id int64) (store.Project, error) {
	return store.Project{ID: id, Slug: "mse", Repository: f.repo}, nil
}

func pluginRunner(t *testing.T, db *fakePluginStore) *PluginActionRunner {
	t.Helper()
	return NewPluginActionRunner(providerRegistry, db, testOpener{}, 2*time.Second, quietLogger())
}

func execution() store.PlanRun {
	id := int64(7)
	return store.PlanRun{
		ID: 42, ProjectID: &id,
		Context: map[string]string{
			"git_commit": "9f1c0deadbeef", "tag": "1.8.0-rc1",
			"outcome": "success", "number": "128",
		},
	}
}

// The nominal path of a status: the target comes from the plugin's configuration, the commit
// from the triggering fact, and the token leaves unsealed in the header.
func TestACommitStatusBorrowsThePluginsConnection(t *testing.T) {
	forge := newWritingForge(t)
	db := &fakePluginStore{url: forge.server.URL, enabled: true, repo: "equipe/mse"}

	job, err := pluginRunner(t, db).Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{
			"commit": "{git_commit}", "state": "success",
			"context": "arkhitekton/verifications", "description": "{tag} verified",
		},
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	// An action is concluded the moment it is done: nothing to wait for, so no job.
	if job != nil {
		t.Errorf("job identifier %v — an action does not go through the queue", *job)
	}

	methode, route, payload := forge.seenParams()
	if methode != http.MethodPost {
		t.Errorf("method %q", methode)
	}
	// The resolved commit, and not the literal template: without the interpolation, the status
	// would be set on a reference that does not exist.
	if route != "/api/v1/repos/equipe/mse/statuses/9f1c0deadbeef" {
		t.Errorf("path %q", route)
	}
	if payload["state"] != "success" || payload["context"] != "arkhitekton/verifications" {
		t.Errorf("body %v", payload)
	}
	if payload["description"] != "1.8.0-rc1 verified" {
		t.Errorf("description %q — the template must be bound to the fact's payload",
			payload["description"])
	}

	forge.mu.Lock()
	auth := forge.auth
	forge.mu.Unlock()
	// Unsealed for the duration of the call. The step named no token: it borrows the plugin's, and
	// therefore cannot serve to extract another.
	if auth != "token forge-token" {
		t.Errorf("authentication header %q", auth)
	}
}

// A comment goes on the request's thread, with the number the fact carries.
func TestACommentGoesOnTheRequestTheFactNames(t *testing.T) {
	forge := newWritingForge(t)
	db := &fakePluginStore{url: forge.server.URL, enabled: true, repo: "equipe/mse"}

	_, err := pluginRunner(t, db).Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.comment",
		Params: map[string]string{"number": "{number}", "body": "Verified in {tag}."},
	})
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

// A switched-off plugin refuses by name, and calls nothing.
//
// The configuration still exists in the database: starting with it would send a status in the
// name of an integration one believes disabled.
func TestASwitchedOffPluginRefusesInsteadOfStarting(t *testing.T) {
	forge := newWritingForge(t)
	db := &fakePluginStore{url: forge.server.URL, enabled: false, repo: "equipe/mse"}

	_, err := pluginRunner(t, db).Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{"commit": "{git_commit}", "state": "success", "context": "c"},
	})

	if err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("%v — the refusal must say the plugin is switched off", err)
	}
	if methode, _, _ := forge.seenParams(); methode != "" {
		t.Errorf("the forge was called (%s) although the plugin is switched off", methode)
	}
}

// A template left literal says so by quoting its value.
//
// It is the common case, and it is worth naming this way: a reaction wired to `release.built`
// has no `{number}`, and "invalid number" without quoting the value would send someone hunting
// for a typo where it is the fact that does not carry the variable.
func TestAnUninterpolatedNumberQuotesWhatItBecame(t *testing.T) {
	forge := newWritingForge(t)
	db := &fakePluginStore{url: forge.server.URL, enabled: true, repo: "equipe/mse"}

	run := execution()
	delete(run.Context, "number")

	_, err := pluginRunner(t, db).Run(context.Background(), run, plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.comment",
		Params: map[string]string{"number": "{number}", "body": "bonjour"},
	})

	if err == nil || !strings.Contains(err.Error(), "{number}") {
		t.Fatalf("%v — the message must quote what the template became", err)
	}
	if methode, _, _ := forge.seenParams(); methode != "" {
		t.Errorf("the forge was called (%s) on an unreadable number", methode)
	}
}

// A fact naming no project has no connection to borrow.
//
// Starting on the instance's would pick a repository at random, and the status would be set on
// someone else's.
func TestAPluginActionWithNoProjectRefuses(t *testing.T) {
	db := &fakePluginStore{url: "https://forge.example.fr", enabled: true, repo: "equipe/mse"}

	_, err := pluginRunner(t, db).Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{"commit": "abc", "state": "success", "context": "c"},
	})

	if err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("%v", err)
	}
	if len(db.consulted) != 0 {
		t.Errorf("the plugin was consulted with no project: %v", db.consulted)
	}
}

// A refusal from the forge becomes the step's reason, with what is needed to act.
func TestAForgeRefusalNamesTheExpectedScope(t *testing.T) {
	forge := newWritingForge(t)
	forge.status = http.StatusForbidden
	db := &fakePluginStore{url: forge.server.URL, enabled: true, repo: "equipe/mse"}

	_, err := pluginRunner(t, db).Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{"commit": "{git_commit}", "state": "success", "context": "c"},
	})

	if err == nil {
		t.Fatal("a 403 must fail the step")
	}
	// The token's scope is the most frequent cause, and naming it avoids hunting for a repository
	// permission that is already there.
	if !strings.Contains(err.Error(), "write:repository") {
		t.Errorf("message %q — it must name the missing scope", err)
	}
}
