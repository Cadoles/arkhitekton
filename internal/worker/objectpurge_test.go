package worker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
)

type references struct{ runs, attachments, plugins map[string]bool }

func (r references) RunNames(context.Context) (map[string]bool, error) { return r.runs, nil }
func (r references) AttachmentObjects(context.Context) (map[string]bool, error) {
	return r.attachments, nil
}
func (r references) PluginObjects(context.Context) (map[string]bool, error) { return r.plugins, nil }

// bucket is objects by key, each with its last modification.
type bucket map[string]time.Time

func (b bucket) List(_ context.Context, prefix string) ([]blob.ObjectInfo, error) {
	var objects []blob.ObjectInfo
	for key, modified := range b {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			objects = append(objects, blob.ObjectInfo{Key: key, LastModified: modified})
		}
	}
	return objects, nil
}

func (b bucket) Stat(_ context.Context, key string) (blob.ObjectInfo, error) {
	modified, found := b[key]
	if !found {
		return blob.ObjectInfo{}, errors.New("not found")
	}
	return blob.ObjectInfo{Key: key, LastModified: modified}, nil
}

func (b bucket) Remove(_ context.Context, key string) error {
	delete(b, key)
	return nil
}

func TestOnlyOldOrphansArePurged(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	objects := bucket{
		"raw/known-1/junit.xml":                 old,
		"raw/forgotten-2/junit.xml":             old,
		"raw/starting-3/junit.xml":              now.Add(-time.Hour), // its run row is not written yet
		"logs/forgotten-2/task/step.log":        old,
		"logs/known-1/task/step.log":            old,
		"attachments/known-1/ab/video.mp4":      old,
		"attachments/known-1/cd/screenshot.png": old, // left by an earlier reading
		"plugins/gotest/1.0.0/sha256-aa.tar.gz": old,
		"plugins/gotest/0.9.0/sha256-bb.tar.gz": old, // a version removed
		"artifacts/releases/1.0/note.pdf":       old, // never considered
	}
	purger := NewObjectPurger(references{
		runs:        map[string]bool{"known-1": true},
		attachments: map[string]bool{"attachments/known-1/ab/video.mp4": true},
		plugins:     map[string]bool{"plugins/gotest/1.0.0/sha256-aa.tar.gz": true},
	}, objects, slog.New(slog.DiscardHandler))
	purger.now = func() time.Time { return now }

	removed, err := purger.Pass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	expected := []string{
		"attachments/known-1/cd/screenshot.png",
		"logs/forgotten-2/task/step.log",
		"plugins/gotest/0.9.0/sha256-bb.tar.gz",
		"raw/forgotten-2/junit.xml",
	}
	if !slices.Equal(removed, expected) {
		t.Errorf("removed %v", removed)
	}
	if len(objects) != 6 {
		t.Errorf("left %v", objects)
	}
}

// A replay writes an attachment again between the listing and the removal: what it wrote stays.
type rewritingBucket struct {
	bucket
	now time.Time
}

func (b rewritingBucket) Stat(ctx context.Context, key string) (blob.ObjectInfo, error) {
	b.bucket[key] = b.now
	return b.bucket.Stat(ctx, key)
}

func TestAnObjectWrittenSinceTheListingStays(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	objects := bucket{"attachments/known-1/cd/screenshot.png": now.Add(-48 * time.Hour)}
	purger := NewObjectPurger(references{runs: map[string]bool{"known-1": true}},
		rewritingBucket{bucket: objects, now: now}, slog.New(slog.DiscardHandler))
	purger.now = func() time.Time { return now }

	if removed, err := purger.Pass(context.Background()); err != nil || len(removed) != 0 || len(objects) != 1 {
		t.Errorf("removed %v, %v", removed, err)
	}
}
