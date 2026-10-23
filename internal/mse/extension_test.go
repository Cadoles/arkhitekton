package mse

import (
	"slices"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/plugin"
)

// The MSE module's promotion fact carries the type the MSE plugin declares: the reaction editor
// offers it under that name, and a gap would make a reaction that never starts.
func TestTheFactCarriesTheTypeThePluginDeclares(t *testing.T) {
	descriptor := plugin.Plugin()
	if descriptor.ID != PluginID {
		t.Fatalf("the descriptor is plugin %q", descriptor.ID)
	}
	declared := make([]string, 0, len(descriptor.Events))
	for _, spec := range descriptor.Events {
		declared = append(declared, descriptor.EventType(spec))
	}
	if !slices.Contains(declared, string(ReleasePromoted)) {
		t.Errorf("type %q missing from the declared facts %v", ReleasePromoted, declared)
	}

	recorded := Facts{}.Promoted(time.Now(), host.JobReleaseContext{
		ReleaseID: 42, JobID: 1400, Tag: "1.8.0", Environment: "preprod", ProjectID: 7,
	})
	if err := recorded.Validate(); err != nil {
		t.Errorf("invalid fact: %v", err)
	}
	// Every declared field exists in the payload: a field offered for matching and absent would
	// never match.
	for _, spec := range descriptor.Events {
		for _, field := range spec.Payload {
			if _, present := recorded.Payload[field.Name]; !present {
				t.Errorf("field %q declared and missing from the payload", field.Name)
			}
		}
	}
}

// The extension is a coherent one: its descriptor declares the module its worker runs, and its
// migrations are its own.
func TestTheExtensionDeclaresWhatItRuns(t *testing.T) {
	extension := Extension()
	if extension.ID != extension.Plugin.ID || extension.Migrations.Table != "goose_mse_db_version" {
		t.Errorf("extension = %q, plugin %q, table %q", extension.ID, extension.Plugin.ID, extension.Migrations.Table)
	}
	declares := false
	for _, header := range extension.Plugin.Capabilities {
		declares = declares || header.Name != "" && header.Kind == "module"
	}
	if !declares {
		t.Error("the descriptor declares no module, the worker would refuse it")
	}
	if len(extension.Stacks["php"]) == 0 {
		t.Error("the integration tests left out of the php stack")
	}
}

// The seeding adds the application's images and the repositories the plugin's pipelines clone.
func TestTheSeedingCompletesTheProjects(t *testing.T) {
	seeded := seedProjects([]host.Project{
		{Repository: "CNOUS/mse", Dockerfile: "Dockerfile"},
		{Repository: "CNOUS/mse-tekton"},
	})
	if len(seeded[0].Targets) != 3 || !seeded[0].MultiStage || len(seeded[1].Targets) != 0 {
		t.Errorf("targets = %+v", seeded[:2])
	}
	labels := []string{}
	for _, project := range seeded[2:] {
		labels = append(labels, project.Label)
	}
	if !slices.Equal(labels, []string{"Kustomizations", "Campagne E2E"}) {
		t.Errorf("added = %v", labels)
	}
}
