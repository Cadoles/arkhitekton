package worker

import (
	"github.com/Cadoles/arkhitekton/internal/action"
	"github.com/Cadoles/arkhitekton/internal/capabilities"
	"github.com/Cadoles/arkhitekton/internal/plugins/builtin"
	"github.com/Cadoles/arkhitekton/sdk/capability"
	"github.com/Cadoles/arkhitekton/sdk/provider"
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
