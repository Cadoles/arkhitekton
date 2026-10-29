// Package native runs installed plugins as separate programs and talks to them over gRPC
// (hashicorp/go-plugin).
//
// The dashboard trusts a plugin the administrator installed: the process runs as the dashboard's
// user, in its pod, bounded by its Kubernetes RBAC and nothing else. What this package guarantees is
// that what runs is what was installed (the binary's digest is checked before every launch), that
// the process gets an explicit environment rather than the dashboard's, and that a plugin that dies
// is launched again without the dashboard going down with it.
package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
)

// Spec names one installed version of a plugin's program.
type Spec struct {
	ID      string
	Version string
	// Digest is the binary's, `sha256:<hex>`: what was verified at installation, and what is
	// checked again before each launch.
	Digest string
	// Binary opens the program, when it is not yet on disk.
	Binary func() (io.ReadCloser, error)
}

func (s Spec) key() string { return s.ID + "@" + s.Digest }

// Config is how a role launches its plugins.
type Config struct {
	// Dir is where programs are written, one directory per plugin and digest. It must allow
	// execution: a filesystem mounted `noexec` refuses every launch.
	Dir  string
	Role string
	// Env is the whole environment of the process: the dashboard's own, with its keys and
	// credentials, is not inherited.
	Env []string
	// Host is the service the dashboard lends a plugin.
	Host   func(pluginID string) pluginv1.HostServer
	Logger *slog.Logger
	// StartTimeout bounds the handshake and the plugin's initialisation.
	StartTimeout time.Duration
	// Backoff is the time a plugin that died waits before it is launched again.
	Backoff time.Duration
}

// ErrRestarting says a plugin died and waits before being launched again.
var ErrRestarting = errors.New("the plugin stopped and is being restarted")

// Launcher keeps a role's plugin processes.
type Launcher struct {
	config Config

	mu        sync.Mutex
	processes map[string]*Process
}

// New returns a launcher; it starts nothing until a process is asked for.
func New(config Config) *Launcher {
	if config.StartTimeout == 0 {
		config.StartTimeout = 30 * time.Second
	}
	if config.Backoff == 0 {
		config.Backoff = 5 * time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.DiscardHandler)
	}
	return &Launcher{config: config, processes: map[string]*Process{}}
}

// Process returns the process of a plugin's version, launched at its first use.
func (l *Launcher) Process(spec Spec) *Process {
	l.mu.Lock()
	defer l.mu.Unlock()
	if process, found := l.processes[spec.key()]; found {
		return process
	}
	process := &Process{spec: spec, launcher: l}
	l.processes[spec.key()] = process
	return process
}

// Retain stops the processes of every version not kept: a plugin uninstalled, a version replaced.
func (l *Launcher) Retain(keep []Spec) {
	kept := make(map[string]bool, len(keep))
	for _, spec := range keep {
		kept[spec.key()] = true
	}
	l.mu.Lock()
	var stopped []*Process
	for key, process := range l.processes {
		if !kept[key] {
			stopped = append(stopped, process)
			delete(l.processes, key)
		}
	}
	l.mu.Unlock()
	for _, process := range stopped {
		process.Stop()
	}
}

// Close stops every process.
func (l *Launcher) Close() { l.Retain(nil) }

// Process is one plugin version's program.
type Process struct {
	spec     Spec
	launcher *Launcher

	mu        sync.Mutex
	client    *goplugin.Client
	conn      *rpc.Connection
	stopped   bool
	lastStart time.Time
	lastError error
}

// Connection returns the running plugin, launching it first if needed, or again if it died.
func (p *Process) Connection(ctx context.Context) (*rpc.Connection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return nil, fmt.Errorf("plugin %s: stopped", p.spec.ID)
	}
	if p.client != nil && !p.client.Exited() {
		return p.conn, nil
	}
	if p.client != nil {
		p.launcher.config.Logger.Warn("plugin process exited", "plugin", p.spec.ID, "version", p.spec.Version)
		p.client, p.conn = nil, nil
	}
	if !p.lastStart.IsZero() && time.Since(p.lastStart) < p.launcher.config.Backoff {
		return nil, fmt.Errorf("plugin %s: %w (%v)", p.spec.ID, ErrRestarting, p.lastError)
	}
	p.lastStart = time.Now()
	client, conn, err := p.launch(ctx)
	p.lastError = err
	if err != nil {
		return nil, fmt.Errorf("plugin %s: %w", p.spec.ID, err)
	}
	p.client, p.conn = client, conn
	return conn, nil
}

// Stop ends the process; calls in flight fail.
func (p *Process) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	if p.client != nil {
		p.client.Kill()
		p.client, p.conn = nil, nil
	}
}

func (p *Process) launch(ctx context.Context) (*goplugin.Client, *rpc.Connection, error) {
	config := p.launcher.config
	path, sum, err := p.install()
	if err != nil {
		return nil, nil, err
	}
	command := exec.Command(path)
	command.Env = append([]string(nil), config.Env...)
	command.Dir = filepath.Dir(path)

	logger := config.Logger.With("plugin", p.spec.ID, "version", p.spec.Version)
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  rpc.Handshake,
		Plugins:          rpc.PluginSet(&rpc.GRPCPlugin{}),
		Cmd:              command,
		SkipHostEnv:      true,
		SecureConfig:     &goplugin.SecureConfig{Checksum: sum, Hash: sha256.New()},
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		GRPCDialOptions:  rpc.DialOptions(),
		AutoMTLS:         true,
		StartTimeout:     config.StartTimeout,
		Logger:           newHCLogger(logger),
		// What the program writes before go-plugin takes its output over, and what it writes after,
		// through go-plugin's stdio stream: both are its log.
		Stderr:     &lineWriter{logger: logger},
		SyncStderr: &lineWriter{logger: logger},
		SyncStdout: &lineWriter{logger: logger},
	})
	fail := func(err error) (*goplugin.Client, *rpc.Connection, error) {
		client.Kill()
		return nil, nil, err
	}
	protocol, err := client.Client()
	if err != nil {
		return fail(fmt.Errorf("launching: %w", err))
	}
	raw, err := protocol.Dispense(rpc.Name)
	if err != nil {
		return fail(fmt.Errorf("connecting: %w", err))
	}
	conn := raw.(*rpc.Connection)
	initCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.StartTimeout)
	defer cancel()
	if err := conn.Init(initCtx, config.Host(p.spec.ID), config.Role, p.spec.ID); err != nil {
		return fail(fmt.Errorf("initialising: %w", err))
	}
	logger.Info("plugin process started", "role", config.Role)
	return client, conn, nil
}

// install writes the program where it runs from, unless the same one is already there.
func (p *Process) install() (string, []byte, error) {
	algorithm, encoded, found := strings.Cut(p.spec.Digest, ":")
	if !found || algorithm != "sha256" {
		return "", nil, fmt.Errorf("digest %q: sha256 only", p.spec.Digest)
	}
	sum, err := hex.DecodeString(encoded)
	if err != nil || len(sum) != sha256.Size {
		return "", nil, fmt.Errorf("digest %q: not a sha256", p.spec.Digest)
	}
	dir := filepath.Join(p.launcher.config.Dir, p.spec.ID, encoded)
	path := filepath.Join(dir, "plugin")
	if existing, err := fileSum(path); err == nil && hex.EncodeToString(existing) == encoded {
		return path, sum, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	source, err := p.spec.Binary()
	if err != nil {
		return "", nil, fmt.Errorf("opening the program: %w", err)
	}
	defer func() { _ = source.Close() }()
	temporary, err := os.CreateTemp(dir, "plugin-*")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), source); err != nil {
		_ = temporary.Close()
		return "", nil, fmt.Errorf("writing the program: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", nil, err
	}
	if written := hex.EncodeToString(hash.Sum(nil)); written != encoded {
		return "", nil, fmt.Errorf("the program's digest is sha256:%s, not %s", written, p.spec.Digest)
	}
	if err := os.Chmod(temporary.Name(), 0o755); err != nil {
		return "", nil, err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return "", nil, err
	}
	return path, sum, nil
}

func fileSum(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}
