// Package mse carries the MSE plugin: the environments, their promotion by Flux, their
// attestation by PortalData. See docs/06-plugins.md.
//
// The core does not import it. Only the `app` package assembles it, and an instance without it
// stays coherent.
package mse

import (
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// PluginID identifies the MSE plugin, whose switch this module follows.
const PluginID = "mse"

// ReleasePromoted is the fact of a completed promotion.
//
// **The re-tag succeeded**, which is an observation and not the intention of asking for it.
// What Flux then makes of it is another matter, and it is the environments screen that tells
// that story.
const ReleasePromoted = event.Type(PluginID + ".release.promoted")

// Facts composes the facts the plugin observes.
type Facts struct{}

// Promoted returns the fact of a completed promotion.
//
// Keyed on the job: one release is promoted several times to the same environment — a retry
// after a missed re-tag — and each completion is a fact.
func (Facts) Promoted(at time.Time, linked store.JobReleaseContext) event.Event {
	projectID := linked.ProjectID
	return event.Event{
		Type: ReleasePromoted, Source: PluginID,
		ProjectID: &projectID, Subject: linked.Tag, OccurredAt: at,
		DedupKey: fmt.Sprintf("%s:%d", ReleasePromoted, linked.JobID),
		Payload: map[string]string{
			"release_id":  fmt.Sprint(linked.ReleaseID),
			"tag":         linked.Tag,
			"environment": linked.Environment,
			"job_id":      fmt.Sprint(linked.JobID),
		},
	}
}
