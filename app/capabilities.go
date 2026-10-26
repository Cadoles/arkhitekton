package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/live"
	pipelinelib "forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/worker"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// referenceLibrary is what a project declaring a stack gets installed.
func referenceLibrary() *pipelinelib.Library {
	return pipelinelib.NewLibrary(carriedStacks())
}

// installedPlugins reads the installed plugins from their stored layer. Each layer is compiled once:
// it is immutable, named by its digest, and a reload that recompiled every module would cost a
// sandbox per plugin each time a binding changes.
type installedPlugins struct {
	db      *store.DB
	objects interface {
		Get(ctx context.Context, key string) (io.ReadCloser, error)
	}

	// interpreters asks a plugin's program to read its pipelines' runs; nil in a role that runs no
	// program.
	interpreters installed.Interpreters

	mu       sync.Mutex
	compiled map[string]installed.Plugin
}

// plugin reads one installed plugin, compiled.
func (p *installedPlugins) plugin(ctx context.Context, object string) (installed.Plugin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if plugin, cached := p.compiled[object]; cached {
		return plugin, nil
	}
	body, err := p.objects.Get(ctx, object)
	if err != nil {
		return installed.Plugin{}, fmt.Errorf("installed plugin %s: %w", object, err)
	}
	defer func() { _ = body.Close() }()
	files, err := installed.Unpack(body, manifest.MaxSize)
	if err != nil {
		return installed.Plugin{}, fmt.Errorf("installed plugin %s: %w", object, err)
	}
	plugin, err := installed.Read(files, p.interpreters)
	if err != nil {
		return installed.Plugin{}, fmt.Errorf("installed plugin %s: %w", object, err)
	}
	if plugin.Plugin, err = capabilities.Prepare(plugin.Plugin); err != nil {
		return installed.Plugin{}, fmt.Errorf("installed plugin %s: %w", object, err)
	}
	if p.compiled == nil {
		p.compiled = map[string]installed.Plugin{}
	}
	p.compiled[object] = plugin
	return plugin, nil
}

// load reads the installed plugins: the active version of each, and the earlier ones kept to read
// the runs they launched.
func (p *installedPlugins) load(ctx context.Context) (active, earlier []installed.Plugin, err error) {
	rows, err := p.db.InstalledPlugins(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		plugin, err := p.plugin(ctx, row.Object)
		if err != nil {
			return nil, nil, err
		}
		if row.Active {
			active = append(active, plugin)
		} else {
			earlier = append(earlier, plugin)
		}
	}
	return active, earlier, nil
}

// capabilitySources builds the capabilities in force: the plugins this binary carries and those an
// administrator installed, and the catalogue file completed by the recorded bindings.
type capabilitySources struct {
	cfg       config.Config
	db        *store.DB
	installed *installedPlugins
}

func newCapabilitySources(cfg config.Config, db *store.DB, objects *blob.Client) *capabilitySources {
	return &capabilitySources{cfg: cfg, db: db, installed: &installedPlugins{db: db, objects: objects}}
}

// registry composes the plugins. With a candidate, it is the registry as it would be with the
// candidate active in place of its plugin's active version.
func (s *capabilitySources) registry(ctx context.Context, candidate *capability.Plugin) (*capabilities.Registry, error) {
	installedActive, installedEarlier, err := s.installed.load(ctx)
	if err != nil {
		return nil, err
	}
	var active, earlier []capability.Plugin
	for _, plugin := range installedActive {
		if candidate == nil || plugin.ID != candidate.ID {
			active = append(active, plugin.Plugin)
		}
	}
	if candidate != nil {
		active = append(active, *candidate)
	}
	for _, plugin := range installedEarlier {
		earlier = append(earlier, plugin.Plugin)
	}

	carried := carriedPlugins()
	builtIn := map[string]bool{}
	for _, plugin := range carried {
		builtIn[plugin.ID] = true
	}
	activeVersion := map[string]string{}
	for _, plugin := range active {
		if builtIn[plugin.ID] {
			return nil, fmt.Errorf("plugin %q: the id of a built-in plugin", plugin.ID)
		}
		activeVersion[plugin.ID] = plugin.Version
	}
	registry, err := capabilities.New(append(carried, active...)...)
	if err != nil {
		return nil, err
	}
	var retained []capability.Plugin
	for _, plugin := range earlier {
		if version, carried := activeVersion[plugin.ID]; carried && version != plugin.Version {
			retained = append(retained, plugin)
		}
	}
	if err := registry.Retain(retained...); err != nil {
		return nil, err
	}
	return registry, nil
}

// catalog loads the catalogue file against a registry, completed by bindings.
func (s *capabilitySources) catalog(registry *capabilities.Registry, bindings []catalog.Binding) (*catalog.Catalog, error) {
	return catalog.Load(s.cfg.Tekton.CatalogPath, catalog.WithCapabilities(registry), catalog.WithBindings(bindings))
}

// snapshot builds the capabilities in force.
func (s *capabilitySources) snapshot(ctx context.Context, version int64) (live.Snapshot, error) {
	registry, err := s.registry(ctx, nil)
	if err != nil {
		return live.Snapshot{}, err
	}
	recorded, err := recordedBindings(ctx, s.db)
	if err != nil {
		return live.Snapshot{}, err
	}
	pipelines, err := s.catalog(registry, recorded)
	if err != nil {
		return live.Snapshot{}, err
	}
	return live.Snapshot{Registry: registry, Catalog: pipelines, Version: version}, nil
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
func (s *capabilitySources) checkBinding(ctx context.Context, binding catalog.Binding) error {
	registry, err := s.registry(ctx, nil)
	if err != nil {
		return err
	}
	recorded, err := recordedBindings(ctx, s.db)
	if err != nil {
		return err
	}
	next := []catalog.Binding{binding}
	for _, other := range recorded {
		if other.Ref != binding.Ref {
			next = append(next, other)
		}
	}
	_, err = s.catalog(registry, next)
	return err
}

// checkPlugin builds the capabilities as they would be with a plugin active, and says why they could
// not be: checked before installing it and before activating it, so that neither leaves the
// dashboard on capabilities it cannot rebuild.
func (s *capabilitySources) checkPlugin(ctx context.Context, plugin capability.Plugin) error {
	registry, err := s.registry(ctx, &plugin)
	if err != nil {
		return err
	}
	recorded, err := recordedBindings(ctx, s.db)
	if err != nil {
		return err
	}
	_, err = s.catalog(registry, recorded)
	return err
}

// checkActivation checks that an installed version can become the active one.
func (s *capabilitySources) checkActivation(ctx context.Context, id int64) error {
	installation, err := s.db.PluginInstallation(ctx, id)
	if err != nil {
		return err
	}
	if installation.State != store.InstallationInstalled {
		return fmt.Errorf("installation %d is %s, not installed", id, installation.State)
	}
	plugin, err := s.installed.plugin(ctx, installation.Object)
	if err != nil {
		return err
	}
	return s.checkPlugin(ctx, plugin.Plugin)
}

// carried lists the plugins whose Tekton definitions stay: the built-in ones and those installed.
func (s *capabilitySources) carried(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.InstalledPlugins(ctx)
	if err != nil {
		return nil, err
	}
	carried := map[string]bool{}
	for _, plugin := range carriedPlugins() {
		carried[plugin.ID] = true
	}
	for _, row := range rows {
		carried[row.PluginID] = true
	}
	return carried, nil
}

// startingCapabilities builds the first snapshot. A recorded binding or an installed plugin that no
// longer holds must not keep the dashboard from starting: it starts on the built-in plugins and the
// catalogue file alone, says so, and the reloader keeps trying.
func startingCapabilities(ctx context.Context, sources *capabilitySources, logger *slog.Logger) (*live.Holder, *live.Reloader, error) {
	version, err := sources.db.CapabilityVersion(ctx)
	if err != nil {
		return nil, nil, err
	}
	first, err := sources.snapshot(ctx, version)
	if err != nil {
		logger.Error("recorded bindings and installed plugins not applied, starting on the built-in plugins and the catalogue file", "error", err)
		registry, regErr := carriedRegistry()
		if regErr != nil {
			return nil, nil, regErr
		}
		pipelines, fileErr := sources.catalog(registry, nil)
		if fileErr != nil {
			return nil, nil, fileErr
		}
		// Built from no version of the counter: the reloader builds again at its first pass.
		first = live.Snapshot{Registry: registry, Catalog: pipelines, Version: -1}
	}
	holder := live.NewHolder(first)
	return holder, live.NewReloader(holder, sources.db.CapabilityVersion, sources.snapshot, capabilityReloadInterval, logger), nil
}

// pluginPuller pulls a plugin as the operator configured: the keys it trusts, read at each
// installation so that a key added or withdrawn applies without a restart, and how registries are
// reached.
func pluginPuller(cfg config.Config) func(ctx context.Context, reference string) (installed.Pulled, error) {
	return func(ctx context.Context, reference string) (installed.Pulled, error) {
		if cfg.Plugins.TrustedKeys == "" {
			return installed.Pulled{}, errors.New("no key trusted to sign plugins: PLUGIN_TRUSTED_KEYS names no file")
		}
		raw, err := os.ReadFile(cfg.Plugins.TrustedKeys)
		if errors.Is(err, fs.ErrNotExist) {
			return installed.Pulled{}, fmt.Errorf("no key trusted to sign plugins: %s is not mounted", cfg.Plugins.TrustedKeys)
		}
		if err != nil {
			return installed.Pulled{}, fmt.Errorf("trusted keys: %w", err)
		}
		trusted, err := installed.ParseTrustedKeys(raw)
		if err != nil {
			return installed.Pulled{}, err
		}
		registries := installed.Registries{PlainHTTP: cfg.Plugins.PlainHTTP}
		// Optional: with no credentials mounted, registries are reached anonymously.
		if _, err := os.Stat(cfg.Plugins.RegistryAuth); cfg.Plugins.RegistryAuth != "" && err == nil {
			registries.DockerConfig = cfg.Plugins.RegistryAuth
		}
		return installed.Pull(ctx, registries, trusted, reference)
	}
}

// capabilityReloadInterval bounds how long an installation or a binding takes to apply.
const capabilityReloadInterval = 30 * time.Second

// capabilityReconciler wires the installation and the check of the bound capabilities.
func capabilityReconciler(
	cluster *tekton.Client, cfg config.Config, inForce *live.Holder, sources *capabilitySources,
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
	return worker.NewCapabilityReconciler(installer, resources, inForce.Catalogs(), cluster, sink, logger).
		WithPruning(sources.carried)
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
				Name: manifest.Name, Kind: manifest.Kind, Manifest: manifest.YAML, Plugin: ref.Plugin,
			})
		}
	}
	return resources, nil
}

// pluginVersions gives, by plugin, the version a registry carries.
func pluginVersions(registry *capabilities.Registry) map[string]string {
	versions := map[string]string{}
	for _, plugin := range registry.Plugins() {
		versions[plugin.ID] = plugin.Version
	}
	return versions
}

// pluginVersionIn reads a plugin's version from the registry in force at the moment of asking.
func pluginVersionIn(registries capabilities.Source) func(plugin string) string {
	return func(plugin string) string {
		found, err := capabilities.Of(registries).Plugin(plugin)
		if err != nil {
			return ""
		}
		return found.Version
	}
}
