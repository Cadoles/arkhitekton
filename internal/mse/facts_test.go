package mse

import (
	"slices"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

func testPromotion(jobID int64) store.JobReleaseContext {
	return store.JobReleaseContext{
		ReleaseID: 42, JobID: jobID, Tag: "1.8.0", Environment: "preprod", ProjectID: 7,
	}
}

// The fact carries the type the registry announces: the reaction editor offers it under that
// name, and a gap would make a reaction that never starts.
func TestTheFactCarriesTheTypeThePluginDeclares(t *testing.T) {
	descriptor, err := plugin.Get(plugin.ModuleMSE)
	if err != nil {
		t.Fatalf("plugin MSE introuvable: %v", err)
	}
	declared := make([]string, 0, len(descriptor.Events))
	for _, spec := range descriptor.Events {
		declared = append(declared, descriptor.EventType(spec))
	}
	if !slices.Contains(declared, string(ReleasePromoted)) {
		t.Errorf("type %q missing from the declared facts %v", ReleasePromoted, declared)
	}

	fait := Facts{}.Promoted(time.Now(), testPromotion(1400))
	if err := fait.Validate(); err != nil {
		t.Errorf("fait invalide: %v", err)
	}
	// Every declared field exists in the payload: a field offered for matching and absent would
	// never match.
	for _, spec := range descriptor.Events {
		for _, field := range spec.Payload {
			if _, present := fait.Payload[field.Name]; !present {
				t.Errorf("field %q declared and missing from the payload", field.Name)
			}
		}
	}
}

// A retried promotion is a distinct fact: the key carries the job.
func TestARetriedPromotionIsADistinctFact(t *testing.T) {
	premiere := Facts{}.Promoted(time.Now(), testPromotion(1400))
	reprise := Facts{}.Promoted(time.Now(), testPromotion(1402))
	if premiere.DedupKey == reprise.DedupKey {
		t.Errorf("shared key %q — the retry would be mute", premiere.DedupKey)
	}
}
