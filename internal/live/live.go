// Package live holds the capabilities in force: the registry of plugins and the catalogue that binds
// their capabilities to the instance.
//
// Both change while the dashboard runs, when an administrator installs a plugin or binds a
// capability. They are replaced together, atomically, as one snapshot; a reader takes the snapshot
// once per operation, so that an operation never reads a registry and a catalogue of two
// different moments.
package live

import (
	"sync/atomic"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
)

// Snapshot is the capabilities in force at one moment.
type Snapshot struct {
	Registry *capabilities.Registry
	Catalog  *catalog.Catalog
	// Version is what the snapshot was built from: the database's counter of installations and
	// bindings. Zero for the one built from the binary and the file alone.
	Version int64
}

// Holder keeps the snapshot in force.
type Holder struct {
	current atomic.Pointer[Snapshot]
}

// NewHolder starts with a snapshot.
func NewHolder(first Snapshot) *Holder {
	holder := &Holder{}
	holder.current.Store(&first)
	return holder
}

// Current returns the snapshot in force.
func (h *Holder) Current() Snapshot { return *h.current.Load() }

// Replace puts a new snapshot in force. Operations already under way finish on the one they took.
func (h *Holder) Replace(next Snapshot) { h.current.Store(&next) }

// Catalogs is the holder seen as a source of catalogues.
func (h *Holder) Catalogs() catalog.Source { return catalogs{h} }

// Registries is the holder seen as a source of registries.
func (h *Holder) Registries() capabilities.Source { return registries{h} }

type catalogs struct{ holder *Holder }

func (c catalogs) Current() *catalog.Catalog { return c.holder.Current().Catalog }

type registries struct{ holder *Holder }

func (r registries) Current() *capabilities.Registry { return r.holder.Current().Registry }
