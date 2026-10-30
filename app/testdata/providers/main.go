// A native plugin serving the core's Redmine tracker and Gitea forge, for the tests comparing the
// native path with the compiled one, and for trying an installed tracker and forge on a cluster.
//
// One plugin brings both, so their settings are prefixed: `tracker_url`, `forge_token`. The
// dashboard's own keys, `project` and `review_statuses`, stay as they are.
package main

import (
	"context"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/extension"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

func main() {
	var tracker, forge capability.Plugin
	for _, plugin := range builtin.Plugins() {
		switch plugin.ID {
		case "redmine":
			tracker = plugin
		case "gitea":
			forge = plugin
		}
	}
	extension.Serve(func(context.Context, extension.Runtime) (extension.Hooks, error) {
		probes := make([]capability.Probe, 0, len(forge.Probes))
		for _, probe := range forge.Probes {
			probes = append(probes, forgeProbe{probe})
		}
		return extension.Hooks{
			Tracker: trackerOnly{tracker.Tracker}, Forge: forgeOnly{forge.Forge},
			Probes: probes, Executor: forgeExecutor{forge.Executor},
			Receiver: forgeReceiver{forge.Webhook.Receiver},
		}, nil
	})
}

// scoped keeps the settings under a prefix, without it.
func scoped(settings capability.Settings, prefix string) capability.Settings {
	kept := capability.Settings{}
	for key, value := range settings {
		if rest, found := strings.CutPrefix(key, prefix); found {
			kept[rest] = value
		}
	}
	return kept
}

func scopedTarget(target capability.Target, prefix string) capability.Target {
	target.Settings = scoped(target.Settings, prefix)
	return target
}

type trackerOnly struct{ capability.TrackerCapability }

func (t trackerOnly) Connect(target capability.Target) (provider.Tracker, error) {
	return t.TrackerCapability.Connect(scopedTarget(target, "tracker_"))
}

func (t trackerOnly) VersionURL(settings capability.Settings, id string) string {
	return t.TrackerCapability.VersionURL(scoped(settings, "tracker_"), id)
}

func (t trackerOnly) IssueURL(settings capability.Settings, id string) string {
	return t.TrackerCapability.IssueURL(scoped(settings, "tracker_"), id)
}

type forgeOnly struct{ capability.ForgeCapability }

func (f forgeOnly) Connect(target capability.Target) (provider.Forge, error) {
	return f.ForgeCapability.Connect(scopedTarget(target, "forge_"))
}

func (f forgeOnly) Writer(target capability.Target) (provider.TagWriter, error) {
	return f.ForgeCapability.Writer(scopedTarget(target, "forge_"))
}

func (f forgeOnly) Prober(target capability.Target) (provider.RepositoryProber, error) {
	return f.ForgeCapability.Prober(scopedTarget(target, "forge_"))
}

type forgeProbe struct{ capability.Probe }

func (p forgeProbe) Poll(ctx context.Context, target capability.Target, cursor string) ([]capability.Observation, string, error) {
	return p.Probe.Poll(ctx, scopedTarget(target, "forge_"), cursor)
}

type forgeExecutor struct{ capability.ActionExecutor }

func (e forgeExecutor) Execute(ctx context.Context, target capability.Target, suffix string, params map[string]string) error {
	return e.ActionExecutor.Execute(ctx, scopedTarget(target, "forge_"), suffix, params)
}

type forgeReceiver struct{ capability.Receiver }

func (r forgeReceiver) Receive(
	ctx context.Context, target capability.Target, delivery capability.Delivery,
) ([]capability.Observation, error) {
	return r.Receiver.Receive(ctx, scopedTarget(target, "forge_"), delivery)
}
