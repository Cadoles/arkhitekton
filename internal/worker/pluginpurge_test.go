package worker

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type purges struct {
	leftover store.PluginLeftover
	purged   []string
	audited  []string
}

func (p *purges) PluginLeftoverOf(_ context.Context, pluginID string) (store.PluginLeftover, error) {
	if pluginID != p.leftover.PluginID {
		return store.PluginLeftover{}, store.ErrNothingToPurge
	}
	return p.leftover, nil
}

func (p *purges) RecordPluginPurged(_ context.Context, pluginID string) error {
	p.purged = append(p.purged, pluginID)
	return nil
}

func (p *purges) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	p.audited = append(p.audited, action+":"+target)
	return nil
}

type layers map[string][]byte

func (l layers) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(l[key])), nil
}

// A purge undoes the migrations of the layer the uninstalled plugin left, and records it.
func TestAPurgeUndoesTheLeftLayersMigrations(t *testing.T) {
	layer, err := installed.Pack(fstest.MapFS{
		"arkhitekton-plugin.yaml": {Data: []byte(`apiVersion: arkhitekton.plugin/v2
id: notes
version: 1.0.0
sdk: "1"
label: Notes
binary: bin/plugin
protocol: 1
migrations: { dir: migrations, table: goose_notes_db_version }
`)},
		"bin/plugin":                {Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		"migrations/0001_notes.sql": {Data: []byte("-- +goose Up\ncreate table notes ();\n-- +goose Down\ndrop table notes;\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	purge := func(table string) (*purges, []string, error) {
		db := &purges{leftover: store.PluginLeftover{
			PluginID: "notes", Version: "1.0.0", Object: "plugins/notes/1.0.0", Table: table,
		}}
		var undone []string
		purger := NewPluginPurger(db, layers{"plugins/notes/1.0.0": layer}, func(_ context.Context, plugin installed.Plugin) error {
			migrations, err := fs.Glob(plugin.Native.Files, "migrations/*.sql")
			undone = append(undone, migrations...)
			return err
		}, quietLogger())
		err := purger.Run(context.Background(), store.Job{ID: 7, Params: map[string]string{ParamPlugin: "notes"}})
		return db, undone, err
	}

	db, undone, err := purge("goose_notes_db_version")
	if err != nil {
		t.Fatal(err)
	}
	if len(undone) != 1 || len(db.purged) != 1 || db.audited[0] != "plugin.purged:notes" {
		t.Errorf("undone %v, purged %v, audited %v", undone, db.purged, db.audited)
	}

	// A layer that does not say what was recorded is not trusted with the tables.
	db, undone, err = purge("goose_other_db_version")
	if err == nil || len(undone) != 0 || len(db.purged) != 0 || !strings.HasPrefix(db.audited[0], "plugin.purge.failed") {
		t.Errorf("err %v, undone %v, purged %v, audited %v", err, undone, db.purged, db.audited)
	}
}
