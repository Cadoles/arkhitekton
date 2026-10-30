package worker

import (
	"context"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * A plugin's probes, run by the loop like the core's.
 *
 * The plugin reads; the dashboard names. A plugin returns observations under the suffixes it
 * declared, and the fact's type, its source and its deduplication key are composed here: a plugin
 * cannot produce a fact under another plugin's name, nor one its declaration does not announce.
 */

// PluginProbes adapts every plugin's probes to the loop.
func PluginProbes(registry *capabilities.Registry, timeout time.Duration) []Probe {
	declared := registry.Probes()
	probes := make([]Probe, 0, len(declared))
	for _, entry := range declared {
		probes = append(probes, pluginProbe{plugin: entry.Plugin, probe: entry.Probe, timeout: timeout})
	}
	return probes
}

type pluginProbe struct {
	plugin  capability.Plugin
	probe   capability.Probe
	timeout time.Duration
}

func (p pluginProbe) PluginID() string { return p.plugin.ID }

// Source is the plugin's identifier and the probe's name: `gitea.pull_requests`.
func (p pluginProbe) Source() string { return p.plugin.ID + "." + p.probe.Name() }

func (p pluginProbe) Schedule() Schedule {
	schedule := p.probe.Schedule()
	return Schedule{Interval: schedule.Interval, Eager: schedule.Eager}
}

func (p pluginProbe) Poll(
	ctx context.Context, project store.Project, config map[string]string, cursor string,
) ([]event.Event, string, error) {
	observed, next, err := p.probe.Poll(ctx, capability.Target{
		Project: project.Slug, Repository: project.Repository,
		Settings: capability.Settings(config), Timeout: p.timeout,
	}, cursor)
	if err != nil {
		return nil, "", err
	}

	facts, err := capabilities.Facts(p.plugin, observed)
	if err != nil {
		return nil, "", err
	}
	return facts, next, nil
}
