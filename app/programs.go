package app

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// programs runs a role's installed plugins' programs: launched at their first use, stopped when
// their version leaves the capabilities in force.
type programs struct {
	launcher *native.Launcher

	mu    sync.Mutex
	specs map[string]native.Spec
	host  native.HostDeps
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
	p := &programs{specs: map[string]native.Spec{}}
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

// interpreters gives the pipelines a program reads their interpreter.
func (p *programs) interpreters() installed.Interpreters {
	return func(plugin capability.Plugin, pipeline string) capability.Interpreter {
		id, version := plugin.ID, plugin.Version
		return native.Interpreter{
			Process: func() (*native.Process, error) { return p.process(id, version) }, Pipeline: pipeline,
		}
	}
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
