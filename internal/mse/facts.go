// Package mse porte le plugin MSE : les environnements, leur promotion par Flux,
// leur attestation par PortalData (doc 16).
//
// Le noyau ne l'importe pas. Seul `cmd/arkhitekton` l'assemble, et une instance
// sans lui reste cohérente.
package mse

import (
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// ReleasePromoted est le fait d'une promotion aboutie.
//
// **Le re-tag a réussi**, ce qui est un constat et non l'intention de le
// demander. Ce que Flux en fait ensuite est une autre affaire, et c'est l'écran
// des environnements qui la raconte (B8).
const ReleasePromoted = event.Type(plugin.ModuleMSE + ".release.promoted")

// Facts compose les faits que le plugin constate.
type Facts struct{}

// Promoted rend le fait d'une promotion aboutie.
//
// Clé sur le job : une même release se promeut plusieurs fois vers le même
// environnement — une reprise après un re-tag manqué —, et chaque aboutissement
// est un fait.
func (Facts) Promoted(at time.Time, linked store.JobReleaseContext) event.Event {
	projectID := linked.ProjectID
	return event.Event{
		Type: ReleasePromoted, Source: plugin.ModuleMSE,
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
