// Package module runs the parts of plugins the dashboard carries out on their behalf.
//
// A contract plugin only reads and reports. Some plugins also need loops that write the dashboard's
// own tables and talk to the cluster, like observing deployment environments: those are modules,
// written against the host and assembled by the app package next to the plugin that declares them.
//
// A module runs while its plugin is switched on, and is restarted with its new settings when they
// change: switching a module off or on, or editing its settings on the administration screen,
// takes effect within one supervision tick, without restarting the worker.
package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// Module is a plugin's loop run by the dashboard.
type Module interface {
	// Plugin is the identifier of the plugin that declares the module.
	Plugin() string
	// Run runs the module with these settings until the context ends. It is cancelled and called
	// again when they change. An error stops it until the next tick, where it is started again.
	Run(ctx context.Context, state State) error
}

// State is what a module runs with: its plugin's settings, and the projects it is switched on for.
type State struct {
	Global map[string]string
	// Projects are the projects the module applies to, with their resolved settings.
	Projects []Project
}

// Project is one project the module applies to.
type Project struct {
	Project  store.Project
	Settings map[string]string
}

// Store is what the supervisor reads to decide what runs.
type Store interface {
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	ListProjects(ctx context.Context) ([]store.Project, error)
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
}

// Versioned is a module that changes with its plugin's version: a new version is another module,
// the previous one stopped.
type Versioned interface {
	Version() string
}

// Supervisor starts, stops and restarts the modules as their settings say.
type Supervisor struct {
	modules []Module
	// more are the modules that come and go while the worker runs: those of installed plugins.
	more     func() []Module
	store    Store
	interval time.Duration
	logger   *slog.Logger
}

// WithModules adds modules read at each tick: a module that leaves the list is stopped, one that
// joins it started.
func (s *Supervisor) WithModules(more func() []Module) *Supervisor {
	s.more = more
	return s
}

// key tells a module from another across ticks.
func key(module Module) string {
	if versioned, ok := module.(Versioned); ok {
		return module.Plugin() + "@" + versioned.Version()
	}
	return module.Plugin()
}

func NewSupervisor(db Store, interval time.Duration, logger *slog.Logger, modules ...Module) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{modules: modules, store: db, interval: interval, logger: logger}
}

// running is a module started with a given state.
type running struct {
	fingerprint string
	cancel      context.CancelFunc
	done        chan struct{}
}

// Run supervises the modules until the context ends, then stops them.
func (s *Supervisor) Run(ctx context.Context) error {
	if len(s.modules) == 0 && s.more == nil {
		<-ctx.Done()
		return nil
	}
	current := map[string]*running{}
	defer func() {
		for _, entry := range current {
			stop(entry)
		}
	}()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		modules := s.modules
		if s.more != nil {
			modules = append(slices.Clone(modules), s.more()...)
		}
		seen := map[string]bool{}
		for _, module := range modules {
			id := key(module)
			seen[id] = true
			if entry := s.reconcile(ctx, module, current[id]); entry != nil {
				current[id] = entry
			} else {
				delete(current, id)
			}
		}
		for id, entry := range current {
			if !seen[id] {
				stop(entry)
				delete(current, id)
				s.logger.Info("module stopped", "module", id, "reason", "no longer installed")
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// reconcile brings one module in line with its settings.
func (s *Supervisor) reconcile(ctx context.Context, module Module, entry *running) *running {
	state, err := s.state(ctx, module.Plugin())
	if err != nil {
		// Unreadable settings change nothing: a module that runs keeps running rather than stopping
		// on a database hiccup.
		s.logger.Error("reading a module's settings", "module", module.Plugin(), "error", err)
		return entry
	}

	fingerprint := ""
	if len(state.Projects) > 0 {
		fingerprint = digest(state)
	}
	if entry != nil && entry.fingerprint == fingerprint && !finished(entry) {
		return entry
	}

	if entry != nil {
		stop(entry)
		s.logger.Info("module stopped", "module", module.Plugin())
	}
	if fingerprint == "" {
		return nil
	}

	moduleCtx, cancel := context.WithCancel(ctx)
	started := &running{fingerprint: fingerprint, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(started.done)
		if err := module.Run(moduleCtx, state); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Error("module failed, restarted at the next tick", "module", module.Plugin(), "error", err)
		}
	}()
	s.logger.Info("module started", "module", module.Plugin(), "projects", len(state.Projects))
	return started
}

// state reads a module's settings and the projects it is switched on for. Empty projects mean the
// module does not run.
func (s *Supervisor) state(ctx context.Context, pluginID string) (State, error) {
	all, err := s.store.PluginSettingsAll(ctx)
	if err != nil {
		return State{}, err
	}
	global, found := all[pluginID]
	if !found || !global.Enabled {
		return State{}, nil
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return State{}, err
	}
	state := State{Global: global.Config}
	for _, project := range projects {
		resolved, err := s.store.ResolvedPlugin(ctx, project.ID, pluginID)
		if errors.Is(err, store.ErrPluginDisabled) {
			continue
		}
		if err != nil {
			return State{}, err
		}
		state.Projects = append(state.Projects, Project{Project: project, Settings: resolved.Config})
	}
	sort.Slice(state.Projects, func(i, j int) bool { return state.Projects[i].Project.ID < state.Projects[j].Project.ID })
	return state, nil
}

// digest identifies a state: the same settings for the same projects give the same digest. Only
// what a module is given to run counts: a project row updated for another reason restarts nothing.
func digest(state State) string {
	type entry struct {
		ID       int64
		Slug     string
		Settings map[string]string
	}
	entries := make([]entry, 0, len(state.Projects))
	for _, project := range state.Projects {
		entries = append(entries, entry{ID: project.Project.ID, Slug: project.Project.Slug, Settings: project.Settings})
	}
	raw, _ := json.Marshal(struct {
		Global   map[string]string
		Projects []entry
	}{state.Global, entries})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func finished(entry *running) bool {
	select {
	case <-entry.done:
		return true
	default:
		return false
	}
}

func stop(entry *running) {
	if entry == nil {
		return
	}
	entry.cancel()
	<-entry.done
}
