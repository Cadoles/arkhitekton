package worker

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// liveRegistry is the binary's own registry: the live tests drive a real deployment, and the
// plugins they exercise are the ones it carries.
func liveRegistry() *capabilities.Registry {
	registry, err := builtin.Registry()
	if err != nil {
		panic(err)
	}
	return registry
}

func liveActions() action.Catalog { return action.NewCatalog(liveRegistry().Plugins()) }

// liveTracker connects through the binary's Redmine plugin.
func liveTracker(target capability.Target) (provider.Tracker, error) {
	tracker, err := liveRegistry().Plugin("redmine")
	if err != nil {
		return nil, err
	}
	return tracker.Tracker.Connect(target)
}
