package live

import (
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
)

// A replaced snapshot is what every source reads at its next operation, registry and catalogue
// together.
func TestAReplacedSnapshotIsReadNext(t *testing.T) {
	first, second := &catalog.Catalog{MaxConcurrentRuns: 1}, &catalog.Catalog{MaxConcurrentRuns: 2}
	firstRegistry, _ := capabilities.New()
	secondRegistry, _ := capabilities.New()

	holder := NewHolder(Snapshot{Catalog: first, Registry: firstRegistry})
	catalogs, registries := holder.Catalogs(), holder.Registries()
	if catalogs.Current() != first || registries.Current() != firstRegistry {
		t.Fatal("the first snapshot is not read")
	}

	holder.Replace(Snapshot{Catalog: second, Registry: secondRegistry, Version: 4})
	if catalogs.Current() != second || registries.Current() != secondRegistry || holder.Current().Version != 4 {
		t.Error("the replaced snapshot is not read")
	}
}
