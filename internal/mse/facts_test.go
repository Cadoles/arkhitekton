package mse

import (
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

func testPromotion(jobID int64) store.JobReleaseContext {
	return store.JobReleaseContext{
		ReleaseID: 42, JobID: jobID, Tag: "1.8.0", Environment: "preprod", ProjectID: 7,
	}
}

// A retried promotion is a distinct fact: the key carries the job.
func TestARetriedPromotionIsADistinctFact(t *testing.T) {
	first := Facts{}.Promoted(time.Now(), testPromotion(1400))
	reprise := Facts{}.Promoted(time.Now(), testPromotion(1402))
	if first.DedupKey == reprise.DedupKey {
		t.Errorf("shared key %q — the retry would be mute", first.DedupKey)
	}
}
