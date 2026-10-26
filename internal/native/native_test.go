package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// program is the test plugin, built once.
var program []byte

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "native-plugin-")
	if err != nil {
		panic(err)
	}
	output := filepath.Join(dir, "plugin")
	build := exec.Command("go", "build", "-o", output, "./testdata/plugin")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building the test plugin: %v\n%s", err, out))
	}
	if program, err = os.ReadFile(output); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func spec(content []byte) Spec {
	return Spec{ID: "widgets", Version: "1.0.0", Digest: digestOf(program), Binary: func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	}}
}

type projects struct{ HostStore }

func (projects) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "app" {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 7, Slug: "app", Label: "Application"}, nil
}

func launcher(t *testing.T, env ...string) *Launcher {
	t.Helper()
	launcher := New(Config{
		Dir: t.TempDir(), Role: rpc.RoleAPI, Env: env, Backoff: 200 * time.Millisecond,
		Host: func(pluginID string) pluginv1.HostServer { return NewHost(HostDeps{Store: projects{}}, pluginID) },
	})
	t.Cleanup(launcher.Close)
	return launcher
}

func get(ctx context.Context, process *Process, path string) (*pluginv1.HTTPResponse, error) {
	conn, err := process.Connection(ctx)
	if err != nil {
		return nil, err
	}
	return conn.Plugin.Serve(ctx, &pluginv1.HTTPRequest{Method: "GET", Path: path})
}

// The process reaches the dashboard's Host service, and sees only the environment it was given:
// not the dashboard's keys.
func TestAPluginRunsWithItsOwnEnvironment(t *testing.T) {
	t.Setenv("SECRETS_KEY", "never-handed-over")
	process := launcher(t, "DATABASE_URL=postgres://plugin").Process(spec(program))

	response, err := get(context.Background(), process, "hello")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Role, Project string
		Environment   []string
	}
	if err := json.Unmarshal(response.GetBody(), &body); err != nil {
		t.Fatalf("%v: %s", err, response.GetBody())
	}
	if body.Role != rpc.RoleAPI || body.Project != "Application" {
		t.Errorf("body = %+v", body)
	}
	if !slices.Contains(body.Environment, "DATABASE_URL") || slices.Contains(body.Environment, "SECRETS_KEY") {
		t.Errorf("environment = %v", body.Environment)
	}
}

func TestAPluginThatDiesIsLaunchedAgain(t *testing.T) {
	process := launcher(t).Process(spec(program))
	if _, err := get(context.Background(), process, "crash"); err == nil {
		t.Fatal("a crashing call answered")
	}
	// The process takes a moment to be seen gone.
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if _, err = get(context.Background(), process, "hello"); errors.Is(err, ErrRestarting) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !errors.Is(err, ErrRestarting) {
		t.Fatalf("within the backoff: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := get(context.Background(), process, "hello"); err != nil {
		t.Fatalf("after the backoff: %v", err)
	}
}

func TestAPluginOfAnotherProtocolIsRefused(t *testing.T) {
	process := launcher(t, "FAKE_PLUGIN_MODE=old-protocol").Process(spec(program))
	if _, err := process.Connection(context.Background()); err == nil || !strings.Contains(err.Error(), "launching") {
		t.Errorf("err = %v", err)
	}
}

func TestAProgramThatIsNotTheOneInstalledIsRefused(t *testing.T) {
	altered := append(slices.Clone(program), 0)
	process := launcher(t).Process(spec(altered))
	if _, err := process.Connection(context.Background()); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("err = %v", err)
	}
}

// A program altered on disk after it was written is written again from the installed one.
func TestAProgramAlteredOnDiskIsReplaced(t *testing.T) {
	launch := launcher(t)
	process := launch.Process(spec(program))
	if _, err := process.Connection(context.Background()); err != nil {
		t.Fatal(err)
	}
	launch.Close()

	sum := sha256.Sum256(program)
	path := filepath.Join(launch.config.Dir, "widgets", hex.EncodeToString(sum[:]), "plugin")
	// Replaced rather than rewritten: the stopped process may still hold the file it ran.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := get(context.Background(), launch.Process(spec(program)), "hello"); err != nil {
		t.Fatalf("the altered program was not replaced: %v", err)
	}
}

func TestACallIsBoundedByItsContext(t *testing.T) {
	process := launcher(t).Process(spec(program))
	if _, err := process.Connection(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := get(ctx, process, "slow"); status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("err = %v", err)
	}
}

func TestAVersionNoLongerRetainedIsStopped(t *testing.T) {
	launch := launcher(t)
	process := launch.Process(spec(program))
	if _, err := process.Connection(context.Background()); err != nil {
		t.Fatal(err)
	}
	launch.Retain(nil)
	if _, err := process.Connection(context.Background()); err == nil {
		t.Error("a stopped version answered")
	}
}

func TestARunIsReadByTheProgram(t *testing.T) {
	process := launcher(t).Process(spec(program))
	interpreter := Interpreter{Process: func() (*Process, error) { return process, nil }, Pipeline: "count"}
	sets, err := interpreter.Interpret(context.Background(), capability.RunState{Status: "passed"}, []parse.Record{
		{Parser: parse.Raw, Tool: "go", Payload: parse.Bytes("one")},
		{Parser: parse.JUnitXML, Tool: "junit", Err: errors.New("unreadable")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].Summary != "2 record(s)" || sets[0].Status != "passed" {
		t.Errorf("sets = %+v", sets)
	}
	if _, err := (Interpreter{Process: interpreter.Process, Pipeline: "absent"}).Interpret(context.Background(), capability.RunState{}, nil); err == nil {
		t.Error("a pipeline the program does not read answered")
	}
}

// syncBuffer is a log written by the launcher's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// What a program logs on its standard error reaches the dashboard's log, with its level and fields.
func TestAProgramsLogReachesTheDashboards(t *testing.T) {
	logs := &syncBuffer{}
	launch := New(Config{
		Dir: t.TempDir(), Role: rpc.RoleWorker, Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		Host: func(pluginID string) pluginv1.HostServer { return NewHost(HostDeps{Store: projects{}}, pluginID) },
	})
	t.Cleanup(launch.Close)
	if _, err := launch.Process(spec(program)).Connection(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), `"msg":"fake plugin started"`) {
		if time.Now().After(deadline) {
			t.Fatalf("log = %s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), `"plugin":"widgets"`) || !strings.Contains(logs.String(), `"role":"worker"`) {
		t.Errorf("log = %s", logs.String())
	}
}
