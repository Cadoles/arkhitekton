package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/module"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/ui"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	nativeproviders "forge.cadoles.com/vcarroy/arkhitekton/rpc/providers"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// programs runs a role's installed plugins' programs: launched at their first use, stopped when
// their version leaves the capabilities in force.
type programs struct {
	launcher *native.Launcher

	mu    sync.Mutex
	specs map[string]native.Spec
	host  native.HostDeps
	// active are the active versions with a program, by plugin id: what serves routes and hooks.
	active map[string]installed.Plugin
	// composed is the interface with what the active versions bring.
	composed *ui.Interface
	logger   *slog.Logger
}

// newPrograms prepares a role's programs. The environment a program gets is explicit: the
// database it keeps its tables in and, in the worker, the cluster it may read. Not the dashboard's
// keys nor its credentials.
func newPrograms(cfg config.Config, role string, logger *slog.Logger) *programs {
	env := []string{"DATABASE_URL=" + cfg.Database.DSN}
	if role == "worker" {
		for _, name := range []string{"KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT"} {
			if value, set := os.LookupEnv(name); set {
				env = append(env, name+"="+value)
			}
		}
	}
	p := &programs{specs: map[string]native.Spec{}, logger: logger}
	p.launcher = native.New(native.Config{
		Dir: filepath.Join(os.TempDir(), "arkhitekton-plugins"), Role: role, Env: env, Logger: logger,
		Host: func(pluginID string) pluginv1.HostServer {
			p.mu.Lock()
			deps := p.host
			p.mu.Unlock()
			return native.NewHost(deps, pluginID)
		},
	})
	return p
}

// lend sets what the Host service lends programs, once the role has built it. A program launched
// before would be lent nothing: none is, the first launch comes with the first operation.
func (p *programs) lend(deps native.HostDeps) {
	p.mu.Lock()
	p.host = deps
	p.mu.Unlock()
}

func specKey(id, version string) string { return id + "@" + version }

// register records an installed version's program.
func (p *programs) register(plugin installed.Plugin) {
	if plugin.Native == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.specs[specKey(plugin.ID, plugin.Version)] = native.Spec{
		ID: plugin.ID, Version: plugin.Version, Digest: plugin.Native.Digest, Binary: plugin.Native.Program,
	}
}

// process returns the program of an installed version.
func (p *programs) process(id, version string) (*native.Process, error) {
	p.mu.Lock()
	spec, found := p.specs[specKey(id, version)]
	p.mu.Unlock()
	if !found {
		return nil, fmt.Errorf("plugin %s %s: no program installed", id, version)
	}
	return p.launcher.Process(spec), nil
}

// runtime gives what an installed version's program carries out the stand-ins that ask it.
func (p *programs) runtime() installed.Programs {
	return installed.Programs{
		Interpreter: func(plugin capability.Plugin, pipeline string) capability.Interpreter {
			id, version := plugin.ID, plugin.Version
			return native.Interpreter{
				Process: func() (*native.Process, error) { return p.process(id, version) }, Pipeline: pipeline,
			}
		},
		Dial: func(plugin capability.Plugin) nativeproviders.Dial {
			id, version := plugin.ID, plugin.Version
			return func(ctx context.Context) (pluginv1.PluginClient, error) {
				process, err := p.process(id, version)
				if err != nil {
					return nil, err
				}
				connection, err := process.Connection(ctx)
				if err != nil {
					return nil, err
				}
				return connection.Plugin, nil
			}
		},
	}
}

// activate records the active versions, whose programs serve routes and hooks.
func (p *programs) activate(active []installed.Plugin) {
	byID := map[string]installed.Plugin{}
	for _, plugin := range active {
		if plugin.Native != nil {
			byID[plugin.ID] = plugin
		}
	}
	composed, refused := ui.Compose(ui.Core(), contributions(byID), interfaceOptions)
	for _, err := range refused {
		// Checked when installed and when activated: an interface refused here is one that composed
		// then, against plugins since changed.
		p.logger.Error("a plugin's interface is left out", "error", err)
	}
	p.mu.Lock()
	p.active = byID
	if composed != nil {
		p.composed = composed
	}
	p.mu.Unlock()
}

// interfaceOptions are what the interface composes against.
var interfaceOptions = ui.Options{KnownPermission: func(permission string) bool {
	return auth.Known(auth.Permission(permission))
}}

// contributions are what the plugins bring to the interface.
func contributions(plugins map[string]installed.Plugin) []ui.Contribution {
	var brought []ui.Contribution
	for _, plugin := range plugins {
		if plugin.Native != nil && len(plugin.Native.Interface) > 0 {
			brought = append(brought, contributionOf(plugin))
		}
	}
	return brought
}

func contributionOf(plugin installed.Plugin) ui.Contribution {
	return ui.Contribution{Plugin: plugin.ID, Resources: plugin.Native.Interface, Routes: plugin.Native.Manifest.Routes}
}

// Interface is the composed interface.
func (p *programs) Interface() *ui.Interface {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.composed == nil {
		composed, _ := ui.Compose(ui.Core(), nil, interfaceOptions)
		p.composed = composed
	}
	return p.composed
}

// EventLabels are the labels the active versions give the facts they state, by type.
func (p *programs) EventLabels() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	labels := map[string]string{}
	for _, plugin := range p.active {
		for _, event := range plugin.Events {
			labels[plugin.EventType(event)] = event.Label
		}
	}
	return labels
}

// activeProcess returns the program of a plugin's active version.
func (p *programs) activeProcess(pluginID string) (installed.Plugin, *native.Process, bool) {
	p.mu.Lock()
	plugin, found := p.active[pluginID]
	p.mu.Unlock()
	if !found {
		return installed.Plugin{}, nil, false
	}
	process, err := p.process(plugin.ID, plugin.Version)
	if err != nil {
		return installed.Plugin{}, nil, false
	}
	return plugin, process, true
}

// Routes returns the routes the active version of a plugin declares.
func (p *programs) Routes(pluginID string) ([]manifest.Route, bool) {
	plugin, _, found := p.activeProcess(pluginID)
	if !found {
		return nil, false
	}
	return plugin.Native.Manifest.Routes, true
}

// Serve hands a request to the active version's program.
func (p *programs) Serve(ctx context.Context, pluginID string, request *pluginv1.HTTPRequest) (*pluginv1.HTTPResponse, error) {
	_, process, found := p.activeProcess(pluginID)
	if !found {
		return nil, fmt.Errorf("plugin %s: no active program", pluginID)
	}
	conn, err := process.Connection(ctx)
	if err != nil {
		return nil, err
	}
	return conn.Plugin.Serve(ctx, request)
}

// retain keeps the programs of the versions still installed, and stops the others.
func (p *programs) retain(kept []installed.Plugin) {
	p.mu.Lock()
	keep := make([]native.Spec, 0, len(kept))
	for _, plugin := range kept {
		if spec, found := p.specs[specKey(plugin.ID, plugin.Version)]; found {
			keep = append(keep, spec)
		}
	}
	p.mu.Unlock()
	p.launcher.Retain(keep)
}

func (p *programs) close() { p.launcher.Close() }

// unsealer opens a plugin's sensitive settings with the instance's key, by its declaration in the
// registry in force.
func unsealer(registries capabilities.Source, cipher *secrets.Cipher) func(string, map[string]string) (map[string]string, error) {
	return func(pluginID string, sealed map[string]string) (map[string]string, error) {
		descriptor, err := capabilities.Of(registries).Plugin(pluginID)
		if err != nil {
			return nil, err
		}
		return plugin.Unseal(descriptor, cipher, sealed)
	}
}

// pluginMigrations returns an installed plugin's migrations; nil when it brings none.
func pluginMigrations(plugin installed.Plugin) (*store.MigrationSet, error) {
	if plugin.Native == nil || plugin.Native.Manifest.Migrations == nil {
		return nil, nil
	}
	declared := plugin.Native.Manifest.Migrations
	files, err := fs.Sub(plugin.Native.Files, declared.Dir)
	if err != nil {
		return nil, err
	}
	return &store.MigrationSet{Name: plugin.ID, Table: declared.Table, Files: files}, nil
}

// migratePlugin applies a plugin's migrations, as the `migrate` role applies the core's.
func migratePlugin(
	cfg config.DatabaseConfig, logger *slog.Logger,
) func(ctx context.Context, plugin installed.Plugin) error {
	return func(ctx context.Context, plugin installed.Plugin) error {
		migrations, err := pluginMigrations(plugin)
		if err != nil || migrations == nil {
			return err
		}
		return migrations.Migrate(ctx, cfg, logger)
	}
}

// purgePlugin undoes an uninstalled plugin's migrations and drops its version table.
func purgePlugin(
	cfg config.DatabaseConfig, logger *slog.Logger,
) func(ctx context.Context, plugin installed.Plugin) error {
	return func(ctx context.Context, plugin installed.Plugin) error {
		migrations, err := pluginMigrations(plugin)
		if err != nil {
			return err
		}
		if migrations == nil {
			return fmt.Errorf("plugin %s has no migrations to undo", plugin.ID)
		}
		return migrations.Purge(ctx, cfg, logger)
	}
}

// installedSchemas checks, at each probe, the schema of every active installed plugin that has
// one: `schema-<id>`, as for an extension.
func installedSchemas(
	sources *capabilitySources, pool *pgxpool.Pool, logger *slog.Logger,
) func(context.Context) []dashhttp.ReadinessCheck {
	failing := func(name string, err error) dashhttp.ReadinessCheck {
		return dashhttp.ReadinessCheck{Name: name, Check: func(context.Context) error { return err }}
	}
	return func(ctx context.Context) []dashhttp.ReadinessCheck {
		active, _, err := sources.installed.load(ctx)
		if err != nil {
			logger.Error("listing the installed plugins' schemas", "error", err)
			return []dashhttp.ReadinessCheck{failing("schema-plugins", err)}
		}
		var checks []dashhttp.ReadinessCheck
		for _, plugin := range active {
			migrations, err := pluginMigrations(plugin)
			switch {
			case err != nil:
				checks = append(checks, failing("schema-"+plugin.ID, err))
			case migrations != nil:
				checks = append(checks, dashhttp.ReadinessCheck{Name: "schema-" + plugin.ID, Check: migrations.CheckSchema(pool)})
			}
		}
		return checks
	}
}

// nativeModule runs an installed plugin's module in its program: the supervisor's run lasts as
// long as the program's RunModule call.
type nativeModule struct {
	plugin  installed.Plugin
	process *native.Process
}

func (m nativeModule) Plugin() string  { return m.plugin.ID }
func (m nativeModule) Version() string { return m.plugin.Version }

func (m nativeModule) Run(ctx context.Context, state module.State) error {
	conn, err := m.process.Connection(ctx)
	if err != nil {
		return err
	}
	request := &pluginv1.ModuleState{Global: state.Global}
	for _, project := range state.Projects {
		request.Projects = append(request.Projects, &pluginv1.ModuleProject{
			Project: native.ProjectTo(project.Project), Settings: project.Settings,
		})
	}
	stream, err := conn.Plugin.RunModule(ctx, request)
	if err != nil {
		return err
	}
	if _, err = stream.Recv(); errors.Is(err, io.EOF) || ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// modules are the modules of the active versions whose program runs one.
func (p *programs) modules() []module.Module {
	p.mu.Lock()
	active := make([]installed.Plugin, 0, len(p.active))
	for _, plugin := range p.active {
		if plugin.Native.Manifest.Module {
			active = append(active, plugin)
		}
	}
	p.mu.Unlock()
	var modules []module.Module
	for _, plugin := range active {
		if process, err := p.process(plugin.ID, plugin.Version); err == nil {
			modules = append(modules, nativeModule{plugin: plugin, process: process})
		}
	}
	return modules
}

// InterfaceFiles returns the directory of an active version's interface entry.
func (p *programs) InterfaceFiles(pluginID, version string) (fs.FS, bool) {
	p.mu.Lock()
	plugin, found := p.active[pluginID]
	p.mu.Unlock()
	if !found || plugin.Version != version || plugin.Native.Manifest.UI == nil || plugin.Native.Manifest.UI.Entry == "" {
		return nil, false
	}
	files, err := fs.Sub(plugin.Native.Files, path.Dir(plugin.Native.Manifest.UI.Entry))
	return files, err == nil
}

// interfaces lists the interfaces of the active versions, for the browser to load.
func (p *programs) interfaces() []dashhttp.PluginInterface {
	p.mu.Lock()
	defer p.mu.Unlock()
	listed := []dashhttp.PluginInterface{}
	for _, plugin := range p.active {
		declared := plugin.Native.Manifest.UI
		if declared == nil || declared.Entry == "" {
			continue
		}
		dir := path.Dir(declared.Entry)
		file := func(name string) string {
			return dashhttp.InterfacePath(plugin.ID, plugin.Version, strings.TrimPrefix(name, dir+"/"))
		}
		styles := make([]string, 0, len(declared.Styles))
		for _, style := range declared.Styles {
			styles = append(styles, file(style))
		}
		listed = append(listed, dashhttp.PluginInterface{Plugin: plugin.ID, Script: file(declared.Entry), Styles: styles})
	}
	slices.SortFunc(listed, func(a, b dashhttp.PluginInterface) int { return strings.Compare(a.Plugin, b.Plugin) })
	return listed
}
