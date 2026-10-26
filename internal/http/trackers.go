package http

import (
	"context"
	"errors"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/connect"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// Trackers names a project's tracker plugin: the one enabled for it that brings a tracker.
type Trackers interface {
	TrackerPlugin(ctx context.Context, projectID int64) (connect.Tracker, error)
}

// Forges resolves a project's forge from the plugin enabled for it.
type Forges interface {
	Forge(ctx context.Context, projectID int64) (provider.Forge, error)
	TagWriter(ctx context.Context, projectID int64) (provider.TagWriter, error)
}

// projectTracker is a project's tracker as the screens need it: its links, and the name of its
// synchronisation's cursor.
type projectTracker struct {
	tracker connect.Tracker
	found   bool
}

// trackerOf resolves a project's tracker. None, or one that cannot be read, gives no links rather
// than links to nowhere.
func trackerOf(ctx context.Context, trackers Trackers, projectID int64) projectTracker {
	if trackers == nil {
		return projectTracker{}
	}
	tracker, err := trackers.TrackerPlugin(ctx, projectID)
	if err != nil {
		return projectTracker{}
	}
	return projectTracker{tracker: tracker, found: true}
}

func (t projectTracker) versionURL(id string) string {
	if !t.found {
		return ""
	}
	return t.tracker.VersionURL(id)
}

func (t projectTracker) issueURL(id string) string {
	if !t.found {
		return ""
	}
	return t.tracker.IssueURL(id)
}

// source is the synchronisation's cursor: the tracker plugin's identifier.
func (t projectTracker) source() string {
	if !t.found {
		return ""
	}
	return t.tracker.Plugin.ID
}

// notConfigured says whether a connection failed for want of settings rather than for an
// outage: the screens then fall back to what works without it.
func notConfigured(err error) bool {
	return errors.Is(err, connect.ErrNotConfigured) || errors.Is(err, provider.ErrNotConfigured)
}
