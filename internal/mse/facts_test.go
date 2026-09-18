package mse

import (
	"slices"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

func promotionDeTest(jobID int64) store.JobReleaseContext {
	return store.JobReleaseContext{
		ReleaseID: 42, JobID: jobID, Tag: "1.8.0", Environment: "preprod", ProjectID: 7,
	}
}

// Le fait porte le type que le registre annonce : l'éditeur de réactions le
// propose sous ce nom, et un écart ferait une réaction qui ne part jamais.
func TestLeFaitPorteLeTypeDeclareParLePlugin(t *testing.T) {
	descriptor, err := plugin.Get(plugin.ModuleMSE)
	if err != nil {
		t.Fatalf("plugin MSE introuvable: %v", err)
	}
	declared := make([]string, 0, len(descriptor.Events))
	for _, spec := range descriptor.Events {
		declared = append(declared, descriptor.EventType(spec))
	}
	if !slices.Contains(declared, string(ReleasePromoted)) {
		t.Errorf("type %q absent des faits déclarés %v", ReleasePromoted, declared)
	}

	fait := Facts{}.Promoted(time.Now(), promotionDeTest(1400))
	if err := fait.Validate(); err != nil {
		t.Errorf("fait invalide: %v", err)
	}
	// Chaque champ déclaré existe dans la charge : un champ proposé à
	// l'appariement et absent n'apparierait jamais.
	for _, spec := range descriptor.Events {
		for _, field := range spec.Payload {
			if _, present := fait.Payload[field.Name]; !present {
				t.Errorf("champ %q déclaré et absent de la charge", field.Name)
			}
		}
	}
}

// Une promotion reprise est un fait distinct : la clé porte le job.
func TestUneRepriseDePromotionEstUnFaitDistinct(t *testing.T) {
	premiere := Facts{}.Promoted(time.Now(), promotionDeTest(1400))
	reprise := Facts{}.Promoted(time.Now(), promotionDeTest(1402))
	if premiere.DedupKey == reprise.DedupKey {
		t.Errorf("clé partagée %q — la reprise serait muette", premiere.DedupKey)
	}
}
