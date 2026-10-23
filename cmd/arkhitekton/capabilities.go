package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/live"
	pipelinelib "forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/worker"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// referenceLibrary is what a project declaring a stack gets installed.
func referenceLibrary() *pipelinelib.Library {
	return pipelinelib.NewLibrary(builtin.Stacks())
}

// capabilitySnapshot builds the capabilities in force: the plugins this binary carries, and the
// catalogue file completed by the bindings an administrator recorded.
func capabilitySnapshot(cfg config.Config, db *store.DB) func(ctx context.Context, version int64) (live.Snapshot, error) {
	return func(ctx context.Context, version int64) (live.Snapshot, error) {
		registry, err := builtin.Registry()
		if err != nil {
			return live.Snapshot{}, fmt.Errorf("built-in plugins: %w", err)
		}
		recorded, err := recordedBindings(ctx, db)
		if err != nil {
			return live.Snapshot{}, err
		}
		pipelines, err := catalog.Load(cfg.Tekton.CatalogPath, catalog.WithCapabilities(registry), catalog.WithBindings(recorded))
		if err != nil {
			return live.Snapshot{}, err
		}
		return live.Snapshot{Registry: registry, Catalog: pipelines, Version: version}, nil
	}
}

func recordedBindings(ctx context.Context, db *store.DB) ([]catalog.Binding, error) {
	rows, err := db.CapabilityBindings(ctx)
	if err != nil {
		return nil, err
	}
	bindings := make([]catalog.Binding, 0, len(rows))
	for _, row := range rows {
		binding, err := catalog.ParseBinding(row.Binding)
		if err != nil {
			return nil, fmt.Errorf("binding of %s: %w", row.Ref, err)
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

// checkBinding builds the catalogue as it would be with one more binding, or a changed one, and says
// why it could not.
func checkBinding(cfg config.Config, db *store.DB) func(ctx context.Context, binding catalog.Binding) error {
	return func(ctx context.Context, binding catalog.Binding) error {
		registry, err := builtin.Registry()
		if err != nil {
			return err
		}
		recorded, err := recordedBindings(ctx, db)
		if err != nil {
			return err
		}
		next := []catalog.Binding{binding}
		for _, other := range recorded {
			if other.Ref != binding.Ref {
				next = append(next, other)
			}
		}
		_, err = catalog.Load(cfg.Tekton.CatalogPath, catalog.WithCapabilities(registry), catalog.WithBindings(next))
		return err
	}
}

// startingCapabilities builds the first snapshot. A recorded binding that no longer holds must not
// keep the dashboard from starting: it starts on the file alone, and says so.
func startingCapabilities(ctx context.Context, cfg config.Config, db *store.DB, logger *slog.Logger) (*live.Holder, *live.Reloader, error) {
	build := capabilitySnapshot(cfg, db)
	version, err := db.CapabilityVersion(ctx)
	if err != nil {
		return nil, nil, err
	}
	first, err := build(ctx, version)
	if err != nil {
		logger.Error("recorded bindings not applied, starting on the catalogue file alone", "error", err)
		registry, regErr := builtin.Registry()
		if regErr != nil {
			return nil, nil, regErr
		}
		pipelines, fileErr := catalog.Load(cfg.Tekton.CatalogPath, catalog.WithCapabilities(registry))
		if fileErr != nil {
			return nil, nil, fileErr
		}
		first = live.Snapshot{Registry: registry, Catalog: pipelines, Version: version}
	}
	holder := live.NewHolder(first)
	return holder, live.NewReloader(holder, db.CapabilityVersion, build, capabilityReloadInterval, logger), nil
}

// capabilityReloadInterval bounds how long an installation or a binding takes to apply.
const capabilityReloadInterval = 30 * time.Second

// capabilityReconciler wires the installation and the check of the bound capabilities.
func capabilityReconciler(
	cluster *tekton.Client, cfg config.Config, inForce *live.Holder,
	sink worker.AvailabilitySink, logger *slog.Logger,
) *worker.CapabilityReconciler {
	var installer worker.ResourceInstaller
	if built, err := libraryInstaller(cluster, cfg); err != nil {
		logger.Warn("pipeline installation unavailable", "reason", err,
			"consequence", "the pipelines already present are used, and checked")
	} else {
		installer = built
	}
	resources := func() []pipelinelib.Resource {
		snapshot := inForce.Current()
		bound, err := boundResources(snapshot.Catalog, snapshot.Registry)
		if err != nil {
			logger.Error("bound capabilities' definitions", "error", err)
		}
		return bound
	}
	return worker.NewCapabilityReconciler(installer, resources, inForce.Catalogs(), cluster, sink, logger)
}

// boundResources are the definitions the bound capabilities ship, each once.
func boundResources(pipelines *catalog.Catalog, registry *capabilities.Registry) ([]pipelinelib.Resource, error) {
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
