package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * A native plugin's tracker, forge, probe and actions, against the compiled ones.
 *
 * The program of testdata/providers serves the core's Redmine and Gitea code over gRPC. Both paths
 * read the same fake instances, and must read them alike: the native path adds a wire, never a
 * difference.
 */

// providersServer is a Redmine and a Gitea in one server, recording what is written to it.
type providersServer struct {
	*httptest.Server

	mu      sync.Mutex
	written []string
	// hold blocks the versions of the `slow` project until it is closed.
	hold chan struct{}
}

func newProvidersServer(t *testing.T) *providersServer {
	t.Helper()
	fixture := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join("..", "internal", "plugins", "redmine", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	merged := "2026-10-20T10:00:00Z"
	opened := "2026-10-27T09:30:00Z"
	routes := map[string]any{
		"/projects/mse-dev/versions.json": json.RawMessage(fixture("versions.json")),
		"/issues.json":                    json.RawMessage(fixture("issues_page1.json")),
		"/issues.json?offset=5":           json.RawMessage(fixture("issues_page2.json")),
		"/users/current.json":             map[string]any{"user": map[string]any{"id": 4, "login": "bot", "admin": false}},
		"/api/v1/repos/team/app":          map[string]any{"default_branch": "develop"},
		"/api/v1/repos/team/app/branches": []any{map[string]any{"name": "develop", "commit": map[string]any{"id": "c1"}}},
		"/api/v1/repos/team/app/tags":     []any{map[string]any{"name": "v1", "commit": map[string]any{"sha": "c0"}}},
		"/api/v1/repos/team/app/commits":  []any{map[string]any{"sha": "c1"}},
		"/api/v1/user":                    map[string]any{"login": "forge-bot", "is_admin": true},
		"/api/v1/repos/team/app/pulls?closed": []any{
			map[string]any{"number": 42, "title": "Feature #9001", "body": "", "merged": true, "merged_at": merged},
			map[string]any{"number": 41, "title": "Abandoned", "merged": false},
		},
		"/api/v1/repos/team/app/pulls?open": []any{map[string]any{
			"number": 43, "title": "Next", "html_url": "https://forge.test/team/app/pulls/43",
			"user": map[string]any{"login": "alice"}, "head": map[string]any{"ref": "feature"},
			"base": map[string]any{"ref": "develop"}, "created_at": opened,
		}},
		"/api/v1/repos/team/app/pulls/42/commits": []any{map[string]any{"commit": map[string]any{"message": "feat: refs #9001"}}},
		"/api/v1/repos/team/other":                map[string]any{"default_branch": "main"},
		"/api/v1/repos/team/other/keys":           []any{map[string]any{"key": "ssh-ed25519 AAAA", "title": "dashboard"}},
	}
	server := &providersServer{hold: make(chan struct{})}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		switch {
		case r.Method != http.MethodGet:
			body, _ := io.ReadAll(r.Body)
			server.mu.Lock()
			server.written = append(server.written, r.Method+" "+r.URL.Path+" "+string(body))
			server.mu.Unlock()
			if strings.Contains(string(body), `"tag_name":"v1"`) {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
			return
		case strings.HasSuffix(key, "/slow/versions.json"):
			<-server.hold
		case strings.HasSuffix(key, "/pulls"):
			key += "?" + r.URL.Query().Get("state")
		case key == "/issues.json" && r.URL.Query().Get("offset") != "0":
			key += "?offset=" + r.URL.Query().Get("offset")
		}
		if (strings.HasSuffix(key, "/branches") || strings.HasSuffix(key, "/tags") || strings.HasSuffix(key, "/keys") ||
			strings.Contains(key, "/pulls")) && r.URL.Query().Get("page") > "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		payload, found := routes[key]
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(func() {
		server.release()
		server.Close()
	})
	return server
}

func (s *providersServer) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.hold:
	default:
		close(s.hold)
	}
}

func (s *providersServer) writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	written := s.written
	s.written = nil
	return written
}

// providersPlugin builds the test program and reads it as an installed plugin, its tracker, forge,
// probe and executor asking the program.
func providersPlugin(t *testing.T) (installed.Plugin, *programs) {
	t.Helper()
	program := filepath.Join(t.TempDir(), "plugin")
	build := exec.Command("go", "build", "-o", program, "./testdata/providers")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	files := fstest.MapFS{}
	for name, path := range map[string]string{
		"bin/plugin":                program,
		"arkhitekton-plugin.yaml":   "testdata/providers/arkhitekton-plugin.yaml",
		"migrations/0001_notes.sql": "testdata/providers/migrations/0001_notes.sql",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: raw, Mode: 0o755}
	}
	programs := newPrograms(config.Config{}, rpc.RoleWorker, slog.New(slog.DiscardHandler))
	t.Cleanup(programs.close)
	programs.lend(native.HostDeps{})
	plugin, err := installed.Read(files, programs.runtime())
	if err != nil {
		t.Fatal(err)
	}
	programs.register(plugin)
	return plugin, programs
}

func compiled(t *testing.T, id string) capability.Plugin {
	t.Helper()
	for _, plugin := range builtin.Plugins() {
		if plugin.ID == id {
			return plugin
		}
	}
	t.Fatalf("no built-in plugin %s", id)
	return capability.Plugin{}
}

// same calls a read on both paths and wants the same answer.
func same[R any](t *testing.T, what string, read func(native bool) (R, error)) R {
	t.Helper()
	want, wantErr := read(false)
	got, gotErr := read(true)
	if wantErr != nil || gotErr != nil {
		t.Fatalf("%s: compiled %v, native %v", what, wantErr, gotErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\nnative   %+v\ncompiled %+v", what, got, want)
	}
	return got
}

func TestANativeTrackerReadsAsTheCompiledOne(t *testing.T) {
	server := newProvidersServer(t)
	plugin, _ := providersPlugin(t)
	redmine := compiled(t, "redmine")
	ctx := context.Background()

	settings := func(native bool) capability.Settings {
		if native {
			return capability.Settings{"tracker_url": server.URL + "/", "tracker_api_key": "key", "project": "mse-dev"}
		}
		return capability.Settings{"url": server.URL + "/", "api_key": "key", "project": "mse-dev"}
	}
	connect := func(native bool) (provider.Tracker, error) {
		target := capability.Target{Project: "app", Settings: settings(native), Timeout: 5 * time.Second}
		if native {
			return plugin.Tracker.Connect(target)
		}
		return redmine.Tracker.Connect(target)
	}
	trackers := map[bool]provider.Tracker{}
	for _, native := range []bool{false, true} {
		tracker, err := connect(native)
		if err != nil {
			t.Fatal(err)
		}
		trackers[native] = tracker
	}

	versions := same(t, "versions", func(native bool) ([]provider.Version, error) {
		return trackers[native].ListVersions(ctx, "mse-dev")
	})
	issues := same(t, "issues", func(native bool) ([]provider.Issue, error) {
		return trackers[native].ListIssues(ctx, provider.IssueQuery{Project: "mse-dev"})
	})
	same(t, "account", func(native bool) (provider.Account, error) { return trackers[native].CheckAccess(ctx) })
	if len(versions) == 0 || len(issues) == 0 {
		t.Fatalf("the fixtures read nothing: %d versions, %d issues", len(versions), len(issues))
	}

	links := same(t, "links", func(native bool) ([]string, error) {
		capability := redmine.Tracker
		if native {
			capability = plugin.Tracker
		}
		return []string{capability.VersionURL(settings(native), "805"), capability.IssueURL(settings(native), "9001")}, nil
	})
	if links[0] != server.URL+"/versions/805" {
		t.Errorf("links = %v", links)
	}

	if _, err := plugin.Tracker.Connect(capability.Target{Project: "app"}); !errors.Is(err, provider.ErrNotConfigured) ||
		!strings.Contains(err.Error(), "providers-demo") {
		t.Errorf("an unconfigured native tracker answered %v", err)
	}
}

func TestANativeForgeReadsAndWritesAsTheCompiledOne(t *testing.T) {
	server := newProvidersServer(t)
	plugin, _ := providersPlugin(t)
	gitea := compiled(t, "gitea")
	ctx := context.Background()

	target := func(native bool) capability.Target {
		settings := capability.Settings{"url": server.URL, "token": "token"}
		if native {
			settings = capability.Settings{"forge_url": server.URL, "forge_token": "token"}
		}
		return capability.Target{Project: "app", Repository: "team/app", Settings: settings, Timeout: 5 * time.Second}
	}
	capabilityOf := func(native bool) capability.ForgeCapability {
		if native {
			return plugin.Forge
		}
		return gitea.Forge
	}
	forges := map[bool]provider.Forge{}
	for _, native := range []bool{false, true} {
		forge, err := capabilityOf(native).Connect(target(native))
		if err != nil {
			t.Fatal(err)
		}
		forges[native] = forge
	}

	same(t, "default branch", func(native bool) (string, error) { return forges[native].DefaultBranch(ctx) })
	same(t, "branches", func(native bool) ([]provider.Ref, error) {
		refs, _, err := forges[native].ListBranches(ctx)
		return refs, err
	})
	same(t, "tags", func(native bool) ([]provider.Ref, error) {
		refs, _, err := forges[native].ListTags(ctx)
		return refs, err
	})
	same(t, "commit", func(native bool) (string, error) { return forges[native].ResolveCommit(ctx, "develop") })
	pulls := same(t, "merged pull requests", func(native bool) ([]provider.PullRequest, error) {
		return forges[native].ListMergedPullRequests(ctx, time.Time{})
	})
	if len(pulls) != 1 {
		t.Fatalf("pulls = %+v", pulls)
	}
	same(t, "commit messages", func(native bool) (provider.PullRequest, error) {
		pull := pulls[0]
		err := forges[native].LoadCommitMessages(ctx, &pull)
		return pull, err
	})
	same(t, "account", func(native bool) (provider.Account, error) { return forges[native].CheckAccess(ctx) })
	same(t, "repository probe", func(native bool) (provider.Probe, error) {
		prober, err := capabilityOf(native).Prober(target(native))
		if err != nil {
			return provider.Probe{}, err
		}
		return prober.Probe(ctx, "team/other", "ssh-ed25519 AAAA")
	})

	tagged := same(t, "tags written", func(native bool) ([]string, error) {
		writer, err := capabilityOf(native).Writer(target(native))
		if err != nil {
			return nil, err
		}
		if err := writer.CreateTag(ctx, "v2", "c1", "release"); err != nil {
			return nil, err
		}
		if err := writer.CreateTag(ctx, "v1", "c0", "release"); !errors.Is(err, provider.ErrTagExists) {
			return nil, fmt.Errorf("a tag already set answered %v", err)
		}
		return server.writes(), nil
	})
	if len(tagged) != 2 {
		t.Errorf("written = %v", tagged)
	}

	same(t, "actions", func(native bool) ([]string, error) {
		executor := gitea.Executor
		if native {
			executor = plugin.Executor
		}
		if err := executor.Execute(ctx, target(native), "status", map[string]string{
			"commit": "c1", "state": "success", "context": "arkhitekton", "description": "ok",
		}); err != nil {
			return nil, err
		}
		if err := executor.Execute(ctx, target(native), "comment", map[string]string{"number": "43", "body": "Merci"}); err != nil {
			return nil, err
		}
		return server.writes(), nil
	})
}

func TestANativeProbeObservesAsTheCompiledOne(t *testing.T) {
	server := newProvidersServer(t)
	plugin, _ := providersPlugin(t)
	gitea := compiled(t, "gitea")

	if len(plugin.Probes) != 1 || plugin.Probes[0].Name() != "pull_requests" {
		t.Fatalf("probes = %v", plugin.Probes)
	}
	type pass struct {
		Observations []capability.Observation
		Cursor       string
	}
	observed := same(t, "pull requests opened", func(native bool) (pass, error) {
		probe, settings := gitea.Probes[0], capability.Settings{"url": server.URL, "token": "token"}
		if native {
			probe, settings = plugin.Probes[0], capability.Settings{"forge_url": server.URL, "forge_token": "token"}
		}
		observations, cursor, err := probe.Poll(context.Background(), capability.Target{
			Project: "app", Repository: "team/app", Settings: settings, Timeout: 5 * time.Second,
		}, "2026-10-01T00:00:00Z")
		return pass{observations, cursor}, err
	})
	if len(observed.Observations) != 1 || observed.Observations[0].Payload["author"] != "alice" {
		t.Errorf("observed = %+v", observed)
	}
}

// A program stopped during a call fails that call, and the error names the plugin.
func TestAProgramStoppedDuringACallFailsItNamed(t *testing.T) {
	server := newProvidersServer(t)
	plugin, programs := providersPlugin(t)
	tracker, err := plugin.Tracker.Connect(capability.Target{
		Project: "app", Settings: capability.Settings{"tracker_url": server.URL, "tracker_api_key": "key"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	process, err := programs.process(plugin.ID, plugin.Version)
	if err != nil {
		t.Fatal(err)
	}

	failed := make(chan error, 1)
	go func() {
		_, err := tracker.ListVersions(context.Background(), "slow")
		failed <- err
	}()
	time.Sleep(200 * time.Millisecond)
	process.Stop()

	select {
	case err := <-failed:
		if err == nil || !strings.Contains(err.Error(), "providers-demo") {
			t.Errorf("the call answered %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call outlived its program")
	}
}
