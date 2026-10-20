package worker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * The actions a plugin supplies.
 *
 * What these tests hold is not the shape of the calls, which the plugin owns, but the properties
 * that tell a plugin action from an outgoing call: the target comes from the configuration and
 * not from the step, the token is the plugin's, and a switched-off plugin refuses instead of
 * starting.
 */

// recordingExecutor records what the runner hands to a plugin.
type recordingExecutor struct {
	mu     sync.Mutex
	calls  int
	target capability.Target
	suffix string
	params map[string]string
}

func (e *recordingExecutor) Execute(
	_ context.Context, target capability.Target, suffix string, params map[string]string,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.target, e.suffix, e.params = target, suffix, params
	return nil
}

func (e *recordingExecutor) seen() (int, capability.Target, string, map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, e.target, e.suffix, e.params
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

func pluginRunner(t *testing.T, db *fakePluginStore) (*PluginActionRunner, *recordingExecutor) {
	t.Helper()
	recorder := &recordingExecutor{}
	return NewPluginActionRunner(forgeRegistry(recorder), db, testOpener{}, 2*time.Second, quietLogger()), recorder
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

// The nominal path: the target comes from the plugin's configuration, the parameters from the
// triggering fact, and the token reaches the plugin unsealed.
func TestAPluginActionBorrowsThePluginsConnection(t *testing.T) {
	db := &fakePluginStore{url: "https://forge.example.fr", enabled: true, repo: "equipe/mse"}
	runner, recorder := pluginRunner(t, db)

	job, err := runner.Run(context.Background(), execution(), plan.Geste{
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
		t.Errorf("job identifier %v: an action does not go through the queue", *job)
	}

	calls, target, suffix, params := recorder.seen()
	if calls != 1 || suffix != "status" {
		t.Fatalf("%d call(s), suffix %q", calls, suffix)
	}
	if target.Repository != "equipe/mse" || target.Project != "mse" {
		t.Errorf("target %+v", target)
	}
	// Unsealed for the duration of the call. The step named no token: it borrows the plugin's, and
	// therefore cannot serve to extract another.
	if target.Settings["token"] != "forge-token" || target.Settings["url"] != "https://forge.example.fr" {
		t.Errorf("settings %v", target.Settings)
	}
	// The resolved commit, and not the literal template.
	if params["commit"] != "9f1c0deadbeef" || params["description"] != "1.8.0-rc1 verified" {
		t.Errorf("params %v: the templates must be bound to the fact's payload", params)
	}
}

// A switched-off plugin refuses by name, and calls nothing.
//
// The configuration still exists in the database: starting with it would send a status in the
// name of an integration one believes disabled.
func TestASwitchedOffPluginRefusesInsteadOfStarting(t *testing.T) {
	db := &fakePluginStore{url: "https://forge.example.fr", enabled: false, repo: "equipe/mse"}
	runner, recorder := pluginRunner(t, db)

	_, err := runner.Run(context.Background(), execution(), plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{"commit": "{git_commit}", "state": "success", "context": "c"},
	})

	if err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("%v: the refusal must say the plugin is switched off", err)
	}
	if calls, _, _, _ := recorder.seen(); calls != 0 {
		t.Errorf("the plugin was called although it is switched off")
	}
}

// A fact naming no project has no connection to borrow.
//
// Starting on the instance's would pick a repository at random, and the status would be set on
// someone else's.
func TestAPluginActionWithNoProjectRefuses(t *testing.T) {
	db := &fakePluginStore{url: "https://forge.example.fr", enabled: true, repo: "equipe/mse"}
	runner, _ := pluginRunner(t, db)

	_, err := runner.Run(context.Background(), store.PlanRun{}, plan.Geste{
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
