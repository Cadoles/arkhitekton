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

func promotionAboutie() store.JobReleaseContext {
	return store.JobReleaseContext{ReleaseID: 42, JobID: 1, Tag: "1.8.0", Environment: "preprod", ProjectID: 7}
}

// Sans plugin qui promeut, une promotion aboutie n'est pas constatée : le noyau
// ne sait pas ce qu'elle signifie (doc 16 §16.7).
func TestSansPluginAucunePromotionNestConstatee(t *testing.T) {
	recorder := &recordedEvents{}
	sink := (&runSink{logger: quietLogger()}).WithEvents(recorder)

	sink.emitReleasePromoted(context.Background(), promotionAboutie(), store.JobSucceeded)

	if len(recorder.facts) != 0 {
		t.Errorf("faits émis sans plugin: %v", recorder.facts)
	}
}

// Avec le plugin, le fait émis est celui qu'il compose, et seulement sur un succès.
func TestLePluginComposeLeFaitDePromotion(t *testing.T) {
	recorder := &recordedEvents{}
	sink := (&runSink{logger: quietLogger()}).WithEvents(recorder).WithPromotionFacts(promotionFacts{})

	sink.emitReleasePromoted(context.Background(), promotionAboutie(), store.JobFailed)
	if len(recorder.facts) != 0 {
		t.Fatalf("promotion échouée constatée: %v", recorder.facts)
	}

	sink.emitReleasePromoted(context.Background(), promotionAboutie(), store.JobSucceeded)
	if len(recorder.facts) != 1 || recorder.facts[0].Type != "module.release.promoted" {
		t.Errorf("faits émis: %v", recorder.facts)
	}
}
