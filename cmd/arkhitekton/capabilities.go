package main

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	pipelinelib "forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/core"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/k6"
	mseplugin "forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/mse"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/php"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/security"
)

// builtinPlugins are the plugins compiled into the binary.
func builtinPlugins() []capability.Plugin {
	return []capability.Plugin{core.Plugin(), php.Plugin(), mseplugin.Plugin(), security.Plugin(), k6.Plugin()}
}

// capabilityRegistry gathers what the built-in plugins bring.
func capabilityRegistry() (*capability.Registry, error) {
	return capability.NewRegistry(builtinPlugins()...)
}

// referenceLibrary is what a project declaring the `php` stack gets installed: the PHP pipelines
// and the MSE integration tests, which the stack carried before plugins split them.
func referenceLibrary() *pipelinelib.Library {
	return pipelinelib.NewLibrary(map[string][]capability.Manifest{
		"php": append(php.Manifests(), mseplugin.Manifests()...),
	})
}
