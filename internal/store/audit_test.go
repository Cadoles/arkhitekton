package store

import (
	"context"
	"testing"
)

// The log exists for what the dashboard triggers outside itself: a tag in a registry
// outlives any correction in the database.
func TestAuditKeepsWhatWasRequested(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.Audit(ctx, "vcarroy", AuditReleasePromote, "1.8.0",
		map[string]any{"environment": "prod", "tag": "1.8.0_prod"}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if err := db.Audit(ctx, "autre", AuditJobEnqueue, "", nil); err != nil {
		t.Fatalf("audit with no target: %v", err)
	}

	all, err := db.ListAudit(ctx, "", 0)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("%d entry(ies), expected 2", len(all))
	}
	// Newest first: the last action is the one looked for first.
	if all[0].Action != AuditJobEnqueue {
		t.Errorf("first entry = %q, expected the most recent", all[0].Action)
	}

	scoped, err := db.ListAudit(ctx, "1.8.0", 0)
	if err != nil {
		t.Fatalf("targeted listing: %v", err)
	}
	if len(scoped) != 1 {
		t.Fatalf("%d entry(ies) for 1.8.0", len(scoped))
	}
	if scoped[0].Payload["tag"] != "1.8.0_prod" {
		t.Errorf("payload = %v, expected the tag actually pushed", scoped[0].Payload)
	}
}
