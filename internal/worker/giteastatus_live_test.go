package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * A plugin action that really writes to a forge.
 *
 * # Against a forge of one's own, and not the team's
 *
 * A commit status is **visible to others** and cannot be withdrawn. Proving it on a team's
 * repository would leave a test trace on a real commit in order to establish a property that is
 * established just as well on a throwaway instance.
 *
 * The test therefore targets a Gitea stood up for the occasion. It is a real server, its real
 * API, a real token, a real commit — only the address changes.
 *
 *   docker run -d --name gitea-live -p 3080:3000 \
 *     -e GITEA__security__INSTALL_LOCK=true -e GITEA__database__DB_TYPE=sqlite3 \
 *     gitea/gitea:1.22
 *   docker exec -u git gitea-live gitea admin user create --admin \
 *     --username mse --password … --email mse@example.invalid
 *   docker exec -u git gitea-live gitea admin user generate-access-token \
 *     -u mse --scopes write:repository --raw
 *
 * then, once the repository and its commit are created through the API:
 *
 *   TEST_DATABASE_URL=… GITEA_LIVE=1 GITEA_URL=http://127.0.0.1:3080 \
 *     GITEA_TOKEN=… GITEA_REPO=mse/mse-demo GITEA_COMMIT=… \
 *     go test -count=1 -run TestLiveACommitStatus ./internal/worker/ -v
 *
 * # What a 201 does not prove
 *
 * That the status is **there**. The forge can accept a call and make nothing visible of it — a
 * malformed context, an unknown commit accepted leniently. The test therefore re-reads the
 * status through the API, and it is that read which concludes.
 */

func localForge(t *testing.T) (url, token, repo, commit string) {
	t.Helper()
	if os.Getenv("GITEA_LIVE") != "1" {
		t.Skip("GITEA_LIVE not set: the check against a real forge is skipped")
	}
	url = os.Getenv("GITEA_URL")
	token = os.Getenv("GITEA_TOKEN")
	repo = os.Getenv("GITEA_REPO")
	commit = os.Getenv("GITEA_COMMIT")
	if url == "" || token == "" || repo == "" || commit == "" {
		t.Fatal("GITEA_URL, GITEA_TOKEN, GITEA_REPO and GITEA_COMMIT are required")
	}
	return url, token, repo, commit
}

// commitStatuses re-reads the statuses set on a commit.
type returnedStatus struct {
	State       string `json:"status"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

func commitStatuses(t *testing.T, url, token, repo, commit string) []returnedStatus {
	t.Helper()

	target := strings.TrimSuffix(url, "/") + "/api/v1/repos/" + repo + "/statuses/" + commit
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("re-reading request: %v", err)
	}
	request.Header.Set("Authorization", "token "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("re-reading the statuses: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("re-reading the statuses: the forge answered %d", response.StatusCode)
	}

	var statuses []returnedStatus
	if err := json.NewDecoder(response.Body).Decode(&statuses); err != nil {
		t.Fatalf("decoding the statuses: %v", err)
	}
	return statuses
}

func TestLiveACommitStatusIsReallySet(t *testing.T) {
	url, token, repo, commit := localForge(t)
	db := throwawayDatabase(t)
	ctx := context.Background()

	cipher, err := secrets.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	scelle, err := cipher.Seal(token)
	if err != nil {
		t.Fatalf("sealing the token: %v", err)
	}

	projectID, err := db.CreateProject(ctx, store.Project{
		// The repository comes from the project's settings, and not from the step: that is where it is
		// declared once and for all.
		Repository: repo, Label: "Demo", ForgeURL: url,
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("projectRow: %v", err)
	}

	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "gitea", Enabled: true,
		Config: map[string]string{"url": url, "token": scelle},
	}, "test"); err != nil {
		t.Fatalf("plugin global: %v", err)
	}
	if err := db.SetProjectPluginSettings(ctx, projectID, store.PluginSettings{
		PluginID: "gitea", Enabled: true,
	}, "test"); err != nil {
		t.Fatalf("project's plugin: %v", err)
	}

	// The context and the tag can be overridden. On a throwaway forge they may look like a real
	// check; on a real repository, a status cannot be withdrawn, and it must announce itself for
	// what it is — a verification — rather than pass for a CI verdict nobody asked for.
	runContext := os.Getenv("GITEA_STATUS_CONTEXT")
	if runContext == "" {
		runContext = "arkhitekton/verifications"
	}
	tag := os.Getenv("GITEA_STATUS_TAG")
	if tag == "" {
		tag = "1.8.0-rc1"
	}

	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Status on a concluded build",
		EventType: string(event.ReleaseCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "gitea.status",
				Params: map[string]string{
					"commit": "{git_ref}",
					// Literal, and that is the right usage: a fact's outcome and a forge status's state are two
					// distinct vocabularies. A reaction that wants to mirror the outcome matches on `outcome`. See
					// the next test, which holds the other half.
					"state":       "success",
					"context":     runContext,
					"description": "Release {tag} construite",
					"target_url":  "https://dashboard.example.fr/releases/{tag}",
				},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("reaction: %v", err)
	}

	// The action runner, with the plugin actions wired — `main`'s assembly, without the webhook
	// client this step does not need.
	actions := NewActionRunner(nil,
		testResolver{db: db, cipher: cipher}, quietLogger()).
		WithPlugins(NewPluginActionRunner(db, cipher, 15*time.Second, quietLogger()))

	engine := NewEngine(db, stepRouter{actions: actions}, quietLogger())
	dispatcher := NewDispatcher(db, engine, quietLogger())

	// The triggering fact goes through the real write path: where facts come from has already been
	// exercised by the probe, and doing it again here would tie this test to Redmine's
	// availability.
	recorded := event.ReleaseCreatedAt(time.Now().UTC(), projectID, 42,
		tag, "pre", commit, "1.8.0")
	fresh, err := db.RecordEvent(ctx, recorded)
	if err != nil {
		t.Fatalf("writing the fact: %v", err)
	}
	if !fresh {
		t.Fatal("the fact was already known: nothing would trigger")
	}
	dispatcher.Dispatch(ctx, recorded)

	/*
	 * 1. The run concludes.
	 */
	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(executions) != 1 {
		t.Fatalf("%d runs, expected 1", len(executions))
	}
	if executions[0].State != "succeeded" {
		t.Fatalf("run %q — reason: %s",
			executions[0].State, firstStepDetail(executions[0]))
	}

	/*
	 * 2. And the status is **there**, re-read from the forge.
	 *
	 * It is the only assertion that counts: an accepted call is not a status set.
	 */
	statuses := commitStatuses(t, url, token, repo, commit)
	var applied *returnedStatus
	for index := range statuses {
		if statuses[index].Context == runContext {
			applied = &statuses[index]
			break
		}
	}
	if applied == nil {
		t.Fatalf("no status with context %q on %s (%d statuses read)",
			runContext, commit[:8], len(statuses))
	}

	if applied.State != "success" {
		t.Errorf("state %q, expected success", applied.State)
	}
	if !strings.Contains(applied.Description, tag) {
		t.Errorf("description %q — it must carry the interpolated tag", applied.Description)
	}
	if !strings.Contains(applied.TargetURL, tag) {
		t.Errorf("link %q — it must carry the interpolated tag", applied.TargetURL)
	}
	t.Logf("status re-read on %s: %s / %s — %q",
		commit[:8], applied.Context, applied.State, applied.Description)
}

// A switched-off plugin does not speak to the forge, even with a valid connection.
//
// The configuration stays in the database when a plugin is switched off: starting with it would
// send a status in the name of an integration one believes disabled.
func TestLiveASwitchedOffPluginDoesNotWriteToTheForge(t *testing.T) {
	url, token, repo, commit := localForge(t)
	db := throwawayDatabase(t)
	ctx := context.Background()

	cipher, _ := secrets.New(testEncryptionKey)
	scelle, err := cipher.Seal(token)
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: repo, Label: "Demo", ForgeURL: url,
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("projectRow: %v", err)
	}
	// The connection is there, complete and valid — but switched off.
	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "gitea", Enabled: false,
		Config: map[string]string{"url": url, "token": scelle},
	}, "test"); err != nil {
		t.Fatalf("plugin global: %v", err)
	}

	const runContext = "arkhitekton/must-not-appear"
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Status on a switched-off plugin",
		EventType: string(event.ReleaseCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "gitea.status",
				Params: map[string]string{
					"commit": "{git_ref}", "state": "success", "context": runContext,
				},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("reaction: %v", err)
	}

	actions := NewActionRunner(nil, testResolver{db: db, cipher: cipher}, quietLogger()).
		WithPlugins(NewPluginActionRunner(db, cipher, 15*time.Second, quietLogger()))
	dispatcher := NewDispatcher(db,
		NewEngine(db, stepRouter{actions: actions}, quietLogger()), quietLogger())

	recorded := event.ReleaseCreatedAt(time.Now().UTC(), projectID, 43,
		"1.8.0-rc2", "pre", commit, "1.8.0")
	if _, err := db.RecordEvent(ctx, recorded); err != nil {
		t.Fatalf("writing the fact: %v", err)
	}
	dispatcher.Dispatch(ctx, recorded)

	// Nothing arrived at the forge.
	for _, observed := range commitStatuses(t, url, token, repo, commit) {
		if observed.Context == runContext {
			t.Fatalf("a status was set although the plugin is switched off: %+v", observed)
		}
	}

	// And the refusal can be read, rather than passing for a silent success.
	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(executions) != 1 || executions[0].State != "failed" {
		t.Fatalf("runs = %+v, expected one failed", executions)
	}
	reason := firstStepDetail(executions[0])
	t.Logf("refusal recorded: %s", reason)
	// The refusal comes from the store, which returns `ErrPluginDisabled` before the runner even
	// has to observe it. What counts is that it names the plugin and its state, not the exact
	// wording.
	if !strings.Contains(reason, "gitea") || !strings.Contains(reason, "disabled") {
		t.Errorf("reason %q — it must name the plugin and its being switched off", reason)
	}
}

// A state outside the forge's vocabulary is refused **before** the call.
//
// # The defect the real check revealed
//
// The offending template was written `state: {outcome}`: mirroring the fact's outcome, which
// yields `succeeded` because that is a fact's vocabulary, where the forge says `success`. Gitea
// **accepted** the call and stored the state as it was: a status outside its own vocabulary,
// which no interface can display, and not a message.
//
// A lenient recipient is the worst case — nothing fails, and nothing works. The closed set is
// therefore checked again after interpolation, and the step fails naming what is allowed.
//
// # Why the test's template changed
//
// `outcome` no longer exists: it lived on `release.built`, removed when the facts were reduced.
// The trap, though, has not moved — **any** payload field interpolated into a closed-set
// parameter sets it. The test therefore sets it with `{kind}`, which yields `pre`: a perfectly
// legitimate value for a release, and one Gitea has no reason to allow as a state.
func TestLiveAStateOutsideTheVocabularyDoesNotReachTheForge(t *testing.T) {
	url, token, repo, commit := localForge(t)
	db := throwawayDatabase(t)
	ctx := context.Background()

	cipher, _ := secrets.New(testEncryptionKey)
	scelle, err := cipher.Seal(token)
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: repo, Label: "Demo", ForgeURL: url,
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("projectRow: %v", err)
	}
	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "gitea", Enabled: true,
		Config: map[string]string{"url": url, "token": scelle},
	}, "test"); err != nil {
		t.Fatalf("plugin global: %v", err)
	}

	const runContext = "arkhitekton/invalid-vocabulary"
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Status mirroring the outcome",
		EventType: string(event.ReleaseCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "gitea.status",
				Params: map[string]string{
					"commit": "{git_ref}", "state": "{kind}", "context": runContext,
				},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("reaction: %v", err)
	}

	actions := NewActionRunner(nil, testResolver{db: db, cipher: cipher}, quietLogger()).
		WithPlugins(NewPluginActionRunner(db, cipher, 15*time.Second, quietLogger()))
	dispatcher := NewDispatcher(db,
		NewEngine(db, stepRouter{actions: actions}, quietLogger()), quietLogger())

	recorded := event.ReleaseCreatedAt(time.Now().UTC(), projectID, 44,
		"1.8.0-rc3", "pre", commit, "1.8.0")
	if _, err := db.RecordEvent(ctx, recorded); err != nil {
		t.Fatalf("writing the fact: %v", err)
	}
	dispatcher.Dispatch(ctx, recorded)

	// Nothing reached the forge. That is the assertion that counts: before the fix, a status with
	// state `succeeded` was indeed deposited there.
	for _, observed := range commitStatuses(t, url, token, repo, commit) {
		if observed.Context == runContext {
			t.Fatalf("a status outside the vocabulary was set: %+v", observed)
		}
	}

	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(executions) != 1 || executions[0].State != "failed" {
		t.Fatalf("runs = %+v, expected one failed", executions)
	}
	reason := firstStepDetail(executions[0])
	t.Logf("refusal recorded: %s", reason)
	// The message must carry the three elements of the diagnosis: what was obtained, where it comes
	// from, and what is allowed.
	for _, expected := range []string{"pre", "{kind}", "success"} {
		if !strings.Contains(reason, expected) {
			t.Errorf("reason %q — it must carry %q", reason, expected)
		}
	}
}

// firstStepDetail returns the reason of a run's first step.
func firstStepDetail(run store.PlanRunSummary) string {
	if len(run.Steps) == 0 {
		return "(no step)"
	}
	return run.Steps[0].Detail
}
