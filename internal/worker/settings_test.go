package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The layering of a project's secrets over the deployment's.
 *
 * It is the place where a run starts with the wrong secret **saying nothing**: the pipeline
 * succeeds in cloning or fails at the push, and the message names the repository or the
 * registry, never the setting that sent it there. Hence tests on a six-line function.
 */

type fakeProjectSecrets struct {
	deployKey string
	registry  string
	err       error
}

func (f *fakeProjectSecrets) DeployKeySecret(context.Context, int64) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.deployKey, f.deployKey != "", nil
}

func (f *fakeProjectSecrets) RegistrySecret(context.Context, int64) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.registry, f.registry != "", nil
}

func schedulerWith(keys ProjectKeys) *Scheduler {
	scheduler := &Scheduler{
		config: map[string]string{
			"pipeline.gitSecret":      "git-credentials",
			"pipeline.registrySecret": "regcred",
			"tekton.namespace":        "mse-tekton-pipelines",
		},
		logger: quietLogger(),
	}
	if keys != nil {
		scheduler.deployKeys = keys
	}
	return scheduler
}

func jobOf(projectID int64) store.Job {
	return store.Job{ID: 1, ProjectID: &projectID}
}

// cloning is a pipeline that clones the project's repository with the project's key.
var cloning = catalog.Pipeline{Kind: "php/unit-tests", Workspaces: []catalog.Workspace{
	{Name: "git-credentials", Secret: &catalog.SecretSource{ConfigKey: ProjectDeployKey}},
}}

// elsewhere is a pipeline that clones another repository with the deployment's credential.
var elsewhere = catalog.Pipeline{Kind: "mse/e2e-tests", Workspaces: []catalog.Workspace{
	{Name: "git-credentials", Secret: &catalog.SecretSource{ConfigKey: "pipeline.gitSecret"}},
}}

// A project's two secrets replace the deployment's, and the rest of the configuration passes
// through untouched.
func TestTheProjectsSecretsReplaceTheDeploymentsOnes(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{
		deployKey: "arkhitekton-project-mse-git",
		registry:  "regcred-mse",
	})

	settings, err := scheduler.settingsFor(context.Background(), jobOf(7), cloning)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}

	if settings[ProjectDeployKey] != "arkhitekton-project-mse-git" {
		t.Errorf("project key = %q", settings[ProjectDeployKey])
	}
	// The deployment's clone credential stays what it is: the project's key opens only its own
	// repository.
	if settings["pipeline.gitSecret"] != "git-credentials" {
		t.Errorf("deployment's credential = %q", settings["pipeline.gitSecret"])
	}
	if settings["pipeline.registrySecret"] != "regcred-mse" {
		t.Errorf("registry secret = %q", settings["pipeline.registrySecret"])
	}
	if settings["tekton.namespace"] != "mse-tekton-pipelines" {
		t.Errorf("the rest of the configuration was lost: %v", settings)
	}
}

// Both are read **separately**: a project that has its key without naming a registry pushes
// where the others push. Handling them together would lose the deployment's registry secret on
// the grounds that the project names none.
func TestAProjectNamingNoRegistryPushesWhereTheOthersPush(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{
		deployKey: "arkhitekton-project-mse-git",
	})

	settings, err := scheduler.settingsFor(context.Background(), jobOf(7), cloning)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}

	if settings[ProjectDeployKey] != "arkhitekton-project-mse-git" {
		t.Errorf("project key = %q", settings[ProjectDeployKey])
	}
	if settings["pipeline.registrySecret"] != "regcred" {
		t.Errorf("registry secret = %q — the project names none",
			settings["pipeline.registrySecret"])
	}
}

/*
 * The refusal to build with no key of its own.
 *
 * It replaces a fallback to a **shared** key, removed because a single key added to several
 * repositories makes its revocation an outage for all of them at once, and says nothing about
 * who cloned what.
 */

// With no active key, the run does not start — and the message names the missing act.
//
// Without it, the clone would fail in the pipeline on an SSH error, very far from its cause:
// nobody would connect it with a key never pasted into the forge.
func TestAProjectWithNoActiveKeyDoesNotBuild(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{registry: "regcred-mse"})

	_, err := scheduler.settingsFor(context.Background(), jobOf(7), cloning)
	if err == nil {
		t.Fatal("a project with no deploy key was able to start")
	}
	for _, expected := range []string{"deploy key", "repository"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("message %q — it must mention %q", err, expected)
		}
	}
}

// The shared map must **never** be written to: it serves every run, and writing into it would
// have the next job carry the previous one's secret — a repository built with another client's
// key, with no error reporting it.
func TestTheSharedConfigurationIsNeverWrittenTo(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{deployKey: "project-key"})

	_, _ = scheduler.settingsFor(context.Background(), jobOf(7), cloning)

	if scheduler.config["pipeline.gitSecret"] != "git-credentials" {
		t.Errorf("shared configuration altered: %v — the next job would inherit it",
			scheduler.config)
	}
}

// A job with no project uses the deployment's secrets. That is the case of the jobs predating
// the migration, and it must break nothing: they are frozen in the queue, and nothing will
// attach a project to them retroactively.
func TestAJobWithNoProjectUsesTheDeploymentsSecrets(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{deployKey: "project-key"})

	settings, err := scheduler.settingsFor(context.Background(), store.Job{ID: 1}, cloning)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}

	if settings[ProjectDeployKey] != "git-credentials" {
		t.Errorf("git key = %q", settings[ProjectDeployKey])
	}
}

// A failed read does not disguise itself as a missing key.
//
// The distinction is what avoids sending someone to enable a key that already exists: the
// database is mute for a second, not the forge. The retry covers the case, and the attempt cap
// concludes if it persists.
func TestAReadFailureDoesNotDisguiseItselfAsAMissingKey(t *testing.T) {
	silentStore := errors.New("silent database")
	scheduler := schedulerWith(&fakeProjectSecrets{err: silentStore})

	_, err := scheduler.settingsFor(context.Background(), jobOf(7), cloning)
	if !errors.Is(err, silentStore) {
		t.Fatalf("error = %v, expected the database's", err)
	}
	if strings.Contains(err.Error(), "coller") {
		t.Errorf("message %q — it sends someone to enable a key that may exist", err)
	}
}

// A pipeline cloning another repository than the project's needs no key of its own, and keeps
// the deployment's credential: the project's key would not open that repository.
func TestAPipelineCloningElsewhereNeedsNoProjectKey(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{registry: "regcred-mse"})

	settings, err := scheduler.settingsFor(context.Background(), jobOf(7), elsewhere)
	if err != nil {
		t.Fatalf("refused for want of a key it does not use: %v", err)
	}
	resolved, err := elsewhere.ResolveSecrets(settings)
	if err != nil {
		t.Fatal(err)
	}
	if name := resolved.Workspaces[0].Secret.SecretName; name != "git-credentials" {
		t.Errorf("mounted %q, expected the deployment's credential", name)
	}
}
