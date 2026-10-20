// Package builtin assembles the plugins compiled into the binary.
//
// The one place that lists them. The binary and the tests of the shipped configuration both need
// the same list; a second copy would let them disagree about what a deployment runs.
package builtin

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/core"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/k6"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/mse"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/php"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/security"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// Plugins are the plugins compiled into the binary.
func Plugins() []capability.Plugin {
	return []capability.Plugin{core.Plugin(), php.Plugin(), mse.Plugin(), security.Plugin(), k6.Plugin()}
}

// Registry gathers what the built-in plugins bring.
func Registry() (*capabilities.Registry, error) {
	return capabilities.New(Plugins()...)
}

// Stacks are the reference libraries a project may declare: `php` installs the PHP pipelines and
// the MSE integration tests, which the stack carried before plugins split them.
func Stacks() map[string][]capability.Manifest {
	return map[string][]capability.Manifest{
		"php": append(php.Manifests(), mse.Manifests()...),
	}
}
