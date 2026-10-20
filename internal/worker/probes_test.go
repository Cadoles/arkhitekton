package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The probes.
 *
 * Three things are exercised here, and none of them is exercised by reading the code: that
 * enabling one emits nothing of the past, that a second pass over the same overlap window does
 * not re-notify, and that an unreachable forge does not stop the other projects advancing.
 */

// fakeProbe returns what it is given, and keeps what it was asked.
type fakeProbe struct {
	facts  []event.Event
	cursor string
	// err is returned for a named project, to exercise the isolation.
	errFor string
	// seen keeps the cursors received, project by project.
	seen map[string]string
}

func (p *fakeProbe) PluginID() string   { return "gitea" }
func (p *fakeProbe) Source() string     { return "gitea.pull_requests" }
func (p *fakeProbe) Schedule() Schedule { return Schedule{} }

func (p *fakeProbe) Poll(
	_ context.Context, project store.Project, _ map[string]string, cursor string,
) ([]event.Event, string, error) {
	if p.seen == nil {
		p.seen = map[string]string{}
	}
	p.seen[project.Slug] = cursor

	if p.errFor == project.Slug {
		return nil, "", errors.New("forge injoignable")
	}
	return p.facts, p.cursor, nil
}

// fakeProbeStore holds the state the loop reads and writes.
type fakeProbeStore struct {
	projects []store.Project
	cursors  map[string]string
	// facts keeps the keys emitted, like the database: uniqueness is what is being exercised, and
	// a slice would not say whether a duplicate was set aside.
	facts map[string]event.Event
	// fresh counts the **new** facts, that is, those the loop would route. The total does not say
	// it: a pass rewriting the same fact ten times leaves a map of a single element.
	fresh    int
	failures []string
	disabled bool
}

func newFakeProbeStore(projects ...store.Project) *fakeProbeStore {
	return &fakeProbeStore{
		projects: projects,
		cursors:  map[string]string{},
		facts:    map[string]event.Event{},
	}
}

func (f *fakeProbeStore) ListProjects(context.Context) ([]store.Project, error) {
	return f.projects, nil
}

func (f *fakeProbeStore) ResolvedPlugin(
	_ context.Context, _ int64, pluginID string,
) (store.PluginSettings, error) {
	if f.disabled {
		return store.PluginSettings{}, store.ErrPluginDisabled
	}
	return store.PluginSettings{
		PluginID: pluginID, Enabled: true,
		Config: map[string]string{"url": "https://forge.example.fr", "token": "sealed:token"},
	}, nil
}

func (f *fakeProbeStore) GetSyncState(
	_ context.Context, source string, projectID int64,
) (store.SyncState, error) {
	return store.SyncState{Source: source, Cursor: f.cursors[cursorKey(source, projectID)]}, nil
}

func (f *fakeProbeStore) RecordSyncSuccess(
	_ context.Context, source string, projectID int64, cursor string,
) error {
	f.cursors[cursorKey(source, projectID)] = cursor
	return nil
}

func (f *fakeProbeStore) RecordSyncFailure(
	_ context.Context, source string, projectID int64, message string,
) error {
	f.failures = append(f.failures, source+":"+message)
	return nil
}

func (f *fakeProbeStore) RecordEvent(_ context.Context, fact event.Event) (bool, error) {
	if err := fact.Validate(); err != nil {
		return false, err
	}
	if _, known := f.facts[fact.DedupKey]; known {
		return false, nil
	}
	f.facts[fact.DedupKey] = fact
	f.fresh++
	return true, nil
}

func cursorKey(source string, projectID int64) string {
	return source + ":" + string(rune('0'+projectID))
}

func probeFact(key string) event.Event {
	return event.Event{
		Type: "gitea.pull_request.opened", Subject: "#412",
		Payload:    map[string]string{"number": "412", "title": "Fix the clone"},
		OccurredAt: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
		DedupKey:   key,
	}
}

func testProject(id int64, slug string) store.Project {
	return store.Project{ID: id, Slug: slug, Repository: "CNOUS/" + slug}
}

func probeRunnerFor(t *testing.T, db *fakeProbeStore, probe Probe) *ProbeRunner {
	t.Helper()
	return NewProbeRunner(db, testOpener{}, time.Minute, quietLogger(), probe)
}

/*
 * 1. Enabling something does not wake the past.
 */

// The first pass **establishes the cursor and triggers nothing**.
//
// Without that rule, enabling a probe on a repository carrying four hundred pull requests
// would send four hundred notifications.
//
// It nonetheless **keeps** what it sees, and that is a fix the tracker's migration brought:
// see the next test.
func TestEnablingDoesNotWakeThePast(t *testing.T) {
	base := newFakeProbeStore(testProject(1, "mse"))
	probe := &fakeProbe{
		facts:  []event.Event{probeFact("gitea.pr:mse:410"), probeFact("gitea.pr:mse:411")},
		cursor: "2026-09-03T10:00:00Z",
	}

	probeRunnerFor(t, base, probe).sweep(context.Background(), probe)

	if len(base.facts) != 2 {
		t.Errorf("%d facts kept at enabling, expected 2", len(base.facts))
	}
	// The cursor is set: the next pass will start again from there.
	if base.cursors[cursorKey("gitea.pull_requests", 1)] != "2026-09-03T10:00:00Z" {
		t.Errorf("cursor = %q", base.cursors[cursorKey("gitea.pull_requests", 1)])
	}
}

// A probe returning a **snapshot** does not trigger after its bootstrap.
//
// # The defect the second implementation revealed
//
// The bootstrap discarded its facts. That was enough as long as every probe filtered on its
// cursor: the Gitea probe returns only the requests opened since, and never sees them again.
//
// The tracker synchronisation re-reads **every** version at each pass — Redmine's API does not
// filter them. Discarded at bootstrap without being kept, those facts came back new at the
// next round and triggered everything the bootstrap claimed to silence. "Enabling something
// does not wake the past" was true for one round only.
func TestASnapshotDoesNotTriggerAfterItsBootstrap(t *testing.T) {
	base := newFakeProbeStore(testProject(1, "mse"))
	// A snapshot probe: the same facts at each pass, whatever the cursor. That is exactly what
	// reading the versions does.
	probe := &fakeProbe{
		facts:  []event.Event{probeFact("redmine.version:mse:803"), probeFact("redmine.version:mse:805")},
		cursor: "2026-09-03T10:00:00Z",
	}
	runner := probeRunnerFor(t, base, probe)

	runner.sweep(context.Background(), probe) // bootstrap
	afterBootstrap := base.fresh

	runner.sweep(context.Background(), probe) // the next round sees everything again

	if base.fresh != afterBootstrap {
		t.Errorf("%d new facts after the bootstrap, expected none — "+
			"the snapshot re-triggered what the bootstrap was to silence",
			base.fresh-afterBootstrap)
	}
}

// The next pass does emit.
func TestTheNextPassEmits(t *testing.T) {
	base := newFakeProbeStore(testProject(1, "mse"))
	probe := &fakeProbe{cursor: "2026-09-03T10:00:00Z"}
	runner := probeRunnerFor(t, base, probe)

	// Bootstrap.
	runner.sweep(context.Background(), probe)

	// A pull request opens.
	probe.facts = []event.Event{probeFact("gitea.pr:mse:412")}
	probe.cursor = "2026-09-03T11:00:00Z"
	runner.sweep(context.Background(), probe)

	if len(base.facts) != 1 {
		t.Fatalf("%d facts emitted, expected 1", len(base.facts))
	}
	recorded := base.facts["gitea.pr:mse:412"]
	// The loop attaches the project and the source; the probe has no business doing it.
	if recorded.ProjectID == nil || *recorded.ProjectID != 1 {
		t.Errorf("fact's project = %v", recorded.ProjectID)
	}
	if recorded.Source != "gitea" {
		t.Errorf("fact's source = %q, expected the plugin's identifier", recorded.Source)
	}
}

/*
 * 2. A second pass over the same window does not re-notify.
 */

// The probe re-reads an overlap window at each pass — that is what avoids losing a fact
// straddling two seconds. With no stable key, every pass would re-notify.
func TestASecondPassOverTheSameWindowDoesNotReNotify(t *testing.T) {
	base := newFakeProbeStore(testProject(1, "mse"))
	probe := &fakeProbe{cursor: "2026-09-03T10:00:00Z"}
	runner := probeRunnerFor(t, base, probe)

	runner.sweep(context.Background(), probe) // bootstrap

	probe.facts = []event.Event{probeFact("gitea.pr:mse:412")}
	runner.sweep(context.Background(), probe)
	// The overlap window makes the same fact come round again.
	runner.sweep(context.Background(), probe)
	runner.sweep(context.Background(), probe)

	if len(base.facts) != 1 {
		t.Errorf("%d facts recorded after three passes, expected 1", len(base.facts))
	}
}

/*
 * 3. A failure is isolated per project.
 */

// A forge unreachable for one project does not stop the others advancing.
//
// Two projects may target two forges, and one's outage says nothing about the other. It is the
// rule the tracker synchronisation has just learnt.
func TestAnUnreachableForgeDoesNotStopTheOtherProjects(t *testing.T) {
	base := newFakeProbeStore(
		testProject(1, "casse"), testProject(2, "sain"))
	probe := &fakeProbe{errFor: "casse", cursor: "2026-09-03T10:00:00Z"}
	runner := probeRunnerFor(t, base, probe)

	runner.sweep(context.Background(), probe) // bootstrap of both

	probe.facts = []event.Event{probeFact("gitea.pr:sain:1")}
	runner.sweep(context.Background(), probe)

	// The healthy project did emit.
	if len(base.facts) != 1 {
		t.Errorf("%d facts emitted, expected 1 for the healthy project", len(base.facts))
	}
	// And the other's failure is recorded, so that the screen says it.
	if len(base.failures) == 0 {
		t.Error("the failing project's failure was not recorded")
	}
	// Its cursor has **not** advanced: the next pass will see again what it missed.
	if base.cursors[cursorKey("gitea.pull_requests", 1)] != "" {
		t.Errorf("cursor of the failing project = %q, expected unchanged",
			base.cursors[cursorKey("gitea.pull_requests", 1)])
	}
}

/*
 * What the loop does besides.
 */

// A switched-off plugin is not queried, and that is not an outage: reporting it at every round
// would fill the log with a wanted state.
func TestASwitchedOffPluginIsNotQueried(t *testing.T) {
	base := newFakeProbeStore(testProject(1, "mse"))
	base.disabled = true
	probe := &fakeProbe{cursor: "x"}

	probeRunnerFor(t, base, probe).sweep(context.Background(), probe)

	if len(probe.seen) != 0 {
		t.Errorf("the probe was queried for a switched-off plugin: %v", probe.seen)
	}
	if len(base.failures) != 0 {
		t.Errorf("a switched-off plugin was recorded as failed: %v", base.failures)
	}
}

// The sealed field is opened before reaching the probe; the others pass through as they are.
func TestSealedFieldsAreOpenedBeforeReachingTheProbe(t *testing.T) {
	base := newFakeProbeStore(testProject(1, "mse"))

	var seenParams map[string]string
	probe := &configReader{target: &seenParams}
	NewProbeRunner(base, testOpener{}, time.Minute, quietLogger(), probe).
		sweep(context.Background(), probe)

	if seenParams["token"] != "token" {
		t.Errorf("token passed = %q, expected unsealed", seenParams["token"])
	}
	if seenParams["url"] != "https://forge.example.fr" {
		t.Errorf("URL passed = %q", seenParams["url"])
	}
}

// configReader keeps the configuration it received.
type configReader struct{ target *map[string]string }

func (l *configReader) PluginID() string   { return "gitea" }
func (l *configReader) Source() string     { return "gitea.pull_requests" }
func (l *configReader) Schedule() Schedule { return Schedule{} }
func (l *configReader) Poll(
	_ context.Context, _ store.Project, config map[string]string, _ string,
) ([]event.Event, string, error) {
	*l.target = config
	return nil, "cursor", nil
}

// The interval floor is enforced: the load counts as *projects × probes × frequency*, and the
// multiplicand is a machine that is not ours.
func TestTheIntervalFloorIsEnforced(t *testing.T) {
	runner := NewProbeRunner(newFakeProbeStore(), nil, time.Second, quietLogger())

	if runner.interval < minProbeInterval {
		t.Errorf("interval = %s, floor %s", runner.interval, minProbeInterval)
	}
}

// A probe declares its cadence, and the deployment's floor stays above it.
//
// The tracker is re-read every five minutes — human typing goes no faster — a forge is watched
// more often. A single cadence would force both to agree, and the slowest would lay down the
// law.
func TestEachProbeDeclaresItsCadenceAboveTheFloor(t *testing.T) {
	// The tracker probe asks for five minutes: above the floor, so honoured.
	if got := NewRedmineProbe(nil).Schedule(); got.Interval != syncInterval {
		t.Errorf("tracker cadence = %s, expected %s", got.Interval, syncInterval)
	}
	// And it asks for an immediate pass: it fills a screen, which would be empty for five minutes
	// after each start-up.
	if !NewRedmineProbe(nil).Schedule().Eager {
		t.Error("the tracker probe must ask for an immediate pass")
	}
	// The forge probe only observes: no pass straight away, otherwise a restart loop would hammer
	// a third-party machine.
	if NewGiteaPullProbe(0).Schedule().Eager {
		t.Error("a probe that only observes does not start straight away")
	}
}
