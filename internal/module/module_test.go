package module

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// switchboard holds a plugin's settings, changed by the test while the supervisor runs.
type switchboard struct {
	mu        sync.Mutex
	enabled   bool
	namespace string
	off       map[int64]bool
}

func (b *switchboard) set(change func(*switchboard)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	change(b)
}

func (b *switchboard) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]store.PluginSettings{"mse": {PluginID: "mse", Enabled: b.enabled,
		Config: map[string]string{"namespace": b.namespace}}}, nil
}

func (b *switchboard) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{{ID: 1, Slug: "mse"}, {ID: 2, Slug: "tekton"}}, nil
}

func (b *switchboard) ResolvedPlugin(_ context.Context, projectID int64, _ string) (store.PluginSettings, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled || b.off[projectID] {
		return store.PluginSettings{}, store.ErrPluginDisabled
	}
	return store.PluginSettings{Enabled: true, Config: map[string]string{"namespace": b.namespace}}, nil
}

// recorder records each start of the module and the projects it was given.
type recorder struct {
	mu     sync.Mutex
	starts [][]int64
	live   int
}

func (r *recorder) Plugin() string { return "mse" }

func (r *recorder) Run(ctx context.Context, state State) error {
	ids := make([]int64, 0, len(state.Projects))
	for _, project := range state.Projects {
		ids = append(ids, project.Project.ID)
	}
	r.mu.Lock()
	r.starts = append(r.starts, ids)
	r.live++
	r.mu.Unlock()
	<-ctx.Done()
	r.mu.Lock()
	r.live--
	r.mu.Unlock()
	return ctx.Err()
}

func (r *recorder) seen() (int, int, []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	last := []int64{}
	if len(r.starts) > 0 {
		last = r.starts[len(r.starts)-1]
	}
	return len(r.starts), r.live, last
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Switched on, off, edited, and on again: the module follows without a restart of the worker.
func TestAModuleFollowsItsSwitchAndItsSettings(t *testing.T) {
	board := &switchboard{namespace: "flux-system", off: map[int64]bool{}}
	module := &recorder{}
	supervisor := NewSupervisor(board, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), module)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- supervisor.Run(ctx) }()

	// Off: nothing runs.
	time.Sleep(30 * time.Millisecond)
	if starts, _, _ := module.seen(); starts != 0 {
		t.Fatalf("started %d time(s) while switched off", starts)
	}

	board.set(func(b *switchboard) { b.enabled = true })
	eventually(t, "started for both projects", func() bool {
		starts, live, last := module.seen()
		return starts == 1 && live == 1 && len(last) == 2
	})

	// A project switches it off: restarted without it.
	board.set(func(b *switchboard) { b.off[2] = true })
	eventually(t, "restarted for one project", func() bool {
		starts, live, last := module.seen()
		return starts == 2 && live == 1 && len(last) == 1 && last[0] == 1
	})

	// A setting edited: restarted with it.
	board.set(func(b *switchboard) { b.namespace = "flux-mse" })
	eventually(t, "restarted with the new setting", func() bool {
		starts, live, _ := module.seen()
		return starts == 3 && live == 1
	})

	// Off on the instance: stopped.
	board.set(func(b *switchboard) { b.enabled = false })
	eventually(t, "stopped", func() bool {
		_, live, _ := module.seen()
		return live == 0
	})

	cancel()
	if err := <-done; err != nil {
		t.Errorf("supervisor: %v", err)
	}
}

type versioned struct {
	*recorder
	version string
}

func (v versioned) Version() string { return v.version }

// An installed plugin's module joins while the worker runs, is replaced by its next version, and
// is stopped once uninstalled.
func TestAnInstalledModuleComesAndGoes(t *testing.T) {
	board := &switchboard{enabled: true, namespace: "flux-system", off: map[int64]bool{}}
	first, second := &recorder{}, &recorder{}
	var mu sync.Mutex
	var installed []Module
	install := func(modules ...Module) {
		mu.Lock()
		defer mu.Unlock()
		installed = modules
	}
	supervisor := NewSupervisor(board, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithModules(func() []Module {
			mu.Lock()
			defer mu.Unlock()
			return installed
		})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- supervisor.Run(ctx) }()

	install(versioned{first, "1.0.0"})
	eventually(t, "the installed module started", func() bool { _, live, _ := first.seen(); return live == 1 })

	install(versioned{second, "1.0.1"})
	eventually(t, "the next version replaced it", func() bool {
		_, firstLive, _ := first.seen()
		_, secondLive, _ := second.seen()
		return firstLive == 0 && secondLive == 1
	})

	install()
	eventually(t, "uninstalled, stopped", func() bool { _, live, _ := second.seen(); return live == 0 })

	cancel()
	if err := <-done; err != nil {
		t.Errorf("supervisor: %v", err)
	}
}
