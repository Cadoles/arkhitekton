package live

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

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

// The capabilities are built again when the counter moves, and only then; a failed build leaves
// the previous ones in force and is not retried until the counter moves again.
func TestTheReloaderFollowsTheCounter(t *testing.T) {
	registry, _ := capabilities.New()
	holder := NewHolder(Snapshot{Catalog: &catalog.Catalog{}, Registry: registry, Version: 1})
	version, builds, fail := int64(1), 0, false
	reloader := NewReloader(holder,
		func(context.Context) (int64, error) { return version, nil },
		func(context.Context, int64) (Snapshot, error) {
			builds++
			if fail {
				return Snapshot{}, errors.New("a binding names an unknown capability")
			}
			return Snapshot{Catalog: &catalog.Catalog{MaxConcurrentRuns: builds}, Registry: registry}, nil
		}, time.Hour, slog.New(slog.DiscardHandler))
	var seen []int64
	reloader.OnReplace(func(next Snapshot) { seen = append(seen, next.Version) })

	_ = reloader.Reload(context.Background())
	if builds != 0 {
		t.Fatalf("built %d time(s) with an unchanged counter", builds)
	}

	version = 2
	_ = reloader.Reload(context.Background())
	if holder.Current().Version != 2 || holder.Current().Catalog.MaxConcurrentRuns != 1 || len(seen) != 1 {
		t.Fatalf("version 2: %+v, seen %v", holder.Current(), seen)
	}

	version, fail = 3, true
	if err := reloader.Reload(context.Background()); err == nil {
		t.Error("a failed build reported no error")
	}
	_ = reloader.Reload(context.Background())
	if holder.Current().Version != 2 || builds != 2 {
		t.Errorf("after a failure: version %d, %d builds", holder.Current().Version, builds)
	}

	version, fail = 4, false
	_ = reloader.Reload(context.Background())
	if holder.Current().Version != 4 || len(seen) != 2 {
		t.Errorf("version 4: %+v, seen %v", holder.Current(), seen)
	}
}
