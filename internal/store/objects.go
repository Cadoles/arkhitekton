package store

import (
	"context"
	"fmt"
)

/*
 * What the database refers to in object storage, for the purge of what it no longer does.
 */

// RunNames are the PipelineRuns the database knows.
func (db *DB) RunNames(ctx context.Context) (map[string]bool, error) {
	return db.keys(ctx, `select pipelinerun_name from runs`)
}

// AttachmentObjects are the objects a result refers to as an attachment.
func (db *DB) AttachmentObjects(ctx context.Context) (map[string]bool, error) {
	return db.keys(ctx, `select object from result_attachments`)
}

// PluginObjects are the stored layers of the plugin versions installed, and of those an
// uninstalled plugin left behind: they undo its tables when it is purged.
func (db *DB) PluginObjects(ctx context.Context) (map[string]bool, error) {
	return db.keys(ctx, `select object from plugin_installations where state = 'installed' or leftover`)
}

func (db *DB) keys(ctx context.Context, query string) (map[string]bool, error) {
	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("object references: %w", err)
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("object references: %w", err)
		}
		keys[key] = true
	}
	return keys, rows.Err()
}
