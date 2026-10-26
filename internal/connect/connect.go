// Package connect builds a project's tracker and forge clients from the plugins enabled for it.
//
// The connection is a plugin's settings, the project's layered over the instance's, and nothing
// else: the deployment's variables only seed those settings once. Resolved at each call, so that
// a setting changed on the administration screen applies to the next request without a restart.
package connect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// ErrNotConfigured reports that no enabled plugin brings the connection asked for.
var ErrNotConfigured = errors.New("no enabled plugin brings this connection")

// Store is what resolving a connection reads.
type Store interface {
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
}

// Timeouts bound each call to the outside. They belong to the deployment, not to the plugins: they
// protect the dashboard from a slow boundary.
type Timeouts struct {
	Forge   time.Duration
	Tracker time.Duration
}

// Connections resolves and builds the connections.
type Connections struct {
	registry *capabilities.Registry
	store    Store
	opener   plugin.Opener
	timeouts Timeouts
}

func New(registry *capabilities.Registry, db Store, opener plugin.Opener, timeouts Timeouts) *Connections {
	return &Connections{registry: registry, store: db, opener: opener, timeouts: timeouts}
}

// Tracker is a project's tracker: the plugin, its resolved settings and a client.
type Tracker struct {
	Plugin   capability.Plugin
	Settings capability.Settings
	Client   provider.Tracker
}

// VersionURL links a version in the tracker's interface.
func (t Tracker) VersionURL(id string) string { return t.Plugin.Tracker.VersionURL(t.Settings, id) }

// IssueURL links an issue in the tracker's interface.
func (t Tracker) IssueURL(id string) string { return t.Plugin.Tracker.IssueURL(t.Settings, id) }

// Tracker returns the project's tracker.
func (c *Connections) Tracker(ctx context.Context, projectID int64) (Tracker, error) {
	found, settings, err := c.resolve(ctx, projectID, func(p capability.Plugin) bool { return p.Tracker != nil })
	if err != nil {
		return Tracker{}, err
	}
	project, err := c.store.GetProject(ctx, projectID)
	if err != nil {
		return Tracker{}, err
	}
	client, err := found.Tracker.Connect(c.target(project, settings, c.timeouts.Tracker))
	if err != nil {
		return Tracker{}, err
	}
	return Tracker{Plugin: found, Settings: settings, Client: client}, nil
}

// TrackerPlugin names the project's tracker plugin without connecting to it, for what only needs
// its identifier and its links.
func (c *Connections) TrackerPlugin(ctx context.Context, projectID int64) (Tracker, error) {
	found, settings, err := c.resolve(ctx, projectID, func(p capability.Plugin) bool { return p.Tracker != nil })
	if err != nil {
		return Tracker{}, err
	}
	return Tracker{Plugin: found, Settings: settings}, nil
}

// Forge returns the project's forge, bound to its repository.
func (c *Connections) Forge(ctx context.Context, projectID int64) (provider.Forge, error) {
	found, target, err := c.forge(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return found.Forge.Connect(target)
}

// TagWriter returns what creates tags in the project's repository.
func (c *Connections) TagWriter(ctx context.Context, projectID int64) (provider.TagWriter, error) {
	found, target, err := c.forge(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return found.Forge.Writer(target)
}

// Probe checks a repository with the instance's forge settings: the project being declared may
// not exist yet.
func (c *Connections) Probe(ctx context.Context, repository, publicKey string) (provider.Probe, error) {
	all, err := c.store.PluginSettingsAll(ctx)
	if err != nil {
		return provider.Probe{}, err
	}
	for _, candidate := range c.registry.Plugins() {
		if candidate.Forge == nil || !all[candidate.ID].Enabled {
			continue
		}
		settings, err := plugin.Unseal(candidate, c.opener, all[candidate.ID].Config)
		if err != nil {
			return provider.Probe{}, fmt.Errorf("connection of plugin %q: %w", candidate.ID, err)
		}
		prober, err := candidate.Forge.Prober(capability.Target{
			Settings: settings, Timeout: c.timeouts.Forge,
		})
		if err != nil {
			return provider.Probe{}, err
		}
		return prober.Probe(ctx, repository, publicKey)
	}
	return provider.Probe{}, ErrNotConfigured
}

func (c *Connections) forge(ctx context.Context, projectID int64) (capability.Plugin, capability.Target, error) {
	found, settings, err := c.resolve(ctx, projectID, func(p capability.Plugin) bool { return p.Forge != nil })
	if err != nil {
		return capability.Plugin{}, capability.Target{}, err
	}
	project, err := c.store.GetProject(ctx, projectID)
	if err != nil {
		return capability.Plugin{}, capability.Target{}, err
	}
	return found, c.target(project, settings, c.timeouts.Forge), nil
}

// resolve finds the plugin enabled for the project that brings what is asked, and its settings
// unsealed. At most one such plugin is enabled for a project: the administration screen refuses a
// second.
func (c *Connections) resolve(
	ctx context.Context, projectID int64, brings func(capability.Plugin) bool,
) (capability.Plugin, capability.Settings, error) {
	for _, candidate := range c.registry.Plugins() {
		if !brings(candidate) {
			continue
		}
		resolved, err := c.store.ResolvedPlugin(ctx, projectID, candidate.ID)
		if errors.Is(err, store.ErrPluginDisabled) {
			continue
		}
		if err != nil {
			return capability.Plugin{}, nil, err
		}
		settings, err := plugin.Unseal(candidate, c.opener, resolved.Config)
		if err != nil {
			return capability.Plugin{}, nil, fmt.Errorf("connection of plugin %q: %w", candidate.ID, err)
		}
		return candidate, settings, nil
	}
	return capability.Plugin{}, nil, ErrNotConfigured
}

func (c *Connections) target(project store.Project, settings capability.Settings, timeout time.Duration) capability.Target {
	return capability.Target{
		Project: project.Slug, Repository: project.Repository, Settings: settings, Timeout: timeout,
	}
}

// ReviewStatuses are the tracker statuses the project declares as making an issue eligible for
// its release note. Empty when it declares none, or has no tracker.
func (c *Connections) ReviewStatuses(ctx context.Context, projectID int64) ([]string, error) {
	tracker, err := c.TrackerPlugin(ctx, projectID)
	if errors.Is(err, ErrNotConfigured) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return plugin.List(tracker.Settings["review_statuses"]), nil
}
