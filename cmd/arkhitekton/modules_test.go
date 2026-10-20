package main

import (
	"slices"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// The MSE module's promotion fact carries the type the MSE plugin declares: the reaction editor
// offers it under that name, and a gap would make a reaction that never starts.
func TestTheFactCarriesTheTypeThePluginDeclares(t *testing.T) {
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := registry.Plugin(plugin.ModuleMSE)
	if err != nil {
		t.Fatalf("MSE plugin not found: %v", err)
	}
	declared := make([]string, 0, len(descriptor.Events))
	for _, spec := range descriptor.Events {
		declared = append(declared, descriptor.EventType(spec))
	}
	if !slices.Contains(declared, string(mse.ReleasePromoted)) {
		t.Errorf("type %q missing from the declared facts %v", mse.ReleasePromoted, declared)
	}

	recorded := mse.Facts{}.Promoted(time.Now(), store.JobReleaseContext{
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
