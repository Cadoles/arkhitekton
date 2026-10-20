package main

import (
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/module"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// moduleTick bounds how long switching a module on or off, or editing its settings, takes to
// apply.
const moduleTick = 30 * time.Second

// hostModules binds each module to the plugin that declares it, and refuses one whose plugin is
// absent or declares no module: the switch on the administration screen would control nothing.
func hostModules(registry *capabilities.Registry, modules ...module.Module) ([]module.Module, error) {
	for _, candidate := range modules {
		declared, err := registry.Plugin(candidate.Plugin())
		if err != nil {
			return nil, fmt.Errorf("module %q: %w", candidate.Plugin(), err)
		}
		if !declared.HasCapability(capability.KindModule) {
			return nil, fmt.Errorf("module %q: its plugin declares no module", candidate.Plugin())
		}
	}
	return modules, nil
}
