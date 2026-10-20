package main

import (
	"fmt"
	"log/slog"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	pipelinelib "forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/worker"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// referenceLibrary is what a project declaring a stack gets installed.
func referenceLibrary() *pipelinelib.Library {
	return pipelinelib.NewLibrary(builtin.Stacks())
}

// loadCatalogue reads the catalogue and binds it to the built-in plugins' capabilities.
func loadCatalogue(cfg config.Config) (*catalog.Catalog, error) {
	registry, err := builtin.Registry()
	if err != nil {
		return nil, fmt.Errorf("built-in plugins: %w", err)
	}
	return catalog.Load(cfg.Tekton.CatalogPath, catalog.WithCapabilities(registry))
}

// capabilityReconciler wires the installation and the check of the bound capabilities.
func capabilityReconciler(
	cluster *tekton.Client, cfg config.Config, pipelines *catalog.Catalog,
	sink worker.AvailabilitySink, logger *slog.Logger,
) *worker.CapabilityReconciler {
	resources, err := boundResources(pipelines)
	if err != nil {
		logger.Error("bound capabilities' definitions", "error", err)
	}

	var installer worker.ResourceInstaller
	if built, err := libraryInstaller(cluster, cfg); err != nil {
		logger.Warn("pipeline installation unavailable", "reason", err,
			"consequence", "the pipelines already present are used, and checked")
	} else {
		installer = built
	}
	return worker.NewCapabilityReconciler(installer, resources, pipelines, cluster, sink, logger)
}

// boundResources are the definitions the bound capabilities ship, each once.
func boundResources(pipelines *catalog.Catalog) ([]pipelinelib.Resource, error) {
	registry, err := builtin.Registry()
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var resources []pipelinelib.Resource
	for _, pipeline := range pipelines.Pipelines {
		if pipeline.Capability == "" {
			continue
		}
		ref, err := capability.ParseRef(pipeline.Capability)
		if err != nil {
			return nil, err
		}
		declared, _, _ := registry.Pipeline(ref)
		for _, manifest := range declared.Spec.Manifests {
			key := manifest.Kind + "/" + manifest.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			resources = append(resources, pipelinelib.Resource{
				Name: manifest.Name, Kind: manifest.Kind, Manifest: manifest.YAML,
			})
		}
	}
	return resources, nil
}

// pluginVersions gives, by plugin, the version of the built-in plugins.
func pluginVersions() map[string]string {
	versions := map[string]string{}
	for _, plugin := range builtin.Plugins() {
		versions[plugin.ID] = plugin.Version
	}
	return versions
}
