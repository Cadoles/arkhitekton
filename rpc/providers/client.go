package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// Dial returns the plugin's program when a call is made: it may be launched, replaced or stopped
// between two calls.
type Dial func(ctx context.Context) (pluginv1.PluginClient, error)

// named prefixes an error with the plugin that answered it, its sentinel kept.
func named(plugin string, err error) error {
	if err == nil || plugin == "" {
		return err
	}
	return fmt.Errorf("plugin %s: %w", plugin, err)
}

const (
	// margin is what a call is given beyond the target's timeout: the program's own round trip.
	margin = 5 * time.Second
	// defaultTimeout bounds a call whose target sets none.
	defaultTimeout = 30 * time.Second
	// pollTimeout bounds a probe's pass, which may read several pages.
	pollTimeout = 2 * time.Minute
	// linksTimeout bounds the reading of the tracker's links, asked while a page is rendered.
	linksTimeout = 5 * time.Second
	// linksKept bounds the links remembered, one entry per distinct settings.
	linksKept = 64
)

func timeoutOf(target capability.Target) time.Duration {
	if target.Timeout <= 0 {
		return defaultTimeout
	}
	return target.Timeout + margin
}

// call is one provider call: the program dialled, its answer's error turned back into the
// provider's.
func call[R any](
	ctx context.Context, plugin string, dial Dial, target capability.Target, arguments any,
	method func(pluginv1.PluginClient, context.Context, *pluginv1.ProviderRequest, ...grpc.CallOption) (*pluginv1.ProviderResponse, error),
) (R, error) {
	var result R
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(target))
	defer cancel()
	client, err := dial(ctx)
	if err != nil {
		return result, named(plugin, err)
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return result, err
	}
	answer, err := method(client, ctx, &pluginv1.ProviderRequest{Target: TargetOf(target), Arguments: encoded})
	if err != nil {
		return result, named(plugin, Error(err))
	}
	if err := json.Unmarshal(answer.GetResult(), &result); err != nil {
		return result, named(plugin, fmt.Errorf("its answer: %w", err))
	}
	return result, nil
}

// connect asks the program whether it reaches a connection of this kind, and whether it has one.
func connect(plugin string, dial Dial, kind string, target capability.Target) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutOf(target))
	defer cancel()
	client, err := dial(ctx)
	if err != nil {
		return false, named(plugin, err)
	}
	answer, err := client.Connect(ctx, &pluginv1.ConnectRequest{Kind: kind, Target: TargetOf(target)})
	if err != nil {
		return false, named(plugin, Error(err))
	}
	return !answer.GetAbsent(), nil
}

// Tracker is a native plugin's tracker.
type Tracker struct {
	// Plugin names the plugin in errors.
	Plugin string
	Dial   Dial

	mu    sync.Mutex
	links map[string]*pluginv1.Links
}

var _ capability.TrackerCapability = (*Tracker)(nil)

func (t *Tracker) Connect(target capability.Target) (provider.Tracker, error) {
	if _, err := connect(t.Plugin, t.Dial, KindTracker, target); err != nil {
		return nil, err
	}
	return tracker{plugin: t.Plugin, dial: t.Dial, target: target}, nil
}

func (t *Tracker) VersionURL(settings capability.Settings, id string) string {
	return link(t.patterns(settings).GetVersion(), id)
}

func (t *Tracker) IssueURL(settings capability.Settings, id string) string {
	return link(t.patterns(settings).GetIssue(), id)
}

func link(pattern, id string) string {
	if pattern == "" {
		return ""
	}
	return strings.ReplaceAll(pattern, "{id}", url.PathEscape(id))
}

// patterns returns the links' patterns for these settings, asked once per distinct settings. A
// failed reading is not remembered: the next row asks again.
func (t *Tracker) patterns(settings capability.Settings) *pluginv1.Links {
	key := fingerprint(settings)
	t.mu.Lock()
	known, ok := t.links[key]
	t.mu.Unlock()
	if ok {
		return known
	}

	ctx, cancel := context.WithTimeout(context.Background(), linksTimeout)
	defer cancel()
	client, err := t.Dial(ctx)
	if err != nil {
		return nil
	}
	answer, err := client.TrackerLinks(ctx, &pluginv1.Settings{Values: settings})
	if err != nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.links == nil || len(t.links) >= linksKept {
		t.links = make(map[string]*pluginv1.Links)
	}
	t.links[key] = answer
	return answer
}

func fingerprint(settings capability.Settings) string {
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		_, _ = fmt.Fprintf(hash, "%q=%q;", key, settings[key])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type tracker struct {
	plugin string
	dial   Dial
	target capability.Target
}

func (t tracker) ListVersions(ctx context.Context, project string) ([]provider.Version, error) {
	return call[[]provider.Version](ctx, t.plugin, t.dial, t.target, VersionsArguments{Project: project}, pluginv1.PluginClient.ListVersions)
}

func (t tracker) ListIssues(ctx context.Context, query provider.IssueQuery) ([]provider.Issue, error) {
	return call[[]provider.Issue](ctx, t.plugin, t.dial, t.target, query, pluginv1.PluginClient.ListIssues)
}

func (t tracker) CheckAccess(ctx context.Context) (provider.Account, error) {
	return call[provider.Account](ctx, t.plugin, t.dial, t.target, struct{}{}, pluginv1.PluginClient.TrackerAccess)
}

// Forge is a native plugin's forge.
type Forge struct {
	// Plugin names the plugin in errors.
	Plugin string
	Dial   Dial
}

var _ capability.ForgeCapability = Forge{}

func (f Forge) Connect(target capability.Target) (provider.Forge, error) {
	if _, err := connect(f.Plugin, f.Dial, KindForge, target); err != nil {
		return nil, err
	}
	return forge{plugin: f.Plugin, dial: f.Dial, target: target}, nil
}

func (f Forge) Writer(target capability.Target) (provider.TagWriter, error) {
	present, err := connect(f.Plugin, f.Dial, KindWriter, target)
	if err != nil || !present {
		return nil, err
	}
	return forge{plugin: f.Plugin, dial: f.Dial, target: target}, nil
}

func (f Forge) Prober(target capability.Target) (provider.RepositoryProber, error) {
	present, err := connect(f.Plugin, f.Dial, KindProber, target)
	if err != nil || !present {
		return nil, err
	}
	return forge{plugin: f.Plugin, dial: f.Dial, target: target}, nil
}

type forge struct {
	plugin string
	dial   Dial
	target capability.Target
}

func (f forge) DefaultBranch(ctx context.Context) (string, error) {
	return call[string](ctx, f.plugin, f.dial, f.target, struct{}{}, pluginv1.PluginClient.DefaultBranch)
}

func (f forge) ListBranches(ctx context.Context) ([]provider.Ref, bool, error) {
	refs, err := call[RefsResult](ctx, f.plugin, f.dial, f.target, struct{}{}, pluginv1.PluginClient.ListBranches)
	return refs.Refs, refs.Truncated, err
}

func (f forge) ListTags(ctx context.Context) ([]provider.Ref, bool, error) {
	refs, err := call[RefsResult](ctx, f.plugin, f.dial, f.target, struct{}{}, pluginv1.PluginClient.ListTags)
	return refs.Refs, refs.Truncated, err
}

func (f forge) ResolveCommit(ctx context.Context, ref string) (string, error) {
	return call[string](ctx, f.plugin, f.dial, f.target, RefArguments{Ref: ref}, pluginv1.PluginClient.ResolveCommit)
}

func (f forge) ListMergedPullRequests(ctx context.Context, since time.Time) ([]provider.PullRequest, error) {
	return call[[]provider.PullRequest](ctx, f.plugin, f.dial, f.target, SinceArguments{Since: since},
		pluginv1.PluginClient.ListMergedPullRequests)
}

func (f forge) LoadCommitMessages(ctx context.Context, pull *provider.PullRequest) error {
	loaded, err := call[provider.PullRequest](ctx, f.plugin, f.dial, f.target, pull, pluginv1.PluginClient.LoadCommitMessages)
	if err != nil {
		return err
	}
	*pull = loaded
	return nil
}

func (f forge) CheckAccess(ctx context.Context) (provider.Account, error) {
	return call[provider.Account](ctx, f.plugin, f.dial, f.target, struct{}{}, pluginv1.PluginClient.ForgeAccess)
}

func (f forge) CreateTag(ctx context.Context, tag, commit, message string) error {
	_, err := call[json.RawMessage](ctx, f.plugin, f.dial, f.target, TagArguments{Tag: tag, Commit: commit, Message: message},
		pluginv1.PluginClient.CreateTag)
	return err
}

func (f forge) Probe(ctx context.Context, repository, publicKey string) (provider.Probe, error) {
	return call[provider.Probe](ctx, f.plugin, f.dial, f.target, ProbeArguments{Repository: repository, PublicKey: publicKey},
		pluginv1.PluginClient.ProbeRepository)
}

// Probe returns one of the probes a native plugin's manifest declares, its name and schedule taken
// from the manifest.
func Probe(plugin string, dial Dial, name string, schedule capability.Schedule) capability.Probe {
	return probe{plugin: plugin, dial: dial, name: name, schedule: schedule}
}

type probe struct {
	plugin   string
	dial     Dial
	name     string
	schedule capability.Schedule
}

func (p probe) Name() string                  { return p.name }
func (p probe) Schedule() capability.Schedule { return p.schedule }

func (p probe) Poll(ctx context.Context, target capability.Target, cursor string) ([]capability.Observation, string, error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	client, err := p.dial(ctx)
	if err != nil {
		return nil, "", named(p.plugin, err)
	}
	answer, err := client.Poll(ctx, &pluginv1.PollRequest{Probe: p.name, Target: TargetOf(target), Cursor: cursor})
	if err != nil {
		return nil, "", named(p.plugin, Error(err))
	}
	var observations []capability.Observation
	if err := json.Unmarshal(answer.GetObservations(), &observations); err != nil {
		return nil, "", named(p.plugin, fmt.Errorf("the probe's observations: %w", err))
	}
	return observations, answer.GetCursor(), nil
}

// Executor carries out a native plugin's actions.
type Executor struct {
	// Plugin names the plugin in errors.
	Plugin string
	Dial   Dial
}

var _ capability.ActionExecutor = Executor{}

func (e Executor) Execute(ctx context.Context, target capability.Target, suffix string, params map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(target))
	defer cancel()
	client, err := e.Dial(ctx)
	if err != nil {
		return named(e.Plugin, err)
	}
	_, err = client.Execute(ctx, &pluginv1.ExecuteRequest{Target: TargetOf(target), Suffix: suffix, Params: params})
	return named(e.Plugin, Error(err))
}

// Receiver reads a native plugin's webhook deliveries.
type Receiver struct {
	// Plugin names the plugin in errors.
	Plugin string
	Dial   Dial
}

var _ capability.Receiver = Receiver{}

func (r Receiver) Receive(
	ctx context.Context, target capability.Target, delivery capability.Delivery,
) ([]capability.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(target))
	defer cancel()
	client, err := r.Dial(ctx)
	if err != nil {
		return nil, named(r.Plugin, err)
	}
	answer, err := client.Receive(ctx, &pluginv1.ReceiveRequest{
		Target: TargetOf(target), Event: delivery.Event, Body: delivery.Body,
	})
	if err != nil {
		return nil, named(r.Plugin, Error(err))
	}
	var observations []capability.Observation
	if err := json.Unmarshal(answer.GetObservations(), &observations); err != nil {
		return nil, named(r.Plugin, fmt.Errorf("the delivery's observations: %w", err))
	}
	return observations, nil
}
