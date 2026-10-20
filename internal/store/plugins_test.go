package store

import (
	"context"
	"errors"
	"testing"
)

/*
 * The plugins' settings, and the layering every outgoing call depends on.
 *
 * Exercised against a real database rather than with a fake: what is at stake here is a join and
 * a conflict rule, that is, precisely what a fake does not reproduce. The defect that motivated
 * the front-end harness lived in the same place — between a handler and PostgreSQL, out of reach
 * of both.
 */

func TestPluginsAreBornEnabledAndAreNotOverwritten(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.EnsurePlugins(ctx, []string{"redmine", "flux"}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	settings, err := db.PluginSettingsAll(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Enabled at birth: every embedded plugin describes something the dashboard already does,
	// and being born disabled would stop, at the first deployment, mechanisms that worked the
	// day before.
	if !settings["redmine"].Enabled || !settings["flux"].Enabled {
		t.Fatalf("settings = %+v", settings)
	}

	// A manual switch-off survives a restart: otherwise the setting would not outlive a pod.
	if err := db.SetPluginSettings(ctx,
		PluginSettings{PluginID: "flux", Enabled: false}, "alice"); err != nil {
		t.Fatalf("switch-off: %v", err)
	}
	if err := db.EnsurePlugins(ctx, []string{"redmine", "flux"}); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	settings, _ = db.PluginSettingsAll(ctx)
	if settings["flux"].Enabled {
		t.Error("the bootstrap switched back on a plugin disabled by hand")
	}
	if settings["flux"].UpdatedBy != "alice" {
		t.Errorf("author = %q", settings["flux"].UpdatedBy)
	}
}

// The layering: the project wins, emptiness does not erase.
//
// The trap the second rule avoids is certain: the API never returns a sensitive field, so
// a form submits it empty. Treating that as a deletion would lose the instance's key at
// the first save.
func TestAProjectsSettingsLayerWithoutErasing(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.EnsurePlugins(ctx, []string{"redmine"}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := db.SetPluginSettings(ctx, PluginSettings{
		PluginID: "redmine", Enabled: true,
		Config: map[string]string{"url": "https://redmine.example.fr", "api_key": "sealed"},
	}, "alice"); err != nil {
		t.Fatalf("global setting: %v", err)
	}

	if err := db.SetProjectPluginSettings(ctx, testProjectID, PluginSettings{
		PluginID: "redmine", Enabled: true,
		// An empty URL means "nothing particular to say", not "erase it".
		Config: map[string]string{"project": "mse-dev", "url": ""},
	}, "alice"); err != nil {
		t.Fatalf("project setting: %v", err)
	}

	resolved, err := db.ResolvedPlugin(ctx, testProjectID, "redmine")
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}
	if resolved.Config["url"] != "https://redmine.example.fr" {
		t.Errorf("resolved URL = %q — emptiness does not erase", resolved.Config["url"])
	}
	if resolved.Config["api_key"] != "sealed" {
		t.Errorf("resolved key = %q", resolved.Config["api_key"])
	}
	if resolved.Config["project"] != "mse-dev" {
		t.Errorf("resolved project = %q", resolved.Config["project"])
	}
}

// A project does not switch back on what the instance switched off: the deployment does
// not carry it, and every call would fail.
func TestAProjectDoesNotSwitchBackOnWhatTheInstanceSwitchedOff(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.SetPluginSettings(ctx,
		PluginSettings{PluginID: "redmine", Enabled: false}, "alice"); err != nil {
		t.Fatalf("global setting: %v", err)
	}
	if err := db.SetProjectPluginSettings(ctx, testProjectID,
		PluginSettings{PluginID: "redmine", Enabled: true}, "alice"); err != nil {
		t.Fatalf("project setting: %v", err)
	}

	_, err := db.ResolvedPlugin(ctx, testProjectID, "redmine")
	if !errors.Is(err, ErrPluginDisabled) {
		t.Errorf("resolution = %v, expected ErrPluginDisabled", err)
	}
}

// Removal returns the project to inheritance, and is not a disabling.
//
// Confusing them would mean a project, once departed from the common setting, could never
// follow it again.
func TestRemovalReturnsTheProjectToInheritance(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.EnsurePlugins(ctx, []string{"delivery-note"}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := db.SetProjectPluginSettings(ctx, testProjectID,
		PluginSettings{PluginID: "delivery-note", Enabled: false}, "alice"); err != nil {
		t.Fatalf("switch-off: %v", err)
	}
	if _, err := db.ResolvedPlugin(ctx, testProjectID, "delivery-note"); !errors.Is(err, ErrPluginDisabled) {
		t.Fatalf("resolution = %v", err)
	}

	if err := db.ClearProjectPluginSettings(ctx, testProjectID, "delivery-note"); err != nil {
		t.Fatalf("removal: %v", err)
	}
	resolved, err := db.ResolvedPlugin(ctx, testProjectID, "delivery-note")
	if err != nil || !resolved.Enabled {
		t.Errorf("after removal: %+v (%v) — the project should follow the instance", resolved, err)
	}
}

/*
 * The pipelines a project keeps.
 */

// The absence of a decision means enabled.
//
// Treating absence as refusal would stop at a stroke the verifications of every project
// declared before the table: a deployment must be able to apply a migration without its
// verification chain stopping.
func TestAnUndeclaredPipelineStaysEnabled(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	enabled, err := db.PipelineEnabled(ctx, testProjectID, "load-tests")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !enabled {
		t.Error("an undeclared kind should stay enabled")
	}

	if err := db.SetProjectPipeline(ctx, testProjectID, "load-tests", false, "alice"); err != nil {
		t.Fatalf("switch-off: %v", err)
	}
	if enabled, _ := db.PipelineEnabled(ctx, testProjectID, "load-tests"); enabled {
		t.Error("the disabled kind stays enabled")
	}

	decisions, err := db.ProjectPipelines(ctx, testProjectID)
	if err != nil {
		t.Fatalf("reading the decisions: %v", err)
	}
	// Only explicit decisions appear: that is what lets the screen tell a choice from a
	// default, and therefore offer to return to the default.
	if len(decisions) != 1 || decisions["load-tests"].Enabled {
		t.Errorf("decisions = %+v", decisions)
	}
}

/*
 * The status mapping, layered too.
 */

// The instance default holds for a project that declared nothing, and its departures win
// where it has any.
//
// Both halves count. Without the default, a fresh project would be born with no
func TestTheMappingLayersTheDefaultAndTheProjectsDepartures(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// "Fermé" comes from the bootstrap, with no project: the tracker's own
	// vocabulary, which the migration writes as it is.
	mapping, err := db.StatusMapping(ctx, testProjectID)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if mapping["Fermé"] != "done" {
		t.Fatalf("the instance default is not inherited: %q", mapping["Fermé"])
	}

	// The project files it elsewhere.
	if err := db.SetStatusBucket(ctx, testProjectID, "Fermé", "client-test"); err != nil {
		t.Fatalf("departure: %v", err)
	}
	mapping, _ = db.StatusMapping(ctx, testProjectID)
	if mapping["Fermé"] != "client-test" {
		t.Errorf("the project's departure = %q, it should win", mapping["Fermé"])
	}

	// And the default has not moved: another project reads it unchanged. That is the point
	// that protects the figure displayed to the other deployment.
	other, err := db.CreateProject(ctx, Project{
		Repository: "CNOUS/other", Slug: "other", Label: "other",
		ForgeURL: "https://forge.example.fr", BuildContext: ".",
	})
	if err != nil {
		t.Fatalf("second project: %v", err)
	}
	neighbour, _ := db.StatusMapping(ctx, other)
	if neighbour["Fermé"] != "done" {
		t.Errorf("the neighbour's mapping = %q — a departure spilled over", neighbour["Fermé"])
	}
}

// The cursor is specific to each project.
//
// It fixes the defect the synchroniser documented: a shared cursor made a second project
// resume from an instant at which it had read nothing, and skip its history.
func TestEachProjectCarriesItsOwnCursor(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	other, err := db.CreateProject(ctx, Project{
		Repository: "CNOUS/other", Slug: "other", Label: "other",
		ForgeURL: "https://forge.example.fr", BuildContext: ".",
	})
	if err != nil {
		t.Fatalf("second project: %v", err)
	}

	if err := db.RecordSyncSuccess(ctx, "redmine", testProjectID, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatalf("cursor of the first: %v", err)
	}

	state, err := db.GetSyncState(ctx, "redmine", other)
	if err != nil {
		t.Fatalf("state of the second: %v", err)
	}
	if state.Cursor != "" {
		t.Errorf("cursor of the second = %q — it would have skipped its history", state.Cursor)
	}

	// And one's failure does not touch the other.
	if err := db.RecordSyncFailure(ctx, "redmine", other, "injoignable"); err != nil {
		t.Fatalf("failure of the second: %v", err)
	}
	first, _ := db.GetSyncState(ctx, "redmine", testProjectID)
	if first.LastError != "" {
		t.Errorf("error of the first = %q — it comes from the other project", first.LastError)
	}
}
