package http

import (
	"context"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/connect"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// fixedTracker is a project's tracker, whatever the project: a plugin of this identifier whose
// links live under the URL given.
type fixedTracker struct {
	id, url string
}

func (f fixedTracker) TrackerPlugin(context.Context, int64) (connect.Tracker, error) {
	return connect.Tracker{
		Plugin:   capability.Plugin{ID: f.id, Tracker: linkTracker{}},
		Settings: capability.Settings{"url": f.url},
	}, nil
}

// linkTracker composes links like Redmine.
type linkTracker struct{ capability.TrackerCapability }

func (linkTracker) VersionURL(settings capability.Settings, id string) string {
	return settings["url"] + "/versions/" + id
}

func (linkTracker) IssueURL(settings capability.Settings, id string) string {
	return settings["url"] + "/issues/" + id
}

// fixedForges is every project's forge: a resolver and a tagger, either of which may be missing.
type fixedForges struct {
	resolver CommitResolver
	tagger   Tagger
}

func (f fixedForges) Forge(context.Context, int64) (provider.Forge, error) {
	if f.resolver == nil {
		return nil, connect.ErrNotConfigured
	}
	return resolvingForge{resolver: f.resolver}, nil
}

func (f fixedForges) TagWriter(context.Context, int64) (provider.TagWriter, error) {
	if f.tagger == nil {
		return nil, connect.ErrNotConfigured
	}
	return f.tagger, nil
}

// resolvingForge is a forge that only resolves commits.
type resolvingForge struct {
	provider.Forge
	resolver CommitResolver
}

func (f resolvingForge) ResolveCommit(ctx context.Context, ref string) (string, error) {
	return f.resolver.ResolveCommit(ctx, ref)
}
