package store

import (
	"context"
	"testing"
)

// Le journal existe pour ce que le dashboard déclenche **hors de lui** : un tag
// dans un registre survit à toute correction en base.
func TestAuditKeepsWhatWasRequested(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.Audit(ctx, "vcarroy", AuditReleasePromote, "1.8.0",
		map[string]any{"environment": "prod", "tag": "1.8.0_prod"}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if err := db.Audit(ctx, "autre", AuditJobEnqueue, "", nil); err != nil {
		t.Fatalf("audit sans cible: %v", err)
	}

	all, err := db.ListAudit(ctx, "", 0)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("%d entrée(s), attendu 2", len(all))
	}
	// Antichronologique : la dernière action est celle qu'on cherche en premier.
	if all[0].Action != AuditJobEnqueue {
		t.Errorf("première entrée = %q, attendu la plus récente", all[0].Action)
	}

	scoped, err := db.ListAudit(ctx, "1.8.0", 0)
	if err != nil {
		t.Fatalf("liste ciblée: %v", err)
	}
	if len(scoped) != 1 {
		t.Fatalf("%d entrée(s) pour 1.8.0", len(scoped))
	}
	if scoped[0].Payload["tag"] != "1.8.0_prod" {
		t.Errorf("payload = %v, attendu le tag réellement poussé", scoped[0].Payload)
	}
}
