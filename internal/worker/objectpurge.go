package worker

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
)

/*
 * Removing the objects nothing refers to any more.
 *
 * Runs are never deleted, so their raw artifacts and logs stay: they are what a replay reads.
 * What becomes an orphan is narrower:
 *
 *   - an attachment no result refers to, left by an earlier reading whose members differ;
 *   - the layer of a plugin version removed, or of an installation that never completed;
 *   - the raw artifacts and logs of a run the database does not know, after it was reset.
 *
 * Nothing younger than a day is touched: a run uploads before its row exists, and a reading
 * writes its attachments before the results that refer to them.
 */

const (
	// objectPurgeInterval spaces two passes: orphans appear at the pace of replays and uninstalls.
	objectPurgeInterval = 24 * time.Hour
	// objectGrace is the age under which an object is never removed.
	objectGrace = 24 * time.Hour
)

// ObjectReferences is what the purge reads from the database.
type ObjectReferences interface {
	// RunNames are the PipelineRuns the database knows.
	RunNames(ctx context.Context) (map[string]bool, error)
	// AttachmentObjects are the objects a result refers to as an attachment.
	AttachmentObjects(ctx context.Context) (map[string]bool, error)
	// PluginObjects are the layers of the plugin versions installed.
	PluginObjects(ctx context.Context) (map[string]bool, error)
}

// PurgeStorage is what the purge needs from object storage.
type PurgeStorage interface {
	List(ctx context.Context, prefix string) ([]blob.ObjectInfo, error)
	Stat(ctx context.Context, key string) (blob.ObjectInfo, error)
	Remove(ctx context.Context, key string) error
}

// ObjectPurger removes the orphan objects, once a day.
type ObjectPurger struct {
	references ObjectReferences
	storage    PurgeStorage
	now        func() time.Time
	logger     *slog.Logger
}

func NewObjectPurger(references ObjectReferences, storage PurgeStorage, logger *slog.Logger) *ObjectPurger {
	return &ObjectPurger{references: references, storage: storage, now: time.Now, logger: logger}
}

// Run purges until cancellation. A failed pass is logged and the loop goes on: it is
// maintenance, and the worker's other loops have nothing to do with it.
func (p *ObjectPurger) Run(ctx context.Context) error {
	p.once(ctx)
	ticker := time.NewTicker(objectPurgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.once(ctx)
		}
	}
}

func (p *ObjectPurger) once(ctx context.Context) {
	removed, err := p.Pass(ctx)
	if err != nil {
		p.logger.Error("purging orphan objects", "error", err, "removed", len(removed))
		return
	}
	if len(removed) > 0 {
		p.logger.Info("orphan objects removed", "count", len(removed), "objects", removed)
	}
}

// Pass removes the orphans and returns their keys.
func (p *ObjectPurger) Pass(ctx context.Context) ([]string, error) {
	runs, err := p.references.RunNames(ctx)
	if err != nil {
		return nil, err
	}
	attachments, err := p.references.AttachmentObjects(ctx)
	if err != nil {
		return nil, err
	}
	plugins, err := p.references.PluginObjects(ctx)
	if err != nil {
		return nil, err
	}

	orphan := map[string]func(key string) bool{
		"raw/":         func(key string) bool { return !runs[runOf(key)] },
		"logs/":        func(key string) bool { return !runs[runOf(key)] },
		"attachments/": func(key string) bool { return !attachments[key] },
		"plugins/":     func(key string) bool { return !plugins[key] },
	}
	var removed []string
	for _, prefix := range []string{"raw/", "logs/", "attachments/", "plugins/"} {
		objects, err := p.storage.List(ctx, prefix)
		if err != nil {
			return removed, err
		}
		for _, object := range objects {
			if !orphan[prefix](object.Key) || p.recent(object) {
				continue
			}
			// Read again just before removing: a replay may have written the same key since the
			// listing, and what it wrote is not an orphan.
			current, err := p.storage.Stat(ctx, object.Key)
			if err != nil || p.recent(current) {
				continue
			}
			if err := p.storage.Remove(ctx, object.Key); err != nil {
				return removed, err
			}
			removed = append(removed, object.Key)
		}
	}
	return removed, nil
}

func (p *ObjectPurger) recent(object blob.ObjectInfo) bool {
	return p.now().Sub(object.LastModified) < objectGrace
}

// runOf is the run a `raw/` or `logs/` key belongs to: its second segment.
func runOf(key string) string {
	segments := strings.SplitN(key, "/", 3)
	if len(segments) < 3 {
		return ""
	}
	return segments[1]
}
