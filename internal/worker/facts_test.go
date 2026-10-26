package worker

import (
	"context"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type recordedEvents struct{ facts []event.Event }

func (r *recordedEvents) RecordEvent(_ context.Context, fact event.Event) (bool, error) {
	r.facts = append(r.facts, fact)
	return true, nil
}

type promotionFacts struct{}

func (promotionFacts) Promoted(at time.Time, linked store.JobReleaseContext) event.Event {
	return event.Event{Type: "module.release.promoted", OccurredAt: at, Subject: linked.Tag,
		DedupKey: "module.release.promoted:1"}
}

func completedPromotion() store.JobReleaseContext {
	return store.JobReleaseContext{ReleaseID: 42, JobID: 1, Tag: "1.8.0", Environment: "preprod", ProjectID: 7}
}

// Without a plugin that promotes, a completed promotion is not observed: the core does not
// know what it means.
func TestWithNoPluginNoPromotionIsObserved(t *testing.T) {
	recorder := &recordedEvents{}
	sink := (&runSink{logger: quietLogger()}).WithEvents(recorder)

	sink.emitReleasePromoted(context.Background(), completedPromotion(), store.JobSucceeded)

	if len(recorder.facts) != 0 {
		t.Errorf("facts emitted with no plugin: %v", recorder.facts)
	}
}

// With the plugin, the fact emitted is the one it composes, and only on a success.
func TestThePluginComposesThePromotionFact(t *testing.T) {
	recorder := &recordedEvents{}
	sink := (&runSink{logger: quietLogger()}).WithEvents(recorder).WithPromotionFacts(promotionFacts{})

	sink.emitReleasePromoted(context.Background(), completedPromotion(), store.JobFailed)
	if len(recorder.facts) != 0 {
		t.Fatalf("failed promotion observed: %v", recorder.facts)
	}

	sink.emitReleasePromoted(context.Background(), completedPromotion(), store.JobSucceeded)
	if len(recorder.facts) != 1 || recorder.facts[0].Type != "module.release.promoted" {
		t.Errorf("facts emitted: %v", recorder.facts)
	}
}
