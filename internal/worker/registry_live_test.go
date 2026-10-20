package worker

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
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
