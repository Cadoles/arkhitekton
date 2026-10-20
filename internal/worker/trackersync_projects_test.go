package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * Multi-project synchronisation.
 *
 * The defect it fixes was documented and accepted: a single cursor advanced on the first
 * project read, and the second resumed from an instant at which **it** had read nothing. Its
 * history was skipped, for good, with no message.
 *
 * # These tests now drive the probe loop
 *
 * And that is the heart of the migration. The properties they hold — one cursor per project,
 * one connection per project, an isolated failure, a key unsealed before the call — no longer
 * belong to the tracker: they belong to `ProbeRunner`, which holds them for **every** source.
 *
 * The assertions have not changed. What has changed is who honours them — and that is the only
 * proof worth having that the contract absorbed this source.
 */

// multiStore returns two projects and keeps what each wrote.
//
// It implements both contracts at once: the loop's — projects, settings, cursors, facts — and
// the projection's — versions, requests, adoptions, status mapping.
type multiStore struct {
	fakeRedmineStore
	// configs carries each project's resolved connection, key **sealed** as in the database.
	configs map[int64]map[string]string
	// cursors keeps the cursor recorded per project, and that is the heart of the subject.
	cursors  map[int64]string
	failures map[int64]string
	mappings map[int64]map[string]string
	facts    map[string]event.Event
}

func newMultiStore() *multiStore {
	return &multiStore{
		configs: map[int64]map[string]string{
			1: {"url": "https://redmine.one.fr", "api_key": "sealed:key-one", "project": "project-one"},
			2: {"url": "https://redmine.two.fr", "api_key": "sealed:key-two", "project": "project-two"},
		},
		cursors:  map[int64]string{},
		failures: map[int64]string{},
		mappings: map[int64]map[string]string{
			1: {"En cours": "doing"},
			2: {"En cours": "done"},
		},
		facts: map[string]event.Event{},
	}
}

func (m *multiStore) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{
		{ID: 1, Slug: "one", TrackerProject: "project-one"},
		{ID: 2, Slug: "two", TrackerProject: "project-two"},
	}, nil
}

func (m *multiStore) ResolvedPlugin(_ context.Context, projectID int64, _ string) (store.PluginSettings, error) {
	return store.PluginSettings{PluginID: "redmine", Enabled: true, Config: m.configs[projectID]}, nil
}

func (m *multiStore) StatusMapping(_ context.Context, projectID int64) (map[string]string, error) {
	return m.mappings[projectID], nil
}

func (m *multiStore) GetSyncState(_ context.Context, source string, projectID int64) (store.SyncState, error) {
	return store.SyncState{Source: source, Cursor: m.cursors[projectID]}, nil
}

func (m *multiStore) RecordSyncSuccess(_ context.Context, _ string, projectID int64, cursor string) error {
	m.cursors[projectID] = cursor
	return nil
}

func (m *multiStore) RecordSyncFailure(_ context.Context, _ string, projectID int64, message string) error {
	m.failures[projectID] = message
	return nil
}

func (m *multiStore) RecordEvent(_ context.Context, fact event.Event) (bool, error) {
	if err := fact.Validate(); err != nil {
		return false, err
	}
	if _, known := m.facts[fact.DedupKey]; known {
		return false, nil
	}
	m.facts[fact.DedupKey] = fact
	return true, nil
}

// trackerProbe assembles the probe and its loop on a given store.
func trackerHarness(
	t *testing.T, db *multiStore, opener SecretOpener,
	connect TrackerConnector,
) (*ProbeRunner, Probe) {
	t.Helper()
	probe := NewTrackerProbe(NewTrackerSync(db, redmineTracker, quietLogger()), connect)
	return NewProbeRunner(providerRegistry, db, opener, time.Minute, quietLogger(), probe), probe
}

// Each project advances its own cursor.
//
// It is the fix for the defect: a shared cursor made a project resume from an instant at which
// it had read nothing, and skip its history.
func TestEachProjectAdvancesItsOwnCursor(t *testing.T) {
	db := newMultiStore()
	client := &fakeRedmine{versions: []provider.Version{{ID: "1", Name: "v1.0.0", Status: "open"}}}

	connect := func(capability.Target) (TrackerReader, error) { return client, nil }
	runner, probe := trackerHarness(t, db, testOpener{}, connect)
	runner.sweep(context.Background(), probe)

	if len(db.cursors) != 2 {
		t.Fatalf("cursors recorded = %v, expected one per project", db.cursors)
	}
	if db.cursors[1] == "" || db.cursors[2] == "" {
		t.Errorf("cursors = %v", db.cursors)
	}
}

// Each project queries **its** server, with **its** unsealed key.
//
// Without that, two clients would share a connection: the second would read the first's
// versions, and the versions screen would be wrong with nothing reporting it.
func TestEachProjectQueriesItsOwnServer(t *testing.T) {
	db := newMultiStore()

	var seenItems []capability.Target
	connect := func(connection capability.Target) (TrackerReader, error) {
		seenItems = append(seenItems, connection)
		return &fakeRedmine{}, nil
	}
	runner, probe := trackerHarness(t, db, testOpener{}, connect)
	runner.sweep(context.Background(), probe)

	if len(seenItems) != 2 {
		t.Fatalf("%d connections, expected 2", len(seenItems))
	}
	if seenItems[0].Settings["url"] == seenItems[1].Settings["url"] {
		t.Errorf("both projects target the same server: %q", seenItems[0].Settings["url"])
	}
	// The key is **unsealed** before reaching the client. It is no longer the tracker that opens it
	// but the loop, for every probe — and the guarantee is the same: the opposite would send an
	// envelope in place of an API key, and the tracker would answer 401 with no message naming the
	// sealing.
	for _, connection := range seenItems {
		if strings.HasPrefix(connection.Settings["api_key"], "sealed:") {
			t.Errorf("key still sealed: %q", connection.Settings["api_key"])
		}
	}
	if seenItems[0].Settings["project"] != "project-one" || seenItems[1].Settings["project"] != "project-two" {
		t.Errorf("projects targeted = %q / %q", seenItems[0].Settings["project"], seenItems[1].Settings["project"])
	}
}

// One project's failure does not stop the next ones, and each records its own.
//
// Two projects may target two instances: one's outage says nothing about the other, and
// interrupting the pass would mean a client with no working tracker would deprive all the
// others of synchronisation.
func TestOneProjectsFailureDoesNotStopTheNextOnes(t *testing.T) {
	db := newMultiStore()
	outage := errors.New("redmine injoignable")

	connect := func(connection capability.Target) (TrackerReader, error) {
		if connection.Settings["project"] == "project-one" {
			return nil, outage
		}
		return &fakeRedmine{}, nil
	}
	runner, probe := trackerHarness(t, db, testOpener{}, connect)
	runner.sweep(context.Background(), probe)

	recorded, present := db.failures[1]
	if !present {
		t.Fatal("the first project's failure was not recorded")
	}
	if !strings.Contains(recorded, "injoignable") {
		t.Errorf("reason recorded %q — it must carry the outage", recorded)
	}
	// The failed project's cursor does not advance: the next pass will see again the window it
	// missed.
	if db.cursors[1] != "" {
		t.Errorf("cursor of the failing project = %q, expected unchanged", db.cursors[1])
	}
	if db.cursors[2] == "" {
		t.Error("the second project was not synchronised")
	}
	if _, recorded := db.failures[2]; recorded {
		t.Errorf("the second project carries a failure: %q", db.failures[2])
	}
}

// An unreadable key says so as such, and does not quote the value.
//
// It is the symptom of a rotation done without keeping the old key. Confusing it with a tracker
// outage would send someone hunting on the server's side.
func TestAnUnreadableKeySaysSoWithoutQuotingTheValue(t *testing.T) {
	db := newMultiStore()
	connect := func(capability.Target) (TrackerReader, error) { return &fakeRedmine{}, nil }

	runner, probe := trackerHarness(t, db,
		testOpener{err: errors.New("decryption refused")}, connect)
	runner.sweep(context.Background(), probe)

	reason, present := db.failures[1]
	if !present {
		t.Fatal("an unreadable key must fail the project's pass")
	}
	// The field is named: without it, "decryption refused" would not say what to go and look at.
	if !strings.Contains(reason, "api_key") {
		t.Errorf("reason %q — it must name the field", reason)
	}
	if strings.Contains(reason, "key-one") {
		t.Errorf("reason %q — it quotes the value", reason)
	}
}

// The project's identifier in the tracker falls back to the `tracker_project` column.
//
// It predates the plugins, it is right, and a deployment whose bootstrap did not run must not
// stop synchronising for that.
func TestTheTrackerIdentifierFallsBackToTheColumn(t *testing.T) {
	db := newMultiStore()
	// The setting says nothing about the project: only the column knows it.
	db.configs[1] = map[string]string{"url": "https://redmine.one.fr", "api_key": "sealed:key"}

	var seenItems []capability.Target
	connect := func(connection capability.Target) (TrackerReader, error) {
		seenItems = append(seenItems, connection)
		return &fakeRedmine{}, nil
	}
	runner, probe := trackerHarness(t, db, testOpener{}, connect)
	runner.sweep(context.Background(), probe)

	if len(seenItems) == 0 || seenItems[0].Settings["project"] != "project-one" {
		t.Errorf("project targeted = %+v", seenItems)
	}
}

// A project with no tracker identifier records no failure.
//
// It has nothing to read, which is not an outage. Recording a failure would have the
// administration screen say synchronisation is failing on a project that simply uses no
// tracker.
func TestAProjectWithNoTrackerIdentifierRecordsNoFailure(t *testing.T) {
	db := newMultiStore()
	db.configs[1] = map[string]string{"url": "https://redmine.one.fr", "api_key": "sealed:key"}

	connect := func(capability.Target) (TrackerReader, error) { return &fakeRedmine{}, nil }
	runner, probe := trackerHarness(t, db, testOpener{}, connect)

	untrackedProjects := &multiStoreWithoutColumn{multiStore: db}
	runner.store = untrackedProjects
	runner.sweep(context.Background(), probe)

	if reason, present := db.failures[1]; present {
		t.Errorf("failure recorded %q — the absence of a tracker is not one", reason)
	}
}

// multiStoreWithoutColumn returns the same projects, without their tracker identifier.
type multiStoreWithoutColumn struct{ *multiStore }

func (m *multiStoreWithoutColumn) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{{ID: 1, Slug: "one"}}, nil
}
