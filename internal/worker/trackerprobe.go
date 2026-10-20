package worker

import (
	"context"
	"log/slog"
	"maps"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * The tracker probe.
 *
 * The one that **existed before the contract**, and that is its whole value. A probe contract
 * written on the Gitea probe alone would have taken its shape without anyone noticing; written
 * then exercised on a source that has been running for months, it has a chance of being right.
 *
 * # What this second implementation revealed
 *
 * Two gaps, both fixed in `probes.go`:
 *
 *   - **the cadence was the loop's**, not the probe's. The tracker is re-read every five
 *     minutes — human typing goes no faster — and a forge is watched more often. [Schedule]
 *     lets each declare its own, above the deployment's floor;
 *   - **the bootstrap discarded its facts instead of keeping them**. That was enough as long as
 *     a probe filtered on its cursor. This one returns a **snapshot** — Redmine's API does not
 *     filter the versions — and the discarded facts would have come back new at the next pass,
 *     triggering everything the bootstrap claimed to silence.
 *
 * # What the contract absorbed without contorting itself
 *
 * The rest: per-project plugin settings, unsealing the key, cursor in `sync_state`, isolation
 * of failures. The synchronisation carried its own version of it, written six months earlier;
 * nothing of it remains here.
 */

// trackerProbe adapts the tracker synchronisation to the probe contract.
//
// A thin wrapper, deliberately: the projection has not moved. This file only translates what
// the loop brings — a configuration resolved and unsealed — into what the projection needs, a
// client and a cursor.
type trackerProbe struct {
	sync    *TrackerSync
	connect TrackerConnector
}

// NewTrackerProbe builds the probe of one tracker plugin.
func NewTrackerProbe(sync *TrackerSync, connect TrackerConnector) Probe {
	return trackerProbe{sync: sync, connect: connect}
}

// TrackerProbes builds a probe for each plugin that brings a tracker.
func TrackerProbes(
	registry *capabilities.Registry, db TrackerStore, timeout time.Duration, logger *slog.Logger,
) []Probe {
	var probes []Probe
	for _, plugin := range registry.Plugins() {
		if plugin.Tracker == nil {
			continue
		}
		source := TrackerSource{Tool: plugin.ID}
		for _, spec := range plugin.Events {
			if spec.Suffix == "issue.closed" {
				source.ClosedType = plugin.EventType(spec)
			}
		}
		tracker := plugin.Tracker
		probes = append(probes, NewTrackerProbe(NewTrackerSync(db, source, logger),
			func(target capability.Target) (TrackerReader, error) {
				target.Timeout = timeout
				return tracker.Connect(target)
			}))
	}
	return probes
}

func (p trackerProbe) PluginID() string { return p.sync.source.Tool }

// Source names the cursor in `sync_state`: the plugin's identifier, which for Redmine keeps the
// name the cursors have carried from the start.
func (p trackerProbe) Source() string { return p.sync.source.Tool }

// Schedule: five minutes, and an immediate pass.
//
// The immediate pass is no convenience. This probe **fills a screen** — the versions one —
// which would be empty for five minutes after each start-up. It is the only case where waiting
// costs more than the call it avoids, and it is what [Schedule.Eager] exists for.
func (p trackerProbe) Schedule() Schedule {
	return Schedule{Interval: syncInterval, Eager: true}
}

// Poll projects one pass and returns the facts it observes.
//
// The configuration arrives **unsealed**: the loop opened it, and that is now the only place
// where this happens. The key exists in clear text only for the time it takes to build the
// client, in a process that exposes no port.
func (p trackerProbe) Poll(
	ctx context.Context, project store.Project, config map[string]string, cursor string,
) ([]event.Event, string, error) {
	// The project's identifier in the tracker comes from the setting, and falls back to the
	// `tracker_project` column: it predates the plugins, it is right, and a deployment whose
	// bootstrap did not run must not stop synchronising for that.
	trackerProject := strings.TrimSpace(config["project"])
	if trackerProject == "" {
		trackerProject = strings.TrimSpace(project.TrackerProject)
	}
	if trackerProject == "" {
		// **Not an error**, and that is a fix.
		//
		// I had made this case a named failure, taking it for a thwarted intention: "the plugin is
		// switched on, so someone wants to track this project". The production log showed the opposite
		// — the plugin is enabled at the **instance** level, and four projects out of five simply have
		// no counterpart in the tracker. The normal case therefore shouted every five minutes, for
		// each of them.
		return nil, cursor, nil
	}

	settings := capability.Settings{}
	maps.Copy(settings, config)
	settings["project"] = trackerProject
	client, err := p.connect(capability.Target{
		Project: project.Slug, Repository: project.Repository, Settings: settings,
	})
	if err != nil {
		return nil, "", err
	}

	return p.sync.Observe(ctx, project.ID, trackerProject, client, cursor)
}
