package store

import (
	"context"
	"encoding/json"
	"testing"
)

// Every change of bindings moves the counter the running dashboard watches.
func TestABindingChangeMovesTheCounter(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	before, err := db.CapabilityVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetCapabilityBinding(ctx, "go/unit", json.RawMessage(`{"ref":"go/unit","timeout":"20m"}`), "admin"); err != nil {
		t.Fatal(err)
	}
	bindings, err := db.CapabilityBindings(ctx)
	if err != nil || len(bindings) != 1 || bindings[0].UpdatedBy != "admin" {
		t.Fatalf("bindings %+v, err %v", bindings, err)
	}
	if err := db.DeleteCapabilityBinding(ctx, "go/unit"); err != nil {
		t.Fatal(err)
	}
	after, _ := db.CapabilityVersion(ctx)
	if after != before+2 {
		t.Errorf("version %d then %d", before, after)
	}
	if err := db.SetCapabilityBinding(ctx, "Not A Ref", json.RawMessage(`{}`), "admin"); err == nil {
		t.Error("a malformed reference accepted")
	}
}
