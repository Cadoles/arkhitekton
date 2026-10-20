package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/webhook"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * The whole loop, on a real database and a real instance.
 *
 * # What the previous checks did not cover
 *
 * `redmineprobe_live_test.go` queries the real Redmine, but projects into an in-memory store:
 * nothing was written, no cursor was persisted, no reaction was routed. The unit tests cover
 * the loop, but with a test probe and a simulated database.
 *
 * What was left unexercised is therefore precisely the junction: **real facts crossing the real
 * schema**. That is where the uniqueness constraints live, the type of the payload column, the
 * shape constraint on `payload`, and the `sync_state` cursor. A key too long, a payload a
 * constraint refuses, a fact with no date — none of that shows anywhere else.
 *
 * # It writes, but never where it counts
 *
 *   - **Redmine** is only read: its transport structurally refuses any method other than GET
 *     and HEAD;
 *   - **the database** is the one from `make test-integration`, throwaway, whose schema is
 *     reset at the start of this test. It is not a deployment's database, and the DSN comes
 *     from `TEST_DATABASE_URL`;
 *   - **the steps** do not go out: the plan carries only one action handed to a runner that
 *     counts, and nothing else.
 *
 * A double guard: `TEST_DATABASE_URL` **and** `REDMINE_LIVE=1`.
 *
 *   set -a; . deploy/overlays/local/secrets/dashboard.env; set +a
 *   TEST_DATABASE_URL=… REDMINE_LIVE=1 REDMINE_URL=$TRACKER_URL \
 *     REDMINE_PROJECT=$TRACKER_PROJECT REDMINE_API_KEY=$TRACKER_API_KEY \
 *     go test -count=1 -run TestLiveTheWholeLoop ./internal/worker/ -v
 */

// throwawayDatabase opens the integration database and resets its schema.
//
// To zero, deliberately: inheriting a previous test's state would run this test on facts it did
// not produce.
func throwawayDatabase(t *testing.T) *store.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set: the whole loop is skipped")
	}
	cfg := config.DatabaseConfig{DSN: dsn, MaxConns: 4, ConnMaxLifetime: time.Minute}

	ctx := context.Background()
	rawBody, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	if _, err := rawBody.Pool().Exec(ctx,
		"drop schema public cascade; create schema public;"); err != nil {
		rawBody.Close()
		t.Fatalf("resetting the schema: %v", err)
	}
	rawBody.Close()

	if err := store.Migrate(ctx, cfg, quietLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// countingRunner keeps the steps handed to it, emitting nothing.
type countingRunner struct {
	sent []plan.Geste
}

func (g *countingRunner) Run(
	_ context.Context, _ store.PlanRun, geste plan.Geste,
) (*int64, error) {
	g.sent = append(g.sent, geste)
	return nil, nil
}

// harness carries what an end-to-end test must stand up.
type harness struct {
	db             *store.DB
	cipher         *secrets.Cipher
	projectID      int64
	trackerProject string
	probe          Probe
}

// newHarness assembles database, project, plugin and probe as `main` would.
func newHarness(t *testing.T) harness {
	t.Helper()
	if os.Getenv("REDMINE_LIVE") != "1" {
		t.Skip("REDMINE_LIVE not set: the check against the real instance is skipped")
	}
	trackerProject := os.Getenv("REDMINE_PROJECT")
	if trackerProject == "" {
		t.Fatal("REDMINE_PROJECT is required")
	}

	ctx := context.Background()
	db := throwawayDatabase(t)

	// The sealing, as in production: the API key enters the database encrypted only, and it is the
	// loop that will open it.
	cipher, err := secrets.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	sealed, err := cipher.Seal(os.Getenv("REDMINE_API_KEY"))
	if err != nil {
		t.Fatalf("sealing the key: %v", err)
	}

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: "CNOUS/mse", Label: "MSE", ForgeURL: "https://forge.example.fr",
		Dockerfile: "./Dockerfile", BuildContext: ".",
		TrackerProject: trackerProject, CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("projectRow: %v", err)
	}

	// The plugin, at both scopes: the instance carries the connection, the project enables it. That
	// is the real split.
	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "redmine", Enabled: true,
		Config: map[string]string{
			"url": os.Getenv("REDMINE_URL"), "api_key": sealed,
		},
	}, "test"); err != nil {
		t.Fatalf("global settings: %v", err)
	}
	if err := db.SetProjectPluginSettings(ctx, projectID, store.PluginSettings{
		PluginID: "redmine", Enabled: true,
		Config: map[string]string{"project": trackerProject},
	}, "test"); err != nil {
		t.Fatalf("project settings: %v", err)
	}

	probe := NewTrackerProbe(NewTrackerSync(db, redmineTracker, quietLogger()),
		func(target capability.Target) (TrackerReader, error) {
			target.Timeout = 30 * time.Second
			return liveTracker(target)
		})

	return harness{
		db: db, cipher: cipher, projectID: projectID,
		trackerProject: trackerProject, probe: probe,
	}
}

// TestLiveTheWholeLoopOnARealDatabase runs probe, database and routing together.
func TestLiveTheWholeLoopOnARealDatabase(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db, projectID, probe := h.db, h.projectID, h.probe

	// A reaction on the declared versions. It is what would have fired twenty times if the
	// bootstrap had discarded its facts.
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Notify on a new version",
		EventType: string(event.VersionCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "webhook"}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("reaction: %v", err)
	}

	counter := &countingRunner{}
	engine := NewEngine(db, counter, quietLogger())
	dispatcher := NewDispatcher(db, engine, quietLogger())

	runner := NewProbeRunner(liveRegistry(), db, h.cipher, time.Minute, quietLogger(), probe).
		WithDispatcher(dispatcher)

	/*
	 * 1. The bootstrap: it writes its facts, and triggers nothing.
	 */
	runner.sweep(ctx, probe)

	afterBootstrap := countFacts(t, db)
	if afterBootstrap == 0 {
		t.Fatal("no fact written at bootstrap: the probe read nothing, " +
			"or the schema refused them")
	}
	if len(counter.sent) != 0 {
		t.Errorf("%d steps started at bootstrap, expected none — "+
			"enabling something does not wake the past", len(counter.sent))
	}
	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(executions) != 0 {
		t.Errorf("%d runs opened at bootstrap, expected none", len(executions))
	}
	// The cursor is set, in the database this time.
	state, err := db.GetSyncState(ctx, redmineTracker.Tool, projectID)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if state.Cursor == "" {
		t.Error("no cursor persisted: the next pass would re-read everything")
	}
	t.Logf("bootstrap: %d facts written, cursor set, no run", afterBootstrap)

	/*
	 * 2. The next pass: it sees the snapshot again, and does not re-trigger.
	 *
	 * It is the defect fixed during the migration, exercised here end to end — real database, real
	 * uniqueness constraints, real routing.
	 */
	runner.sweep(ctx, probe)

	if after := countFacts(t, db); after != afterBootstrap {
		t.Errorf("%d facts after the second pass, %d after the bootstrap: "+
			"the snapshot was observed again", after, afterBootstrap)
	}
	if len(counter.sent) != 0 {
		t.Errorf("%d steps started at the second pass, expected none", len(counter.sent))
	}

	/*
	 * 3. A genuinely new fact does trigger.
	 *
	 * Without this step, everything above would be satisfied by routing that is unplugged: "nothing
	 * starts" is proved too easily when nothing can start. A fact already observed is erased, which
	 * is exactly the state one would be in if this version had just been declared.
	 */
	forget(t, db, string(event.VersionCreated))
	runner.sweep(ctx, probe)

	if len(counter.sent) == 0 {
		t.Fatal("a new fact triggered no step: the routing is unplugged, " +
			"and the two observations above prove nothing")
	}
	executions, err = db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(executions) != 1 {
		t.Errorf("%d runs, expected 1", len(executions))
	}
	t.Logf("new fact: %d step(s) started, %d run(s) opened",
		len(counter.sent), len(executions))
}

// countFacts returns the number of facts written.
func countFacts(t *testing.T, db *store.DB) int {
	t.Helper()

	var total int
	if err := db.Pool().QueryRow(context.Background(),
		"select count(*) from events").Scan(&total); err != nil {
		t.Fatalf("counting the facts: %v", err)
	}
	return total
}

// forget erases a fact of a given type, so that the next pass sees it as new.
func forget(t *testing.T, db *store.DB, factType string) {
	t.Helper()

	tag, err := db.Pool().Exec(context.Background(),
		"delete from events where id = (select id from events where type = $1 limit 1)",
		factType)
	if err != nil {
		t.Fatalf("forgetting a fact: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("no fact of type %q to forget", factType)
	}
}

// testEncryptionKey seals the API key for the duration of the test. Worthless outside it: it is
// regenerated at each run of the schema.
const testEncryptionKey = "sN0Xk1t2Qv8yZ3aB4cD5eF6gH7iJ8kL9mN0oP1qR2sQ="

/*
 * An outgoing call that really starts.
 *
 * # Where it goes, and why not elsewhere
 *
 * To a local receiver, stood up by the test. The chain is whole — real PostgreSQL, real Redmine
 * facts, real `webhook.Client`, real TCP, real response — but **nothing leaves the machine**.
 * Targeting a team chat or a public service would publish version names at a third party's in
 * order to prove a property that is proved here.
 *
 * An accepted consequence: the transport runs with `AllowPrivate` and `AllowInsecure`, without
 * which it would refuse the local loopback. The second case of this test puts the guard rail
 * back to its default and checks that it refuses — both halves count.
 */

// receiver keeps the call it receives.
type receiver struct {
	server  *httptest.Server
	mu      sync.Mutex
	calls   int
	methode string
	typeMIM string
	auth    string
	payload string
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		payload, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.calls++
		r.methode = req.Method
		r.typeMIM = req.Header.Get("Content-Type")
		r.auth = req.Header.Get("Authorization")
		r.payload = string(payload)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) seenParams() (int, string, string, string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.methode, r.typeMIM, r.auth, r.payload
}

// testResolver opens a named sensitive field, as `main` does.
type testResolver struct {
	db     *store.DB
	cipher *secrets.Cipher
}

func (r testResolver) ResolveSecret(
	ctx context.Context, projectID *int64, reference string,
) (string, error) {
	pluginID, field, found := strings.Cut(reference, ".")
	if !found || projectID == nil {
		return "", fmt.Errorf("invalid designation %q", reference)
	}
	settings, err := r.db.ResolvedPlugin(ctx, *projectID, pluginID)
	if err != nil {
		return "", err
	}
	return r.cipher.Open(settings.Config[field])
}

// madeUpToken is nobody's secret: it serves to check that a **named** token is unsealed, set in
// a header, and never reaches the body.
const madeUpToken = "token-made-up-for-this-test-only"

func TestLiveAnOutgoingCallReallyStarts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db, projectID, probe := h.db, h.projectID, h.probe

	// A plugin carrying a sensitive field, with a made-up value. It is the token the step will
	// **name** — never a real key: sending one out to a test server would be exactly what the rule
	// forbids.
	scelle, err := h.cipher.Seal(madeUpToken)
	if err != nil {
		t.Fatalf("sealing the token: %v", err)
	}
	if err := db.SetPluginSettings(ctx, store.PluginSettings{
		PluginID: "gitea", Enabled: true,
		Config: map[string]string{"url": "https://forge.example.fr", "token": scelle},
	}, "test"); err != nil {
		t.Fatalf("plugin carrying the token: %v", err)
	}

	received := newReceiver(t)

	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Real outgoing call",
		EventType: string(event.VersionCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "webhook",
				Params: map[string]string{
					"url": received.server.URL,
					// The template is bound to the fact's payload: `{name}` must become a real version name.
					"body":        `{"version":"{name}","status":"{status}"}`,
					"auth_header": "Authorization",
					"auth_secret": "gitea.token",
				},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("reaction: %v", err)
	}

	// The real action runner, with the real transport. The two permissions are those a local
	// deployment declares, and `main` logs them as a warning when they are set.
	actions := NewActionRunner(liveActions(), webhook.New(webhook.Options{
		AllowPrivate: true, AllowInsecure: true, Timeout: 10 * time.Second,
	}), testResolver{db: db, cipher: h.cipher}, quietLogger())

	engine := NewEngine(db, stepRouter{actions: actions}, quietLogger())
	runner := NewProbeRunner(liveRegistry(), db, h.cipher, time.Minute, quietLogger(), probe).
		WithDispatcher(NewDispatcher(db, engine, quietLogger()))

	runner.sweep(ctx, probe) // bootstrap: nothing is sent
	if calls, _, _, _, _ := received.seenParams(); calls != 0 {
		t.Fatalf("%d calls at bootstrap, expected none", calls)
	}

	// A fact becomes new again, and the reaction starts for real.
	forget(t, db, string(event.VersionCreated))
	runner.sweep(ctx, probe)

	calls, methode, typeMIM, auth, payload := received.seenParams()
	if calls != 1 {
		t.Fatalf("%d calls received, expected 1", calls)
	}
	if methode != http.MethodPost {
		t.Errorf("method %q", methode)
	}
	if !strings.Contains(typeMIM, "application/json") {
		t.Errorf("content type %q", typeMIM)
	}

	// The token is unsealed and set in a header.
	if auth != madeUpToken {
		t.Errorf("authentication header %q, expected the unsealed token", auth)
	}
	// And it is **nowhere** in the body. It is the rule made observable: a sensitive field is named,
	// never interpolated.
	if strings.Contains(payload, madeUpToken) {
		t.Error("the token leaked into the call's body")
	}

	// The template was bound: `{name}` is no longer literal.
	var charge map[string]string
	if err := json.Unmarshal([]byte(payload), &charge); err != nil {
		t.Fatalf("unreadable body (%q): %v", payload, err)
	}
	if charge["version"] == "" || strings.Contains(charge["version"], "{") {
		t.Errorf("version returned %q — the template was not bound", charge["version"])
	}
	t.Logf("call received: POST, version %q, status %q", charge["version"], charge["status"])

	// The run concludes, and so does its step.
	executions, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(executions) != 1 {
		t.Fatalf("%d runs, expected 1", len(executions))
	}
	if executions[0].State != "succeeded" {
		t.Errorf("run %q, expected concluded", executions[0].State)
	}
	if len(executions[0].Steps) != 1 || executions[0].Steps[0].State != "succeeded" {
		t.Errorf("gestes = %+v", executions[0].Steps)
	}
}

// The transport's two guard rails are alive in the real chain.
//
// The previous test disarmed them to target the local loop — what a local deployment declares
// knowingly. This one rearms them, one at a time, because they do not protect against the same
// thing:
//
//   - **clear text** refuses `http://`, whatever the target. A webhook payload carries a tag,
//     sometimes a request title, and the token that authenticates it;
//   - **the private target** refuses the loopback and the internal networks, judging the
//     **resolved address**. It is the defence against server-side request forgery, and the more
//     important of the two.
//
// Exercising them together would prove only the first: it applies first, and would mask the
// second.
func TestLiveTheTransportGuardRailsRefuse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db, projectID, probe := h.db, h.projectID, h.probe

	received := newReceiver(t)

	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Call to the loopback",
		EventType: string(event.VersionCreated), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{
				Kind: plan.GesteAction, Ref: "webhook",
				Params: map[string]string{"url": received.server.URL},
			}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("reaction: %v", err)
	}

	// The bootstrap once only: both phases share the database and the cursor, and reading Redmine
	// twice would prove nothing more.
	bootstrap := func(options webhook.Options) *ProbeRunner {
		actions := NewActionRunner(liveActions(), webhook.New(options),
			testResolver{db: db, cipher: h.cipher}, quietLogger())
		return NewProbeRunner(liveRegistry(), db, h.cipher, time.Minute, quietLogger(), probe).
			WithDispatcher(NewDispatcher(db,
				NewEngine(db, stepRouter{actions: actions}, quietLogger()), quietLogger()))
	}

	phases := []struct {
		name     string
		options  webhook.Options
		expected string
	}{
		{
			name:     "the production default refuses clear text",
			options:  webhook.Options{Timeout: 10 * time.Second},
			expected: "clair",
		},
		{
			// Clear text allowed, the private target not: it is the address check that must speak this
			// time, and it alone.
			name:     "clear text allowed, the private target still refused",
			options:  webhook.Options{AllowInsecure: true, Timeout: 10 * time.Second},
			expected: "",
		},
	}

	// The first pass bootstraps without triggering; each phase then returns a new fact so that
	// exactly one run starts.
	bootstrap(phases[0].options).sweep(ctx, probe)

	for index, phase := range phases {
		t.Run(phase.name, func(t *testing.T) {
			forget(t, db, string(event.VersionCreated))
			bootstrap(phase.options).sweep(ctx, probe)

			if calls, _, _, _, _ := received.seenParams(); calls != 0 {
				t.Fatalf("%d calls received: the guard rail let it through", calls)
			}

			executions, err := db.ListPlanRuns(ctx, projectID, 0)
			if err != nil {
				t.Fatalf("runs: %v", err)
			}
			if len(executions) != index+1 {
				t.Fatalf("%d runs, expected %d", len(executions), index+1)
			}
			// The most recent first: it is this phase's.
			last := executions[0]
			if last.State != "failed" {
				t.Errorf("run %q, expected failed", last.State)
			}
			if len(last.Steps) != 1 {
				t.Fatalf("gestes = %+v", last.Steps)
			}
			// The reason must be readable: "failed" with no reason would force a re-run to understand that
			// it is a deliberate refusal and not an outage at the recipient.
			reason := last.Steps[0].Detail
			t.Logf("refusal recorded: %s", reason)
			if reason == "" {
				t.Fatal("no reason recorded for the refusal")
			}
			if phase.expected != "" && !strings.Contains(reason, phase.expected) {
				t.Errorf("reason %q — expected it to name %q", reason, phase.expected)
			}
			// The refusal of a private target must not disguise itself as a refusal of clear text: they are
			// two distinct protections, and confusing them would make the second look exercised when the
			// first masked it.
			if phase.expected == "" && strings.Contains(reason, "clair") {
				t.Errorf("reason %q — it is the refusal of clear text, not that of the target", reason)
			}
		})
	}
}
