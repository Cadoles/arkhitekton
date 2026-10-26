package app

import (
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// moduleTick bounds how long switching a module on or off, or editing its settings, takes to
// apply.
const moduleTick = 30 * time.Second

// carriedPlugins are the plugins this binary carries.
func carriedPlugins() []capability.Plugin { return builtin.Plugins() }

// carriedRegistry gathers the carried plugins.
func carriedRegistry() (*capabilities.Registry, error) {
	registry, err := capabilities.New(carriedPlugins()...)
	if err != nil {
		return nil, fmt.Errorf("carried plugins: %w", err)
	}
	return registry, nil
}

// carriedStacks are the reference stacks a project may declare.
func carriedStacks() map[string][]capability.Manifest { return builtin.Stacks() }
