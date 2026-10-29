package providers_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/extension"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/providers"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

var due = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

// fakeTracker answers from its settings: no `url`, not configured.
type fakeTracker struct{ connects *int }

func (t fakeTracker) Connect(target capability.Target) (provider.Tracker, error) {
	*t.connects++
	if target.Settings["url"] == "" {
		return nil, fmt.Errorf("tracker: %w", provider.ErrNotConfigured)
	}
	return fakeTrackerClient{project: target.Project}, nil
}

func (fakeTracker) VersionURL(settings capability.Settings, id string) string {
	if settings["url"] == "" {
		return ""
	}
	return settings["url"] + "/versions/" + id
}

func (fakeTracker) IssueURL(settings capability.Settings, id string) string {
	return settings["url"] + "/issues/" + id
}

type fakeTrackerClient struct{ project string }

func (c fakeTrackerClient) ListVersions(_ context.Context, project string) ([]provider.Version, error) {
	return []provider.Version{{ID: "12", Name: project + " 3.4", Status: "open", DueDate: &due}}, nil
}

func (fakeTrackerClient) ListIssues(_ context.Context, query provider.IssueQuery) ([]provider.Issue, error) {
	if !query.Since.Equal(due) {
		return nil, fmt.Errorf("since = %v", query.Since)
	}
	version := "12"
	return []provider.Issue{{ID: "MSE-1", VersionID: &version, Subject: "Fix", StatusClosed: true}}, nil
}

func (c fakeTrackerClient) CheckAccess(context.Context) (provider.Account, error) {
	return provider.Account{Login: "bot@" + c.project}, nil
}

// fakeForge writes only with a `write` setting, and refuses a tag already set.
type fakeForge struct{}

func (fakeForge) Connect(target capability.Target) (provider.Forge, error) {
	return fakeForgeClient{repository: target.Repository}, nil
}

func (fakeForge) Writer(target capability.Target) (provider.TagWriter, error) {
	if target.Settings["write"] == "" {
		return nil, nil
	}
	return fakeForgeClient{repository: target.Repository}, nil
}

func (fakeForge) Prober(capability.Target) (provider.RepositoryProber, error) {
	return fakeForgeClient{}, nil
}

type fakeForgeClient struct{ repository string }

func (c fakeForgeClient) DefaultBranch(context.Context) (string, error) {
	return "main@" + c.repository, nil
}

func (fakeForgeClient) ListBranches(context.Context) ([]provider.Ref, bool, error) {
	return []provider.Ref{{Name: "main", SHA: "abc"}}, true, nil
}

func (fakeForgeClient) ListTags(context.Context) ([]provider.Ref, bool, error) {
	return nil, false, nil
}

func (fakeForgeClient) ResolveCommit(_ context.Context, ref string) (string, error) {
	return "sha-of-" + ref, nil
}

func (fakeForgeClient) ListMergedPullRequests(_ context.Context, since time.Time) ([]provider.PullRequest, error) {
	return []provider.PullRequest{{Number: 42, Title: "Feature", MergedAt: &since}}, nil
}

func (fakeForgeClient) LoadCommitMessages(_ context.Context, pull *provider.PullRequest) error {
	pull.Commits = []string{"feat: one"}
	return nil
}

func (fakeForgeClient) CheckAccess(context.Context) (provider.Account, error) {
	return provider.Account{ID: 3, Login: "forge-bot", Admin: true}, nil
}

func (fakeForgeClient) CreateTag(_ context.Context, tag, _, _ string) error {
	if tag == "v1" {
		return fmt.Errorf("tag %s: %w", tag, provider.ErrTagExists)
	}
	return nil
}

func (fakeForgeClient) Probe(_ context.Context, repository, publicKey string) (provider.Probe, error) {
	present := publicKey != ""
	return provider.Probe{DefaultBranch: "main", KeyPresent: &present, KeyNote: repository}, nil
}

type fakeProbe struct{}

func (fakeProbe) Name() string                  { return "pulls" }
func (fakeProbe) Schedule() capability.Schedule { return capability.Schedule{} }

func (fakeProbe) Poll(_ context.Context, target capability.Target, cursor string) ([]capability.Observation, string, error) {
	if cursor == "boom" {
		panic("boom")
	}
	return []capability.Observation{{
		Suffix: "pull_request.opened", Subject: target.Repository, Payload: map[string]string{"number": "7"},
		OccurredAt: due, Key: "7",
	}}, cursor + "+1", nil
}

type fakeExecutor struct{ done *[]string }

func (e fakeExecutor) Execute(_ context.Context, target capability.Target, suffix string, params map[string]string) error {
	if suffix == "fail" {
		return errors.New("refused by the forge")
	}
	*e.done = append(*e.done, suffix+" "+params["commit"]+" "+target.Repository)
	return nil
}

type plugin struct {
	connects int
	executed []string
	client   pluginv1.PluginClient
}

func (p *plugin) dial(context.Context) (pluginv1.PluginClient, error) { return p.client, nil }

func serve(t *testing.T) *plugin {
	t.Helper()
	served := &plugin{}
	start := func(context.Context, extension.Runtime) (extension.Hooks, error) {
		return extension.Hooks{
			Tracker: fakeTracker{connects: &served.connects}, Forge: fakeForge{},
			Probes: []capability.Probe{fakeProbe{}}, Executor: fakeExecutor{done: &served.executed},
		}, nil
	}
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		rpc.Name: &rpc.GRPCPlugin{Implementation: func(broker *goplugin.GRPCBroker) pluginv1.PluginServer {
			return extension.Server(start, broker)
		}},
	})
	t.Cleanup(func() { _ = client.Close() })
	raw, err := client.Dispense(rpc.Name)
	if err != nil {
		t.Fatal(err)
	}
	connection := raw.(*rpc.Connection)
	if err := connection.Init(context.Background(), pluginv1.UnimplementedHostServer{}, rpc.RoleWorker, "fake"); err != nil {
		t.Fatal(err)
	}
	served.client = connection.Plugin
	return served
}

func target(settings capability.Settings) capability.Target {
	return capability.Target{Project: "app", Repository: "team/app", Settings: settings, Timeout: time.Second}
}

func TestATrackerIsReadThroughTheProgram(t *testing.T) {
	served := serve(t)
	tracker := &providers.Tracker{Dial: served.dial}

	if _, err := tracker.Connect(target(nil)); !errors.Is(err, provider.ErrNotConfigured) {
		t.Fatalf("an unconfigured tracker answered %v", err)
	}
	client, err := tracker.Connect(target(capability.Settings{"url": "https://tracker.test"}))
	if err != nil {
		t.Fatal(err)
	}
	versions, err := client.ListVersions(context.Background(), "mse")
	if err != nil || len(versions) != 1 || versions[0].Name != "mse 3.4" || !versions[0].DueDate.Equal(due) {
		t.Errorf("versions = %+v, %v", versions, err)
	}
	issues, err := client.ListIssues(context.Background(), provider.IssueQuery{Project: "mse", Since: due})
	if err != nil || len(issues) != 1 || *issues[0].VersionID != "12" || !issues[0].StatusClosed {
		t.Errorf("issues = %+v, %v", issues, err)
	}
	account, err := client.CheckAccess(context.Background())
	if err != nil || account.Login != "bot@app" {
		t.Errorf("account = %+v, %v", account, err)
	}
	// One connection for the target, kept by the program across the calls.
	if served.connects != 2 {
		t.Errorf("the program connected %d times", served.connects)
	}
}

func TestTheTrackersLinksAreFilledWithTheIdentifier(t *testing.T) {
	served := serve(t)
	tracker := &providers.Tracker{Dial: served.dial}
	settings := capability.Settings{"url": "https://tracker.test"}

	if link := tracker.VersionURL(settings, "3 4"); link != "https://tracker.test/versions/3%204" {
		t.Errorf("version link = %q", link)
	}
	if link := tracker.IssueURL(settings, "MSE-1"); link != "https://tracker.test/issues/MSE-1" {
		t.Errorf("issue link = %q", link)
	}
	if link := tracker.VersionURL(capability.Settings{}, "1"); link != "" {
		t.Errorf("a tracker without an instance links %q", link)
	}
}

func TestAForgeIsReadAndWrittenThroughTheProgram(t *testing.T) {
	served := serve(t)
	forge := providers.Forge{Dial: served.dial}
	ctx := context.Background()

	client, err := forge.Connect(target(nil))
	if err != nil {
		t.Fatal(err)
	}
	if branch, err := client.DefaultBranch(ctx); err != nil || branch != "main@team/app" {
		t.Errorf("default branch = %q, %v", branch, err)
	}
	if refs, truncated, err := client.ListBranches(ctx); err != nil || len(refs) != 1 || !truncated {
		t.Errorf("branches = %+v %v, %v", refs, truncated, err)
	}
	if commit, err := client.ResolveCommit(ctx, "main"); err != nil || commit != "sha-of-main" {
		t.Errorf("commit = %q, %v", commit, err)
	}
	pulls, err := client.ListMergedPullRequests(ctx, due)
	if err != nil || len(pulls) != 1 || pulls[0].Number != 42 {
		t.Fatalf("pulls = %+v, %v", pulls, err)
	}
	if err := client.LoadCommitMessages(ctx, &pulls[0]); err != nil || len(pulls[0].Commits) != 1 || pulls[0].Title != "Feature" {
		t.Errorf("loaded = %+v, %v", pulls[0], err)
	}

	if writer, err := forge.Writer(target(nil)); writer != nil || err != nil {
		t.Errorf("a read-only forge has a writer: %v, %v", writer, err)
	}
	writer, err := forge.Writer(target(capability.Settings{"write": "yes"}))
	if err != nil || writer == nil {
		t.Fatalf("writer = %v, %v", writer, err)
	}
	if err := writer.CreateTag(ctx, "v2", "abc", "release"); err != nil {
		t.Errorf("tag: %v", err)
	}
	if err := writer.CreateTag(ctx, "v1", "abc", "release"); !errors.Is(err, provider.ErrTagExists) ||
		!strings.Contains(err.Error(), "tag v1") {
		t.Errorf("a tag already set answered %v", err)
	}

	prober, err := forge.Prober(target(nil))
	if err != nil {
		t.Fatal(err)
	}
	probe, err := prober.Probe(ctx, "team/other", "ssh-ed25519 AAAA")
	if err != nil || probe.KeyPresent == nil || !*probe.KeyPresent || probe.KeyNote != "team/other" {
		t.Errorf("probe = %+v, %v", probe, err)
	}
}

func TestAProbeIsPolledAndAPanicIsAnError(t *testing.T) {
	served := serve(t)
	probe := providers.Probe(served.dial, "pulls", capability.Schedule{Interval: time.Hour})
	if probe.Name() != "pulls" || probe.Schedule().Interval != time.Hour {
		t.Errorf("probe = %s %+v", probe.Name(), probe.Schedule())
	}
	observations, cursor, err := probe.Poll(context.Background(), target(nil), "c")
	if err != nil || cursor != "c+1" || len(observations) != 1 || observations[0].Subject != "team/app" ||
		!observations[0].OccurredAt.Equal(due) {
		t.Errorf("poll = %+v %q, %v", observations, cursor, err)
	}
	if _, _, err := probe.Poll(context.Background(), target(nil), "boom"); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("a panic answered %v", err)
	}
	if _, _, err := providers.Probe(served.dial, "other", capability.Schedule{}).Poll(context.Background(), target(nil), ""); err == nil {
		t.Error("an undeclared probe was polled")
	}
}

func TestAnActionIsCarriedOut(t *testing.T) {
	served := serve(t)
	executor := providers.Executor{Dial: served.dial}
	if err := executor.Execute(context.Background(), target(nil), "status", map[string]string{"commit": "abc"}); err != nil {
		t.Fatal(err)
	}
	if len(served.executed) != 1 || served.executed[0] != "status abc team/app" {
		t.Errorf("executed = %v", served.executed)
	}
	if err := executor.Execute(context.Background(), target(nil), "fail", nil); err == nil ||
		!strings.Contains(err.Error(), "refused by the forge") {
		t.Errorf("a failed action answered %v", err)
	}
}
